package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ManagedMailAttachment struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Content     string `json:"content_base64"`
	Inline      bool   `json:"inline"`
	ContentID   string `json:"content_id"`
}

type ManagedMailMessage struct {
	RequestID   string                  `json:"request_id"`
	FromName    string                  `json:"from_name"`
	To          []string                `json:"to"`
	CC          []string                `json:"cc"`
	BCC         []string                `json:"bcc"`
	ReplyTo     []string                `json:"reply_to"`
	Subject     string                  `json:"subject"`
	TextBody    string                  `json:"text_body"`
	HTMLBody    string                  `json:"html_body"`
	Headers     map[string]string       `json:"headers"`
	Attachments []ManagedMailAttachment `json:"attachments"`
}

type ManagedMailSendResult struct {
	RequestID      string `json:"request_id"`
	Status         string `json:"status"`
	MessageID      string `json:"message_id,omitempty"`
	ErrorCode      string `json:"error_code,omitempty"`
	SMTPCode       int    `json:"smtp_code,omitempty"`
	DurationMS     int    `json:"duration_ms"`
	RecipientCount int    `json:"recipient_count"`
	MessageSize    int    `json:"message_size"`
	Existing       bool   `json:"existing"`
}

type managedMailEnvelope struct {
	from       string
	recipients []string
	message    []byte
	messageID  string
}

type managedMailDispatchResult struct {
	status    string
	errorCode string
	smtpCode  int
}

type ManagedMailSender struct {
	policy   *ManagedMailPolicyService
	dispatch func(context.Context, *SMTPConfig, managedMailEnvelope) managedMailDispatchResult
	now      func() time.Time
}

func NewManagedMailSender(policy *ManagedMailPolicyService) *ManagedMailSender {
	return &ManagedMailSender{policy: policy, dispatch: dispatchManagedMailSMTP, now: time.Now}
}

func (s *ManagedMailSender) Send(ctx context.Context, siteID int64, input ManagedMailMessage) (ManagedMailSendResult, error) {
	if s == nil || s.policy == nil || s.policy.db == nil {
		return ManagedMailSendResult{}, ErrManagedMailUnavailable
	}
	policy, err := s.policy.GetSitePolicy(ctx, siteID)
	if err != nil {
		return ManagedMailSendResult{}, err
	}
	envelope, recipientCount, err := buildManagedMailEnvelope(policy, getSMTPConfig(s.policy.db), input, s.now())
	if err != nil {
		return ManagedMailSendResult{}, err
	}
	reservation, err := s.policy.Reserve(ctx, siteID, input.RequestID, recipientCount, len(envelope.message), len(input.Attachments))
	if err != nil {
		return ManagedMailSendResult{}, err
	}
	result := ManagedMailSendResult{RequestID: input.RequestID, Status: reservation.Status, RecipientCount: recipientCount, MessageSize: len(envelope.message), Existing: reservation.Existing}
	if reservation.Existing {
		var messageID, errorCode string
		if scanErr := s.policy.db.QueryRowContext(ctx, `SELECT message_id,error_code,smtp_code,duration_ms,message_size FROM managed_mail_deliveries WHERE request_id=? AND site_id=?`, input.RequestID, siteID).Scan(&messageID, &errorCode, &result.SMTPCode, &result.DurationMS, &result.MessageSize); scanErr != nil {
			return ManagedMailSendResult{}, scanErr
		}
		result.MessageID, result.ErrorCode = messageID, errorCode
		return result, nil
	}
	started := time.Now()
	dispatched := s.dispatch(ctx, getSMTPConfig(s.policy.db), envelope)
	result.Status, result.ErrorCode, result.SMTPCode = dispatched.status, dispatched.errorCode, dispatched.smtpCode
	result.DurationMS = int(time.Since(started).Milliseconds())
	if result.DurationMS < 0 {
		result.DurationMS = 0
	}
	if result.Status == "accepted" {
		result.MessageID = envelope.messageID
	}
	if err := s.policy.Complete(ctx, siteID, input.RequestID, result.Status, result.MessageID, result.ErrorCode, result.SMTPCode, result.DurationMS); err != nil {
		return ManagedMailSendResult{}, err
	}
	_ = s.policy.ApplyDeliveryPolicy(ctx, siteID, result.Status, result.ErrorCode)
	if result.Status == "accepted" || result.Status == "unknown" {
		_ = s.policy.ProcessQuotaNotices(ctx, siteID)
	}
	return result, nil
}

func buildManagedMailEnvelope(policy ManagedMailSitePolicy, cfg *SMTPConfig, input ManagedMailMessage, now time.Time) (managedMailEnvelope, int, error) {
	if cfg == nil || !validManagedMailAddress(cfg.FromEmail) || len([]rune(input.Subject)) > 998 || strings.ContainsAny(input.Subject, "\r\n") || (input.TextBody == "" && input.HTMLBody == "") {
		return managedMailEnvelope{}, 0, ErrManagedMailValidation
	}
	fromName := strings.TrimSpace(input.FromName)
	if policy.LockFromName || fromName == "" {
		fromName = policy.FromName
	}
	if len([]rune(fromName)) > 100 || strings.ContainsAny(fromName, "\r\n") {
		return managedMailEnvelope{}, 0, ErrManagedMailValidation
	}
	to, toSMTP, err := parseManagedMailAddresses(input.To)
	if err != nil || len(to) == 0 {
		return managedMailEnvelope{}, 0, ErrManagedMailValidation
	}
	cc, ccSMTP, err := parseManagedMailAddresses(input.CC)
	if err != nil {
		return managedMailEnvelope{}, 0, ErrManagedMailValidation
	}
	_, bccSMTP, err := parseManagedMailAddresses(input.BCC)
	if err != nil {
		return managedMailEnvelope{}, 0, ErrManagedMailValidation
	}
	unique := make(map[string]string)
	for _, address := range append(append(toSMTP, ccSMTP...), bccSMTP...) {
		unique[strings.ToLower(address)] = address
	}
	recipients := make([]string, 0, len(unique))
	for _, address := range unique {
		recipients = append(recipients, address)
	}
	sort.Strings(recipients)
	if len(recipients) == 0 {
		return managedMailEnvelope{}, 0, ErrManagedMailValidation
	}
	replyInput := input.ReplyTo
	if len(replyInput) == 0 && policy.ReplyTo != "" {
		replyInput = []string{policy.ReplyTo}
	}
	replyTo, _, err := parseManagedMailAddresses(replyInput)
	if err != nil {
		return managedMailEnvelope{}, 0, ErrManagedMailValidation
	}
	messageID, err := newManagedMailMessageID(cfg.FromEmail)
	if err != nil {
		return managedMailEnvelope{}, 0, err
	}
	var out bytes.Buffer
	writeManagedMailHeader(&out, "Date", now.Format(time.RFC1123Z))
	writeManagedMailHeader(&out, "Message-ID", messageID)
	writeManagedMailHeader(&out, "From", (&mail.Address{Name: fromName, Address: cfg.FromEmail}).String())
	writeManagedMailHeader(&out, "To", strings.Join(to, ", "))
	if len(cc) > 0 {
		writeManagedMailHeader(&out, "Cc", strings.Join(cc, ", "))
	}
	if len(replyTo) > 0 {
		writeManagedMailHeader(&out, "Reply-To", strings.Join(replyTo, ", "))
	}
	writeManagedMailHeader(&out, "Subject", mime.QEncoding.Encode("UTF-8", input.Subject))
	writeManagedMailHeader(&out, "MIME-Version", "1.0")
	if err := writeManagedMailCustomHeaders(&out, input.Headers); err != nil {
		return managedMailEnvelope{}, 0, err
	}
	if err := writeManagedMailBody(&out, input); err != nil {
		return managedMailEnvelope{}, 0, err
	}
	return managedMailEnvelope{from: cfg.FromEmail, recipients: recipients, message: out.Bytes(), messageID: messageID}, len(recipients), nil
}

func parseManagedMailAddresses(values []string) ([]string, []string, error) {
	display, smtpAddresses := make([]string, 0, len(values)), make([]string, 0, len(values))
	for _, value := range values {
		if strings.ContainsAny(value, "\r\n") {
			return nil, nil, ErrManagedMailValidation
		}
		address, err := mail.ParseAddress(strings.TrimSpace(value))
		if err != nil || address.Address == "" {
			return nil, nil, ErrManagedMailValidation
		}
		display, smtpAddresses = append(display, address.String()), append(smtpAddresses, address.Address)
	}
	return display, smtpAddresses, nil
}

func writeManagedMailHeader(out *bytes.Buffer, name, value string) {
	fmt.Fprintf(out, "%s: %s\r\n", name, value)
}

func writeManagedMailCustomHeaders(out *bytes.Buffer, headers map[string]string) error {
	reserved := map[string]bool{"from": true, "sender": true, "return-path": true, "to": true, "cc": true, "bcc": true, "reply-to": true, "subject": true, "date": true, "message-id": true, "mime-version": true, "content-type": true, "content-transfer-encoding": true, "received": true, "authentication-results": true, "dkim-signature": true}
	allowedStandard := map[string]bool{
		"list-unsubscribe": true, "list-unsubscribe-post": true,
		"precedence": true, "auto-submitted": true, "in-reply-to": true,
		"references": true, "importance": true, "priority": true,
	}
	keys := make([]string, 0, len(headers))
	for name := range headers {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		value := headers[name]
		canonical := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name))
		lower := strings.ToLower(canonical)
		if canonical == "" || reserved[lower] || strings.ContainsAny(name+value, "\r\n\x00") || len(value) > 2000 || !(strings.HasPrefix(lower, "x-") || allowedStandard[lower]) {
			return ErrManagedMailValidation
		}
		writeManagedMailHeader(out, canonical, value)
	}
	return nil
}

func writeManagedMailBody(out *bytes.Buffer, input ManagedMailMessage) error {
	attachments := make([]struct {
		meta ManagedMailAttachment
		data []byte
	}, 0, len(input.Attachments))
	for _, attachment := range input.Attachments {
		name := filepath.Base(strings.ReplaceAll(strings.TrimSpace(attachment.Name), "\\", "/"))
		if name == "." || name == "" || strings.ContainsAny(name+attachment.ContentType+attachment.ContentID, "\r\n\x00") {
			return ErrManagedMailValidation
		}
		data, err := base64.StdEncoding.DecodeString(attachment.Content)
		if err != nil {
			return ErrManagedMailValidation
		}
		attachment.Name = name
		attachments = append(attachments, struct {
			meta ManagedMailAttachment
			data []byte
		}{attachment, data})
	}
	if len(attachments) == 0 {
		return writeManagedMailAlternative(out, input.TextBody, input.HTMLBody)
	}
	mixed := multipart.NewWriter(out)
	writeManagedMailHeader(out, "Content-Type", `multipart/mixed; boundary="`+mixed.Boundary()+`"`)
	out.WriteString("\r\n")
	bodyHeader := make(textproto.MIMEHeader)
	if input.TextBody != "" && input.HTMLBody != "" {
		var body bytes.Buffer
		alternative := multipart.NewWriter(&body)
		bodyHeader.Set("Content-Type", `multipart/alternative; boundary="`+alternative.Boundary()+`"`)
		part, _ := mixed.CreatePart(bodyHeader)
		writeManagedMailTextPart(alternative, "text/plain", input.TextBody)
		writeManagedMailTextPart(alternative, "text/html", input.HTMLBody)
		_ = alternative.Close()
		_, _ = io.Copy(part, &body)
	} else {
		kind, body := "text/plain", input.TextBody
		if input.HTMLBody != "" {
			kind, body = "text/html", input.HTMLBody
		}
		bodyHeader.Set("Content-Type", kind+`; charset="UTF-8"`)
		bodyHeader.Set("Content-Transfer-Encoding", "quoted-printable")
		part, _ := mixed.CreatePart(bodyHeader)
		writer := quotedprintable.NewWriter(part)
		_, _ = writer.Write([]byte(body))
		_ = writer.Close()
	}
	for _, attachment := range attachments {
		header := make(textproto.MIMEHeader)
		contentType := strings.TrimSpace(attachment.meta.ContentType)
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil || !strings.Contains(mediaType, "/") {
			return ErrManagedMailValidation
		}
		header.Set("Content-Type", mime.FormatMediaType(mediaType, map[string]string{"name": attachment.meta.Name}))
		disposition := "attachment"
		if attachment.meta.Inline {
			disposition = "inline"
		}
		header.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": attachment.meta.Name}))
		header.Set("Content-Transfer-Encoding", "base64")
		if attachment.meta.ContentID != "" {
			header.Set("Content-ID", "<"+strings.Trim(attachment.meta.ContentID, "<>")+">")
		}
		part, _ := mixed.CreatePart(header)
		encoder := base64.NewEncoder(base64.StdEncoding, newBase64LineWriter(part))
		_, _ = encoder.Write(attachment.data)
		_ = encoder.Close()
	}
	return mixed.Close()
}

func writeManagedMailAlternative(out *bytes.Buffer, textBody, htmlBody string) error {
	if textBody == "" || htmlBody == "" {
		kind, body := "text/plain", textBody
		if htmlBody != "" {
			kind, body = "text/html", htmlBody
		}
		writeManagedMailHeader(out, "Content-Type", kind+`; charset="UTF-8"`)
		writeManagedMailHeader(out, "Content-Transfer-Encoding", "quoted-printable")
		out.WriteString("\r\n")
		writer := quotedprintable.NewWriter(out)
		_, err := writer.Write([]byte(body))
		if closeErr := writer.Close(); err == nil {
			err = closeErr
		}
		return err
	}
	alternative := multipart.NewWriter(out)
	writeManagedMailHeader(out, "Content-Type", `multipart/alternative; boundary="`+alternative.Boundary()+`"`)
	out.WriteString("\r\n")
	writeManagedMailTextPart(alternative, "text/plain", textBody)
	writeManagedMailTextPart(alternative, "text/html", htmlBody)
	return alternative.Close()
}

func writeManagedMailTextPart(writer *multipart.Writer, kind, body string) {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Type", kind+`; charset="UTF-8"`)
	header.Set("Content-Transfer-Encoding", "quoted-printable")
	part, _ := writer.CreatePart(header)
	quoted := quotedprintable.NewWriter(part)
	_, _ = quoted.Write([]byte(body))
	_ = quoted.Close()
}

type base64LineWriter struct {
	w      io.Writer
	column int
}

func newBase64LineWriter(w io.Writer) io.Writer { return &base64LineWriter{w: w} }

func (w *base64LineWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		space := 76 - w.column
		if space == 0 {
			if _, err := io.WriteString(w.w, "\r\n"); err != nil {
				return written, err
			}
			w.column = 0
			space = 76
		}
		n := space
		if n > len(p) {
			n = len(p)
		}
		m, err := w.w.Write(p[:n])
		written += m
		w.column += m
		p = p[m:]
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

func newManagedMailMessageID(from string) (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	domain := "localhost"
	if at := strings.LastIndex(from, "@"); at >= 0 && at+1 < len(from) {
		domain = from[at+1:]
	}
	return fmt.Sprintf("<%x@%s>", random, domain), nil
}

func dispatchManagedMailSMTP(ctx context.Context, cfg *SMTPConfig, envelope managedMailEnvelope) managedMailDispatchResult {
	if cfg == nil {
		return managedMailDispatchResult{status: "temporary_failure", errorCode: "smtp_unavailable"}
	}
	ctx, cancel := context.WithTimeout(ctx, smtpOperationTimeout)
	defer cancel()
	address := net.JoinHostPort(cfg.Host, cfg.Port)
	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return classifyManagedMailSMTPError(err, false)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(smtpOperationTimeout))
	var client *smtp.Client
	if cfg.Encryption == "ssl" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return classifyManagedMailSMTPError(err, false)
		}
		client, err = smtp.NewClient(tlsConn, cfg.Host)
	} else {
		client, err = smtp.NewClient(conn, cfg.Host)
	}
	if err != nil {
		return classifyManagedMailSMTPError(err, false)
	}
	defer client.Close()
	if cfg.Encryption != "ssl" && cfg.Encryption != "none" {
		if err := client.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return classifyManagedMailSMTPError(err, false)
		}
	}
	if err := client.Auth(smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)); err != nil {
		result := classifyManagedMailSMTPError(err, false)
		if isManagedMailAuthenticationFailure(result.smtpCode) {
			result.errorCode = "authentication_failed"
		}
		return result
	}
	if err := client.Mail(envelope.from); err != nil {
		return classifyManagedMailSMTPError(err, false)
	}
	for _, recipient := range envelope.recipients {
		if err := client.Rcpt(recipient); err != nil {
			return classifyManagedMailSMTPError(err, false)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return classifyManagedMailSMTPError(err, false)
	}
	if _, err := writer.Write(envelope.message); err != nil {
		_ = writer.Close()
		return classifyManagedMailSMTPError(err, true)
	}
	if err := writer.Close(); err != nil {
		return classifyManagedMailSMTPError(err, true)
	}
	return managedMailDispatchResult{status: "accepted"}
}

func isManagedMailAuthenticationFailure(code int) bool {
	return code == 530 || code == 534 || code == 535
}

func classifyManagedMailSMTPError(err error, dataStarted bool) managedMailDispatchResult {
	if dataStarted {
		code := smtpStatusCode(err)
		if code >= 500 {
			return managedMailDispatchResult{status: "rejected", errorCode: "smtp_rejected", smtpCode: code}
		}
		if code >= 400 {
			return managedMailDispatchResult{status: "temporary_failure", errorCode: "smtp_temporary_failure", smtpCode: code}
		}
		return managedMailDispatchResult{status: "unknown", errorCode: "data_result_unknown", smtpCode: code}
	}
	code := smtpStatusCode(err)
	// 421 is a server-wide temporary service refusal. Do not classify free-form
	// provider text or recipient-specific 4xx replies as a global rate limit;
	// those contribute only to the site's consecutive-failure policy.
	if code == 421 {
		return managedMailDispatchResult{status: "temporary_failure", errorCode: "provider_rate_limited", smtpCode: code}
	}
	if code >= 500 {
		return managedMailDispatchResult{status: "rejected", errorCode: "smtp_rejected", smtpCode: code}
	}
	return managedMailDispatchResult{status: "temporary_failure", errorCode: "smtp_temporary_failure", smtpCode: code}
}

func smtpStatusCode(err error) int {
	var protocolErr *textproto.Error
	if errors.As(err, &protocolErr) {
		return protocolErr.Code
	}
	return 0
}

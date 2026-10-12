package executor

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"html"
	"net"
	"net/smtp"
	"sort"
	"strings"
	"syscall"
	"time"
)

const smtpDiagnosticStepTimeout = 6 * time.Second

type SMTPDiagnosticStage struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

type SMTPDiagnosticResult struct {
	Success         bool                  `json:"success"`
	ConclusionCode  string                `json:"conclusion_code"`
	Host            string                `json:"host"`
	Port            string                `json:"port"`
	Encryption      string                `json:"encryption"`
	IPv4            []string              `json:"ipv4"`
	IPv6            []string              `json:"ipv6"`
	Stages          []SMTPDiagnosticStage `json:"stages"`
	Alternative2525 bool                  `json:"alternative_2525"`
}

type smtpDiagnosticError struct {
	phase string
	code  string
	err   error
}

type smtpEndpoint struct {
	network string
	ip      string
	family  string
}

func (e *smtpDiagnosticError) Error() string { return e.err.Error() }
func (e *smtpDiagnosticError) Unwrap() error { return e.err }

var smtpLookupIPAddr = net.DefaultResolver.LookupIPAddr
var smtpDialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, address)
}
var smtpRunDiagnosticSession = runSMTPDiagnosticSession

func DiagnoseSMTP(ctx context.Context, to string) (*SMTPDiagnosticResult, error) {
	cfg := GetSMTPConfig()
	if cfg == nil || strings.TrimSpace(cfg.Host) == "" || strings.TrimSpace(cfg.Port) == "" {
		return nil, fmt.Errorf("SMTP 未配置")
	}
	if strings.TrimSpace(cfg.User) == "" || strings.TrimSpace(cfg.Pass) == "" {
		return nil, fmt.Errorf("SMTP 认证信息未配置")
	}
	if strings.TrimSpace(to) == "" {
		to = strings.TrimSpace(cfg.AdminEmail)
	}
	if to == "" {
		return nil, fmt.Errorf("管理员邮箱未设置")
	}

	result := &SMTPDiagnosticResult{Host: cfg.Host, Port: cfg.Port, Encryption: cfg.Encryption}
	lookupCtx, cancel := context.WithTimeout(ctx, smtpDiagnosticStepTimeout)
	addrs, err := smtpLookupIPAddr(lookupCtx, cfg.Host)
	cancel()
	if err != nil || len(addrs) == 0 {
		result.ConclusionCode = "dns_failed"
		result.Stages = append(result.Stages, SMTPDiagnosticStage{Name: "dns", Status: "failed", Code: "dns_failed", Detail: diagnosticErrorDetail(err)})
		return result, nil
	}
	for _, addr := range addrs {
		if addr.IP.To4() != nil {
			result.IPv4 = appendUnique(result.IPv4, addr.IP.String())
		} else {
			result.IPv6 = appendUnique(result.IPv6, addr.IP.String())
		}
	}
	sort.Strings(result.IPv4)
	sort.Strings(result.IPv6)
	result.Stages = append(result.Stages, SMTPDiagnosticStage{Name: "dns", Status: "passed", Code: "dns_ok", Detail: strings.Join(append(append([]string{}, result.IPv4...), result.IPv6...), ", ")})

	endpoints := make([]smtpEndpoint, 0, 2)
	if len(result.IPv4) > 0 {
		endpoints = append(endpoints, smtpEndpoint{"tcp4", result.IPv4[0], "ipv4"})
	}
	if len(result.IPv6) > 0 {
		endpoints = append(endpoints, smtpEndpoint{"tcp6", result.IPv6[0], "ipv6"})
	}
	var selected *smtpEndpoint
	timedOutEndpoints := make([]smtpEndpoint, 0, len(endpoints))
	sawRefused := false
	for i := range endpoints {
		ep := endpoints[i]
		code, detail := probeSMTPPort(ctx, ep.network, ep.ip, cfg.Port)
		status := "failed"
		if code == "connected" {
			status = "passed"
			if selected == nil {
				selected = &endpoints[i]
			}
		} else if code == "timeout" {
			timedOutEndpoints = append(timedOutEndpoints, ep)
		} else if code == "refused" {
			sawRefused = true
		}
		result.Stages = append(result.Stages, SMTPDiagnosticStage{Name: ep.family + "_tcp", Status: status, Code: code, Detail: detail})
	}

	if selected == nil {
		result.ConclusionCode = connectionConclusion(result.Stages)
		if len(timedOutEndpoints) > 0 && !sawRefused && isStandardSMTPPort(cfg.Port) {
			if sameHostPortReachable(ctx, timedOutEndpoints, "443") {
				result.Stages = append(result.Stages, SMTPDiagnosticStage{Name: "https_control", Status: "passed", Code: "connected", Detail: cfg.Host + ":443"})
				result.ConclusionCode = "possible_upstream_filter"
			} else {
				result.Stages = append(result.Stages, SMTPDiagnosticStage{Name: "https_control", Status: "failed", Code: "unreachable", Detail: cfg.Host + ":443"})
			}
			if sameHostPortReachable(ctx, timedOutEndpoints, "2525") {
				result.Alternative2525 = true
				result.Stages = append(result.Stages, SMTPDiagnosticStage{Name: "alternative_2525", Status: "passed", Code: "connected", Detail: cfg.Host + ":2525"})
			} else {
				result.Stages = append(result.Stages, SMTPDiagnosticStage{Name: "alternative_2525", Status: "failed", Code: "unreachable"})
			}
		}
		return result, nil
	}

	if err := smtpRunDiagnosticSession(ctx, cfg, to, selected.network, selected.ip); err != nil {
		var diagnosticErr *smtpDiagnosticError
		if errors.As(err, &diagnosticErr) {
			result.ConclusionCode = diagnosticConclusionForError(diagnosticErr)
			appendCompletedSMTPStages(result, cfg.Encryption, diagnosticErr.phase)
			result.Stages = append(result.Stages, SMTPDiagnosticStage{Name: diagnosticErr.phase, Status: "failed", Code: diagnosticErr.code, Detail: diagnosticErrorDetail(diagnosticErr.err)})
		} else {
			result.ConclusionCode = "smtp_failed"
			result.Stages = append(result.Stages, SMTPDiagnosticStage{Name: "smtp", Status: "failed", Code: "smtp_failed", Detail: diagnosticErrorDetail(err)})
		}
		return result, nil
	}
	result.Stages = append(result.Stages,
		SMTPDiagnosticStage{Name: "tls", Status: "passed", Code: tlsStageCode(cfg.Encryption)},
		SMTPDiagnosticStage{Name: "authentication", Status: "passed", Code: "auth_ok"},
		SMTPDiagnosticStage{Name: "submission", Status: "passed", Code: "submit_ok", Detail: to},
	)
	result.Success = true
	result.ConclusionCode = "success"
	return result, nil
}

func diagnosticConclusionForError(err *smtpDiagnosticError) string {
	if err.phase != "tcp" {
		return err.code
	}
	switch err.code {
	case "timeout":
		return "tcp_timeout"
	case "refused":
		return "tcp_refused"
	default:
		return "tcp_failed"
	}
}

func appendCompletedSMTPStages(result *SMTPDiagnosticResult, encryption, failedPhase string) {
	if failedPhase == "authentication" || failedPhase == "submission" {
		result.Stages = append(result.Stages, SMTPDiagnosticStage{Name: "tls", Status: "passed", Code: tlsStageCode(encryption)})
	}
	if failedPhase == "submission" {
		result.Stages = append(result.Stages, SMTPDiagnosticStage{Name: "authentication", Status: "passed", Code: "auth_ok"})
	}
}

func runSMTPDiagnosticSession(parent context.Context, cfg *SMTPConfig, to, network, ip string) error {
	ctx, cancel := context.WithTimeout(parent, smtpOperationTimeout)
	defer cancel()
	conn, err := smtpDialContext(ctx, network, net.JoinHostPort(ip, cfg.Port))
	if err != nil {
		return &smtpDiagnosticError{phase: "tcp", code: classifyConnectionError(err), err: err}
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(smtpOperationTimeout))

	var smtpConn net.Conn = conn
	if cfg.Encryption == "ssl" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: cfg.Host})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return &smtpDiagnosticError{phase: "tls", code: "tls_failed", err: err}
		}
		smtpConn = tlsConn
	}
	client, err := smtp.NewClient(smtpConn, cfg.Host)
	if err != nil {
		if classifyConnectionError(err) == "timeout" {
			return &smtpDiagnosticError{phase: "smtp", code: "greeting_timeout", err: err}
		}
		return &smtpDiagnosticError{phase: "smtp", code: "smtp_failed", err: err}
	}
	defer client.Close()
	if cfg.Encryption == "starttls" || cfg.Encryption == "" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return &smtpDiagnosticError{phase: "tls", code: "starttls_unavailable", err: fmt.Errorf("服务器未提供 STARTTLS")}
		}
		if err := client.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
			return &smtpDiagnosticError{phase: "tls", code: "tls_failed", err: err}
		}
	}
	if err := client.Auth(smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)); err != nil {
		code := "auth_failed"
		if strings.Contains(err.Error(), "unencrypted connection") {
			code = "auth_requires_encryption"
		} else if strings.Contains(err.Error(), "doesn't support AUTH") {
			code = "auth_unavailable"
		}
		return &smtpDiagnosticError{phase: "authentication", code: code, err: err}
	}
	panelTitle := html.EscapeString(getPanelTitle())
	body := fmt.Sprintf("<p>SMTP diagnostic test from %s.</p>", panelTitle)
	if err := submitSMTPMessage(client, cfg, to, buildMessage(cfg.User, to, getPanelTitle()+" — SMTP diagnostic", body)); err != nil {
		return &smtpDiagnosticError{phase: "submission", code: "submit_failed", err: err}
	}
	_ = client.Quit()
	return nil
}

func submitSMTPMessage(client *smtp.Client, cfg *SMTPConfig, to, msg string) error {
	if err := client.Mail(cfg.User); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	wc, err := client.Data()
	if err != nil {
		return err
	}
	if _, err = wc.Write([]byte(msg)); err != nil {
		_ = wc.Close()
		return err
	}
	return wc.Close()
}

func probeSMTPPort(parent context.Context, network, ip, port string) (string, string) {
	ctx, cancel := context.WithTimeout(parent, smtpDiagnosticStepTimeout)
	defer cancel()
	conn, err := smtpDialContext(ctx, network, net.JoinHostPort(ip, port))
	if err != nil {
		return classifyConnectionError(err), diagnosticErrorDetail(err)
	}
	_ = conn.Close()
	return "connected", net.JoinHostPort(ip, port)
}

func sameHostPortReachable(ctx context.Context, endpoints []smtpEndpoint, port string) bool {
	for _, ep := range endpoints {
		if code, _ := probeSMTPPort(ctx, ep.network, ep.ip, port); code == "connected" {
			return true
		}
	}
	return false
}

func classifyConnectionError(err error) string {
	if err == nil {
		return "connected"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "refused"
	}
	if errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) {
		return "unreachable"
	}
	return "connection_failed"
}

func connectionConclusion(stages []SMTPDiagnosticStage) string {
	for _, stage := range stages {
		if stage.Code == "refused" {
			return "tcp_refused"
		}
	}
	for _, stage := range stages {
		if stage.Code == "timeout" {
			return "tcp_timeout"
		}
	}
	return "tcp_failed"
}

func diagnosticErrorDetail(err error) string {
	if err == nil {
		return ""
	}
	code := classifyConnectionError(err)
	if code != "connection_failed" {
		return code
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns_error"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Err != nil {
		return sanitizeSMTPDetail(opErr.Err.Error())
	}
	return sanitizeSMTPDetail(err.Error())
}

func sanitizeSMTPDetail(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) > 240 {
		value = value[:240]
	}
	return value
}

func isStandardSMTPPort(port string) bool { return port == "25" || port == "465" || port == "587" }
func tlsStageCode(encryption string) string {
	if encryption == "none" {
		return "tls_not_requested"
	}
	return "tls_ok"
}
func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

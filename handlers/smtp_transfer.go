package handlers

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

const (
	smtpTransferPrefix  = "WPPANEL-SMTP-V1:"
	smtpTransferMaxSize = 4096
)

// The shared key makes codes portable across WP Panel servers. It prevents
// casual disclosure and detects tampering, but is not a security boundary.
var smtpTransferKey = sha256.Sum256([]byte("wp-panel-portable-smtp-configuration-v1"))

type smtpTransferConfig struct {
	Host       string `json:"smtp_host"`
	Port       string `json:"smtp_port"`
	Encryption string `json:"smtp_encryption"`
	User       string `json:"smtp_user"`
	Pass       string `json:"smtp_pass"`
	AdminEmail string `json:"admin_email"`
}

func (h *AlertHandler) ExportSMTPConfig(c *gin.Context) {
	cfg := executor.GetSMTPConfig()
	if cfg == nil || strings.TrimSpace(cfg.Host) == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "alert.smtp_transfer_not_configured")))
		return
	}

	code, err := encodeSMTPTransfer(smtpTransferConfig{
		Host: cfg.Host, Port: cfg.Port, Encryption: cfg.Encryption,
		User: cfg.User, Pass: cfg.Pass, AdminEmail: cfg.AdminEmail,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "alert.smtp_transfer_export_failed")))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"code": code}))
}

func (h *AlertHandler) ImportSMTPConfig(c *gin.Context) {
	var req struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "alert.smtp_transfer_invalid")))
		return
	}

	cfg, err := decodeSMTPTransfer(req.Code)
	if err != nil || validateSMTPTransfer(cfg) != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "alert.smtp_transfer_invalid")))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(cfg))
}

func encodeSMTPTransfer(cfg smtpTransferConfig) (string, error) {
	plain, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(smtpTransferKey[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, plain, []byte(smtpTransferPrefix))
	return smtpTransferPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func decodeSMTPTransfer(code string) (smtpTransferConfig, error) {
	var cfg smtpTransferConfig
	code = strings.TrimSpace(code)
	if len(code) > smtpTransferMaxSize || !strings.HasPrefix(code, smtpTransferPrefix) {
		return cfg, errors.New("invalid SMTP transfer code")
	}
	sealed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, smtpTransferPrefix))
	if err != nil {
		return cfg, err
	}
	block, err := aes.NewCipher(smtpTransferKey[:])
	if err != nil {
		return cfg, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return cfg, err
	}
	if len(sealed) < gcm.NonceSize() {
		return cfg, errors.New("invalid SMTP transfer payload")
	}
	nonce, ciphertext := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte(smtpTransferPrefix))
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(plain, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func validateSMTPTransfer(cfg smtpTransferConfig) error {
	values := map[string]string{
		"smtp_host": cfg.Host, "smtp_port": cfg.Port, "smtp_encryption": cfg.Encryption,
		"smtp_user": cfg.User, "smtp_pass": cfg.Pass, "admin_email": cfg.AdminEmail,
	}
	if strings.TrimSpace(cfg.Host) == "" {
		return errors.New("SMTP host is empty")
	}
	for key, value := range values {
		if _, ok, err := normalizeAlertSetting(key, value); err != nil || !ok {
			return fmt.Errorf("invalid %s", key)
		}
	}
	return nil
}

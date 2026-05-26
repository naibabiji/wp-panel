package executor

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"

	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
)

type acmeUser struct {
	Email        string
	Registration *registration.Resource
	key          crypto.PrivateKey
}

func (u *acmeUser) GetEmail() string                        { return u.Email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.Registration }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

func executeEnableSSL(task *Task) TaskResult {
	payload, ok := task.Payload.(*EnableSSLPayload)
	if !ok {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}

	site := payload.Site
	cfg := config.AppConfig
	certDir := filepath.Join(cfg.Paths.Certificates, site.Domain)
	certPath := filepath.Join(certDir, "fullchain.pem")
	keyPath := filepath.Join(certDir, "privkey.pem")

	os.RemoveAll(certDir)
	if err := os.MkdirAll(certDir, 0700); err != nil {
		log.Printf("创建证书目录失败: %v", err)
		return TaskResult{Success: false, Message: "创建证书目录失败"}
	}

	var expiry time.Time
	var applyErr error

	if payload.Mode == "manual" {
		if payload.Certificate == "" || payload.PrivateKey == "" {
			return TaskResult{Success: false, Message: "证书内容和私钥不能为空"}
		}
		if err := os.WriteFile(certPath, []byte(payload.Certificate), 0644); err != nil {
			log.Printf("写入证书文件失败: %v", err)
			return TaskResult{Success: false, Message: "写入证书文件失败"}
		}
		if err := os.WriteFile(keyPath, []byte(payload.PrivateKey), 0600); err != nil {
			os.Remove(certPath)
			log.Printf("写入私钥文件失败: %v", err)
			return TaskResult{Success: false, Message: "写入私钥文件失败"}
		}
		expiry, applyErr = validateCertificate(certPath, site.Domain)
		if applyErr != nil {
			os.Remove(certPath)
			os.Remove(keyPath)
			log.Printf("证书验证失败: %v", applyErr)
			return TaskResult{Success: false, Message: "证书验证失败"}
		}
	} else {
		expiry, applyErr = obtainLegoCert(site.Domain, site.Aliases, site.WebRoot, certDir)
		if applyErr != nil {
			log.Printf("申请 Let's Encrypt 证书失败: %v", applyErr)
			return TaskResult{Success: false, Message: "申请 Let's Encrypt 证书失败: " + applyErr.Error()}
		}
	}

	if applyErr = applySSLToSite(site, certPath, keyPath, expiry); applyErr != nil {
		os.RemoveAll(certDir)
		log.Printf("应用SSL配置失败: %v", applyErr)
		return TaskResult{Success: false, Message: "应用SSL配置失败"}
	}

	return TaskResult{
		Success: true,
		Message: fmt.Sprintf("网站 %s SSL 已启用（到期: %s）", site.Domain, expiry.Format("2006-01-02")),
	}
}

func executeRemoveSSL(task *Task) TaskResult {
	payload, ok := task.Payload.(*RemoveSSLPayload)
	if !ok {
		return TaskResult{Success: false, Message: "任务参数类型错误"}
	}

	site := payload.Site
	cfg := config.AppConfig

	certDir := filepath.Join(cfg.Paths.Certificates, site.Domain)
	os.RemoveAll(certDir)

	engine := NewTemplateEngine(cfg.Panel.BackupDir)
	nginxData := nginxDataFromSite(site)
	nginxData.UseSSL = false
	nginxData.SSLCertPath = ""
	nginxData.SSLKeyPath = ""

	nginxConfig, err := engine.RenderNginxConfig(nginxData)
	if err != nil {
		log.Printf("渲染 HTTP 配置失败: %v", err)
		return TaskResult{Success: false, Message: "渲染 HTTP 配置失败"}
	}

	if err := engine.ApplyNginxConfig(nginxConfig, site.NginxConfPath,
		filepath.Join(cfg.Paths.NginxSitesEnabled, site.Domain+".conf")); err != nil {
		log.Printf("应用 HTTP 配置失败: %v", err)
		return TaskResult{Success: false, Message: "应用 HTTP 配置失败"}
	}

	db := database.GetDB()
	db.Exec(`UPDATE websites SET ssl_enabled = 0, ssl_cert_path = '', ssl_key_path = '', ssl_expires_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, site.ID)

	return TaskResult{Success: true, Message: "网站 " + site.Domain + " SSL 证书已删除，已恢复为 HTTP"}
}

const acmeAccountDir = "/www/server/panel/acme"

func getOrCreateACMEClient(email string, caDirURL string) (*lego.Client, error) {
	if err := os.MkdirAll(acmeAccountDir, 0700); err != nil {
		return nil, fmt.Errorf("创建ACME目录失败: %w", err)
	}

	accountKeyPath := filepath.Join(acmeAccountDir, "account.key")

	var privateKey crypto.PrivateKey
	var err error

	if keyData, readErr := os.ReadFile(accountKeyPath); readErr == nil {
		block, _ := pem.Decode(keyData)
		if block != nil {
			privateKey, err = x509.ParseECPrivateKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("解析ACME账户私钥失败: %w", err)
			}
		}
	}

	if privateKey == nil {
		privateKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("生成ACME账户私钥失败: %w", err)
		}
		keyBytes, _ := x509.MarshalECPrivateKey(privateKey.(*ecdsa.PrivateKey))
		pemData := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
		if err := os.WriteFile(accountKeyPath, pemData, 0600); err != nil {
			return nil, fmt.Errorf("保存ACME账户私钥失败: %w", err)
		}
	}

	user := &acmeUser{Email: email, key: privateKey}

	legoCfg := lego.NewConfig(user)
	legoCfg.CADirURL = caDirURL

	client, err := lego.NewClient(legoCfg)
	if err != nil {
		return nil, fmt.Errorf("创建lego客户端失败: %w", err)
	}

	reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		return nil, fmt.Errorf("注册ACME账户失败: %w", err)
	}
	user.Registration = reg

	return client, nil
}

func obtainLegoCert(domain string, aliases string, webRoot string, certDir string) (time.Time, error) {
	client, err := getOrCreateACMEClient("admin@"+domain, lego.LEDirectoryProduction)
	if err != nil {
		return time.Time{}, err
	}

	provider := &webrootProvider{webroot: webRoot}
	if err := client.Challenge.SetHTTP01Provider(provider); err != nil {
		return time.Time{}, fmt.Errorf("设置HTTP-01验证提供者失败: %w", err)
	}

	domains := []string{domain}
	if aliases != "" {
		for _, a := range strings.Split(aliases, "\n") {
			a = strings.TrimSpace(a)
			if a != "" && a != domain {
				domains = append(domains, a)
			}
		}
	}

	req := certificate.ObtainRequest{
		Domains: domains,
		Bundle:  true,
	}

	certRes, err := client.Certificate.Obtain(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("获取证书失败: %w", err)
	}

	certPath := filepath.Join(certDir, "fullchain.pem")
	keyPath := filepath.Join(certDir, "privkey.pem")

	if err := os.WriteFile(certPath, certRes.Certificate, 0644); err != nil {
		return time.Time{}, fmt.Errorf("保存证书失败: %w", err)
	}
	if err := os.WriteFile(keyPath, certRes.PrivateKey, 0600); err != nil {
		return time.Time{}, fmt.Errorf("保存私钥失败: %w", err)
	}

	expiry, err := validateCertificate(certPath, domain)
	if err != nil {
		return time.Time{}, fmt.Errorf("验证签发证书失败: %w", err)
	}

	return expiry, nil
}

type webrootProvider struct {
	webroot string
}

func (w *webrootProvider) Present(domain, token, keyAuth string) error {
	challengePath := filepath.Join(w.webroot, ".well-known", "acme-challenge")
	if err := os.MkdirAll(challengePath, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(challengePath, token), []byte(keyAuth), 0644)
}

func (w *webrootProvider) CleanUp(domain, token, keyAuth string) error {
	challengeFile := filepath.Join(w.webroot, ".well-known", "acme-challenge", token)
	os.Remove(challengeFile)
	return nil
}

func applySSLToSite(site *models.Website, certPath, keyPath string, expiry time.Time) error {
	cfg := config.AppConfig

	engine := NewTemplateEngine(cfg.Panel.BackupDir)
	nginxData := nginxDataFromSite(site)
	nginxData.UseSSL = true
	nginxData.SSLCertPath = certPath
	nginxData.SSLKeyPath = keyPath

	nginxConfig, err := engine.RenderNginxConfig(nginxData)
	if err != nil {
		return fmt.Errorf("渲染 Nginx 配置失败: %w", err)
	}

	if err := engine.ApplyNginxConfig(nginxConfig, site.NginxConfPath,
		filepath.Join(cfg.Paths.NginxSitesEnabled, site.Domain+".conf")); err != nil {
		return fmt.Errorf("应用 Nginx 配置失败: %w", err)
	}

	db := database.GetDB()
	_, err = db.Exec(
		`UPDATE websites SET ssl_enabled = 1, ssl_cert_path = ?, ssl_key_path = ?, ssl_expires_at = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		certPath, keyPath, expiry, site.ID,
	)
	return err
}

func validateCertificate(certPath string, domain string) (time.Time, error) {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return time.Time{}, fmt.Errorf("读取证书文件失败: %w", err)
	}

	var expiry time.Time
	found := false

	for rest := data; len(rest) > 0; {
		block, remaining := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = remaining

		if block.Type == "CERTIFICATE" {
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				continue
			}
			if !cert.IsCA {
				now := time.Now()
				if now.After(cert.NotAfter) {
					return time.Time{}, fmt.Errorf("证书已过期（到期: %s）", cert.NotAfter.Format("2006-01-02"))
				}
				if now.Before(cert.NotBefore) {
					return time.Time{}, fmt.Errorf("证书尚未生效（生效: %s）", cert.NotBefore.Format("2006-01-02"))
				}
				if err := cert.VerifyHostname(domain); err != nil {
					matched := false
					certDomains := cert.DNSNames
					if len(certDomains) == 0 && cert.Subject.CommonName != "" {
						certDomains = []string{cert.Subject.CommonName}
					}
					for _, san := range certDomains {
						if san == domain {
							matched = true
							break
						}
					}
					if !matched {
						hint := strings.Join(certDomains, ", ")
						if hint == "" {
							hint = "(证书未包含任何域名)"
						}
						return time.Time{}, fmt.Errorf("证书与域名 %s 不匹配，证书包含的域名: %s", domain, hint)
					}
				}
				expiry = cert.NotAfter
				found = true
			}
		}
	}

	if !found {
		return time.Time{}, fmt.Errorf("未找到有效的证书内容")
	}

	return expiry, nil
}

func executeRenewSSL(task *Task) TaskResult {
	cfg := config.AppConfig
	db := database.GetDB()

	rows, err := db.Query(
		`SELECT id, name, domain, aliases, status, system_user, web_root, log_dir,
		        db_name, db_user, php_pool_path, nginx_conf_path, ssl_enabled,
		        ssl_cert_path, ssl_key_path, template_version, ssl_expires_at
		 FROM websites WHERE ssl_enabled = 1 AND ssl_cert_path != ''`,
	)
	if err != nil {
		log.Printf("查询SSL站点失败: %v", err)
		return TaskResult{Success: false, Message: "查询SSL站点失败"}
	}
	defer rows.Close()

	var renewed []string
	var failed []string
	now := time.Now()
	renewThreshold := now.AddDate(0, 0, 30)

	for rows.Next() {
		var w models.Website
		var aliases string
		var status string
		var sslEnabled int
		var sslExpiresAt *time.Time
		if scanErr := rows.Scan(
			&w.ID, &w.Name, &w.Domain, &aliases, &status, &w.SystemUser,
			&w.WebRoot, &w.LogDir, &w.DBName, &w.DBUser, &w.PHPPoolPath,
			&w.NginxConfPath, &sslEnabled, &w.SSLCertPath, &w.SSLKeyPath,
			&w.TemplateVersion, &sslExpiresAt,
		); scanErr != nil {
			failed = append(failed, w.Domain+"(读取失败)")
			continue
		}
		w.Aliases = aliases
		w.Status = models.WebsiteStatus(status)
		w.SSLEnabled = sslEnabled == 1
		w.SSLExpiresAt = sslExpiresAt

		expiry, certErr := validateCertificate(w.SSLCertPath, w.Domain)
		if certErr != nil {
			log.Printf("SSL证书异常 domain=%s: %v", w.Domain, certErr)
			failed = append(failed, w.Domain+"(证书异常)")
			continue
		}

		if expiry.After(renewThreshold) {
			continue
		}

		if expiry.Before(now) {
			failed = append(failed, w.Domain+"(证书已过期)")
			continue
		}

		newExpiry, renewErr := obtainLegoCert(w.Domain, w.Aliases, w.WebRoot,
			filepath.Join(cfg.Paths.Certificates, w.Domain))
		if renewErr != nil {
			log.Printf("SSL续期失败 domain=%s: %v", w.Domain, renewErr)
			failed = append(failed, w.Domain+"(续期失败)")
			continue
		}

		db.Exec("UPDATE websites SET ssl_expires_at = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
			newExpiry, w.ID)

		renewed = append(renewed, w.Domain)
	}

	if len(renewed) > 0 {
		exec.Command("nginx", "-s", "reload").Run()
	}

	msg := fmt.Sprintf("续期完成。成功: %d", len(renewed))
	if len(failed) > 0 {
		msg += "; 失败: " + strings.Join(failed, ", ")
	}

	if len(renewed) > 0 {
		log.Printf("SSL 自动续期: %s", msg)
	}

	return TaskResult{Success: true, Message: msg, Data: map[string]interface{}{"renewed": renewed, "failed": failed}}
}

func StartSSLRenewalScheduler() {
	go func() {
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day()+1, 3, 0, 0, 0, now.Location())
			time.Sleep(next.Sub(now))
			GlobalQueue.Enqueue(TaskRenewSSL, nil)
		}
	}()
}

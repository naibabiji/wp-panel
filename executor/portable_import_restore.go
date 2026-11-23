package executor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (s *PortableImports) restore(task PortableTask, manifest portableManifest, work, tree string) error {
	maintenance, err := acquireMaintenanceLifecycleFile(s.cfg)
	if err != nil {
		return ErrPortableBusy
	}
	defer maintenance.Close()
	creation, err := acquireSiteCreationLock()
	if err != nil {
		return ErrPortableBusy
	}
	defer creation.Close()

	if err := s.phase(task.ID, "running", "resources", ""); err != nil {
		return err
	}
	if err := checkPortableDomainAndMigration(context.Background(), task.Domain, task.Aliases); err != nil {
		return err
	}
	res, err := deriveProvisioningResources(s.cfg, task.Domain, "wordpress", buildSiteName(task.Domain))
	if err != nil {
		return err
	}
	if err := ensureCreateSiteResourcesAvailable(res.SystemUser, res.WebRoot, res.LogDir, res.DBName, res.DBUser, res.PHPFPMPath, res.NginxConfigPath, res.NginxEnabledPath, res.PHPSocketPath); err != nil {
		return err
	}
	certDir := filepath.Join(s.cfg.Paths.Certificates, task.Domain)
	for _, path := range portableRetryPaths(s, task.Domain, res) {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return fmt.Errorf("站点资源已存在或无法检查：%s", filepath.Base(path))
		}
	}
	if group, err := portableGroupIdentity(res.SystemUser); err != nil || group != "" {
		return fmt.Errorf("站点用户组已存在或无法核对：%s", res.SystemUser)
	}
	if err := s.checkPortableDatabaseAvailable(res); err != nil {
		return err
	}
	proof := &portableCleanupProof{Attempt: strings.ReplaceAll(uuid.NewString(), "-", ""), Owned: make(map[string]string)}
	if err := s.saveCleanupProof(task.ID, proof); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE portable_import_tasks SET log_dir=?, system_user=?, web_root=?, db_name=?, db_user=?, nginx_config=?, php_pool=? WHERE id=?`,
		res.LogDir, res.SystemUser, res.WebRoot, res.DBName, res.DBUser, res.NginxConfigPath, res.PHPFPMPath, task.ID); err != nil {
		return err
	}
	accountCommand := func(command string, args ...string) (string, error) {
		if command == "useradd" {
			args = append([]string{"-c", portableOwnerLabel(proof.Attempt)}, args...)
		}
		return executeCommand(command, args...)
	}
	if _, err := createSiteSystemUser(res.SystemUser, accountCommand, ensureSitePrimaryGroup); err != nil {
		return err
	}
	proof.Owned["user"], err = portableAccountIdentity(res.SystemUser)
	if err != nil || !strings.HasPrefix(proof.Owned["user"], portableOwnerLabel(proof.Attempt)+":") {
		return errors.New("无法记录本次创建的系统账号归属")
	}
	proof.Owned["group"], err = portableGroupIdentity(res.SystemUser)
	if err != nil || proof.Owned["group"] == "" {
		return errors.New("无法记录本次创建的系统用户组归属")
	}
	if err := s.saveCleanupProof(task.ID, proof); err != nil {
		return err
	}
	if err := os.Mkdir(res.LogDir, 0755); err != nil {
		return err
	}
	if err := s.recordPortablePaths(task.ID, proof, res.LogDir); err != nil {
		return err
	}
	ensureSiteLogFiles(res.LogDir)
	phpLog := filepath.Join(res.LogDir, "php-error.log")
	if _, err := executeCommand("chown", siteOwner(res.SystemUser), phpLog); err != nil {
		return fmt.Errorf("准备站点 PHP 错误日志失败：%w", err)
	}
	if err := os.Chmod(phpLog, 0640); err != nil {
		return err
	}

	engine := NewTemplateEngine(s.cfg.Panel.BackupDir)
	serverNames := buildServerNames(task.Domain, task.Aliases)
	useSSL := strings.HasPrefix(task.URL, "https://")
	var issuedUntil time.Time
	var source, cert, key, fingerprint, sslWarning string
	acmeRoot := filepath.Join(res.LogDir, ".portable-acme")
	if err := os.Mkdir(acmeRoot, 0755); err != nil {
		return err
	}
	defer os.RemoveAll(acmeRoot)
	if useSSL {
		if err := os.Mkdir(certDir, 0700); err != nil {
			return err
		}
		if err := s.recordPortablePaths(task.ID, proof, certDir); err != nil {
			return err
		}
		cert, key = filepath.Join(certDir, "fullchain.pem"), filepath.Join(certDir, "privkey.pem")
		issuedUntil, err = writePortableTemporaryCertificate(task.Domain, task.Aliases, cert, key)
		if err != nil {
			return err
		}
		source = "manual"
		fingerprint, err = migrationCertificateFingerprint(cert)
		if err != nil {
			return err
		}
	}
	maintenanceConfig := portableMaintenanceConfig(serverNames, acmeRoot, cert, key)
	if err := engine.ApplyNginxConfig(maintenanceConfig, res.NginxConfigPath, res.NginxEnabledPath); err != nil {
		return fmt.Errorf("启用恢复维护页失败：%w", err)
	}

	if err := s.recordPortablePaths(task.ID, proof, res.NginxConfigPath, res.NginxEnabledPath); err != nil {
		return err
	}
	dbPassword := generatePassword(32)
	if err := s.createPortableDatabase(task.ID, proof, res, dbPassword); err != nil {
		return err
	}
	if err := s.phase(task.ID, "running", "files", ""); err != nil {
		return err
	}
	if _, err := executeCommand("chown", "-R", siteOwner(res.SystemUser), tree); err != nil {
		return fmt.Errorf("设置网站文件归属失败：%w", err)
	}
	if err := publishRestoredTree(tree, res.WebRoot); err != nil {
		return fmt.Errorf("发布网站目录失败：%w", err)
	}
	if err := s.recordPortablePaths(task.ID, proof, res.WebRoot); err != nil {
		return err
	}
	if err := s.phase(task.ID, "running", "database", ""); err != nil {
		return err
	}
	if err := s.restorePortableSQL(res.DBName, res.DBUser, dbPassword, filepath.Join(work, "database.sql"), manifest); err != nil {
		return err
	}

	if err := s.phase(task.ID, "running", "configuration", ""); err != nil {
		return err
	}
	if err := generateWPConfigForPrefix(res.WebRoot, task.Domain, res.DBName, res.DBUser, dbPassword, manifest.Source.Prefix); err != nil {
		return fmt.Errorf("生成目标 WordPress 配置失败：%w", err)
	}
	if err := HardenSiteSensitivePermissions(task.Domain, res.WebRoot, res.SystemUser); err != nil {
		return err
	}
	apiKey := generatePassword(64)
	panelURL := fmt.Sprintf("https://127.0.0.1:%d/%s", s.cfg.Panel.TLSPort, s.cfg.Panel.RandomSuffix)
	if err := WriteSitePluginIdentity(task.Domain, res.SystemUser, panelURL, apiKey, true); err != nil {
		return fmt.Errorf("生成本站面板身份失败：%w", err)
	}
	if err := s.recordPortablePaths(task.ID, proof, sitePluginSecretsDir(task.Domain)); err != nil {
		return err
	}
	children := RecommendPHPFPMMaxChildren(CollectSystemFacts())
	poolName := strings.TrimSuffix(filepath.Base(res.PHPFPMPath), ".conf")
	pool, err := engine.RenderPHPFPMPool(&PHPFPMPoolData{
		Domain: task.Domain, PoolName: poolName, SystemUser: res.SystemUser, WebRoot: res.WebRoot,
		SocketPath: s.cfg.Paths.PHPFPMSock, SocketName: poolName, MaxChildren: strconv.Itoa(children),
	})
	if err != nil {
		return err
	}
	if err := engine.ApplyPHPFPMPool(pool, res.PHPFPMPath, res.LogDir, res.PHPSocketPath); err != nil {
		return err
	}

	if err := s.recordPortablePaths(task.ID, proof, res.PHPFPMPath, res.PHPSocketPath); err != nil {
		return err
	}

	if useSSL {
		if err := s.phase(task.ID, "running", "https", ""); err != nil {
			return err
		}
		pending, err := os.MkdirTemp(filepath.Dir(certDir), ".portable-cert-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(pending)
		lookupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		expiry, certErr := obtainPortableCertificate(task.Aliases, func(alias string) bool {
			addresses, err := net.DefaultResolver.LookupHost(lookupContext, alias)
			return err == nil && len(addresses) > 0
		}, func(aliases string) (time.Time, error) {
			return obtainLegoCert(task.Domain, aliases, acmeRoot, pending)
		})
		cancel()
		if certErr != nil {
			sslWarning = FriendlySSLError(certErr)
		} else {
			previous := pending + "-temporary"
			if err := os.Rename(certDir, previous); err != nil {
				return err
			}
			if err := os.Rename(pending, certDir); err != nil {
				return errors.Join(err, os.Rename(previous, certDir))
			}
			defer os.RemoveAll(previous)
			if err := s.recordPortablePaths(task.ID, proof, certDir); err != nil {
				return err
			}
			issuedUntil, source, fingerprint = expiry, "auto", ""
		}
	}
	if err := s.phase(task.ID, "running", "publishing", ""); err != nil {
		return err
	}
	if err := checkPortableDomainAndMigration(context.Background(), task.Domain, task.Aliases); err != nil {
		return err
	}
	businessConfig, err := engine.RenderNginxConfig(&NginxSiteData{
		Domain: task.Domain, ServerNames: serverNames, WebRoot: res.WebRoot, LogDir: res.LogDir, SystemUser: res.SystemUser,
		SiteType: "wordpress", UseSSL: useSSL, SSLCertPath: cert, SSLKeyPath: key, PHPProxy: "unix:" + res.PHPSocketPath,
		TemplateVer: "v1.0", AccessLogMode: "error_only",
	})
	if err != nil {
		return err
	}
	// Once public configuration may have been loaded, ownership alone does
	// not establish that the site stayed unpublished.
	proof.Sealed = true
	if err := s.saveCleanupProof(task.ID, proof); err != nil {
		return err
	}
	if err := engine.ApplyNginxConfig(businessConfig, res.NginxConfigPath, res.NginxEnabledPath); err != nil {
		return fmt.Errorf("数据已恢复，网站配置未能发布：%w", err)
	}
	var expires any
	if useSSL {
		expires = issuedUntil
	}
	if err := s.completePortableSite(task, res, children, useSSL, cert, key, expires, source, fingerprint, apiKey, sslWarning); err != nil {
		return errors.Join(fmt.Errorf("数据已恢复，保存网站登记失败：%w", err),
			engine.ApplyNginxConfig(maintenanceConfig, res.NginxConfigPath, res.NginxEnabledPath))
	}
	logRecoveryFailure("创建恢复网站日志轮转", WriteSiteLogrotateConfig(task.Domain, res.LogDir, defaultSiteLogRetentionDays))
	logRecoveryFailure("重载 Fail2ban", ReloadFail2ban())
	return nil
}

func (s *PortableImports) checkPortableDatabaseAvailable(res siteProvisioningResources) error {
	cfg := s.cfg.MariaDB
	query := fmt.Sprintf("SELECT (SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='%s') + (SELECT COUNT(*) FROM mysql.user WHERE User='%s')", res.DBName, res.DBUser)
	cmd := exec.Command("mysql", "-u", cfg.RootUser, "-N", "-B", "-e", query)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+cfg.RootPassword)
	result, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(result)) != "0" {
		return errors.New("目标数据库或数据库账号已被占用，或无法核实其状态")
	}
	return nil
}

func (s *PortableImports) createPortableDatabase(id string, proof *portableCleanupProof, res siteProvisioningResources, password string) error {
	cfg := s.cfg.MariaDB
	query := fmt.Sprintf("CREATE DATABASE `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci COMMENT '%s'; CREATE USER '%s'@'localhost' IDENTIFIED BY '%s';",
		res.DBName, portableOwnerLabel(proof.Attempt), res.DBUser, password)
	if err := runMySQLInput(cfg.RootPassword, query, "-u", cfg.RootUser); err != nil {
		return errors.New("无法创建目标空数据库及站点专用账号")
	}
	recordAccount := func() error {
		_, account, err := portableDatabaseIdentity(s.portableMySQLQuery, res)
		if err != nil || account == "" || account == "unverified" {
			return errors.New("无法记录本次创建的数据库账号归属")
		}
		proof.Owned["database-user"] = account
		return s.saveCleanupProof(id, proof)
	}
	if err := recordAccount(); err != nil {
		return err
	}
	grant := fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'localhost';", strings.ReplaceAll(res.DBName, "_", `\_`), res.DBUser)
	if err := runMySQLInput(cfg.RootPassword, grant, "-u", cfg.RootUser); err != nil {
		return errors.New("无法授予站点数据库账号权限")
	}
	return recordAccount()
}

func (s *PortableImports) restorePortableSQL(dbName, dbUser, password, path string, manifest portableManifest) error {
	input, err := os.Open(path)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	// The site user is scoped to its own database. In batch mode, binary mode
	// disables local client commands; local file loading is disabled separately.
	client := exec.Command("mysql", "--binary-mode", "--local-infile=0", "--user="+dbUser, "--database="+dbName)
	if result := restoreSQLWithClient(input, client, password, info.Size(), &s.sqlProgress); !result.Success {
		return errors.New(result.Message)
	}
	siteURL, home, err := ReadWPSiteURLs(dbName, manifest.Source.Prefix, s.cfg)
	if err != nil || canonicalPortableURL(siteURL) != canonicalPortableURL(manifest.Source.SiteURL) ||
		canonicalPortableURL(home) != canonicalPortableURL(manifest.Source.Home) {
		return errors.New("恢复后的数据库站点地址与备份清单不一致")
	}
	query := fmt.Sprintf("SELECT HEX(TABLE_NAME) FROM information_schema.TABLES WHERE TABLE_SCHEMA='%s' AND TABLE_TYPE='BASE TABLE'", dbName)
	cmd := exec.Command("mysql", "-u", s.cfg.MariaDB.RootUser, "-N", "-B", "-e", query)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+s.cfg.MariaDB.RootPassword)
	result, err := cmd.Output()
	if err != nil {
		return errors.New("无法核对恢复后的数据库表")
	}
	names := strings.Fields(string(result))
	if len(names) != len(manifest.Tables) {
		return errors.New("恢复后的数据库表数量与备份不一致")
	}
	tables := make(map[string]bool, len(names))
	for _, name := range names {
		decoded, err := hex.DecodeString(name)
		if err != nil {
			return errors.New("无法核对恢复后的数据库表名")
		}
		tables[string(decoded)] = true
	}
	for _, table := range manifest.Tables {
		if !tables[table.Name] {
			return fmt.Errorf("恢复后的数据库缺少表 %s", table.Name)
		}
	}
	return nil
}

func canonicalPortableURL(raw string) string {
	address, err := url.Parse(raw)
	if err != nil || address.User != nil || address.ForceQuery || address.RawQuery != "" || address.Fragment != "" ||
		(address.Path != "" && address.Path != "/") {
		return ""
	}
	port := address.Port()
	if port != "" && (address.Scheme != "http" || port != "80") && (address.Scheme != "https" || port != "443") {
		return ""
	}
	return address.Scheme + "://" + strings.ToLower(address.Hostname())
}

func (s *PortableImports) completePortableSite(task PortableTask, res siteProvisioningResources, children int, useSSL bool, cert, key string, expires any, source, fingerprint, apiKey, warning string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	row, err := tx.Exec(createWebsiteInsertSQL, res.SiteName, task.Domain, strings.Join(task.Aliases, "\n"), res.SystemUser, res.WebRoot, "", res.LogDir,
		res.DBName, res.DBUser, res.PHPFPMPath, res.NginxConfigPath, "wordpress", boolInt(useSSL), cert, key, expires, "", source, defaultSiteLogRetentionDays, children, nil)
	if err != nil {
		return err
	}
	id, err := row.LastInsertId()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE websites SET plugin_api_key=?, ssl_temporary_fingerprint=? WHERE id=?", apiKey, fingerprint, id); err != nil {
		return err
	}
	now := time.Now()
	row, err = tx.Exec(`UPDATE portable_import_tasks SET site_id=?, status='succeeded', phase='completed', cleanup_proof='',
		message=?, updated_at=?, expires_at=? WHERE id=? AND status='running'`, id, warning, now.Unix(), now.Unix(), task.ID)
	if err != nil {
		return err
	}
	count, err := row.RowsAffected()
	if err != nil || count != 1 {
		return errors.New("恢复任务状态未更新")
	}
	return tx.Commit()
}

// Try ready aliases together, then give the primary domain its own order if an alias blocks issuance.
func obtainPortableCertificate(aliases []string, resolves func(string) bool, obtain func(string) (time.Time, error)) (time.Time, error) {
	var candidates []string
	for _, alias := range aliases {
		if resolves(alias) {
			candidates = append(candidates, alias)
		}
	}
	if len(candidates) > 0 {
		if expiry, err := obtain(strings.Join(candidates, "\n")); err == nil {
			return expiry, nil
		}
	}
	return obtain("")
}

func portableMaintenanceConfig(serverNames, acmeRoot, cert, key string) string {
	maintenance := "location / { default_type text/html; add_header Cache-Control no-store always; add_header Retry-After 300 always; return 503 '<!doctype html><meta charset=utf-8><h1>Website restoration in progress</h1><p>Please try again later.</p>'; }"
	http := fmt.Sprintf("server { listen 80; listen [::]:80; server_name %s; root %s; location ^~ /.well-known/acme-challenge/ { try_files $uri =404; } %s }\n", serverNames, strconv.Quote(acmeRoot), maintenance)
	if cert == "" {
		return http
	}
	return http + fmt.Sprintf("server { listen 443 ssl; listen [::]:443 ssl; server_name %s; ssl_certificate %s; ssl_certificate_key %s; %s }\n", serverNames, strconv.Quote(cert), strconv.Quote(key), maintenance)
}

func writePortableTemporaryCertificate(domain string, aliases []string, certPath, keyPath string) (time.Time, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return time.Time{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return time.Time{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	expiry := now.Add(30 * 24 * time.Hour)
	cert := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: domain}, DNSNames: append([]string{domain}, aliases...),
		NotBefore: now.Add(-time.Minute), NotAfter: expiry,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	content, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		return time.Time{}, err
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return time.Time{}, err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: content}), 0644); err != nil {
		return time.Time{}, err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), 0600); err != nil {
		return time.Time{}, err
	}
	return expiry, nil
}

package handlers

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"
	"golang.org/x/crypto/ssh"
)

type AIDevelopmentAccessHandler struct{}

type aiDevelopmentCredential struct {
	PrivateKey  []byte
	PublicKey   string
	Fingerprint string
}

func (h *AIDevelopmentAccessHandler) Status(c *gin.Context) {
	siteID, site, ok := aiDevelopmentSiteFromRequest(c)
	if !ok {
		return
	}
	item, err := database.GetAIDevelopmentAccess(c.Request.Context(), database.GetDB(), int64(siteID))
	if errors.Is(err, database.ErrAIDevelopmentAccessNotFound) {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
			"enabled": false, "status": "disabled", "system_user": site.SystemUser,
			"web_root": site.WebRoot, "tools": executor.DevelopmentToolsStatus(c.Request.Context()),
		}))
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "common.operation_failed")))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"enabled": item.Status == "enabled", "status": item.Status, "operation": item.Operation,
		"system_user": item.SystemUser, "web_root": item.WebRoot, "key_fingerprint": item.KeyFingerprint,
		"enabled_at": item.EnabledAt, "last_error": item.LastError,
		"tools": executor.DevelopmentToolsStatus(c.Request.Context()),
	}))
}

func (h *AIDevelopmentAccessHandler) Enable(c *gin.Context) {
	_, site, ok := aiDevelopmentSiteFromRequest(c)
	if !ok {
		return
	}
	var req struct {
		ConfirmDomain string `json:"confirm_domain"`
		InstallWPCLI  bool   `json:"install_wp_cli"`
		InstallNodeJS bool   `json:"install_nodejs"`
		Force         bool   `json:"force"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.ConfirmDomain) != site.Domain {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "ai_development.confirm_domain_invalid")))
		return
	}
	if site.FileLockEnabled {
		c.JSON(http.StatusLocked, models.ErrorResponse(i18n.TE(c.Request, "ai_development.file_lock_enabled")))
		return
	}
	knownHosts, err := executor.ReadSSHHostKnownHosts()
	if err != nil {
		log.Printf("读取 SSH 主机公钥失败 site=%d: %v", site.ID, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "common.operation_failed")))
		return
	}
	if req.InstallWPCLI {
		if err := executor.InstallDevelopmentTool(c.Request.Context(), "wp-cli"); err != nil {
			log.Printf("安装 AI 开发组件失败 tool=wp-cli site=%d: %v", site.ID, err)
			if errors.Is(err, executor.ErrWPCLIProxyDownloadFailed) {
				c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "software.wp_cli_proxy_download_failed")))
				return
			}
			c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_development.tool_install_failed", i18n.P{"tool": "WP-CLI"})))
			return
		}
	}
	if req.InstallNodeJS {
		if err := executor.InstallDevelopmentTool(c.Request.Context(), "nodejs"); err != nil {
			log.Printf("安装 AI 开发组件失败 tool=nodejs site=%d: %v", site.ID, err)
			c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_development.tool_install_failed", i18n.P{"tool": "Node.js + npm"})))
			return
		}
	}
	credential, err := generateAIDevelopmentCredential()
	if err != nil {
		log.Printf("生成 AI SSH 密钥失败 site=%d: %v", site.ID, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "common.operation_failed")))
		return
	}
	service := executor.NewAIDevelopmentAccessService(database.GetDB())
	if err := service.Enable(c.Request.Context(), websiteAIDevelopmentSite(site), credential.PublicKey, credential.Fingerprint, sessionUsername(c), req.Force); err != nil {
		log.Printf("开启 AI 开发访问失败 site=%d force=%v: %v", site.ID, req.Force, err)
		if !req.Force && errors.Is(err, executor.ErrAIDevelopmentSiteBusy) {
			c.JSON(http.StatusConflict, models.ApiResponse{
				Success: false,
				Message: i18n.TE(c.Request, "ai_development.enable_failed_busy"),
				Data:    gin.H{"code": "ai_development_site_busy"},
			})
			return
		}
		c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "ai_development.enable_failed")))
		return
	}
	host := requestSSHHost(c.Request)
	packageData, err := buildAIDevelopmentCredentialPackage(site.Domain, host, executor.DetectSSHPort(c.Request.Context()), site.SystemUser, site.WebRoot, knownHosts, credential)
	if err != nil {
		log.Printf("构建 AI SSH 连接包失败 site=%d: %v", site.ID, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_development.package_failed_rotate")))
		return
	}
	writeAIDevelopmentPackage(c, site.Domain, packageData)
}

func (h *AIDevelopmentAccessHandler) Rotate(c *gin.Context) {
	_, site, ok := aiDevelopmentSiteFromRequest(c)
	if !ok {
		return
	}
	knownHosts, err := executor.ReadSSHHostKnownHosts()
	if err != nil {
		log.Printf("读取 SSH 主机公钥失败 site=%d: %v", site.ID, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "common.operation_failed")))
		return
	}
	credential, err := generateAIDevelopmentCredential()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "common.operation_failed")))
		return
	}
	service := executor.NewAIDevelopmentAccessService(database.GetDB())
	if err := service.Rotate(c.Request.Context(), websiteAIDevelopmentSite(site), credential.PublicKey, credential.Fingerprint); err != nil {
		log.Printf("轮换 AI SSH 密钥失败 site=%d: %v", site.ID, err)
		c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "ai_development.rotate_failed")))
		return
	}
	packageData, err := buildAIDevelopmentCredentialPackage(site.Domain, requestSSHHost(c.Request), executor.DetectSSHPort(c.Request.Context()), site.SystemUser, site.WebRoot, knownHosts, credential)
	if err != nil {
		log.Printf("构建 AI SSH 轮换连接包失败 site=%d: %v", site.ID, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "ai_development.package_failed_rotate")))
		return
	}
	writeAIDevelopmentPackage(c, site.Domain, packageData)
}

func (h *AIDevelopmentAccessHandler) Disable(c *gin.Context) {
	siteID, _, ok := aiDevelopmentSiteFromRequest(c)
	if !ok {
		return
	}
	service := executor.NewAIDevelopmentAccessService(database.GetDB())
	if err := service.Disable(c.Request.Context(), int64(siteID)); err != nil {
		log.Printf("关闭 AI 开发访问失败 site=%d: %v", siteID, err)
		c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "ai_development.disable_failed")))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": i18n.TE(c.Request, "ai_development.disabled")}))
}

func aiDevelopmentSiteFromRequest(c *gin.Context) (int, *models.Website, bool) {
	siteID, err := strconv.Atoi(c.Param("id"))
	if err != nil || siteID <= 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "common.invalid_params")))
		return 0, nil, false
	}
	site := getWebsiteByID(siteID)
	if site == nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse(i18n.TE(c.Request, "ai_development.site_not_found")))
		return 0, nil, false
	}
	return siteID, site, true
}

func websiteAIDevelopmentSite(site *models.Website) executor.AIDevelopmentSite {
	return executor.AIDevelopmentSite{
		ID: int64(site.ID), Domain: site.Domain, SystemUser: site.SystemUser, WebRoot: site.WebRoot,
		LogDir: site.LogDir, PHPPoolPath: site.PHPPoolPath, NginxConfPath: site.NginxConfPath,
		DBName: site.DBName, DBUser: site.DBUser,
	}
}

func generateAIDevelopmentCredential() (aiDevelopmentCredential, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return aiDevelopmentCredential{}, err
	}
	privateBlock, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		return aiDevelopmentCredential{}, err
	}
	sshPublic, err := ssh.NewPublicKey(public)
	if err != nil {
		return aiDevelopmentCredential{}, err
	}
	return aiDevelopmentCredential{
		PrivateKey:  pem.EncodeToMemory(privateBlock),
		PublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPublic))),
		Fingerprint: ssh.FingerprintSHA256(sshPublic),
	}, nil
}

func buildAIDevelopmentCredentialPackage(domain, host string, port int, systemUser, webRoot, knownHosts string, credential aiDevelopmentCredential) ([]byte, error) {
	if strings.TrimSpace(knownHosts) == "" {
		return nil, errors.New("SSH host keys are unavailable")
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	projectName := aiDevelopmentProjectName(domain)
	prefix := projectName + "/"
	files := map[string]struct {
		content []byte
		mode    os.FileMode
	}{
		prefix + "AGENTS.md":                  {content: []byte(buildAIDevelopmentProjectInstructions(domain, webRoot)), mode: 0644},
		prefix + "CLAUDE.md":                  {content: []byte("Read and follow AGENTS.md completely before working on this project.\n"), mode: 0644},
		prefix + "README.md":                  {content: []byte(buildAIDevelopmentProjectReadme(domain, projectName)), mode: 0644},
		prefix + ".gitignore":                 {content: []byte(".wp-panel-ai/\n"), mode: 0644},
		prefix + ".wp-panel-ai/id_ed25519":    {content: credential.PrivateKey, mode: 0600},
		prefix + ".wp-panel-ai/known_hosts":   {content: []byte(knownHosts), mode: 0600},
		prefix + ".wp-panel-ai/ssh_config":    {content: []byte(fmt.Sprintf("Host wp-panel-ai\n  HostName %s\n  Port %d\n  User %s\n  HostKeyAlias wp-panel-ai-target\n  IdentitiesOnly yes\n  StrictHostKeyChecking yes\n", host, port, systemUser)), mode: 0600},
		prefix + ".wp-panel-ai/CONNECTION.md": {content: []byte(buildAIDevelopmentConnectionGuide(domain, host, port, systemUser, webRoot)), mode: 0644},
		prefix + ".wp-panel-ai/connect.sh":    {content: []byte("#!/bin/sh\nset -eu\ncredential_dir=$(CDPATH= cd -- \"$(dirname -- \"$0\")\" && pwd)\nkey=$credential_dir/id_ed25519\nconfig=$credential_dir/ssh_config\nknown_hosts=$credential_dir/known_hosts\n\nfor required_file in \"$key\" \"$config\" \"$known_hosts\"; do\n  if [ ! -f \"$required_file\" ] || [ -L \"$required_file\" ]; then\n    echo \"WP Panel AI connection file is missing or is not a regular file: $required_file\" >&2\n    exit 1\n  fi\ndone\nif ! chmod 600 \"$key\" \"$known_hosts\"; then\n  echo \"Unable to secure the WP Panel AI connection files. Ensure the current user owns them.\" >&2\n  exit 1\nfi\n\nexec ssh -F \"$config\" -i \"$key\" -o \"UserKnownHostsFile=$known_hosts\" wp-panel-ai \"$@\"\n"), mode: 0700},
		prefix + ".wp-panel-ai/connect.ps1":   {content: []byte("param(\n    [Parameter(Position = 0, ValueFromRemainingArguments = $true)]\n    [string[]]$RemoteCommand\n)\n\n$ErrorActionPreference = 'Stop'\n$CredentialDir = Split-Path -Parent $MyInvocation.MyCommand.Path\n$ConfigPath = Join-Path $CredentialDir 'ssh_config'\n$KeyPath = Join-Path $CredentialDir 'id_ed25519'\n$KnownHostsPath = Join-Path $CredentialDir 'known_hosts'\n$CurrentIdentity = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name\n\nforeach ($ProtectedPath in @($KeyPath, $KnownHostsPath)) {\n    & icacls.exe $ProtectedPath /inheritance:r /grant:r \"$($CurrentIdentity):(F)\" | Out-Null\n    if ($LASTEXITCODE -ne 0) {\n        throw \"Unable to secure the WP Panel AI connection file: $ProtectedPath\"\n    }\n}\n\n$SSHArguments = @('-F', $ConfigPath, '-i', $KeyPath, '-o', \"UserKnownHostsFile=$KnownHostsPath\", 'wp-panel-ai')\nif ($RemoteCommand.Count -gt 0) {\n    $SSHArguments += ($RemoteCommand -join ' ')\n}\n\n& ssh.exe @SSHArguments\nexit $LASTEXITCODE\n"), mode: 0644},
	}
	for name, file := range files {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(file.mode)
		writer, err := archive.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err := writer.Write(file.content); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func buildAIDevelopmentConnectionGuide(domain, host string, port int, systemUser, webRoot string) string {
	return fmt.Sprintf(`# Connection details

- Site: `+"`%s`"+`
- Remote WebRoot: `+"`%s`"+`
- SSH user: `+"`%s`"+`
- SSH host: `+"`%s`"+`
- SSH port: `+"`%d`"+`

Run `+"`bash .wp-panel-ai/connect.sh`"+` on Linux, macOS, or WSL, or `+"`.\\.wp-panel-ai\\connect.ps1`"+` on Windows PowerShell. The package pins the server identity in its own `+"`known_hosts`"+` file, so a new computer does not need a prior global SSH record and must not click through an unknown fingerprint prompt.

If connection fails:

- "Host key verification failed" means the server identity differs from this package. Stop and ask the administrator to verify the server or generate a new package; never disable host-key checking.
- "Permission denied (publickey)" usually means the package was invalidated, AI access was closed, or the private key permissions are wrong.
- Timeout, unreachable, or refused errors mean the network, address, port, or SSH service is unavailable.
- A missing-file message means the ZIP was not fully extracted. Keep all files together and retry.

After connecting, read `+"`~/WP-PANEL-AI-HANDOFF.md`"+` on the server.
`, domain, webRoot, systemUser, host, port)
}

func buildAIDevelopmentProjectInstructions(domain, webRoot string) string {
	return fmt.Sprintf(`# WP Panel Remote AI Project

This package connects to %[1]s. Website files remain on the server at %[2]s.

## Start here

1. Connect with bash .wp-panel-ai/connect.sh on Linux, macOS, or WSL, or .\.wp-panel-ai\connect.ps1 on Windows PowerShell.
2. Read ~/WP-PANEL-AI-HANDOFF.md on the server and confirm the site and WebRoot.
3. When the task involves WP Panel settings or server-level behavior, read ~/WP-PANEL-CAPABILITIES.md before acting or recommending a panel workflow.

## WP Panel boundaries

- Work only on this website and its database. Do not use sudo or modify WP Panel, system services or other sites.
- Do not print, copy, commit or upload passwords, private keys, cookies, tokens, API keys, salts, full wp-config.php contents, Git credentials or payment credentials.
- If WP-CLI, Node.js/npm or another system component is missing, use the current server-side operations.software capability to guide the administrator. Do not attempt system installation or rely on a menu name copied into this package.
- Never print, copy, commit or upload anything inside .wp-panel-ai. It contains connection credentials, the pinned server identity, connection scripts and connection details, and is excluded by .gitignore.
- AI development access cannot coexist with a WordPress maintenance window or site migration. The administrator must close AI development access before using either WP Panel workflow; closing access terminates this SSH session.
- Treat the current server-side ~/WP-PANEL-AI-HANDOFF.md and ~/WP-PANEL-CAPABILITIES.md as authoritative. The downloaded package can become older than the running panel; if it conflicts with either server document, follow the server document. Do not copy capability claims from this local file or rely on remembered menu names.
`, domain, webRoot)
}

func buildAIDevelopmentProjectReadme(domain, projectName string) string {
	return fmt.Sprintf(`# %s AI project

1. Extract this ZIP to a secure local location.
2. Open the extracted %s folder as the project in your AI development tool.
3. Copy the first-message prompt shown by WP Panel and send it to the AI.
4. The AI connects to the website and reads the current server-side handoff documents.

The hidden .wp-panel-ai folder contains the one-time SSH credential, pinned server identity, connection scripts and connection details. It is excluded by .gitignore. Never share or commit it.

Generating a replacement package invalidates the old package and disconnects existing AI sessions. The server-side documents are refreshed by the running WP Panel and are authoritative even when this downloaded package is older.
`, domain, projectName)
}

func aiDevelopmentProjectName(domain string) string {
	var name strings.Builder
	for _, character := range strings.TrimSpace(domain) {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '-' || character == '_' {
			name.WriteRune(character)
		} else {
			name.WriteByte('-')
		}
	}
	base := strings.Trim(name.String(), ".-_")
	if base == "" {
		base = "website"
	}
	return base + "-wp-panel-ai"
}

func requestSSHHost(req *http.Request) string {
	host := req.Host
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return "SERVER_IP"
	}
	return host
}

func writeAIDevelopmentPackage(c *gin.Context, domain string, data []byte) {
	c.Header("Cache-Control", "no-store, private")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, aiDevelopmentProjectName(domain)))
	c.Data(http.StatusOK, "application/zip", data)
}

func sessionUsername(c *gin.Context) string {
	value, _ := c.Get("session_username")
	username, _ := value.(string)
	return strings.TrimSpace(username)
}

func rejectIfAIDevelopmentAccessActive(c *gin.Context, siteID int) bool {
	blocked, err := database.IsAIDevelopmentAccessBlocking(c.Request.Context(), database.GetDB(), int64(siteID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "common.operation_failed")))
		return true
	}
	if blocked {
		c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "ai_development.operation_blocked")))
		return true
	}
	return false
}

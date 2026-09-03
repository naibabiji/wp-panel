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
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.ConfirmDomain) != site.Domain {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "ai_development.confirm_domain_invalid")))
		return
	}
	if site.FileLockEnabled {
		c.JSON(http.StatusLocked, models.ErrorResponse(i18n.TE(c.Request, "ai_development.file_lock_enabled")))
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
	if err := service.Enable(c.Request.Context(), websiteAIDevelopmentSite(site), credential.PublicKey, credential.Fingerprint, sessionUsername(c)); err != nil {
		log.Printf("开启 AI 开发访问失败 site=%d: %v", site.ID, err)
		c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "ai_development.enable_failed")))
		return
	}
	host := requestSSHHost(c.Request)
	packageData, err := buildAIDevelopmentCredentialPackage(site.Domain, host, executor.DetectSSHPort(c.Request.Context()), site.SystemUser, site.WebRoot, credential)
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
	packageData, err := buildAIDevelopmentCredentialPackage(site.Domain, requestSSHHost(c.Request), executor.DetectSSHPort(c.Request.Context()), site.SystemUser, site.WebRoot, credential)
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
	return executor.AIDevelopmentSite{ID: int64(site.ID), Domain: site.Domain, SystemUser: site.SystemUser, WebRoot: site.WebRoot, DBName: site.DBName, DBUser: site.DBUser}
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

func buildAIDevelopmentCredentialPackage(domain, host string, port int, systemUser, webRoot string, credential aiDevelopmentCredential) ([]byte, error) {
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
		prefix + ".wp-panel-ai/ssh_config":    {content: []byte(fmt.Sprintf("Host wp-panel-ai\n  HostName %s\n  Port %d\n  User %s\n  IdentitiesOnly yes\n", host, port, systemUser)), mode: 0600},
		prefix + ".wp-panel-ai/CONNECTION.md": {content: []byte(fmt.Sprintf("# Connection details\n\n- Site: `%s`\n- Remote WebRoot: `%s`\n- SSH user: `%s`\n- SSH host: `%s`\n- SSH port: `%d`\n\nOn Linux, macOS, or WSL, run `bash .wp-panel-ai/connect.sh` from the project root. The script checks the required files and secures the private key before connecting. On Windows PowerShell, run `.\\.wp-panel-ai\\connect.ps1`. Then read `~/WP-PANEL-AI-HANDOFF.md` on the server.\n", domain, webRoot, systemUser, host, port)), mode: 0644},
		prefix + ".wp-panel-ai/connect.sh":    {content: []byte("#!/bin/sh\nset -eu\ncredential_dir=$(CDPATH= cd -- \"$(dirname -- \"$0\")\" && pwd)\nkey=$credential_dir/id_ed25519\nconfig=$credential_dir/ssh_config\n\nif [ ! -f \"$key\" ] || [ -L \"$key\" ]; then\n  echo \"WP Panel AI private key is missing or is not a regular file: $key\" >&2\n  exit 1\nfi\nif [ ! -f \"$config\" ] || [ -L \"$config\" ]; then\n  echo \"WP Panel AI SSH config is missing or is not a regular file: $config\" >&2\n  exit 1\nfi\nif ! chmod 600 \"$key\"; then\n  echo \"Unable to secure the WP Panel AI private key. Ensure the current user owns $key, then run: chmod 600 '$key'\" >&2\n  exit 1\nfi\n\nexec ssh -F \"$config\" -i \"$key\" wp-panel-ai \"$@\"\n"), mode: 0700},
		prefix + ".wp-panel-ai/connect.ps1":   {content: []byte("param(\n    [Parameter(Position = 0, ValueFromRemainingArguments = $true)]\n    [string[]]$RemoteCommand\n)\n\n$ErrorActionPreference = 'Stop'\n$CredentialDir = Split-Path -Parent $MyInvocation.MyCommand.Path\n$ConfigPath = Join-Path $CredentialDir 'ssh_config'\n$KeyPath = Join-Path $CredentialDir 'id_ed25519'\n$CurrentIdentity = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name\n\n& icacls.exe $KeyPath /inheritance:r /grant:r \"$($CurrentIdentity):(F)\" | Out-Null\nif ($LASTEXITCODE -ne 0) {\n    throw 'Unable to secure the SSH private key for the current Windows user.'\n}\n\n$SSHArguments = @('-F', $ConfigPath, '-i', $KeyPath, 'wp-panel-ai')\nif ($RemoteCommand.Count -gt 0) {\n    $SSHArguments += ($RemoteCommand -join ' ')\n}\n\n& ssh.exe @SSHArguments\nexit $LASTEXITCODE\n"), mode: 0644},
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

func buildAIDevelopmentProjectInstructions(domain, webRoot string) string {
	return fmt.Sprintf(`# WP Panel Remote AI Project

This local folder controls remote development for %[1]s. Website files remain on the server at %[2]s; do not copy secrets into this local project.

## Mandatory first-run workflow

Do not modify files or the database, install or update components, create accounts, or generate test data until the user has answered the safety questions and explicitly approved a written plan.

1. Connect with bash .wp-panel-ai/connect.sh on Linux, macOS, or WSL, or .\.wp-panel-ai\connect.ps1 on Windows PowerShell. The Unix script secures the private key before connecting, even when the ZIP extractor did not preserve file modes.
2. Read ~/WP-PANEL-AI-HANDOFF.md on the server and confirm the site and WebRoot.
3. Perform read-only discovery. Determine WordPress, PHP, WP-CLI and Node.js versions; active theme and child theme; installed and active plugins; custom code locations; multisite status; WooCommerce and likely payment, shipping, tax or external-integration components; Git repository, branch and working-tree state; build tools; and relevant logs. Never print passwords, private keys, cookies, tokens, API keys, salts, full wp-config.php contents, Git credentials, or payment credentials.
4. Create or update AI-CONTEXT.md in this local control-project root, not in the remote WebRoot. Record non-sensitive facts, unknowns, constraints and the date checked.
5. Explain the discovered environment in beginner-friendly language. Ask only what cannot be safely inferred: whether this is staging or production; whether a restorable backup exists; the desired outcome, references and acceptance criteria; business flows that must not be affected; permission to create test content, orders, users or a temporary WordPress account; third-party sandbox constraints; and the required Git workflow.
6. After the user answers, update AI-CONTEXT.md and create or update DEVELOPMENT-PLAN.md with scope, implementation steps, backup prerequisite, risks, test-data permissions, validation, rollback and acceptance criteria. Wait for explicit approval before making changes.
7. During approved work, maintain AI-CHANGELOG.md with changed files, database and test-data changes, commands or migrations, verification results, rollback notes and remaining work.

## Safety boundaries

- Work only on this website and its database. Do not use sudo or modify WP Panel, system services or other sites.
- Do not ask for root, WP Panel or database passwords. Most WordPress inspection should use WP-CLI. If browser-admin testing is genuinely required, explain why and ask permission for a temporary account; never record its password in project documents.
- Do not claim that a backup exists merely because WP Panel supports backups. Ask the user to confirm a recent restorable backup. Do not automatically create backups or start one yourself unless the user explicitly authorizes it.
- If WP-CLI, Node.js/npm or another system component is missing, ask the administrator to use WP Panel -> Software Management -> Development Tools. Do not attempt system installation.
- Never print, copy, commit or upload anything inside .wp-panel-ai. The directory contains the SSH private key and is excluded by .gitignore.
`, domain, webRoot)
}

func buildAIDevelopmentProjectReadme(domain, projectName string) string {
	return fmt.Sprintf(`# %s AI project

1. Extract this ZIP to a secure local location.
2. Open the extracted %s folder as the project in your AI development tool.
3. Copy the first-message prompt shown by WP Panel and send it to the AI.
4. The AI will inspect the site without changing it, create local context and plan documents, ask you the necessary safety questions, and wait for your approval before development.

The hidden .wp-panel-ai folder contains the one-time SSH credential and is excluded by .gitignore. Never share or commit it. If this folder is lost, generate a new package in WP Panel; doing so invalidates the old package and disconnects existing AI sessions.
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

package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

const siteProvisioningPlanTTL = 10 * time.Minute

// Kept as a narrow seam so the plan protocol can be tested without invoking
// the host's user, database, PHP-FPM and Nginx services.
var executeSiteProvisioningCore = executeCreateSiteUnlocked

type SiteProvisioningError struct {
	Code       string
	Message    string
	Diagnostic string
}

func (e *SiteProvisioningError) Error() string { return e.Message }

func siteProvisioningError(code, message string) error {
	return &SiteProvisioningError{Code: code, Message: message}
}

func SiteProvisioningErrorCode(err error) string {
	var target *SiteProvisioningError
	if errors.As(err, &target) {
		return target.Code
	}
	return "create_failed"
}

func SiteProvisioningPublicErrorMessage(err error) string {
	switch SiteProvisioningErrorCode(err) {
	case "plan_not_found":
		return "操作计划不存在，请重新执行 dry-run"
	case "plan_invalid":
		return "操作计划无效，请重新执行 dry-run"
	case "plan_expired":
		return "操作计划已过期，请重新执行 dry-run"
	case "plan_stale":
		return "网站创建条件已变化，请重新执行 dry-run"
	case "plan_already_used":
		return "操作计划已经执行过，请重新执行 dry-run"
	case "invalid_domain":
		return "域名格式不合法"
	case "domain_conflict":
		return "域名或别名已被其它网站占用"
	case "resource_conflict":
		return "网站资源已存在或冲突，请先检查服务器状态"
	case "site_create_busy":
		return "当前已有网站创建任务正在执行，请稍后重试"
	case "migration_locked":
		return "该域名正受网站搬家任务保护，请稍后重试"
	case "wordpress_package_unavailable":
		return "WordPress 安装包不可用，请先在面板准备安装包"
	case "rollback_required":
		return "网站创建失败，资源恢复不完整，请人工检查"
	default:
		return "网站创建失败，请检查面板操作日志"
	}
}

type siteCreationLock struct{ file *os.File }

func (l *siteCreationLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
}

func acquireSiteCreationLock() (*siteCreationLock, error) {
	if config.AppConfig == nil || strings.TrimSpace(config.AppConfig.Panel.DataDir) == "" {
		return nil, errors.New("面板数据目录未配置")
	}
	lockDir := filepath.Join(filepath.Clean(config.AppConfig.Panel.DataDir), "locks")
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(lockDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("建站锁目录不安全")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return nil, errors.New("建站锁目录属主不安全")
	}
	if err := os.Chmod(lockDir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(lockDir, "site-create.lock")
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm() != 0600 {
		_ = file.Close()
		return nil, errors.New("建站锁文件不安全")
	}
	if sys, ok := stat.Sys().(*syscall.Stat_t); !ok || sys.Nlink != 1 || int(sys.Uid) != os.Geteuid() {
		_ = file.Close()
		return nil, errors.New("建站锁文件不安全")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, siteProvisioningError("site_create_busy", "网站创建锁正在被占用")
		}
		return nil, err
	}
	return &siteCreationLock{file: file}, nil
}

type siteProvisioningResources struct {
	SystemUser       string `json:"system_user"`
	DBName           string `json:"db_name"`
	DBUser           string `json:"db_user"`
	WebRoot          string `json:"web_root"`
	LogDir           string `json:"log_dir"`
	PHPFPMPath       string `json:"php_fpm_path"`
	NginxConfigPath  string `json:"nginx_config_path"`
	NginxEnabledPath string `json:"nginx_enabled_path"`
	PHPSocketPath    string `json:"php_socket_path"`
}

func deriveBasicSiteProvisioningResources(domain string) (siteProvisioningResources, error) {
	if config.AppConfig == nil {
		return siteProvisioningResources{}, errors.New("面板配置未加载")
	}
	domain = strings.ToLower(strings.TrimSpace(domain))
	siteName := buildSiteName(domain)
	configBase := siteConfigBaseName(siteName)
	resources := siteProvisioningResources{
		SystemUser:       "wp_" + siteName,
		DBName:           "db_" + siteName,
		DBUser:           "user_" + siteName,
		WebRoot:          filepath.Join(config.AppConfig.Paths.WWWRoot, domain),
		LogDir:           filepath.Join(config.AppConfig.Paths.WWWLogs, domain),
		PHPFPMPath:       filepath.Join(config.AppConfig.Paths.PHPFPMPool, configBase+".conf"),
		NginxConfigPath:  filepath.Join(config.AppConfig.Paths.NginxSitesAvailable, configBase+".conf"),
		NginxEnabledPath: filepath.Join(config.AppConfig.Paths.NginxSitesEnabled, configBase+".conf"),
		PHPSocketPath:    filepath.Join(config.AppConfig.Paths.PHPFPMSock, configBase+".sock"),
	}
	if err := validateUnixSocketPath(resources.PHPSocketPath); err != nil {
		return siteProvisioningResources{}, err
	}
	return resources, nil
}

func normalizeSiteProvisioningAliases(aliases []string, domain string) ([]string, error) {
	seen := map[string]bool{domain: true}
	result := make([]string, 0, len(aliases))
	for _, raw := range aliases {
		alias := strings.ToLower(strings.TrimSpace(raw))
		if alias == "" {
			continue
		}
		if !IsValidDomain(alias) {
			return nil, siteProvisioningError("invalid_domain", "附加域名格式不合法")
		}
		if !seen[alias] {
			seen[alias] = true
			result = append(result, alias)
		}
	}
	sort.Strings(result)
	return result, nil
}

func findSiteProvisioningDomainConflict(ctx context.Context, domain string, aliases []string) error {
	claimed := map[string]bool{strings.ToLower(strings.TrimSpace(domain)): true}
	for _, alias := range aliases {
		claimed[strings.ToLower(strings.TrimSpace(alias))] = true
	}
	rows, err := database.GetDB().QueryContext(ctx, "SELECT domain, aliases FROM websites")
	if err != nil {
		return fmt.Errorf("check website domains: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var existingDomain, existingAliases string
		if err := rows.Scan(&existingDomain, &existingAliases); err != nil {
			return err
		}
		if claimed[strings.ToLower(strings.TrimSpace(existingDomain))] {
			return siteProvisioningError("domain_conflict", "域名或别名已被其它网站占用")
		}
		for _, alias := range strings.Split(existingAliases, "\n") {
			if claimed[strings.ToLower(strings.TrimSpace(alias))] {
				return siteProvisioningError("domain_conflict", "域名或别名已被其它网站占用")
			}
		}
	}
	return rows.Err()
}

func checkCreateSiteDomainAndMigration(ctx context.Context, domain string, aliases []string) error {
	if err := findSiteProvisioningDomainConflict(ctx, domain, aliases); err != nil {
		return err
	}
	locked, err := SiteMigrationLocked(ctx, 0, domain)
	if err != nil {
		return siteProvisioningError("migration_locked", "无法确认域名迁移锁状态，请稍后重试")
	}
	if locked {
		return siteProvisioningError("migration_locked", "该域名正受网站搬家任务保护，请稍后重试")
	}
	return nil
}

func checkBasicSiteProvisioningConflicts(ctx context.Context, domain string, aliases []string) (siteProvisioningResources, error) {
	if !IsValidDomain(domain) {
		return siteProvisioningResources{}, siteProvisioningError("invalid_domain", "域名格式不合法")
	}
	if err := checkCreateSiteDomainAndMigration(ctx, domain, aliases); err != nil {
		return siteProvisioningResources{}, err
	}
	resources, err := deriveBasicSiteProvisioningResources(domain)
	if err != nil {
		return siteProvisioningResources{}, siteProvisioningError("resource_conflict", "网站资源路径无效")
	}
	if err := ensureCreateSiteResourcesAvailable(resources.SystemUser, resources.WebRoot, resources.LogDir, resources.DBName, resources.DBUser, resources.PHPFPMPath, resources.NginxConfigPath, resources.NginxEnabledPath, resources.PHPSocketPath); err != nil {
		return siteProvisioningResources{}, siteProvisioningError("resource_conflict", "网站资源已存在或冲突")
	}
	return resources, nil
}

type siteProvisioningPlanPayload struct {
	CapabilityID   string   `json:"capability_id"`
	Action         string   `json:"action"`
	Domain         string   `json:"domain"`
	Aliases        []string `json:"aliases"`
	SiteType       string   `json:"site_type"`
	SSLEnabled     bool     `json:"ssl_enabled"`
	ExpiresAt      string   `json:"expires_at"`
	OptionsDigest  string   `json:"options_digest"`
	ResourcesHash  string   `json:"resources_hash"`
	PanelVersion   string   `json:"panel_version"`
	ManifestSHA256 string   `json:"manifest_sha256"`
	GeneratedAt    string   `json:"generated_at"`
	ExpiresPlanAt  string   `json:"expires_plan_at"`
	SideEffects    string   `json:"side_effects"`
}

type siteProvisioningPlanFile struct {
	Payload     siteProvisioningPlanPayload `json:"payload"`
	PlanDigest  string                      `json:"plan_digest"`
	Consumed    bool                        `json:"consumed"`
	ConsumedAt  string                      `json:"consumed_at,omitempty"`
	OperationID string                      `json:"operation_id,omitempty"`
}

type SiteProvisioningResponse struct {
	OK             bool     `json:"ok"`
	Action         string   `json:"action"`
	Mode           string   `json:"mode"`
	Domain         string   `json:"domain"`
	Aliases        []string `json:"aliases,omitempty"`
	SiteID         int64    `json:"site_id,omitempty"`
	Status         string   `json:"status,omitempty"`
	SSLEnabled     bool     `json:"ssl_enabled"`
	Warnings       []string `json:"warnings,omitempty"`
	PlanDigest     string   `json:"plan_digest,omitempty"`
	ExpiresAt      string   `json:"expires_at,omitempty"`
	PanelVersion   string   `json:"panel_version,omitempty"`
	ManifestSHA256 string   `json:"manifest_sha256,omitempty"`
	Consumed       bool     `json:"consumed"`
	OperationID    string   `json:"operation_id,omitempty"`
}

func siteProvisioningOptionsDigest() string {
	sum := sha256.Sum256([]byte(`site_type=wordpress|ssl=false|expires_at=|clean_defaults=false|remove_unused_themes=false|install_themes=|install_plugins=`))
	return hex.EncodeToString(sum[:])
}

func siteProvisioningResourcesHash(resources siteProvisioningResources) string {
	data, _ := json.Marshal(resources)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func makeSiteProvisioningPayload(domain string, aliases []string, resources siteProvisioningResources, panelVersion string, generated, expires time.Time) siteProvisioningPlanPayload {
	return siteProvisioningPlanPayload{
		CapabilityID: "server-ai.site-provisioning", Action: "create", Domain: domain, Aliases: aliases,
		SiteType: "wordpress", SSLEnabled: false, ExpiresAt: "", OptionsDigest: siteProvisioningOptionsDigest(),
		ResourcesHash: siteProvisioningResourcesHash(resources), PanelVersion: panelVersion, ManifestSHA256: aiPanelManifestPayloadSHA256,
		GeneratedAt: generated.UTC().Format(time.RFC3339Nano), ExpiresPlanAt: expires.UTC().Format(time.RFC3339Nano),
		SideEffects: "create one basic WordPress HTTP site and managed runtime resources",
	}
}

func digestSiteProvisioningPlan(payload siteProvisioningPlanPayload) string {
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func siteProvisioningPlanPath(digest string) (string, error) {
	if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
		return "", siteProvisioningError("plan_invalid", "操作计划摘要格式不合法")
	}
	dir, err := siteStatePlanDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "site-create-"+digest+".json"), nil
}

func writeSiteProvisioningPlan(digest string, file siteProvisioningPlanFile) error {
	path, err := siteProvisioningPlanPath(digest)
	if err != nil {
		return err
	}
	data, err := json.Marshal(file)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data, 0600)
}

func readSiteProvisioningPlan(digest string) (siteProvisioningPlanFile, error) {
	path, err := siteProvisioningPlanPath(digest)
	if err != nil {
		return siteProvisioningPlanFile{}, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return siteProvisioningPlanFile{}, siteProvisioningError("plan_not_found", "操作计划不存在")
	}
	if err != nil {
		return siteProvisioningPlanFile{}, siteProvisioningError("plan_invalid", "读取操作计划失败")
	}
	var file siteProvisioningPlanFile
	if json.Unmarshal(data, &file) != nil || file.PlanDigest != digest || digestSiteProvisioningPlan(file.Payload) != digest {
		return siteProvisioningPlanFile{}, siteProvisioningError("plan_invalid", "操作计划内容无效")
	}
	return file, nil
}

func PrepareSiteProvisioningPlan(ctx context.Context, domain string, aliases []string, panelVersion string) (SiteProvisioningResponse, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if !IsValidDomain(domain) {
		return SiteProvisioningResponse{}, siteProvisioningError("invalid_domain", "域名格式不合法")
	}
	aliases, err := normalizeSiteProvisioningAliases(aliases, domain)
	if err != nil {
		return SiteProvisioningResponse{}, err
	}
	lock, err := acquireSiteCreationLock()
	if err != nil {
		return SiteProvisioningResponse{}, err
	}
	defer lock.Close()
	resources, err := checkBasicSiteProvisioningConflicts(ctx, domain, aliases)
	if err != nil {
		return SiteProvisioningResponse{}, err
	}
	now := time.Now().UTC()
	payload := makeSiteProvisioningPayload(domain, aliases, resources, panelVersion, now, now.Add(siteProvisioningPlanTTL))
	digest := digestSiteProvisioningPlan(payload)
	if err := writeSiteProvisioningPlan(digest, siteProvisioningPlanFile{Payload: payload, PlanDigest: digest}); err != nil {
		return SiteProvisioningResponse{}, siteProvisioningError("create_failed", "保存操作计划失败")
	}
	return SiteProvisioningResponse{OK: true, Action: "create", Mode: "dry_run", Domain: domain, Aliases: aliases, PlanDigest: digest, ExpiresAt: payload.ExpiresPlanAt, PanelVersion: panelVersion, ManifestSHA256: payload.ManifestSHA256}, nil
}

func validateSiteProvisioningPlanFile(file siteProvisioningPlanFile, digest, panelVersion string) error {
	if file.Consumed {
		return siteProvisioningError("plan_already_used", "操作计划已经执行过")
	}
	if file.PlanDigest != digest || digestSiteProvisioningPlan(file.Payload) != digest {
		return siteProvisioningError("plan_invalid", "操作计划摘要不匹配")
	}
	if file.Payload.Action != "create" || file.Payload.SiteType != "wordpress" || file.Payload.SSLEnabled || file.Payload.ExpiresAt != "" {
		return siteProvisioningError("plan_invalid", "操作计划参数不符合第一版范围")
	}
	expires, err := time.Parse(time.RFC3339Nano, file.Payload.ExpiresPlanAt)
	if err != nil || !time.Now().UTC().Before(expires) {
		return siteProvisioningError("plan_expired", "操作计划已过期，请重新执行 dry-run")
	}
	if file.Payload.PanelVersion != panelVersion || file.Payload.ManifestSHA256 != aiPanelManifestPayloadSHA256 {
		return siteProvisioningError("plan_stale", "面板版本或能力清单已变化，请重新执行 dry-run")
	}
	return nil
}

func ExecuteSiteProvisioningPlan(ctx context.Context, digest, panelVersion string) (SiteProvisioningResponse, error) {
	initial, err := readSiteProvisioningPlan(digest)
	if err != nil {
		return SiteProvisioningResponse{}, err
	}
	lock, err := acquireSiteCreationLock()
	if err != nil {
		return SiteProvisioningResponse{}, err
	}
	defer lock.Close()
	file, err := readSiteProvisioningPlan(digest)
	if err != nil {
		return SiteProvisioningResponse{}, err
	}
	if err := validateSiteProvisioningPlanFile(file, digest, panelVersion); err != nil {
		return SiteProvisioningResponse{}, err
	}
	if initial.Payload.Domain != file.Payload.Domain {
		return SiteProvisioningResponse{}, siteProvisioningError("plan_stale", "网站目标已变化，请重新执行 dry-run")
	}
	aliases, err := normalizeSiteProvisioningAliases(file.Payload.Aliases, file.Payload.Domain)
	if err != nil || !equalStringSlices(aliases, file.Payload.Aliases) {
		return SiteProvisioningResponse{}, siteProvisioningError("plan_invalid", "操作计划域名参数无效")
	}
	resources, err := checkBasicSiteProvisioningConflicts(ctx, file.Payload.Domain, aliases)
	if err != nil {
		return SiteProvisioningResponse{}, err
	}
	if siteProvisioningResourcesHash(resources) != file.Payload.ResourcesHash {
		return SiteProvisioningResponse{}, siteProvisioningError("plan_stale", "网站资源条件已变化，请重新执行 dry-run")
	}
	file.Consumed = true
	file.ConsumedAt = time.Now().UTC().Format(time.RFC3339Nano)
	file.OperationID = uuid.NewString()
	if err := writeSiteProvisioningPlan(digest, file); err != nil {
		return SiteProvisioningResponse{}, siteProvisioningError("create_failed", "确认操作计划失败")
	}
	payload := &CreateSitePayload{Domain: file.Payload.Domain, Aliases: file.Payload.Aliases, SiteType: "wordpress", SSLEnabled: false}
	recordOperationLog("create_site", payload.Domain, "started", "服务器级 AI 建站已开始")
	result := executeSiteProvisioningCore(&Task{Type: TaskCreateSite, Payload: payload})
	if !result.Success {
		message := SiteProvisioningPublicErrorMessage(siteProvisioningError(result.ErrorCode, result.Message))
		diagnostic := result.Diagnostic
		if diagnostic == "" {
			diagnostic = result.Message
		}
		recordOperationLog("create_site", payload.Domain, "failed", diagnostic)
		code := result.ErrorCode
		if code == "" {
			code = "create_failed"
		}
		return SiteProvisioningResponse{}, &SiteProvisioningError{Code: code, Message: message, Diagnostic: diagnostic}
	}
	recordOperationLog("create_site", payload.Domain, "success", "服务器级 AI 建站完成")
	var siteID int64
	if data, ok := result.Data.(map[string]interface{}); ok {
		if value, ok := data["id"].(int64); ok {
			siteID = value
		}
	}
	return SiteProvisioningResponse{OK: true, Action: "create", Mode: "execute", Domain: payload.Domain, Aliases: payload.Aliases, SiteID: siteID, Status: "active", SSLEnabled: false, PlanDigest: digest, PanelVersion: panelVersion, ManifestSHA256: aiPanelManifestPayloadSHA256, Consumed: true, OperationID: file.OperationID}, nil
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

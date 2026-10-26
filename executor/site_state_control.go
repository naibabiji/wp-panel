package executor

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

const siteStatePlanTTL = 10 * time.Minute

type SiteStateControlError struct {
	Code    string
	Message string
}

func (e *SiteStateControlError) Error() string { return e.Message }

func siteStateError(code, message string) error {
	return &SiteStateControlError{Code: code, Message: message}
}

func SiteStateControlErrorCode(err error) string {
	var target *SiteStateControlError
	if errors.As(err, &target) {
		return target.Code
	}
	return "operation_failed"
}

type siteStatePlanPayload struct {
	CapabilityID     string `json:"capability_id"`
	Action           string `json:"action"`
	SiteID           int    `json:"site_id"`
	Domain           string `json:"domain"`
	FromStatus       string `json:"from_status"`
	ToStatus         string `json:"to_status"`
	PanelVersion     string `json:"panel_version"`
	ManifestSHA256   string `json:"manifest_sha256"`
	GeneratedAt      string `json:"generated_at"`
	ExpiresAt        string `json:"expires_at"`
	StateFingerprint string `json:"state_fingerprint"`
	SideEffects      string `json:"side_effects"`
}

type siteStatePlanFile struct {
	Payload     siteStatePlanPayload `json:"payload"`
	PlanDigest  string               `json:"plan_digest"`
	Consumed    bool                 `json:"consumed"`
	ConsumedAt  string               `json:"consumed_at,omitempty"`
	OperationID string               `json:"operation_id,omitempty"`
}

type SiteStatePlanResponse struct {
	OK             bool   `json:"ok"`
	Action         string `json:"action"`
	Mode           string `json:"mode"`
	SiteID         int    `json:"site_id"`
	Domain         string `json:"domain"`
	FromStatus     string `json:"from_status"`
	ToStatus       string `json:"to_status"`
	PlanDigest     string `json:"plan_digest,omitempty"`
	ExpiresAt      string `json:"expires_at,omitempty"`
	PanelVersion   string `json:"panel_version,omitempty"`
	ManifestSHA256 string `json:"manifest_sha256,omitempty"`
	Consumed       bool   `json:"consumed"`
	OperationID    string `json:"operation_id,omitempty"`
}

type siteStatePlanCurrent struct {
	Site           *models.Website
	PanelVersion   string
	ManifestSHA256 string
}

func PrepareSiteStatePlan(ctx context.Context, action, domain, panelVersion string) (SiteStatePlanResponse, error) {
	site, err := findSiteStateByDomain(ctx, domain)
	if err != nil {
		return SiteStatePlanResponse{}, err
	}
	if err := checkSiteMigrationGate(ctx, site); err != nil {
		return SiteStatePlanResponse{}, err
	}
	if err := validateSiteStateAction(site, action, false); err != nil {
		return SiteStatePlanResponse{}, err
	}
	if err := checkSiteAccessGates(ctx, site); err != nil {
		return SiteStatePlanResponse{}, err
	}
	if !TryAcquireSiteOpLock(site.ID, "site_state_plan") {
		return SiteStatePlanResponse{}, siteStateError("site_lock_busy", "网站正在执行其它操作，请稍后重试")
	}
	ReleaseSiteOpLock(site.ID)

	now := time.Now().UTC()
	payload := makeSiteStatePlanPayload(site, action, panelVersion, now, now.Add(siteStatePlanTTL))
	digest, err := digestSiteStatePlan(payload)
	if err != nil {
		return SiteStatePlanResponse{}, siteStateError("plan_invalid", "生成操作计划失败")
	}
	file := siteStatePlanFile{Payload: payload, PlanDigest: digest}
	if err := writeSiteStatePlan(digest, file); err != nil {
		return SiteStatePlanResponse{}, siteStateError("operation_failed", "保存操作计划失败")
	}
	return SiteStatePlanResponse{
		OK: true, Action: action, Mode: "dry_run", SiteID: site.ID, Domain: site.Domain,
		FromStatus: string(site.Status), ToStatus: payload.ToStatus, PlanDigest: digest,
		ExpiresAt: payload.ExpiresAt, PanelVersion: payload.PanelVersion,
		ManifestSHA256: payload.ManifestSHA256, Consumed: false,
	}, nil
}

func ExecuteSiteStatePlan(ctx context.Context, digest, panelVersion, expectedAction string) (SiteStatePlanResponse, error) {
	file, err := readSiteStatePlan(digest)
	if err != nil {
		return SiteStatePlanResponse{}, err
	}
	now := time.Now().UTC()
	expires, err := time.Parse(time.RFC3339Nano, file.Payload.ExpiresAt)
	if err != nil || !now.Before(expires) {
		return SiteStatePlanResponse{}, siteStateError("plan_expired", "操作计划已过期，请重新执行 dry-run")
	}
	if file.Consumed {
		return SiteStatePlanResponse{}, siteStateError("plan_already_used", "操作计划已经执行过")
	}
	if file.PlanDigest != digest {
		return SiteStatePlanResponse{}, siteStateError("plan_invalid", "操作计划摘要不匹配")
	}
	if file.Payload.Action != expectedAction {
		return SiteStatePlanResponse{}, siteStateError("plan_invalid", "操作计划与命令不匹配")
	}
	if expected, err := digestSiteStatePlan(file.Payload); err != nil || expected != digest {
		return SiteStatePlanResponse{}, siteStateError("plan_invalid", "操作计划内容无效")
	}
	site, err := loadSiteStateTarget(ctx, file.Payload.SiteID)
	if err != nil || site.Domain != file.Payload.Domain {
		return SiteStatePlanResponse{}, siteStateError("plan_stale", "网站目标已变化，请重新执行 dry-run")
	}
	if !TryAcquireSiteOpLock(site.ID, "site_state_plan_execute") {
		return SiteStatePlanResponse{}, siteStateError("site_lock_busy", "网站正在执行其它操作，请稍后重试")
	}
	defer ReleaseSiteOpLock(site.ID)
	// The first read only locates the site and lets us find its lock. Re-read
	// the plan after acquiring that lock so two processes that observed the
	// same unconsumed file cannot both consume it.
	lockedFile, err := readSiteStatePlan(digest)
	if err != nil {
		return SiteStatePlanResponse{}, err
	}
	if lockedFile.Consumed {
		return SiteStatePlanResponse{}, siteStateError("plan_already_used", "操作计划已经执行过")
	}
	if lockedFile.PlanDigest != digest {
		return SiteStatePlanResponse{}, siteStateError("plan_invalid", "操作计划摘要不匹配")
	}
	if lockedFile.Payload.Action != expectedAction {
		return SiteStatePlanResponse{}, siteStateError("plan_invalid", "操作计划与命令不匹配")
	}
	if expected, err := digestSiteStatePlan(lockedFile.Payload); err != nil || expected != digest {
		return SiteStatePlanResponse{}, siteStateError("plan_invalid", "操作计划内容无效")
	}
	file = lockedFile
	expires, err = time.Parse(time.RFC3339Nano, file.Payload.ExpiresAt)
	if err != nil || !time.Now().UTC().Before(expires) {
		return SiteStatePlanResponse{}, siteStateError("plan_expired", "操作计划已过期，请重新执行 dry-run")
	}
	if err := checkSiteMigrationGate(ctx, site); err != nil {
		return SiteStatePlanResponse{}, siteStateError("plan_stale", "网站状态已变化，请重新执行 dry-run")
	}
	if err := validateSiteStateAction(site, file.Payload.Action, false); err != nil {
		return SiteStatePlanResponse{}, siteStateError("plan_stale", "网站状态已变化，请重新执行 dry-run")
	}
	if err := checkSiteAccessGates(ctx, site); err != nil {
		return SiteStatePlanResponse{}, siteStateError("plan_stale", "网站操作门禁已变化，请重新执行 dry-run")
	}
	current := makeSiteStatePlanPayload(site, file.Payload.Action, panelVersion, file.PayloadGeneratedAt(), expires)
	if current != file.Payload {
		return SiteStatePlanResponse{}, siteStateError("plan_stale", "网站状态或面板能力已变化，请重新执行 dry-run")
	}
	file.Consumed = true
	file.ConsumedAt = now.Format(time.RFC3339Nano)
	file.OperationID = uuid.NewString()
	if err := writeSiteStatePlan(digest, file); err != nil {
		return SiteStatePlanResponse{}, siteStateError("operation_failed", "确认操作计划失败")
	}
	result := executeSiteStateWithVerification(ctx, site, file.Payload.Action)
	if !result.Success {
		code := "operation_failed"
		if strings.Contains(result.Message, "人工检查") {
			code = "rollback_required"
		}
		recordOperationLog(siteStateOperationName(file.Payload.Action), site.Domain, "failed", result.Message)
		return SiteStatePlanResponse{}, siteStateError(code, result.Message)
	}
	recordOperationLog(siteStateOperationName(file.Payload.Action), site.Domain, "success", result.Message)
	return SiteStatePlanResponse{
		OK: true, Action: file.Payload.Action, Mode: "execute", SiteID: site.ID, Domain: site.Domain,
		FromStatus: file.Payload.FromStatus, ToStatus: file.Payload.ToStatus,
		PanelVersion: file.Payload.PanelVersion, ManifestSHA256: file.Payload.ManifestSHA256,
		Consumed: true, OperationID: file.OperationID,
	}, nil
}

func siteStateOperationName(action string) string {
	if action == "resume" {
		return "enable_site"
	}
	return "pause_site"
}

func (p siteStatePlanFile) PayloadGeneratedAt() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, p.Payload.GeneratedAt)
	return t
}

func executeSiteStateUnlocked(site *models.Website, action string) TaskResult {
	if action == "pause" {
		return executePauseSiteUnlocked(site)
	}
	return executeEnableSiteUnlocked(site)
}

func validateSiteStateAction(site *models.Website, action string, allowMigrated bool) error {
	if site == nil {
		return siteStateError("site_not_found", "网站不存在")
	}
	switch action {
	case "pause":
		if site.Status != models.StatusActive {
			return siteStateError("invalid_state", "当前网站状态不能执行暂停")
		}
	case "resume":
		if site.Status != models.StatusPaused && !(allowMigrated && site.Status == models.StatusMigrated) {
			return siteStateError("invalid_state", "当前网站状态不能执行启用；已搬家网站请通过面板恢复")
		}
	default:
		return siteStateError("invalid_state", "无效的网站状态操作")
	}
	return nil
}

func checkSiteMigrationGate(ctx context.Context, site *models.Website) error {
	if locked, err := SiteMigrationLocked(ctx, site.ID, site.Domain); err != nil {
		return siteStateError("migration_locked", "检查站点迁移锁失败")
	} else if locked {
		return siteStateError("migration_locked", "网站正在迁移维护中")
	}
	return nil
}

func checkSiteAccessGates(ctx context.Context, site *models.Website) error {
	if blocked, err := database.IsAIDevelopmentAccessBlocking(ctx, database.GetDB(), int64(site.ID)); err != nil {
		return siteStateError("ai_development_active", "检查 AI 开发授权失败")
	} else if blocked {
		return siteStateError("ai_development_active", "该网站已开启 AI 开发访问，请先关闭授权")
	}
	if active, err := database.MaintenanceWindowActive(database.GetDB(), site.ID); err != nil {
		return siteStateError("maintenance_window_active", "检查网站维护窗口失败")
	} else if active {
		return siteStateError("maintenance_window_active", "网站处于维护窗口，暂不能执行操作")
	}
	return nil
}

func findSiteStateByDomain(ctx context.Context, domain string) (*models.Website, error) {
	normalized := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if !IsValidDomain(normalized) {
		return nil, siteStateError("invalid_domain", "域名格式不合法")
	}
	db := database.GetDB()
	if db == nil {
		return nil, siteStateError("site_not_found", "网站不存在")
	}
	var id int
	if err := db.QueryRowContext(ctx, "SELECT id FROM websites WHERE lower(domain)=?", normalized).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, siteStateError("site_not_found", "网站不存在")
		}
		return nil, siteStateError("site_not_found", "网站查询失败")
	}
	return loadSiteStateTarget(ctx, id)
}

func makeSiteStatePlanPayload(site *models.Website, action, panelVersion string, generated, expires time.Time) siteStatePlanPayload {
	from, to := string(site.Status), "paused"
	if action == "resume" {
		to = "active"
	}
	fingerprint := fmt.Sprintf("%d|%s|%s|%s", site.ID, site.Domain, from, to)
	sum := sha256.Sum256([]byte(fingerprint))
	return siteStatePlanPayload{
		CapabilityID: "server-ai.site-state-control", Action: action, SiteID: site.ID, Domain: site.Domain,
		FromStatus: from, ToStatus: to, PanelVersion: panelVersion, ManifestSHA256: aiPanelManifestPayloadSHA256,
		GeneratedAt: generated.UTC().Format(time.RFC3339Nano), ExpiresAt: expires.UTC().Format(time.RFC3339Nano),
		StateFingerprint: hex.EncodeToString(sum[:]), SideEffects: "website status and managed Nginx enabled link",
	}
}

func digestSiteStatePlan(payload siteStatePlanPayload) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func siteStatePlanDir() (string, error) {
	if config.AppConfig == nil || strings.TrimSpace(config.AppConfig.Panel.DataDir) == "" {
		return "", errors.New("panel data directory is not configured")
	}
	dir := filepath.Join(config.AppConfig.Panel.DataDir, "ai-plans")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

func writeSiteStatePlan(digest string, file siteStatePlanFile) error {
	dir, err := siteStatePlanDir()
	if err != nil {
		return err
	}
	data, err := json.Marshal(file)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, digest+".json")
	if err := writeFileAtomic(path, data, 0600); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		return os.Chown(path, 0, 0)
	}
	return nil
}

func readSiteStatePlan(digest string) (siteStatePlanFile, error) {
	if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
		return siteStatePlanFile{}, siteStateError("plan_invalid", "操作计划摘要格式不合法")
	}
	dir, err := siteStatePlanDir()
	if err != nil {
		return siteStatePlanFile{}, siteStateError("plan_not_found", "操作计划不存在")
	}
	data, err := os.ReadFile(filepath.Join(dir, digest+".json"))
	if os.IsNotExist(err) {
		return siteStatePlanFile{}, siteStateError("plan_not_found", "操作计划不存在")
	}
	if err != nil {
		return siteStatePlanFile{}, siteStateError("plan_invalid", "读取操作计划失败")
	}
	var file siteStatePlanFile
	if json.Unmarshal(data, &file) != nil {
		return siteStatePlanFile{}, siteStateError("plan_invalid", "操作计划损坏")
	}
	return file, nil
}

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
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

const siteCacheClearScope = "fastcgi_page_cache_only"

var siteCacheNotCleared = []string{"redis_object_cache", "wordpress_file_cache", "cdn_cache", "browser_cache"}

type siteCacheClearPlanPayload struct {
	CapabilityID     string `json:"capability_id"`
	Action           string `json:"action"`
	SiteID           int    `json:"site_id"`
	Domain           string `json:"domain"`
	Status           string `json:"status"`
	CacheKey         string `json:"cache_key"`
	PanelVersion     string `json:"panel_version"`
	ManifestSHA256   string `json:"manifest_sha256"`
	GeneratedAt      string `json:"generated_at"`
	ExpiresAt        string `json:"expires_at"`
	StateFingerprint string `json:"state_fingerprint"`
	SideEffects      string `json:"side_effects"`
}

type siteCacheClearPlanFile struct {
	Payload     siteCacheClearPlanPayload `json:"payload"`
	PlanDigest  string                    `json:"plan_digest"`
	Consumed    bool                      `json:"consumed"`
	ConsumedAt  string                    `json:"consumed_at,omitempty"`
	OperationID string                    `json:"operation_id,omitempty"`
}

type SiteCacheClearResponse struct {
	OK             bool     `json:"ok"`
	Action         string   `json:"action"`
	Mode           string   `json:"mode"`
	SiteID         int      `json:"site_id"`
	Domain         string   `json:"domain"`
	Status         string   `json:"status"`
	Scope          string   `json:"scope"`
	NotCleared     []string `json:"not_cleared"`
	PlanDigest     string   `json:"plan_digest,omitempty"`
	ExpiresAt      string   `json:"expires_at,omitempty"`
	PanelVersion   string   `json:"panel_version,omitempty"`
	ManifestSHA256 string   `json:"manifest_sha256,omitempty"`
	Consumed       bool     `json:"consumed"`
	OperationID    string   `json:"operation_id,omitempty"`
}

func SiteCacheClearErrorCode(err error) string {
	var target *SiteCacheClearError
	if errors.As(err, &target) {
		return target.Code
	}
	var stateErr *SiteStateControlError
	if errors.As(err, &stateErr) {
		return stateErr.Code
	}
	return "operation_failed"
}

func makeSiteCacheClearPayload(site *models.Website, panelVersion string, generated, expires time.Time) siteCacheClearPlanPayload {
	fingerprint := fmt.Sprintf("%d|%s|%s|%s", site.ID, site.Domain, site.Status, site.FCacheKey)
	sum := sha256.Sum256([]byte(fingerprint))
	return siteCacheClearPlanPayload{
		CapabilityID: "server-ai.site-cache-clear", Action: "clear", SiteID: site.ID, Domain: site.Domain,
		Status: string(site.Status), CacheKey: site.FCacheKey, PanelVersion: panelVersion,
		ManifestSHA256: aiPanelManifestPayloadSHA256, GeneratedAt: generated.UTC().Format(time.RFC3339Nano),
		ExpiresAt: expires.UTC().Format(time.RFC3339Nano), StateFingerprint: hex.EncodeToString(sum[:]),
		SideEffects: "rotate fastcgi cache key and republish site nginx config",
	}
}

func digestSiteCacheClearPlan(payload siteCacheClearPlanPayload) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func siteCachePlanPath(digest string) (string, error) {
	dir, err := siteStatePlanDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cache-"+digest+".json"), nil
}

func writeSiteCachePlan(digest string, file siteCacheClearPlanFile) error {
	path, err := siteCachePlanPath(digest)
	if err != nil {
		return err
	}
	data, err := json.Marshal(file)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(path, data, 0600); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		return os.Chown(path, 0, 0)
	}
	return nil
}

func readSiteCachePlan(digest string) (siteCacheClearPlanFile, error) {
	if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
		return siteCacheClearPlanFile{}, siteStateError("plan_invalid", "操作计划摘要格式不合法")
	}
	path, err := siteCachePlanPath(digest)
	if err != nil {
		return siteCacheClearPlanFile{}, siteStateError("plan_not_found", "操作计划不存在")
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return siteCacheClearPlanFile{}, siteStateError("plan_not_found", "操作计划不存在")
	}
	if err != nil {
		return siteCacheClearPlanFile{}, siteStateError("plan_invalid", "读取操作计划失败")
	}
	var file siteCacheClearPlanFile
	if json.Unmarshal(data, &file) != nil {
		return siteCacheClearPlanFile{}, siteStateError("plan_invalid", "操作计划损坏")
	}
	return file, nil
}

func siteCacheClearResponse(file siteCacheClearPlanFile, mode, status string) SiteCacheClearResponse {
	return SiteCacheClearResponse{
		OK: true, Action: file.Payload.Action, Mode: mode, SiteID: file.Payload.SiteID, Domain: file.Payload.Domain,
		Status: status, Scope: siteCacheClearScope, NotCleared: append([]string(nil), siteCacheNotCleared...),
		PlanDigest: file.PlanDigest, ExpiresAt: file.Payload.ExpiresAt, PanelVersion: file.Payload.PanelVersion,
		ManifestSHA256: file.Payload.ManifestSHA256, Consumed: file.Consumed, OperationID: file.OperationID,
	}
}

func PrepareSiteCacheClearPlan(ctx context.Context, domain, panelVersion string) (SiteCacheClearResponse, error) {
	site, err := findSiteStateByDomain(ctx, domain)
	if err != nil {
		return SiteCacheClearResponse{}, err
	}
	if err := loadSiteCacheKey(ctx, site); err != nil {
		return SiteCacheClearResponse{}, err
	}
	if err := checkSiteMigrationGate(ctx, site); err != nil {
		return SiteCacheClearResponse{}, err
	}
	if !TryAcquireCompanionDeployLock(site.ID) {
		return SiteCacheClearResponse{}, siteStateError("site_lock_busy", "网站正在执行其它操作，请稍后重试")
	}
	ReleaseSiteOpLock(site.ID)
	now := time.Now().UTC()
	payload := makeSiteCacheClearPayload(site, panelVersion, now, now.Add(siteStatePlanTTL))
	digest, err := digestSiteCacheClearPlan(payload)
	if err != nil {
		return SiteCacheClearResponse{}, siteStateError("plan_invalid", "生成操作计划失败")
	}
	if err := writeSiteCachePlan(digest, siteCacheClearPlanFile{Payload: payload, PlanDigest: digest}); err != nil {
		return SiteCacheClearResponse{}, siteStateError("operation_failed", "保存操作计划失败")
	}
	return siteCacheClearResponse(siteCacheClearPlanFile{Payload: payload, PlanDigest: digest}, "dry_run", string(site.Status)), nil
}

func ExecuteSiteCacheClearPlan(ctx context.Context, digest, panelVersion string) (SiteCacheClearResponse, error) {
	file, err := readSiteCachePlan(digest)
	if err != nil {
		return SiteCacheClearResponse{}, err
	}
	if file.Payload.Action != "clear" || file.PlanDigest != digest {
		return SiteCacheClearResponse{}, siteStateError("plan_invalid", "操作计划与命令不匹配")
	}
	if expected, digestErr := digestSiteCacheClearPlan(file.Payload); digestErr != nil || expected != digest {
		return SiteCacheClearResponse{}, siteStateError("plan_invalid", "操作计划内容无效")
	}
	site, err := loadSiteStateTarget(ctx, file.Payload.SiteID)
	if err != nil || site.Domain != file.Payload.Domain {
		return SiteCacheClearResponse{}, siteStateError("plan_stale", "网站目标已变化，请重新执行 dry-run")
	}
	if !TryAcquireCompanionDeployLock(site.ID) {
		return SiteCacheClearResponse{}, siteStateError("site_lock_busy", "网站正在执行其它操作，请稍后重试")
	}
	defer ReleaseSiteOpLock(site.ID)
	file, err = readSiteCachePlan(digest)
	if err != nil {
		return SiteCacheClearResponse{}, err
	}
	if file.Consumed {
		return SiteCacheClearResponse{}, siteStateError("plan_already_used", "操作计划已经执行过")
	}
	if file.PlanDigest != digest || file.Payload.Action != "clear" {
		return SiteCacheClearResponse{}, siteStateError("plan_invalid", "操作计划与命令不匹配")
	}
	if expected, digestErr := digestSiteCacheClearPlan(file.Payload); digestErr != nil || expected != digest {
		return SiteCacheClearResponse{}, siteStateError("plan_invalid", "操作计划内容无效")
	}
	expires, parseErr := time.Parse(time.RFC3339Nano, file.Payload.ExpiresAt)
	if parseErr != nil || !time.Now().UTC().Before(expires) {
		return SiteCacheClearResponse{}, siteStateError("plan_expired", "操作计划已过期，请重新执行 dry-run")
	}
	if err := checkSiteMigrationGate(ctx, site); err != nil {
		return SiteCacheClearResponse{}, err
	}
	if err := loadSiteCacheKey(ctx, site); err != nil {
		return SiteCacheClearResponse{}, err
	}
	current := makeSiteCacheClearPayload(site, panelVersion, mustParseTime(file.Payload.GeneratedAt), expires)
	if current != file.Payload {
		return SiteCacheClearResponse{}, siteStateError("plan_stale", "网站状态或缓存标识已变化，请重新执行 dry-run")
	}
	file.Consumed = true
	file.ConsumedAt = time.Now().UTC().Format(time.RFC3339Nano)
	file.OperationID = uuid.NewString()
	if err := writeSiteCachePlan(digest, file); err != nil {
		return SiteCacheClearResponse{}, siteStateError("operation_failed", "确认操作计划失败")
	}
	newKey, err := clearSiteCacheCore(site.ID)
	if err != nil {
		recordOperationLog("clear_site_cache", site.Domain, "failed", err.Error())
		return SiteCacheClearResponse{}, err
	}
	if err := verifySiteCacheKey(site.ID, newKey); err != nil {
		recordOperationLog("clear_site_cache", site.Domain, "failed", err.Error())
		return SiteCacheClearResponse{}, err
	}
	recordOperationLog("clear_site_cache", site.Domain, "success", "FastCGI 页面缓存 key 已轮换并重新发布")
	return siteCacheClearResponse(file, "execute", string(site.Status)), nil
}

func mustParseTime(value string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, value)
	return t
}

func loadSiteCacheKey(ctx context.Context, site *models.Website) error {
	if site == nil || database.GetDB() == nil {
		return siteStateError("site_not_found", "网站不存在")
	}
	if err := database.GetDB().QueryRowContext(ctx, "SELECT fastcgi_cache_key FROM websites WHERE id=?", site.ID).Scan(&site.FCacheKey); err != nil {
		return siteStateError("site_not_found", "读取网站缓存标识失败")
	}
	return nil
}

func verifySiteCacheKey(siteID int, expected string) error {
	var actual string
	db := database.GetDB()
	if db == nil {
		return &SiteCacheClearError{Code: "rollback_required", Message: "执行后无法确认缓存标识，请人工检查"}
	}
	if err := db.QueryRow(`SELECT fastcgi_cache_key FROM websites WHERE id=?`, siteID).Scan(&actual); err != nil {
		return &SiteCacheClearError{Code: "rollback_required", Message: "执行后无法确认缓存标识，请人工检查"}
	}
	if actual != expected {
		return &SiteCacheClearError{Code: "rollback_required", Message: "执行后缓存标识未变化，请人工检查"}
	}
	return nil
}

// ClearSiteCacheExternal is the lock-owning wrapper for HTTP and companion
// plugin entry points. Internal update workflows keep using ClearSiteCache's
// unlocked core because they already own a site lock.
func ClearSiteCacheExternal(ctx context.Context, siteID int, blockMigration bool) (SiteCacheClearResponse, error) {
	site, err := loadSiteStateTarget(ctx, siteID)
	if err != nil {
		return SiteCacheClearResponse{}, siteStateError("site_not_found", "网站不存在")
	}
	if blockMigration {
		if err := checkSiteMigrationGate(ctx, site); err != nil {
			return SiteCacheClearResponse{}, err
		}
	}
	if !TryAcquireCompanionDeployLock(site.ID) {
		return SiteCacheClearResponse{}, siteStateError("site_lock_busy", "网站正在执行其它操作，请稍后重试")
	}
	defer ReleaseSiteOpLock(site.ID)
	newKey, err := clearSiteCacheCore(site.ID)
	if err != nil {
		recordOperationLog("clear_site_cache", site.Domain, "failed", err.Error())
		return SiteCacheClearResponse{}, err
	}
	if err := verifySiteCacheKey(site.ID, newKey); err != nil {
		recordOperationLog("clear_site_cache", site.Domain, "failed", err.Error())
		return SiteCacheClearResponse{}, err
	}
	recordOperationLog("clear_site_cache", site.Domain, "success", "FastCGI 页面缓存 key 已轮换并重新发布")
	return SiteCacheClearResponse{OK: true, Action: "clear", Mode: "execute", SiteID: site.ID, Domain: site.Domain, Status: string(site.Status), Scope: siteCacheClearScope, NotCleared: append([]string(nil), siteCacheNotCleared...), Consumed: true, OperationID: uuid.NewString()}, nil
}

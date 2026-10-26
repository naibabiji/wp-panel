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

const databaseBackupScope = "database_backup"

var databaseBackupNotPerformed = []string{"website_file_backup", "panel_sqlite_backup", "database_restore", "remote_backup_configuration"}

type DatabaseBackupError struct {
	Code    string
	Message string
}

func (e *DatabaseBackupError) Error() string { return e.Message }

func DatabaseBackupErrorCode(err error) string {
	var target *DatabaseBackupError
	if errors.As(err, &target) {
		return target.Code
	}
	var stateErr *SiteStateControlError
	if errors.As(err, &stateErr) {
		return stateErr.Code
	}
	return "backup_failed"
}

// DatabaseBackupPublicErrorMessage keeps raw mysqldump and filesystem details
// out of the server-level AI protocol. Detailed text remains in logs.
func DatabaseBackupPublicErrorMessage(err error) string {
	switch DatabaseBackupErrorCode(err) {
	case "plan_not_found":
		return "操作计划不存在"
	case "plan_invalid":
		return "操作计划无效"
	case "plan_expired":
		return "操作计划已过期，请重新执行 dry-run"
	case "plan_stale":
		return "网站状态或最新备份记录已变化，请重新执行 dry-run"
	case "plan_already_used":
		return "操作计划已经执行过，请重新执行 dry-run"
	case "invalid_domain":
		return "网站域名无效"
	case "site_not_found":
		return "网站不存在"
	case "site_lock_busy":
		return "网站正在执行其它操作，请稍后重试"
	case "migration_locked":
		return "网站正在迁移维护中，请稍后重试"
	case "ai_development_active":
		return "该网站已开启 AI 开发访问，请先关闭授权"
	case "backup_record_unavailable":
		return "无法读取最新备份记录，请稍后重试"
	case "rollback_required":
		return "备份完成后复查失败，请人工检查"
	case "backup_failed":
		return "数据库备份失败"
	default:
		return "数据库备份操作失败"
	}
}

type databaseBackupPlanPayload struct {
	CapabilityID     string `json:"capability_id"`
	Action           string `json:"action"`
	SiteID           int    `json:"site_id"`
	Domain           string `json:"domain"`
	Status           string `json:"status"`
	LatestBackupHash string `json:"latest_backup_hash"`
	PanelVersion     string `json:"panel_version"`
	ManifestSHA256   string `json:"manifest_sha256"`
	GeneratedAt      string `json:"generated_at"`
	ExpiresAt        string `json:"expires_at"`
	SideEffects      string `json:"side_effects"`
}

type databaseBackupPlanFile struct {
	Payload     databaseBackupPlanPayload `json:"payload"`
	PlanDigest  string                    `json:"plan_digest"`
	Consumed    bool                      `json:"consumed"`
	ConsumedAt  string                    `json:"consumed_at,omitempty"`
	OperationID string                    `json:"operation_id,omitempty"`
}

type DatabaseBackupResponse struct {
	OK                   bool     `json:"ok"`
	Action               string   `json:"action"`
	Mode                 string   `json:"mode"`
	SiteID               int      `json:"site_id"`
	Domain               string   `json:"domain"`
	Status               string   `json:"status,omitempty"`
	Scope                string   `json:"scope"`
	BackupID             int64    `json:"backup_id,omitempty"`
	Filename             string   `json:"filename,omitempty"`
	LatestBackupFilename string   `json:"latest_backup_filename,omitempty"`
	FileSize             int64    `json:"file_size,omitempty"`
	CreatedAt            string   `json:"created_at,omitempty"`
	TransportStatus      string   `json:"transport_status,omitempty"`
	NotPerformed         []string `json:"not_performed"`
	PlanDigest           string   `json:"plan_digest,omitempty"`
	ExpiresAt            string   `json:"expires_at,omitempty"`
	PanelVersion         string   `json:"panel_version,omitempty"`
	ManifestSHA256       string   `json:"manifest_sha256,omitempty"`
	Consumed             bool     `json:"consumed"`
	OperationID          string   `json:"operation_id,omitempty"`
}

type databaseBackupSnapshot struct {
	ID              int64
	Filename        string
	FileSize        int64
	CreatedAt       string
	TransportStatus string
}

func loadDatabaseBackupTarget(ctx context.Context, siteID int) (*models.Website, error) {
	site, err := loadSiteStateTarget(ctx, siteID)
	if err != nil {
		return nil, err
	}
	db := database.GetDB()
	if db == nil {
		return nil, errors.New("database unavailable")
	}
	if err := db.QueryRowContext(ctx, "SELECT db_name FROM websites WHERE id=?", siteID).Scan(&site.DBName); err != nil {
		return nil, err
	}
	return site, nil
}

func latestDatabaseBackup(ctx context.Context, siteID int) (databaseBackupSnapshot, error) {
	db := database.GetDB()
	if db == nil {
		return databaseBackupSnapshot{}, errors.New("database unavailable")
	}
	var snapshot databaseBackupSnapshot
	err := db.QueryRowContext(ctx, `SELECT id, filename, file_size, created_at, transport_status
		FROM db_backups WHERE site_id=? ORDER BY created_at DESC, id DESC LIMIT 1`, siteID).Scan(
		&snapshot.ID, &snapshot.Filename, &snapshot.FileSize, &snapshot.CreatedAt, &snapshot.TransportStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return databaseBackupSnapshot{}, nil
	}
	return snapshot, err
}

func databaseBackupSnapshotHash(snapshot databaseBackupSnapshot) string {
	if snapshot.ID == 0 {
		return ""
	}
	value := fmt.Sprintf("%d|%s|%s|%d", snapshot.ID, snapshot.Filename, snapshot.CreatedAt, snapshot.FileSize)
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func makeDatabaseBackupPlanPayload(site *models.Website, latest databaseBackupSnapshot, panelVersion string, generated, expires time.Time) databaseBackupPlanPayload {
	return databaseBackupPlanPayload{
		CapabilityID: "server-ai.site-database-backup", Action: "create", SiteID: site.ID,
		Domain: site.Domain, Status: string(site.Status), LatestBackupHash: databaseBackupSnapshotHash(latest),
		PanelVersion: panelVersion, ManifestSHA256: aiPanelManifestPayloadSHA256,
		GeneratedAt: generated.UTC().Format(time.RFC3339Nano), ExpiresAt: expires.UTC().Format(time.RFC3339Nano),
		SideEffects: "create managed MariaDB database backup",
	}
}

func digestDatabaseBackupPlan(payload databaseBackupPlanPayload) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func databaseBackupPlanPath(digest string) (string, error) {
	dir, err := siteStatePlanDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "db-backup-"+digest+".json"), nil
}

func writeDatabaseBackupPlan(digest string, file databaseBackupPlanFile) error {
	path, err := databaseBackupPlanPath(digest)
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

func readDatabaseBackupPlan(digest string) (databaseBackupPlanFile, error) {
	if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
		return databaseBackupPlanFile{}, siteStateError("plan_invalid", "操作计划摘要格式不合法")
	}
	path, err := databaseBackupPlanPath(digest)
	if err != nil {
		return databaseBackupPlanFile{}, siteStateError("plan_not_found", "操作计划不存在")
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return databaseBackupPlanFile{}, siteStateError("plan_not_found", "操作计划不存在")
	}
	if err != nil {
		return databaseBackupPlanFile{}, siteStateError("plan_invalid", "读取操作计划失败")
	}
	var file databaseBackupPlanFile
	if json.Unmarshal(data, &file) != nil {
		return databaseBackupPlanFile{}, siteStateError("plan_invalid", "操作计划损坏")
	}
	return file, nil
}

func databaseBackupResponse(file databaseBackupPlanFile, mode string, site *models.Website, snapshot databaseBackupSnapshot) DatabaseBackupResponse {
	response := DatabaseBackupResponse{
		OK: true, Action: file.Payload.Action, Mode: mode, SiteID: file.Payload.SiteID, Domain: file.Payload.Domain,
		Status: string(site.Status), Scope: databaseBackupScope, BackupID: snapshot.ID,
		FileSize: snapshot.FileSize, CreatedAt: snapshot.CreatedAt, TransportStatus: snapshot.TransportStatus,
		NotPerformed: append([]string(nil), databaseBackupNotPerformed...), PlanDigest: file.PlanDigest,
		ExpiresAt: file.Payload.ExpiresAt, PanelVersion: file.Payload.PanelVersion, ManifestSHA256: file.Payload.ManifestSHA256,
		Consumed: file.Consumed, OperationID: file.OperationID,
	}
	if mode == "dry_run" {
		// This is the most recent existing record, not the filename that the
		// future backup will receive. Keep it explicit to avoid AI consumers
		// treating it as an execution result.
		if snapshot.ID != 0 {
			response.LatestBackupFilename = filepath.Base(snapshot.Filename)
		}
	} else {
		response.Filename = filepath.Base(snapshot.Filename)
	}
	return response
}

func checkDatabaseBackupGates(ctx context.Context, site *models.Website) error {
	if blocked, err := database.IsAIDevelopmentAccessBlocking(ctx, database.GetDB(), int64(site.ID)); err != nil {
		return siteStateError("ai_development_active", "检查 AI 开发授权失败")
	} else if blocked {
		return siteStateError("ai_development_active", "该网站已开启 AI 开发访问，请先关闭授权")
	}
	if locked, err := SiteMigrationLocked(ctx, site.ID, site.Domain); err != nil {
		return siteStateError("migration_locked", "检查站点迁移锁失败")
	} else if locked {
		return siteStateError("migration_locked", "网站正在迁移维护中")
	}
	return nil
}

func PrepareDatabaseBackupPlan(ctx context.Context, domain, panelVersion string) (DatabaseBackupResponse, error) {
	site, err := findSiteStateByDomain(ctx, domain)
	if err != nil {
		return DatabaseBackupResponse{}, err
	}
	if err := loadDatabaseBackupTargetFields(ctx, site); err != nil {
		return DatabaseBackupResponse{}, err
	}
	if err := checkDatabaseBackupGates(ctx, site); err != nil {
		return DatabaseBackupResponse{}, err
	}
	if !TryAcquireCompanionDeployLock(site.ID) {
		return DatabaseBackupResponse{}, siteStateError("site_lock_busy", "网站正在执行其它操作，请稍后重试")
	}
	ReleaseSiteOpLock(site.ID)
	latest, err := latestDatabaseBackup(ctx, site.ID)
	if err != nil {
		return DatabaseBackupResponse{}, &DatabaseBackupError{Code: "backup_record_unavailable", Message: "读取最新备份记录失败"}
	}
	now := time.Now().UTC()
	payload := makeDatabaseBackupPlanPayload(site, latest, panelVersion, now, now.Add(siteStatePlanTTL))
	digest, err := digestDatabaseBackupPlan(payload)
	if err != nil {
		return DatabaseBackupResponse{}, &DatabaseBackupError{Code: "plan_invalid", Message: "生成操作计划失败"}
	}
	file := databaseBackupPlanFile{Payload: payload, PlanDigest: digest}
	if err := writeDatabaseBackupPlan(digest, file); err != nil {
		return DatabaseBackupResponse{}, &DatabaseBackupError{Code: "backup_failed", Message: "保存操作计划失败"}
	}
	return databaseBackupResponse(file, "dry_run", site, latest), nil
}

func executeManagedDatabaseBackup(ctx context.Context, siteID int) (DatabaseBackupResponse, error) {
	site, err := loadDatabaseBackupTarget(ctx, siteID)
	if err != nil {
		return DatabaseBackupResponse{}, &DatabaseBackupError{Code: "site_not_found", Message: "网站不存在"}
	}
	if err := checkDatabaseBackupGates(ctx, site); err != nil {
		return DatabaseBackupResponse{}, err
	}
	if !TryAcquireCompanionDeployLock(site.ID) {
		return DatabaseBackupResponse{}, siteStateError("site_lock_busy", "网站正在执行其它操作，请稍后重试")
	}
	defer ReleaseSiteOpLock(site.ID)
	result, err := createDatabaseBackupCore(site, false)
	if err != nil {
		return DatabaseBackupResponse{}, &DatabaseBackupError{Code: "backup_failed", Message: err.Error()}
	}
	snapshot, err := verifyDatabaseBackupResult(ctx, site, result)
	if err != nil {
		return DatabaseBackupResponse{}, err
	}
	return DatabaseBackupResponse{OK: true, Action: "create", Mode: "execute", SiteID: site.ID, Domain: site.Domain, Status: string(site.Status), Scope: databaseBackupScope, BackupID: snapshot.ID, Filename: filepath.Base(snapshot.Filename), FileSize: snapshot.FileSize, CreatedAt: snapshot.CreatedAt, TransportStatus: snapshot.TransportStatus, NotPerformed: append([]string(nil), databaseBackupNotPerformed...), Consumed: true, OperationID: uuid.NewString()}, nil
}

func databaseBackupByID(ctx context.Context, siteID int, id int64) (databaseBackupSnapshot, error) {
	db := database.GetDB()
	var snapshot databaseBackupSnapshot
	err := db.QueryRowContext(ctx, `SELECT id, filename, file_size, created_at, transport_status FROM db_backups WHERE id=? AND site_id=?`, id, siteID).Scan(&snapshot.ID, &snapshot.Filename, &snapshot.FileSize, &snapshot.CreatedAt, &snapshot.TransportStatus)
	return snapshot, err
}

func verifyDatabaseBackupResult(ctx context.Context, site *models.Website, result databaseBackupCoreResult) (databaseBackupSnapshot, error) {
	snapshot, err := databaseBackupByID(ctx, site.ID, result.RecordID)
	if err != nil || snapshot.Filename != result.Filename || snapshot.FileSize != result.FileSize {
		return databaseBackupSnapshot{}, &DatabaseBackupError{Code: "rollback_required", Message: "备份完成后记录复查失败"}
	}
	latest, err := latestDatabaseBackup(ctx, site.ID)
	if err != nil {
		return databaseBackupSnapshot{}, &DatabaseBackupError{Code: "rollback_required", Message: "备份完成后最新记录复查失败"}
	}
	if latest.ID != snapshot.ID {
		// Automatic backups currently use an independent scheduler path. If one
		// inserts a newer record before this verification, the managed backup is
		// still present and matches the result; do not misreport it as a rollback.
		latest = snapshot
	}
	cfg := config.AppConfig
	if cfg == nil || strings.TrimSpace(cfg.Panel.BackupDir) == "" {
		return databaseBackupSnapshot{}, &DatabaseBackupError{Code: "rollback_required", Message: "备份文件复查失败"}
	}
	path := filepath.Join(cfg.Panel.BackupDir, site.Domain, "db", filepath.Base(snapshot.Filename))
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != snapshot.FileSize {
		return databaseBackupSnapshot{}, &DatabaseBackupError{Code: "rollback_required", Message: "备份文件复查失败"}
	}
	f, err := os.Open(path)
	if err != nil {
		return databaseBackupSnapshot{}, &DatabaseBackupError{Code: "rollback_required", Message: "备份文件不可读取"}
	}
	if err := f.Close(); err != nil {
		return databaseBackupSnapshot{}, &DatabaseBackupError{Code: "rollback_required", Message: "备份文件复查失败"}
	}
	return latest, nil
}

func loadDatabaseBackupTargetFields(ctx context.Context, site *models.Website) error {
	if site == nil || database.GetDB() == nil {
		return siteStateError("site_not_found", "网站不存在")
	}
	if err := database.GetDB().QueryRowContext(ctx, "SELECT db_name FROM websites WHERE id=?", site.ID).Scan(&site.DBName); err != nil {
		return siteStateError("site_not_found", "网站不存在")
	}
	return nil
}

func ExecuteDatabaseBackupPlan(ctx context.Context, digest, panelVersion string) (DatabaseBackupResponse, error) {
	file, err := readDatabaseBackupPlan(digest)
	if err != nil {
		return DatabaseBackupResponse{}, err
	}
	if file.Payload.Action != "create" || file.PlanDigest != digest {
		return DatabaseBackupResponse{}, siteStateError("plan_invalid", "操作计划与命令不匹配")
	}
	if expected, digestErr := digestDatabaseBackupPlan(file.Payload); digestErr != nil || expected != digest {
		return DatabaseBackupResponse{}, siteStateError("plan_invalid", "操作计划内容无效")
	}
	site, err := loadDatabaseBackupTarget(ctx, file.Payload.SiteID)
	if err != nil || site.Domain != file.Payload.Domain {
		return DatabaseBackupResponse{}, siteStateError("plan_stale", "网站目标已变化，请重新执行 dry-run")
	}
	if !TryAcquireCompanionDeployLock(site.ID) {
		return DatabaseBackupResponse{}, siteStateError("site_lock_busy", "网站正在执行其它操作，请稍后重试")
	}
	defer ReleaseSiteOpLock(site.ID)
	file, err = readDatabaseBackupPlan(digest)
	if err != nil {
		return DatabaseBackupResponse{}, err
	}
	if file.Consumed {
		return DatabaseBackupResponse{}, siteStateError("plan_already_used", "操作计划已经执行过")
	}
	if expected, digestErr := digestDatabaseBackupPlan(file.Payload); digestErr != nil || expected != digest {
		return DatabaseBackupResponse{}, siteStateError("plan_invalid", "操作计划内容无效")
	}
	expires, parseErr := time.Parse(time.RFC3339Nano, file.Payload.ExpiresAt)
	if parseErr != nil || !time.Now().UTC().Before(expires) {
		return DatabaseBackupResponse{}, siteStateError("plan_expired", "操作计划已过期，请重新执行 dry-run")
	}
	// Refresh the complete site snapshot after taking the lock. The pre-lock
	// lookup only locates the plan target; status changes while waiting must
	// participate in the digest recheck instead of being reported stale later.
	site, err = loadDatabaseBackupTarget(ctx, file.Payload.SiteID)
	if err != nil || site.Domain != file.Payload.Domain {
		return DatabaseBackupResponse{}, siteStateError("plan_stale", "网站目标已变化，请重新执行 dry-run")
	}
	if err := checkDatabaseBackupGates(ctx, site); err != nil {
		return DatabaseBackupResponse{}, err
	}
	if err := loadDatabaseBackupTargetFields(ctx, site); err != nil {
		return DatabaseBackupResponse{}, err
	}
	latest, err := latestDatabaseBackup(ctx, site.ID)
	if err != nil {
		return DatabaseBackupResponse{}, &DatabaseBackupError{Code: "backup_record_unavailable", Message: "读取最新备份记录失败"}
	}
	current := makeDatabaseBackupPlanPayload(site, latest, panelVersion, mustParseTime(file.Payload.GeneratedAt), expires)
	if current != file.Payload {
		return DatabaseBackupResponse{}, siteStateError("plan_stale", "网站状态或最新备份记录已变化，请重新执行 dry-run")
	}
	file.Consumed = true
	file.ConsumedAt = time.Now().UTC().Format(time.RFC3339Nano)
	file.OperationID = uuid.NewString()
	if err := writeDatabaseBackupPlan(digest, file); err != nil {
		return DatabaseBackupResponse{}, &DatabaseBackupError{Code: "backup_failed", Message: "确认操作计划失败"}
	}
	result, err := createDatabaseBackupCore(site, false)
	if err != nil {
		recordOperationLog("create_database_backup", site.Domain, "failed", err.Error())
		return DatabaseBackupResponse{}, &DatabaseBackupError{Code: "backup_failed", Message: err.Error()}
	}
	snapshot, err := verifyDatabaseBackupResult(ctx, site, result)
	if err != nil {
		recordOperationLog("create_database_backup", site.Domain, "failed", err.Error())
		return DatabaseBackupResponse{}, &DatabaseBackupError{Code: "rollback_required", Message: "备份完成后复查失败，请人工检查"}
	}
	recordOperationLog("create_database_backup", site.Domain, "success", "数据库备份已创建")
	return databaseBackupResponse(file, "execute", site, snapshot), nil
}

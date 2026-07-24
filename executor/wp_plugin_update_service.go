package executor

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

var (
	ErrWPPluginUpdateInvalid         = errors.New("invalid plugin update request")
	ErrWPPluginUpdateNotFound        = errors.New("plugin update resource not found")
	ErrWPPluginUpdateConflict        = errors.New("plugin update request conflict")
	ErrWPPluginUpdateBusy            = errors.New("plugin update service busy")
	ErrWPPluginUpdateUnavailable     = errors.New("plugin update upstream unavailable")
	ErrWPPluginUpdateSiteBusy        = errors.New("plugin update blocked by active site restore")
	ErrWPPluginUpdateNotInRepository = errors.New("plugin not available in the official WordPress.org repository")
	ErrWPPluginUpdateLicenseInvalid  = errors.New("plugin update license invalid or not activated")
	ErrWPPluginUpdateLicenseProtocolUnsupported = errors.New("plugin update uses a commercial license download protocol not supported by the panel")
)

type wpPluginOffer struct {
	Slug, Version, DownloadURL string
}

type wpPluginOfferFetcher func(ctx context.Context, siteID int, webRoot, componentKey string) (wpPluginOffer, error)
type wpPluginPackageDownloader func(context.Context, string, string) (string, string, error)

type WPPluginUpdateService struct {
	db            *sql.DB
	store         *wpUpdateStore
	artifacts     *wpUpdateArtifactService
	confirmations *wpPluginConfirmationStore
	fetchOffer    wpPluginOfferFetcher
	download      wpPluginPackageDownloader
	now           func() time.Time
}

type wpPluginUpdateCandidate struct {
	siteID                                            int
	domain, webRoot, collectionID, componentKey, name string
	currentVersion, targetVersion                     string
	lastSuccess                                       time.Time
}

func NewWPPluginUpdateService(db *sql.DB, backupDir string) (*WPPluginUpdateService, error) {
	if db == nil || !validWPCoreUpdateRoot(backupDir) {
		return nil, ErrWPPluginUpdateInvalid
	}
	store := newWPUpdateStore(db)
	artifacts, err := newWPUpdateArtifactService(store, filepath.Join(filepath.Clean(backupDir), "wp-updates"), defaultWPUpdateDatabaseDumper)
	if err != nil {
		return nil, ErrWPPluginUpdateUnavailable
	}
	offerRunner, err := newWPUpdateOfferRunner(db)
	if err != nil {
		return nil, ErrWPPluginUpdateUnavailable
	}
	return &WPPluginUpdateService{
		db: db, store: store, artifacts: artifacts, confirmations: newWPPluginConfirmationStore(),
		fetchOffer: func(ctx context.Context, siteID int, webRoot, componentKey string) (wpPluginOffer, error) {
			return offerRunner.FetchPluginOffer(ctx, siteID, webRoot, componentKey)
		},
		download: defaultWPPluginPackageDownloader(nil, artifacts.root), now: time.Now,
	}, nil
}

func (s *WPPluginUpdateService) Preview(ctx context.Context, siteID int, username, componentKey string) (models.WPPluginUpdatePreview, error) {
	if s == nil || siteID <= 0 || username == "" || !validWPPluginComponentKey(componentKey) {
		return models.WPPluginUpdatePreview{}, ErrWPPluginUpdateInvalid
	}
	candidate, err := s.loadCandidate(ctx, siteID, componentKey)
	if err != nil {
		if errors.Is(err, ErrWPPluginUpdateConflict) {
			log.Printf("插件更新预览冲突 site=%d domain=%s component=%s: 候选加载返回冲突（站点状态/库存过期/多站点/版本不一致）", siteID, candidate.domain, componentKey)
		}
		return models.WPPluginUpdatePreview{}, err
	}
	if wpConfigHasUserFileModsLock(candidate.webRoot) {
		log.Printf("插件更新预览冲突 site=%d domain=%s component=%s: 检测到 wp-config 用户文件修改锁", siteID, candidate.domain, componentKey)
		return models.WPPluginUpdatePreview{}, ErrWPPluginUpdateConflict
	}
	slug := componentSlug(componentKey)
	offer, err := s.fetchOffer(ctx, siteID, candidate.webRoot, componentKey)
	if err != nil {
		log.Printf("插件更新预览失败 site=%d domain=%s component=%s: 查询站点 WordPress 更新信息失败: %v", siteID, candidate.domain, componentKey, err)
		if errors.Is(err, errWPOfferNotFound) {
			return models.WPPluginUpdatePreview{}, ErrWPPluginUpdateNotInRepository
		}
		if errors.Is(err, errWPOfferLicenseInvalid) {
			return models.WPPluginUpdatePreview{}, ErrWPPluginUpdateLicenseInvalid
		}
		return models.WPPluginUpdatePreview{}, ErrWPPluginUpdateUnavailable
	}
	if offer.Slug != slug || offer.Version != candidate.targetVersion {
		log.Printf("插件更新预览冲突 site=%d domain=%s component=%s: offer(slug=%q version=%q) 与候选(target=%q slug=%q) 不匹配",
			siteID, candidate.domain, componentKey, offer.Slug, offer.Version, candidate.targetVersion, slug)
		return models.WPPluginUpdatePreview{}, ErrWPPluginUpdateConflict
	}
	if !validWPPluginDownloadURL(offer.DownloadURL, slug, candidate.targetVersion) {
		if wpUpdateDownloadURLUsesLicenseProtocol(offer.DownloadURL) {
			log.Printf("插件更新预览冲突 site=%d domain=%s component=%s: offer(slug=%q version=%q url=%q) 使用商业授权下载协议，面板暂不支持",
				siteID, candidate.domain, componentKey, offer.Slug, offer.Version, offer.DownloadURL)
			return models.WPPluginUpdatePreview{}, ErrWPPluginUpdateLicenseProtocolUnsupported
		}
		log.Printf("插件更新预览冲突 site=%d domain=%s component=%s: offer(slug=%q version=%q url=%q) 与候选(target=%q slug=%q) 不匹配；downloadURLValid=%v",
			siteID, candidate.domain, componentKey, offer.Slug, offer.Version, offer.DownloadURL, candidate.targetVersion, slug,
			validWPPluginDownloadURL(offer.DownloadURL, slug, candidate.targetVersion))
		return models.WPPluginUpdatePreview{}, ErrWPPluginUpdateConflict
	}
	now := s.now().UTC()
	recent := recentBackupOrNil(s.store, ctx, siteID, now)
	var recentID int64
	if recent != nil {
		recentID = recent.BackupID
	}
	record, err := s.confirmations.create(wpPluginConfirmation{
		username: username, siteID: siteID, domain: candidate.domain, collectionID: candidate.collectionID,
		componentKey: componentKey, currentVersion: candidate.currentVersion, targetVersion: candidate.targetVersion,
		downloadURL: offer.DownloadURL, recentBackupID: recentID,
	})
	if err != nil {
		return models.WPPluginUpdatePreview{}, ErrWPPluginUpdateBusy
	}
	expires := record.expiresAt
	return models.WPPluginUpdatePreview{
		Available: true, SiteID: siteID, Domain: candidate.domain, ComponentKey: componentKey, Name: candidate.name,
		CurrentVersion: candidate.currentVersion, TargetVersion: candidate.targetVersion, PackageSource: "wordpress.org",
		VerificationRequired: "structure_only", DatabaseBackup: true, RecentDatabaseBackup: recent, PluginFilesBackup: true,
		ConfirmationToken: record.token, ExpiresAt: &expires,
	}, nil
}

func (s *WPPluginUpdateService) Confirm(ctx context.Context, siteID int, username, componentKey, token, target, backupMode string) (models.WPPluginUpdateTask, error) {
	return s.confirm(ctx, siteID, username, componentKey, token, target, backupMode, false, "", 0)
}

// ConfirmForBatch 与 Confirm 相同，但用于批量更新编排器代表用户派发批量中的下一项：
// 生成的任务 auto_rollback=0（失败后不自动回滚，等待用户决定）并打上 batchID 分组标记。
// databaseBackupSourceID 由编排器通过 ensureBatchDatabaseBackupSource 固定下来，是整个
// 批量共享的数据库备份来源，不使用 Preview 确认令牌里那个基于「最近可复用备份」推断出的
// 备份 id——后者一旦批量里某一项失败进入 requires_attention 状态就会失效，导致后续每一项
// 都被迫重新备份一次数据库，而不是复用批量开始时的那一次。
func (s *WPPluginUpdateService) ConfirmForBatch(ctx context.Context, siteID int, username, componentKey, token, target, backupMode, batchID string, databaseBackupSourceID int64) (models.WPPluginUpdateTask, error) {
	if batchID == "" {
		return models.WPPluginUpdateTask{}, ErrWPPluginUpdateInvalid
	}
	return s.confirm(ctx, siteID, username, componentKey, token, target, backupMode, true, batchID, databaseBackupSourceID)
}

func (s *WPPluginUpdateService) confirm(ctx context.Context, siteID int, username, componentKey, token, target, backupMode string, forBatch bool, batchID string, batchBackupSourceID int64) (models.WPPluginUpdateTask, error) {
	if s == nil || siteID <= 0 || username == "" || !validWPPluginComponentKey(componentKey) || token == "" || target == "" {
		return models.WPPluginUpdateTask{}, ErrWPPluginUpdateInvalid
	}
	record, err := s.confirmations.consume(token, username, siteID, componentKey, target)
	if err != nil {
		return models.WPPluginUpdateTask{}, ErrWPPluginUpdateConflict
	}
	if backupMode == "" {
		backupMode = "fresh"
	}
	var sourceID int64
	if backupMode == "reuse" {
		if forBatch {
			sourceID = batchBackupSourceID
		} else {
			sourceID = record.recentBackupID
		}
	}
	if forBatch {
		if err := s.store.validateBatchDatabaseBackup(ctx, siteID, backupMode, sourceID, s.artifacts.root); err != nil {
			return models.WPPluginUpdateTask{}, ErrWPPluginUpdateConflict
		}
	} else if err := s.store.validateDatabaseBackupChoice(ctx, siteID, backupMode, sourceID, s.artifacts.root, s.now().UTC()); err != nil {
		return models.WPPluginUpdateTask{}, ErrWPPluginUpdateConflict
	}
	candidate, err := s.loadCandidate(ctx, siteID, componentKey)
	if err != nil || candidate.collectionID != record.collectionID || candidate.currentVersion != record.currentVersion ||
		candidate.targetVersion != record.targetVersion || wpConfigHasUserFileModsLock(candidate.webRoot) {
		return models.WPPluginUpdateTask{}, ErrWPPluginUpdateConflict
	}
	if !TryAcquireSiteOpLock(siteID, "wp_plugin_update") {
		return models.WPPluginUpdateTask{}, ErrWPPluginUpdateSiteBusy
	}
	task, err := s.store.createPluginManualPlan(ctx, WPUpdatePlan{
		SiteID: siteID, ComponentKey: componentKey, CurrentVersion: record.currentVersion, TargetVersion: record.targetVersion,
		PackageSource: "wordpress.org", DownloadURL: record.downloadURL,
		DatabaseBackupMode: backupMode, DatabaseBackupSourceID: sourceID,
		SkipAutoRollback: forBatch, BatchID: batchID,
	}, s.now().UTC())
	// See wp_core_update_service.go Confirm() for why the lock is released
	// immediately after this write instead of held through the download/seal.
	ReleaseSiteOpLock(siteID)
	if err != nil {
		return models.WPPluginUpdateTask{}, ErrWPPluginUpdateConflict
	}
	tempPath, digest, err := s.download(ctx, record.downloadURL, record.targetVersion)
	if err == nil {
		defer os.Remove(tempPath)
		_, _, err = s.artifacts.snapshotValidateAndSealPluginPackage(ctx, task.ID, tempPath, digest)
	}
	if err != nil {
		log.Printf("插件更新下载/封存失败 site=%d domain=%s component=%s task=%s: %v", siteID, record.domain, componentKey, task.ID, err)
		if failErr := s.store.failPreparingPlan(context.Background(), task.ID, "package_prepare_failed", s.now().UTC()); failErr == nil {
			_ = os.RemoveAll(filepath.Join(s.artifacts.root, task.ID))
		} else if current, lookupErr := s.store.getTask(context.Background(), task.ID); lookupErr == nil &&
			current.Status == wpUpdateFailed && current.PlanSealedAt == "" {
			_ = os.RemoveAll(filepath.Join(s.artifacts.root, task.ID))
		}
		return models.WPPluginUpdateTask{}, ErrWPPluginUpdateUnavailable
	}
	sealed, err := s.store.getTask(ctx, task.ID)
	if err != nil {
		return models.WPPluginUpdateTask{}, ErrWPPluginUpdateUnavailable
	}
	_, _ = s.db.ExecContext(context.Background(), `INSERT INTO operation_logs(operation,target,status,message)
		VALUES('wp_plugin_update_request',?,'success',?)`, record.domain, "task="+sealed.ID+" component="+sealed.ComponentKey+" version="+sealed.TargetVersion)
	return s.taskModel(ctx, sealed, false)
}

func (s *WPPluginUpdateService) Task(ctx context.Context, siteID int, taskID string) (models.WPPluginUpdateTask, error) {
	if s == nil || siteID <= 0 || !wpUpdateTaskIDPattern.MatchString(taskID) {
		return models.WPPluginUpdateTask{}, ErrWPPluginUpdateInvalid
	}
	task, err := s.store.getTask(ctx, taskID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (task.SiteID != siteID || task.TaskKind != "update" || task.ComponentType != "plugin") {
		return models.WPPluginUpdateTask{}, ErrWPPluginUpdateNotFound
	}
	if err != nil {
		return models.WPPluginUpdateTask{}, err
	}
	return s.taskModel(ctx, task, true)
}

func (s *WPPluginUpdateService) LatestTask(ctx context.Context, siteID int, componentKey string) (models.WPPluginUpdateTask, error) {
	if s == nil || siteID <= 0 || !validWPPluginComponentKey(componentKey) {
		return models.WPPluginUpdateTask{}, ErrWPPluginUpdateInvalid
	}
	task, err := s.store.latestPluginUpdateTask(ctx, siteID, componentKey)
	if errors.Is(err, sql.ErrNoRows) {
		return models.WPPluginUpdateTask{}, ErrWPPluginUpdateNotFound
	}
	if err != nil {
		return models.WPPluginUpdateTask{}, err
	}
	return s.taskModel(ctx, task, true)
}

func (s *WPPluginUpdateService) loadCandidate(ctx context.Context, siteID int, componentKey string) (wpPluginUpdateCandidate, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return wpPluginUpdateCandidate{}, err
	}
	defer tx.Rollback()
	var c wpPluginUpdateCandidate
	var siteStatus, siteType, inventoryStatus, lastSuccess string
	var multisite, blocked int
	err = tx.QueryRowContext(ctx, `SELECT w.id,w.domain,w.web_root,w.status,w.site_type,s.status,s.collection_id,
		s.is_multisite,COALESCE(s.last_success_at,''),
		(SELECT COUNT(*) FROM wp_update_tasks t WHERE t.site_id=w.id AND
			(t.status IN ('preparing','queued','running') OR (t.status='interrupted_unknown' AND t.manual_disposition='')))
		FROM websites w JOIN site_wp_inventory_state s ON s.site_id=w.id WHERE w.id=?`, siteID).
		Scan(&c.siteID, &c.domain, &c.webRoot, &siteStatus, &siteType, &inventoryStatus, &c.collectionID, &multisite, &lastSuccess, &blocked)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrWPPluginUpdateNotFound
	}
	if err != nil {
		return c, err
	}
	parsedSuccess, err := parseRequiredWPInventoryTime(lastSuccess)
	if err != nil {
		log.Printf("插件更新候选冲突 site=%d domain=%s: 库存成功时间解析失败 last_success=%q: %v", siteID, c.domain, lastSuccess, err)
		return c, ErrWPPluginUpdateConflict
	}
	c.lastSuccess = parsedSuccess
	if siteStatus != "active" || siteType != "wordpress" || inventoryStatus != "complete" || multisite != 0 ||
		c.collectionID == "" || blocked != 0 || !filepath.IsAbs(c.webRoot) || sTimeStale(c.lastSuccess, s.now().UTC()) {
		log.Printf("插件更新候选冲突 site=%d domain=%s: 站点/库存状态不满足 siteStatus=%q siteType=%q inventoryStatus=%q multisite=%d blocked=%d stale=%v lastSuccess=%q",
			siteID, c.domain, siteStatus, siteType, inventoryStatus, multisite, blocked, sTimeStale(c.lastSuccess, s.now().UTC()), lastSuccess)
		return c, ErrWPPluginUpdateConflict
	}
	c.componentKey = componentKey
	rows, err := tx.QueryContext(ctx, `SELECT c.name,c.version,u.target_version
		FROM site_wp_components c JOIN site_wp_component_updates u
			ON u.site_id=c.site_id AND u.collection_id=c.collection_id AND u.component_type=c.component_type
			AND u.component_key=c.component_key
		WHERE c.site_id=? AND c.collection_id=? AND c.component_type='plugin' AND c.component_key=?
		`, siteID, c.collectionID, componentKey)
	if err != nil {
		return c, err
	}
	count := 0
	for rows.Next() {
		count++
		if err := rows.Scan(&c.name, &c.currentVersion, &c.targetVersion); err != nil {
			rows.Close()
			return c, err
		}
	}
	if err := rows.Close(); err != nil {
		return c, err
	}
	if count == 0 {
		return c, ErrWPPluginUpdateNotFound
	}
	if count != 1 || c.name == "" || !wpComponentVersionPattern.MatchString(c.currentVersion) ||
		!wpComponentVersionPattern.MatchString(c.targetVersion) || c.currentVersion == c.targetVersion {
		log.Printf("插件更新候选冲突 site=%d domain=%s component=%s: 组件行异常 count=%d name=%q current=%q target=%q",
			siteID, c.domain, componentKey, count, c.name, c.currentVersion, c.targetVersion)
		return c, ErrWPPluginUpdateConflict
	}
	if err := tx.Commit(); err != nil {
		return c, err
	}
	return c, nil
}

func (s *WPPluginUpdateService) taskModel(ctx context.Context, task WPUpdateTask, includeEvents bool) (models.WPPluginUpdateTask, error) {
	requested, err := parseRequiredWPInventoryTime(task.RequestedAt)
	if err != nil {
		return models.WPPluginUpdateTask{}, err
	}
	started, err := parseOptionalWPInventoryTime(task.StartedAt)
	if err != nil {
		return models.WPPluginUpdateTask{}, err
	}
	finished, err := parseOptionalWPInventoryTime(task.FinishedAt)
	if err != nil {
		return models.WPPluginUpdateTask{}, err
	}
	model := models.WPPluginUpdateTask{
		ID: task.ID, SiteID: task.SiteID, ComponentType: task.ComponentType, ComponentKey: task.ComponentKey,
		TaskKind: task.TaskKind, Status: task.Status, Stage: task.Stage, FailureStage: task.FailureStage,
		RollbackStatus: task.RollbackStatus, RequiresAttention: task.RequiresAttention, ManualDisposition: task.ManualDisposition,
		CurrentVersion: task.CurrentVersion, TargetVersion: task.TargetVersion, VerificationLevel: task.VerificationLevel,
		DatabaseBackupMode: task.DatabaseBackupMode, AutoRollback: task.AutoRollback, BatchID: task.BatchID,
		RequestedAt: requested, StartedAt: started, FinishedAt: finished,
	}
	if !includeEvents {
		return model, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT stage,result,error_code,created_at FROM
		(SELECT id,stage,result,error_code,created_at FROM wp_update_task_events WHERE task_id=? ORDER BY id DESC LIMIT 50)
		ORDER BY id`, task.ID)
	if err != nil {
		return model, err
	}
	defer rows.Close()
	for rows.Next() {
		var event models.WPCoreUpdateTaskEvent
		var created string
		if err := rows.Scan(&event.Stage, &event.Result, &event.ErrorCode, &created); err != nil {
			return model, err
		}
		event.CreatedAt, err = parseRequiredWPInventoryTime(created)
		if err != nil {
			return model, err
		}
		model.Events = append(model.Events, event)
	}
	return model, rows.Err()
}

func defaultWPPluginPackageDownloader(client *http.Client, artifactRoot string) wpPluginPackageDownloader {
	if client == nil {
		client = wpPluginUpdateDownloadClient()
	}
	return func(ctx context.Context, rawURL, targetVersion string) (string, string, error) {
		u, err := url.Parse(rawURL)
		if err != nil || u == nil {
			return "", "", errors.New("invalid plugin package download")
		}
		if !wpComponentVersionPattern.MatchString(targetVersion) {
			return "", "", errors.New("invalid plugin package download")
		}
		var slug string
		if u.Hostname() == "downloads.wordpress.org" {
			suffix := "." + targetVersion + ".zip"
			base := path.Base(u.Path)
			slug = strings.TrimSuffix(base, suffix)
			if base == slug || !validWPPluginDownloadURL(rawURL, slug, targetVersion) {
				return "", "", errors.New("invalid plugin package download")
			}
		} else if !validWPPluginDownloadURL(rawURL, "", targetVersion) {
			return "", "", errors.New("invalid plugin package download")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return "", "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.Request == nil ||
			!validWPPluginDownloadURL(resp.Request.URL.String(), slug, targetVersion) {
			return "", "", errors.New("plugin package download failed")
		}
		temp, err := os.CreateTemp(artifactRoot, ".plugin-download-*.zip")
		if err != nil {
			return "", "", err
		}
		name := temp.Name()
		keep := false
		defer func() {
			_ = temp.Close()
			if !keep {
				_ = os.Remove(name)
			}
		}()
		if err := temp.Chmod(0600); err != nil {
			return "", "", err
		}
		written, err := io.Copy(temp, io.LimitReader(resp.Body, wpDownloadMaxBytes+1))
		if err != nil || written == 0 || written > wpDownloadMaxBytes {
			return "", "", errors.New("plugin package download invalid")
		}
		if err := temp.Sync(); err != nil {
			return "", "", err
		}
		if err := temp.Close(); err != nil {
			return "", "", err
		}
		digest, size, err := hashRegularFile(name)
		if err != nil || size != written {
			return "", "", errors.New("plugin package download changed")
		}
		keep = true
		return name, digest, nil
	}
}

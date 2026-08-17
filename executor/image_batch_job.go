package executor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/database"
)

// imageBatchRunningJob 把取消函数和它所属的 jobID 绑在一起——runImageOptimizationJob
// 结束时按 jobID 比对再删，避免"旧任务收尾的 defer 删除掉了新任务刚写入的 cancel"
// 这种竞态（Go 的函数值不可比较，只能靠额外的 jobID 判断是不是同一个条目）。
type imageBatchRunningJob struct {
	JobID  int64
	Cancel context.CancelFunc
}

var (
	imageBatchMu      sync.Mutex
	imageBatchCancels = map[int]imageBatchRunningJob{} // site_id -> 正在运行任务
)

// imageBatchPace 是两个文件之间的处理间隔，低速批处理避免压垮小服务器，
// 跟现有缓存预加载功能的节奏保持一致。
const imageBatchPace = 300 * time.Millisecond

// imageBatchManifestFlushSize 攒够这么多个文件的 filesize 变更就回写一次，
// 不要攒到整个任务结束才一次性回写一个巨大清单。
const imageBatchManifestFlushSize = 50

type ImageOptimizationJobStatus struct {
	ID             int64
	SiteID         int
	Status         string
	TotalFiles     int
	ProcessedFiles int
	SucceededFiles int
	FailedFiles    int
	SkippedFiles   int
	BytesBefore    int64
	BytesAfter     int64
	LastError      string
	CreatedAt      string
	UpdatedAt      string
	FinishedAt     sql.NullString
}

// ResetStuckImageOptimizationJobs 在面板启动时把上次进程退出前遗留的
// queued/running 任务清成 stopped——这些任务的执行 goroutine 随进程一起消失了，
// 但行还留着，唯一索引会一直拒绝该站点的新任务，必须在启动时清掉，不能等用户
// 手动改库。
func ResetStuckImageOptimizationJobs() {
	db := database.GetDB()
	res, err := db.Exec(`UPDATE site_image_optimization_jobs
		SET status='stopped', last_error='面板重启，任务已自动停止', finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE status IN ('queued','running')`)
	if err != nil {
		log.Printf("[图片优化] 启动清理遗留任务失败: %v", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("[图片优化] 启动清理了 %d 个遗留的 queued/running 任务", n)
	}
}

// StartImageOptimizationJob 为站点创建一个新的历史图库批量优化任务并异步执行。
// 同一站点同时只能有一个 queued/running 任务，由
// ux_site_image_optimization_jobs_active_site 唯一索引保证。
func StartImageOptimizationJob(siteID int) (int64, error) {
	if !ImageBatchBinariesReady() {
		return 0, errors.New("服务器尚未就绪：jpegoptim/optipng 还没有安装完成，请稍后重试")
	}

	db := database.GetDB()
	var webRoot, systemUser string
	if err := db.QueryRow(`SELECT web_root, system_user FROM websites WHERE id=? AND site_type='wordpress'`, siteID).
		Scan(&webRoot, &systemUser); err != nil {
		return 0, fmt.Errorf("站点不存在或不是 WordPress 站点: %w", err)
	}

	res, err := db.Exec(`INSERT INTO site_image_optimization_jobs (site_id, status) VALUES (?, 'queued')`, siteID)
	if err != nil {
		return 0, fmt.Errorf("创建任务失败（可能已有正在进行的任务）: %w", err)
	}
	jobID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	imageBatchMu.Lock()
	imageBatchCancels[siteID] = imageBatchRunningJob{JobID: jobID, Cancel: cancel}
	imageBatchMu.Unlock()

	go runImageOptimizationJob(ctx, jobID, siteID, webRoot, systemUser)
	return jobID, nil
}

// StopImageOptimizationJob 取消该站点当前正在运行的任务。
func StopImageOptimizationJob(siteID int) error {
	imageBatchMu.Lock()
	job, ok := imageBatchCancels[siteID]
	imageBatchMu.Unlock()
	if !ok {
		return errors.New("该站点没有正在进行的图片优化任务")
	}
	job.Cancel()
	return nil
}

// GetImageOptimizationJobStatus 返回该站点最近一次任务的状态，没有任务时返回 nil。
func GetImageOptimizationJobStatus(siteID int) (*ImageOptimizationJobStatus, error) {
	db := database.GetDB()
	row := db.QueryRow(`SELECT id, site_id, status, total_files, processed_files, succeeded_files, failed_files, skipped_files,
		bytes_before, bytes_after, last_error, created_at, updated_at, finished_at
		FROM site_image_optimization_jobs WHERE site_id=? ORDER BY id DESC LIMIT 1`, siteID)
	var s ImageOptimizationJobStatus
	if err := row.Scan(&s.ID, &s.SiteID, &s.Status, &s.TotalFiles, &s.ProcessedFiles, &s.SucceededFiles, &s.FailedFiles, &s.SkippedFiles,
		&s.BytesBefore, &s.BytesAfter, &s.LastError, &s.CreatedAt, &s.UpdatedAt, &s.FinishedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

// GetImageOptimizationLifetimeSavedBytes 汇总该站点历史上所有批量任务实际节省的
// 字节数——单次任务的 bytes_before/bytes_after 只统计"这次任务处理过的文件"，
// 跳过的文件（幂等命中）不会计入，所以一次任务展示的节省量会随着可优化文件越
// 来越少而趋近于 0；这里把所有历史任务加总，给出"这个站点总共省了多少"。
func GetImageOptimizationLifetimeSavedBytes(siteID int) (int64, error) {
	db := database.GetDB()
	var saved sql.NullInt64
	err := db.QueryRow(`SELECT SUM(bytes_before - bytes_after) FROM site_image_optimization_jobs WHERE site_id=?`, siteID).Scan(&saved)
	if err != nil {
		return 0, err
	}
	return saved.Int64, nil
}

func runImageOptimizationJob(ctx context.Context, jobID int64, siteID int, webRoot, systemUser string) {
	db := database.GetDB()
	defer func() {
		imageBatchMu.Lock()
		if job, ok := imageBatchCancels[siteID]; ok && job.JobID == jobID {
			delete(imageBatchCancels, siteID)
		}
		imageBatchMu.Unlock()
	}()

	db.Exec(`UPDATE site_image_optimization_jobs SET status='running', updated_at=CURRENT_TIMESTAMP WHERE id=?`, jobID)

	candidates, err := scanSiteUploadsForImages(webRoot)
	if err != nil {
		finishImageOptimizationJob(db, jobID, "failed", err.Error())
		return
	}

	pending := filterAlreadyOptimizedImages(db, siteID, candidates)
	// skipped 是幂等指纹命中、本次不需要重新处理的文件数——只在界面上展示，
	// 让用户能看出"总数变小了"是因为跳过了已处理文件，不是又从头扫描一遍。
	skipped := len(candidates) - len(pending)
	db.Exec(`UPDATE site_image_optimization_jobs SET total_files=?, skipped_files=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		len(pending), skipped, jobID)

	manifest := make(map[string]int64)
	var processed, succeeded, failed int
	var bytesBefore, bytesAfter int64

	stopped := false
	for _, c := range pending {
		select {
		case <-ctx.Done():
			stopped = true
		default:
		}
		if stopped {
			break
		}

		before, after, newModUnix, optErr := optimizeImageFile(ctx, webRoot, systemUser, c.RelativePath, c.Mime)
		processed++
		if optErr != nil {
			failed++
			log.Printf("[图片优化] site=%d 处理失败 %s: %v", siteID, c.RelativePath, optErr)
		} else {
			succeeded++
			bytesBefore += before
			bytesAfter += after
			if after < before {
				manifest[c.RelativePath] = after
			}
			// 存重编码*之后*的 mtime（newModUnix），不是扫描阶段读到的旧值
			// （c.ModUnix）——jpegoptim/optipng 原地重写文件必然会刷新 mtime，
			// 存旧值会导致下次扫描永远对不上指纹，整个媒体库每次都被重新处理。
			db.Exec(`INSERT INTO site_image_optimization_files (site_id, relative_path, original_size, optimized_size, mtime_unix, processed_at)
				VALUES (?,?,?,?,?,CURRENT_TIMESTAMP)
				ON CONFLICT(site_id, relative_path) DO UPDATE SET
					original_size=excluded.original_size, optimized_size=excluded.optimized_size,
					mtime_unix=excluded.mtime_unix, processed_at=CURRENT_TIMESTAMP`,
				siteID, c.RelativePath, before, after, newModUnix)
		}

		db.Exec(`UPDATE site_image_optimization_jobs
			SET processed_files=?, succeeded_files=?, failed_files=?, bytes_before=?, bytes_after=?, updated_at=CURRENT_TIMESTAMP
			WHERE id=?`, processed, succeeded, failed, bytesBefore, bytesAfter, jobID)

		if len(manifest) >= imageBatchManifestFlushSize {
			// flush 失败（例如恰好被 Stop 取消）时不清空 manifest，让这些条目留到
			// 循环结束后用 context.Background() 重试，避免指纹已落库（上面的
			// INSERT）但 filesize 回写永久丢失、下次任务又因为指纹匹配被跳过。
			if flushImageFilesizeManifest(ctx, webRoot, systemUser, siteID, manifest) {
				manifest = make(map[string]int64)
			}
		}

		time.Sleep(imageBatchPace)
	}

	// 即便任务被停止，已经优化成功的文件也要把 filesize 回写掉，不要因为中途
	// 停止就留下一批"文件已经变小、元数据还是旧值"的不一致状态。
	if len(manifest) > 0 {
		flushImageFilesizeManifest(context.Background(), webRoot, systemUser, siteID, manifest)
	}

	if stopped {
		finishImageOptimizationJob(db, jobID, "stopped", "")
		return
	}
	finishImageOptimizationJob(db, jobID, "succeeded", "")
}

func finishImageOptimizationJob(db *sql.DB, jobID int64, status, lastError string) {
	if _, err := db.Exec(`UPDATE site_image_optimization_jobs
		SET status=?, last_error=?, finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		status, lastError, jobID); err != nil {
		log.Printf("[图片优化] job=%d 更新最终状态失败: %v", jobID, err)
	}
}

// filterAlreadyOptimizedImages 幂等过滤：文件大小和修改时间跟上次处理后记录的
// 一致，说明这个文件自上次处理以来没有变化，跳过，不重复处理。
func filterAlreadyOptimizedImages(db *sql.DB, siteID int, candidates []imageBatchCandidate) []imageBatchCandidate {
	var pending []imageBatchCandidate
	for _, c := range candidates {
		var optSize, mtime int64
		err := db.QueryRow(`SELECT optimized_size, mtime_unix FROM site_image_optimization_files WHERE site_id=? AND relative_path=?`,
			siteID, c.RelativePath).Scan(&optSize, &mtime)
		if err == nil && optSize == c.Size && mtime == c.ModUnix {
			continue
		}
		pending = append(pending, c)
	}
	return pending
}

// flushImageFilesizeManifest 返回是否成功——调用方据此决定要不要清空 manifest。
func flushImageFilesizeManifest(ctx context.Context, webRoot, systemUser string, siteID int, manifest map[string]int64) bool {
	updated, total, err := runFilesizeRewrite(ctx, webRoot, systemUser, manifest)
	if err != nil {
		log.Printf("[图片优化] site=%d filesize 回写失败: %v", siteID, err)
		return false
	}
	log.Printf("[图片优化] site=%d filesize 回写完成 %d/%d", siteID, updated, total)
	return true
}

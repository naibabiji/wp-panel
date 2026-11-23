package executor

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/naibabiji/wp-panel/config"
	"golang.org/x/net/publicsuffix"
)

var (
	ErrPortableBusy        = errors.New("另一个备份校验或恢复任务正在运行")
	ErrPortableState       = errors.New("任务当前无法执行此操作")
	ErrPortableMissing     = errors.New("任务不存在或已删除")
	ErrPortableRetryUpload = errors.New("暂存材料不可用，请重新选择本任务的备份文件")
)

type PortableTask struct {
	Resources         bool                  `json:"resources"`
	ResourcePaths     []string              `json:"resource_paths,omitempty"`
	ID                string                `json:"id"`
	Aliases           []string              `json:"aliases"`
	Domain            string                `json:"domain"`
	URL               string                `json:"url"`
	Status            string                `json:"status"`
	Phase             string                `json:"phase"`
	Message           string                `json:"message"`
	SiteID            int64                 `json:"site_id"`
	ExpiresAt         int64                 `json:"expires_at"`
	Files             []PortableArtifact    `json:"files,omitempty"`
	Offsets           map[string]int64      `json:"offsets,omitempty"`
	SSLUncovered      []string              `json:"ssl_uncovered,omitempty"`
	SSLTemporaryUntil string                `json:"ssl_temporary_until,omitempty"`
	SQLProgress       *SQLRestoreProgress   `json:"sql_progress,omitempty"`
	RemoteSource      *PortableRemoteSource `json:"remote_source,omitempty"`
	RemoteConflict    string                `json:"remote_conflict,omitempty"`

	RemoteDownload *PortableRemoteDownloadProgress `json:"remote_download,omitempty"`
}

type PortableImports struct {
	db           *sql.DB
	cfg          *config.Config
	mu           sync.Mutex
	runningID    string
	sqlProgress  sqlRestoreProgress
	retryDBQuery func(string) (string, error)

	remoteDownloadFile string
}

func NewPortableImports(db *sql.DB, cfg *config.Config) *PortableImports {
	return &PortableImports{db: db, cfg: cfg}
}

func (s *PortableImports) root() (string, error) {
	if !filepath.IsAbs(s.cfg.Panel.DataDir) {
		return "", errors.New("未配置面板私有数据目录")
	}
	dataRoot, err := filepath.EvalSymlinks(s.cfg.Panel.DataDir)
	if err != nil {
		return "", err
	}
	root := filepath.Join(dataRoot, "portable-imports")
	for _, public := range []string{s.cfg.Paths.WWWRoot, s.cfg.Paths.WWWLogs} {
		if public == "" {
			continue
		}
		resolved, err := filepath.EvalSymlinks(public)
		if err == nil {
			public = resolved
		}
		if pathWithin(filepath.Clean(public), root, true) {
			return "", errors.New("备份暂存目录必须在网站目录之外")
		}
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	if err := requirePrivateDir(root); err != nil {
		return "", err
	}
	return root, nil
}

func requirePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0700 || !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("备份暂存目录权限不正确")
	}
	return nil
}

func (s *PortableImports) taskDir(id string) (string, error) {
	if !portableID.MatchString(id) {
		return "", ErrPortableMissing
	}
	root, err := s.root()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, id)
	if err := requirePrivateDir(dir); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return dir, nil
}

const portableTaskColumns = "id, domain, url, status, phase, message, COALESCE(site_id,0), expires_at, log_dir<>'', aliases"

const portableRetryUploadPhase = "retry_uploading"

func scanPortableTask(row scannerPortableTask) (PortableTask, error) {
	var task PortableTask
	var aliases string
	err := row.Scan(&task.ID, &task.Domain, &task.URL, &task.Status, &task.Phase, &task.Message, &task.SiteID, &task.ExpiresAt, &task.Resources, &aliases)
	task.Aliases = strings.Fields(aliases)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrPortableMissing
	}
	return task, err
}

type scannerPortableTask interface{ Scan(...any) error }

func (s *PortableImports) task(id string) (PortableTask, error) {
	return scanPortableTask(s.db.QueryRow("SELECT "+portableTaskColumns+" FROM portable_import_tasks WHERE id=?", id))
}

func (s *PortableImports) List() ([]PortableTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT " + portableTaskColumns + " FROM portable_import_tasks ORDER BY updated_at DESC, rowid DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := make([]PortableTask, 0)
	for rows.Next() {
		task, err := scanPortableTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *PortableImports) loadFiles(id, dir string) ([]byte, portableManifest, []PortableArtifact, error) {
	var bundleSize int64
	var manifestHash string
	if err := s.db.QueryRow("SELECT bundle_size,manifest_sha256 FROM portable_import_tasks WHERE id=?", id).Scan(&bundleSize, &manifestHash); err != nil {
		return nil, portableManifest{}, nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, portableManifest{}, nil, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != manifestHash {
		return nil, portableManifest{}, nil, errors.New("备份清单与任务记录不一致")
	}
	m, _, err := parsePortableManifest(raw)
	if err != nil {
		return nil, m, nil, err
	}
	files := m.artifacts()
	if bundleSize > 0 {
		files = []PortableArtifact{{Name: portableBundleName, Size: bundleSize}}
	}
	return raw, m, files, nil
}

func (s *PortableImports) Detail(id string) (PortableTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detail(id)
}

func (s *PortableImports) detail(id string) (PortableTask, error) {
	task, err := s.task(id)
	if err != nil {
		return task, err
	}
	if id == s.runningID && task.Status == "running" && task.Phase == "database" {
		task.SQLProgress = s.sqlProgress.snapshot()
	}
	if task.Status == "receiving" || task.Status == "failed" || task.Status == "interrupted" || task.Phase == "downloading" {
		dir, err := s.taskDir(id)
		if err != nil {
			return task, err
		}
		source, err := readPortableRemoteSource(dir)
		if err != nil {
			return task, err
		}
		if source.IP != "" {
			task.RemoteSource = &source
		}
	}
	showMaterials := task.Status == "receiving" || (task.RemoteSource != nil && (task.Status == "failed" || task.Status == "interrupted"))
	if (showMaterials && time.Now().Unix() < task.ExpiresAt) || (task.Status == "running" && task.Phase == "downloading") {
		dir, err := s.taskDir(id)
		if err != nil {
			return task, err
		}
		_, _, task.Files, err = s.loadFiles(id, dir)
		if err != nil {
			return task, err
		}
		task.Offsets = make(map[string]int64, len(task.Files))
		for _, file := range task.Files {
			size, err := portableFileSize(filepath.Join(dir, file.Name))
			if err != nil {
				return task, err
			}
			task.Offsets[file.Name] = size
			if id == s.runningID && task.Status == "running" && task.Phase == "downloading" && file.Name == s.remoteDownloadFile {
				received, err := portableFileSize(filepath.Join(dir, portableRemotePartialFile))
				if err != nil {
					return task, err
				}
				task.RemoteDownload = &PortableRemoteDownloadProgress{Name: file.Name, Received: min(received, file.Size)}
			}
		}
	}
	if task.Resources && (task.Status == "failed" || task.Status == "interrupted" || task.Phase == portableRetryUploadPhase) {
		var systemUser, webRoot, databaseName, databaseUser, nginx, pool string
		if err := s.db.QueryRow(`SELECT system_user,web_root,db_name,db_user,nginx_config,php_pool
			FROM portable_import_tasks WHERE id=?`, id).Scan(&systemUser, &webRoot, &databaseName, &databaseUser, &nginx, &pool); err != nil {
			return task, err
		}
		task.ResourcePaths = []string{webRoot, systemUser, databaseName, databaseUser, nginx, pool}
	}

	if task.SiteID != 0 {
		var cert, fingerprint, domain, aliases string
		var expires sql.NullTime
		err := s.db.QueryRow("SELECT ssl_cert_path, ssl_temporary_fingerprint, ssl_expires_at, domain, COALESCE(aliases, '') FROM websites WHERE id=? AND ssl_enabled=1", task.SiteID).Scan(&cert, &fingerprint, &expires, &domain, &aliases)
		if err == nil {
			if PortableCertificateIsTemporary(cert, fingerprint) {
				if expires.Valid {
					task.SSLTemporaryUntil = expires.Time.Format(time.RFC3339)
				}
				if aliases != "" {
					task.SSLUncovered = append([]string{domain}, strings.Fields(aliases)...)
				}
			} else {
				for _, alias := range strings.Fields(aliases) {
					if _, err := validateCertificate(cert, alias); err != nil {
						task.SSLUncovered = append(task.SSLUncovered, alias)
					}
				}
			}
		}
	}
	return task, nil
}

func PortableCertificateIsTemporary(cert, fingerprint string) bool {
	if fingerprint == "" || cert == "" {
		return false
	}
	actual, err := migrationCertificateFingerprint(cert)
	return err == nil && actual == fingerprint
}

func (s *PortableImports) Begin(raw []byte, bundleSize int64) (PortableTask, error) {
	return s.begin(raw, bundleSize, PortableRemoteSource{})
}

func (s *PortableImports) begin(raw []byte, bundleSize int64, remote PortableRemoteSource) (PortableTask, error) {
	m, address, err := parsePortableManifest(raw)
	if err != nil {
		return PortableTask{}, err
	}
	if err := checkPortableBundleSize(raw, m, bundleSize); err != nil {
		return PortableTask{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	var id string
	err = s.db.QueryRow("SELECT id FROM portable_import_tasks WHERE manifest_sha256=? AND bundle_size=? AND status='receiving' AND expires_at>? ORDER BY rowid DESC LIMIT 1",
		digest, bundleSize, time.Now().Unix()).Scan(&id)
	if err == nil {
		return s.detail(id)
	}
	if err != sql.ErrNoRows {
		return PortableTask{}, err
	}

	id = strings.ReplaceAll(uuid.NewString(), "-", "")
	dir, err := s.taskDir(id)
	if err != nil {
		return PortableTask{}, err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return PortableTask{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600); err != nil {
		os.RemoveAll(dir)
		return PortableTask{}, err
	}
	now := time.Now()
	aliases := ""
	if remote.IP != "" {
		aliases = PortableAliasSuggestion(address.Host)
	}
	_, err = s.db.Exec(`INSERT INTO portable_import_tasks(id, backup_id, manifest_sha256, domain, url, bundle_size, expires_at, updated_at, aliases)
		VALUES (?,?,?,?,?,?,?,?,?)`, id, m.ID, digest, strings.ToLower(address.Host), address.String(), bundleSize, now.Add(7*24*time.Hour).Unix(), now.Unix(), aliases)
	if err != nil {
		os.RemoveAll(dir)
		return PortableTask{}, err
	}
	return s.detail(id)
}

func checkPortableBundleSize(raw []byte, m portableManifest, bundleSize int64) error {
	var expectedBundle int64 = 1024
	files := append([]PortableArtifact{{Size: int64(len(raw))}}, m.artifacts()...)
	for _, file := range files {
		if file.Size > 8589934591 {
			if bundleSize != 0 {
				return errors.New("材料超出 USTAR 单成员容量")
			}
			break
		}
		expectedBundle += 512 + (file.Size+511)/512*512
	}
	if bundleSize < 0 || (bundleSize > 0 && bundleSize != expectedBundle) {
		return errors.New("整套 TAR 文件大小与清单不一致")
	}
	return nil
}

// PrepareRetry reopens a failed import for upload and validation using the same task.
func (s *PortableImports) PrepareRetry(id string, raw []byte, bundleSize int64) (PortableTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, err := s.task(id)
	if err != nil {
		return PortableTask{}, err
	}
	if s.runningID == id || (task.Status != "failed" && task.Status != "interrupted" && task.Status != "receiving") {
		return PortableTask{}, ErrPortableState
	}
	dir, err := s.taskDir(id)
	if err != nil {
		return PortableTask{}, err
	}
	expired := time.Now().Unix() >= task.ExpiresAt
	if len(raw) == 0 {
		if expired {
			return PortableTask{}, ErrPortableRetryUpload
		}
		if _, _, _, err := s.loadFiles(id, dir); err != nil {
			return PortableTask{}, ErrPortableRetryUpload
		}
	} else {
		var digest string
		var originalBundleSize int64
		if err := s.db.QueryRow("SELECT manifest_sha256, bundle_size FROM portable_import_tasks WHERE id=?", id).Scan(&digest, &originalBundleSize); err != nil {
			return PortableTask{}, err
		}
		sum := sha256.Sum256(raw)
		if digest != hex.EncodeToString(sum[:]) || originalBundleSize != bundleSize {
			return PortableTask{}, errors.New("请选择本任务原先使用的备份和上传格式")
		}
		if expired {
			if err := os.RemoveAll(dir); err != nil {
				return PortableTask{}, err
			}
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return PortableTask{}, err
		}
		if err := writeFileAtomic(filepath.Join(dir, "manifest.json"), raw, 0600); err != nil {
			return PortableTask{}, err
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return PortableTask{}, err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "verify-") {
			if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
				return PortableTask{}, fmt.Errorf("清理上次校验的暂存文件失败：%w", err)
			}
		}
	}
	if err := s.removePortableTree(id); err != nil {
		return PortableTask{}, err
	}
	if err := s.setPhase(id, "receiving", portableRetryUploadPhase, task.Message, 7*24*time.Hour); err != nil {
		return PortableTask{}, err
	}
	return s.detail(id)
}

func portableFileSize(filename string) (int64, error) {
	info, err := os.Lstat(filename)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("备份材料不是普通文件")
	}
	return info.Size(), nil
}

// SaveUploadAliases persists the selection before any upload fragments are sent.
func (s *PortableImports) SaveUploadAliases(id string, aliases []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, err := s.task(id)
	if err != nil {
		return nil, err
	}
	if task.Status != "receiving" || time.Now().Unix() >= task.ExpiresAt {
		return nil, ErrPortableState
	}
	if aliases == nil {
		return nil, errors.New("缺少域名别名设置")
	}
	aliases, err = normalizeSiteProvisioningAliases(aliases, task.Domain)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec("UPDATE portable_import_tasks SET aliases=? WHERE id=?", strings.Join(aliases, "\n"), id)
	return aliases, err
}

func (s *PortableImports) Upload(id, name string, offset int64, data []byte) (int64, error) {
	if len(data) == 0 || len(data) > PortableChunkSize || offset < 0 {
		return 0, errors.New("无效的上传分片")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task, err := s.detail(id)
	if err != nil {
		return 0, err
	}
	if task.Status != "receiving" || time.Now().Unix() >= task.ExpiresAt {
		return 0, ErrPortableState
	}
	var total int64
	for _, file := range task.Files {
		if file.Name == name {
			total = file.Size
			break
		}
	}
	current := task.Offsets[name]
	if total == 0 || offset > total || int64(len(data)) > total-offset {
		return current, errors.New("分片超出材料声明大小")
	}
	if offset < current && offset+int64(len(data)) <= current {
		return current, nil
	}
	if offset != current {
		return current, errors.New("请从服务器确认的上传位置继续")
	}
	dir, err := s.taskDir(id)
	if err != nil {
		return current, err
	}
	if space, err := migrationAvailableBytes(dir); err != nil || space < uint64(len(data)) {
		return current, errors.New("上传暂存磁盘空间不足")
	}
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return current, err
	}
	_, err = file.WriteAt(data, offset)
	if err == nil {
		err = file.Sync()
	}
	if err != nil {
		_ = file.Truncate(offset)
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return current, err
	}
	now := time.Now()
	_, err = s.db.Exec("UPDATE portable_import_tasks SET expires_at=?, updated_at=? WHERE id=?", now.Add(7*24*time.Hour).Unix(), now.Unix(), id)
	return offset + int64(len(data)), err
}

// PortableAliasSuggestion pairs a public registrable domain and its www form.
func PortableAliasSuggestion(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if !IsValidDomain(domain) {
		return ""
	}
	_, icann := publicsuffix.PublicSuffix(domain)
	if !icann {
		return ""
	}
	registered, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil {
		return ""
	}
	www := "www." + registered
	if domain == www {
		return registered
	}
	if domain == registered && IsValidDomain(www) {
		return www
	}
	return ""
}

func checkPortableDomainAndMigration(ctx context.Context, domain string, aliases []string) error {
	if err := checkCreateSiteDomainAndMigration(ctx, domain, aliases); err != nil {
		return err
	}
	for _, alias := range aliases {
		locked, err := SiteMigrationLocked(ctx, 0, alias)
		if err != nil {
			return siteProvisioningError("migration_locked", "无法确认别名迁移锁状态："+alias)
		}
		if locked {
			return siteProvisioningError("migration_locked", "别名正受网站搬家任务保护："+alias)
		}
	}
	return nil
}

func (s *PortableImports) Submit(id string, aliases []string, cleanupToken string) error {
	return s.submit(id, aliases, cleanupToken, nil)
}

func (s *PortableImports) submit(id string, aliases []string, cleanupToken string, remote *portableRemoteDownload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, err := s.detail(id)
	if err != nil {
		return err
	}
	if task.Status == "running" || task.Status == "succeeded" {
		return nil
	}
	if task.Status != "receiving" || time.Now().Unix() >= task.ExpiresAt {
		return ErrPortableState
	}
	if s.runningID != "" {
		return ErrPortableBusy
	}
	phase := "validating"
	if remote == nil {
		for _, file := range task.Files {
			if task.Offsets[file.Name] != file.Size {
				return fmt.Errorf("缺少或未上传完成：%s", file.Name)
			}
		}
	} else {
		if task.RemoteSource == nil || *task.RemoteSource != remote.source || task.Files[0].Name == portableBundleName {
			return errors.New("备份下载来源已变化，请重新预览")
		}
		phase = "downloading"
	}
	if aliases != nil {
		task.Aliases, err = normalizeSiteProvisioningAliases(aliases, task.Domain)
		if err != nil {
			return err
		}
	}
	if err := checkPortableDomainAndMigration(context.Background(), task.Domain, task.Aliases); err != nil {
		return err
	}
	retry := task.Phase == portableRetryUploadPhase
	if retry || remote != nil {
		plan, err := s.portableRetryPlan(task)
		if err != nil {
			return err
		}
		if cleanupToken != plan.Token || (!retry && plan.Token != "") {
			return errors.New("请先检查并确认要清理的恢复残留")
		}
	} else if cleanupToken != "" {
		return ErrPortableState
	}
	if _, err := s.db.Exec(`UPDATE portable_import_tasks SET aliases=?, status='running', phase=?, message='', updated_at=? WHERE id=?`,
		strings.Join(task.Aliases, "\n"), phase, time.Now().Unix(), id); err != nil {
		return err
	}
	s.runningID = id
	go s.run(task, retry, cleanupToken, remote)
	return nil
}

func (s *PortableImports) phase(id, status, phase, message string) error {
	return s.setPhase(id, status, phase, message, 0)
}

func (s *PortableImports) setPhase(id, status, phase, message string, retention time.Duration) error {
	now := time.Now()
	var expiry int64
	if retention > 0 {
		expiry = now.Add(retention).Unix()
	}
	_, err := s.db.Exec(`UPDATE portable_import_tasks SET status=?, phase=?, message=?, updated_at=?,
		expires_at=CASE WHEN ?=0 THEN expires_at ELSE ? END WHERE id=?`, status, phase, message, now.Unix(), expiry, expiry, id)
	return err
}

func (s *PortableImports) run(task PortableTask, retry bool, cleanupToken string, remote *portableRemoteDownload) {
	dir, err := s.taskDir(task.ID)
	if err == nil && remote != nil {
		err = s.downloadPortableMaterials(dir, task.Files, remote.open)
		if err == nil {
			err = s.phase(task.ID, "running", "validating", "")
		}
		if err != nil {
			log.Printf("portable import %s: %v", task.ID,
				errors.Join(err, s.setPhase(task.ID, "failed", "downloading", err.Error(), 7*24*time.Hour)))
			s.finishRun(task.ID, false)
			return
		}
	}
	if err == nil {
		var beforeAssembly func() error
		if retry {
			beforeAssembly = func() error { return s.cleanPortableRetry(task, cleanupToken) }
		}
		err = s.validateAndRestore(task, dir, beforeAssembly)
	} else {
		err = errors.Join(err, s.setPhase(task.ID, "failed", "validating", "无法读取暂存任务", 7*24*time.Hour))
	}
	if err != nil {
		log.Printf("portable import %s: %v", task.ID, err)
	}
	s.finishRun(task.ID, err == nil)
}

func (s *PortableImports) finishRun(id string, succeeded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runningID = ""
	s.sqlProgress.set(nil)
	if !succeeded {
		return
	}
	task, err := s.task(id)
	if err == nil {
		err = s.removeMaterials(task, false)
	}
	if err != nil {
		log.Printf("clean successful portable import %s: %v", id, err)
	}
}

func (s *PortableImports) validateAndRestore(task PortableTask, dir string, beforeAssembly func() error) error {
	raw, manifest, files, err := s.loadFiles(task.ID, dir)
	if err != nil {
		return errors.Join(err, s.setPhase(task.ID, "failed", "validating", "无法读取备份清单", 7*24*time.Hour))
	}
	work, err := os.MkdirTemp(dir, "verify-")
	if err != nil {
		return errors.Join(err, s.setPhase(task.ID, "failed", "validating", "无法创建校验暂存目录", 7*24*time.Hour))
	}
	defer os.RemoveAll(work)
	tree, err := s.verifyAndStage(task.ID, dir, work, raw, manifest, files[0].Name == portableBundleName, beforeAssembly)
	if err != nil {
		phase := "validating"
		if current, queryErr := s.task(task.ID); queryErr == nil && current.Phase == "cleanup" {
			phase = current.Phase
		}
		return errors.Join(err, s.setPhase(task.ID, "failed", phase, err.Error(), 7*24*time.Hour))
	}
	defer logPortableTreeCleanup(s, task.ID)

	if err := s.restore(task, manifest, work, tree); errors.Is(err, ErrPortableBusy) {
		return errors.Join(err, s.setPhase(task.ID, "failed", "resources", "建站或系统维护正在运行，请稍后重试", 7*24*time.Hour))
	} else if err != nil {
		current, readErr := s.task(task.ID)
		phase := "resources"
		if readErr == nil {
			phase = current.Phase
		}
		return errors.Join(err, s.setPhase(task.ID, "failed", phase, err.Error(), 7*24*time.Hour))
	}
	return nil
}

func (s *PortableImports) verifyAndStage(id, dir, work string, raw []byte, manifest portableManifest, bundle bool, beforeAssembly func() error) (string, error) {
	var index io.Reader
	var openZIP func(string) (*zip.Reader, io.Closer, error)
	if bundle {
		bundlePath := filepath.Join(dir, portableBundleName)
		input, err := os.Open(bundlePath)
		if err != nil {
			return "", err
		}
		defer input.Close()
		sections, err := verifiedPortableTAR(input, raw, manifest)
		if err != nil {
			if errors.Is(err, errPortableTARInvalid) || errors.Is(err, errPortableArtifactMismatch) {
				err = resetPortableUpload(bundlePath, err)
			}
			return "", err
		}
		index = sections[manifest.Index.Name]
		openZIP = func(name string) (*zip.Reader, io.Closer, error) {
			section := sections[name]
			reader, err := zip.NewReader(section, section.Size())
			return reader, nil, err
		}
	} else {
		for _, item := range manifest.artifacts() {
			if err := verifyPortableArtifact(dir, item); err != nil {
				if errors.Is(err, errPortableArtifactMismatch) {
					err = resetPortableUpload(filepath.Join(dir, item.Name), err)
				}
				return "", err
			}
		}
		input, err := os.Open(filepath.Join(dir, manifest.Index.Name))
		if err != nil {
			return "", err
		}
		defer input.Close()
		index = input
		openZIP = func(name string) (*zip.Reader, io.Closer, error) {
			reader, err := zip.OpenReader(filepath.Join(dir, name))
			if err != nil {
				return nil, nil, err
			}
			return &reader.Reader, reader, nil
		}
	}
	if beforeAssembly != nil {
		if err := beforeAssembly(); err != nil {
			return "", err
		}
	}
	tree, err := s.createPortableTree(id)
	if err != nil {
		return "", err
	}
	if err := assemblePortableBackup(index, openZIP, tree, work, manifest); err != nil {
		return "", errors.Join(err, s.removePortableTree(id))
	}
	return tree, nil
}

func resetPortableUpload(path string, cause error) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("无法重置损坏材料：%w", errors.Join(cause, err))
	}
	return cause
}

func (s *PortableImports) DeleteTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, err := s.task(id)
	if err != nil {
		return err
	}
	if id == s.runningID {
		return ErrPortableState
	}
	switch task.Status {
	case "receiving", "succeeded", "failed", "interrupted":
		return s.removeMaterials(task, true)
	default:
		return ErrPortableState
	}
}

func (s *PortableImports) removeMaterials(task PortableTask, deleteRecord bool) error {
	dir, err := s.taskDir(task.ID)
	if err != nil {
		return err
	}
	if err := s.removePortableTree(task.ID); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if deleteRecord {
		_, err = s.db.Exec("DELETE FROM portable_import_tasks WHERE id=?", task.ID)
	} else if task.Status == "receiving" && task.Phase == portableRetryUploadPhase {
		_, err = s.db.Exec(`UPDATE portable_import_tasks SET expires_at=0, status='failed', phase='uploading',
			message='重试的上传未完成，暂存材料已清理，请重新上传' WHERE id=?`, task.ID)
	} else {
		_, err = s.db.Exec("UPDATE portable_import_tasks SET expires_at=0 WHERE id=?", task.ID)
	}
	return err
}

func (s *PortableImports) cleanupExpired() error {
	rows, err := s.db.Query("SELECT "+portableTaskColumns+" FROM portable_import_tasks WHERE expires_at>0 AND status<>'running' AND (expires_at<=? OR status='succeeded')", time.Now().Unix())
	if err != nil {
		return err
	}
	var expired []PortableTask
	for rows.Next() {
		task, err := scanPortableTask(rows)
		if err != nil {
			rows.Close()
			return err
		}
		expired = append(expired, task)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	var cleanupErr error
	for _, task := range expired {
		if task.ID == s.runningID {
			continue
		}
		if err := s.removeMaterials(task, task.Status == "receiving" && task.Phase != portableRetryUploadPhase); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("%s: %w", task.ID, err))
		}
	}
	return cleanupErr
}

func (s *PortableImports) Start(ctx context.Context) {
	now := time.Now()
	if _, err := s.db.Exec(`UPDATE portable_import_tasks SET status='interrupted', message='面板已重启，请核实本次资源。系统不会重新导入数据库。',
		updated_at=?, expires_at=? WHERE status='running'`, now.Unix(), now.Add(7*24*time.Hour).Unix()); err != nil {
		log.Printf("mark interrupted portable imports: %v", err)
	}
	go func() {
		s.mu.Lock()
		err := s.cleanupPortableTrees()
		s.mu.Unlock()
		if err != nil {
			log.Printf("clean interrupted portable file staging: %v", err)
		}
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			s.mu.Lock()
			err := s.cleanupExpired()
			s.mu.Unlock()
			if err != nil {
				log.Printf("clean expired portable imports: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *PortableImports) PHPLog(id string) (string, error) {
	var dir string
	if err := s.db.QueryRow(`SELECT t.log_dir FROM portable_import_tasks t
		JOIN websites w ON w.id=t.site_id AND w.log_dir=t.log_dir
		WHERE t.id=? AND t.status='succeeded'`, id).Scan(&dir); err != nil {
		return "", err
	}
	if _, err := managedSubpath(s.cfg.Paths.WWWLogs, dir, "站点日志"); err != nil {
		return "", ErrPortableState
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, err := root.Open("php-error.log")
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("PHP 日志不可用")
	}
	start := max(int64(0), info.Size()-64*1024)
	content, err := io.ReadAll(io.NewSectionReader(file, start, info.Size()-start))
	return string(content), err
}

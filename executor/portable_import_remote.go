package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const portableRemotePinFile = "remote-source.json"
const portableRemotePartialFile = ".remote-partial"

type PortableRemoteSource struct {
	Host string `json:"host"`
	IP   string `json:"ip"`
	HTTP bool   `json:"http,omitempty"`
}

type PortableRemoteOpen func(context.Context, string) (io.ReadCloser, error)

type PortableRemoteDownloadProgress struct {
	Name     string `json:"name"`
	Received int64  `json:"received"`
}

type portableRemoteDownload struct {
	source PortableRemoteSource
	open   PortableRemoteOpen
}

// Only the origin, protocol and pinned address are persisted. The access token stays
// in the request and, once started, in the downloader closure.
func readPortableRemoteSource(dir string) (PortableRemoteSource, error) {
	var source PortableRemoteSource
	raw, err := os.ReadFile(filepath.Join(dir, portableRemotePinFile))
	if os.IsNotExist(err) {
		return source, nil
	}
	if err != nil {
		return source, err
	}
	if err := json.Unmarshal(raw, &source); err != nil {
		return source, err
	}
	return source, nil
}

func (s *PortableImports) RemoteSource(id string) (PortableRemoteSource, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var backupID string
	if _, err := s.task(id); err != nil {
		return PortableRemoteSource{}, "", err
	}
	if err := s.db.QueryRow("SELECT backup_id FROM portable_import_tasks WHERE id=?", id).Scan(&backupID); err != nil {
		return PortableRemoteSource{}, "", err
	}
	dir, err := s.taskDir(id)
	if err != nil {
		return PortableRemoteSource{}, "", err
	}
	source, err := readPortableRemoteSource(dir)
	return source, backupID, err
}

func (s *PortableImports) BindRemoteSource(id string, raw []byte, source PortableRemoteSource, replace bool) (PortableTask, error) {
	_, address, err := parsePortableManifest(raw)
	if err != nil {
		return PortableTask{}, err
	}
	if id == "" {
		if err := checkPortableDomainAndMigration(context.Background(), address.Host, nil); err != nil {
			return PortableTask{}, err
		}
		task, err := s.begin(raw, 0, source)
		if err != nil {
			return PortableTask{}, err
		}
		id = task.ID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task, err := s.task(id)
	if err != nil {
		return task, err
	}
	if task.Status != "receiving" && task.Status != "failed" && task.Status != "interrupted" {
		return task, ErrPortableState
	}
	if task.ExpiresAt <= time.Now().Unix() {
		return task, ErrPortableRetryUpload
	}
	dir, err := s.taskDir(id)
	if err != nil {
		return task, err
	}
	saved, _, files, err := s.loadFiles(id, dir)
	if err != nil {
		return task, err
	}
	if sha256.Sum256(saved) != sha256.Sum256(raw) || files[0].Name == portableBundleName {
		return task, errors.New("新来源必须对应本任务原先的同一份分包备份")
	}
	current, err := readPortableRemoteSource(dir)
	if err != nil {
		return task, err
	}
	if current.IP != "" && current != source && !replace {
		return task, errors.New("来源地址已固定，请打开原任务后再继续或明确更换来源")
	}
	record, err := json.Marshal(source)
	if err != nil {
		return task, err
	}
	if err := writeFileAtomic(filepath.Join(dir, portableRemotePinFile), record, 0600); err != nil {
		return task, err
	}
	task, err = s.detail(id)
	if err != nil {
		return task, err
	}
	if _, err := s.portableRetryPlan(task); err != nil {
		task.RemoteConflict = err.Error()
	}
	return task, nil
}

func (s *PortableImports) SubmitRemote(id string, aliases []string, cleanupToken string, source PortableRemoteSource, open PortableRemoteOpen) error {
	task, err := s.Detail(id)
	if err != nil {
		return err
	}
	if task.Status == "failed" || task.Status == "interrupted" {
		if _, err := s.PrepareRetry(id, nil, 0); err != nil {
			return err
		}
	}
	return s.submit(id, aliases, cleanupToken, &portableRemoteDownload{source: source, open: open})
}

func (s *PortableImports) downloadPortableMaterials(dir string, files []PortableArtifact, open PortableRemoteOpen) error {
	partial := filepath.Join(dir, portableRemotePartialFile)
	if err := os.Remove(partial); err != nil && !os.IsNotExist(err) {
		return err
	}
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		os.Remove(partial)
		s.remoteDownloadFile = ""
	}()
	for _, item := range files {
		size, err := portableFileSize(filepath.Join(dir, item.Name))
		if err != nil {
			return err
		}
		if size == item.Size {
			err := verifyPortableArtifact(dir, item)
			if err == nil {
				continue
			}
			if !errors.Is(err, errPortableArtifactMismatch) {
				return err
			}
		}
		space, err := migrationAvailableBytes(dir)
		if err != nil || space < uint64(item.Size) {
			return fmt.Errorf("暂存磁盘空间不足：%s", item.Name)
		}
		s.mu.Lock()
		s.remoteDownloadFile = item.Name
		s.mu.Unlock()
		if err := receivePortableArtifact(partial, item, open); err != nil {
			return fmt.Errorf("下载 %s 失败：%w", item.Name, err)
		}
		s.mu.Lock()
		err = os.Rename(partial, filepath.Join(dir, item.Name))
		s.remoteDownloadFile = ""
		s.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

func receivePortableArtifact(partial string, item PortableArtifact, open PortableRemoteOpen) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	stream, err := open(ctx, item.Name)
	if err != nil {
		return err
	}
	defer stream.Close()
	out, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, err := io.CopyN(io.MultiWriter(out, hash), stream, item.Size)
	if err == nil && written == item.Size {
		var extra [1]byte
		if _, readErr := io.ReadFull(stream, extra[:]); readErr != io.EOF {
			err = errors.New("材料长度与备份清单不符")
		}
	}
	if err == nil && hex.EncodeToString(hash.Sum(nil)) != item.SHA256 {
		err = errPortableArtifactMismatch
	}
	if err == nil {
		err = out.Sync()
	}
	return errors.Join(err, out.Close())
}

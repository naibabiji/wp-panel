package executor

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/naibabiji/wp-panel/config"

	"github.com/google/uuid"
)

type TaskQueue struct {
	queue     chan *Task
	running   atomic.Bool
	mu        sync.Mutex
	taskCount int
	tasks     map[string]*Task
}

var GlobalQueue *TaskQueue

func InitQueue(cfg *config.Config) *TaskQueue {
	q := &TaskQueue{
		queue: make(chan *Task, 100),
		tasks: make(map[string]*Task),
	}
	GlobalQueue = q
	go q.worker()
	log.Println("任务队列已启动(单线程串行模式)")
	return q
}

func (q *TaskQueue) Enqueue(taskType TaskType, payload interface{}) *Task {
	task := &Task{
		ID:        uuid.New().String(),
		Type:      taskType,
		Payload:   payload,
		Status:    TaskStatusWaiting,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		ResultCh:  make(chan TaskResult, 1),
	}

	q.mu.Lock()
	q.taskCount++
	q.tasks[task.ID] = task
	q.mu.Unlock()

	q.queue <- task

	return task
}

func (q *TaskQueue) GetTask(id string) (*Task, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	task, ok := q.tasks[id]
	if !ok {
		return nil, false
	}
	copyTask := *task
	if task.Result != nil {
		result := *task.Result
		copyTask.Result = &result
	}
	copyTask.ResultCh = nil
	return &copyTask, true
}

func (q *TaskQueue) QueueLength() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.taskCount
}

func (q *TaskQueue) IsRunning() bool {
	return q.running.Load()
}

func (q *TaskQueue) worker() {
	for task := range q.queue {
		func() {
			q.running.Store(true)
			q.mu.Lock()
			task.Status = TaskStatusRunning
			task.UpdatedAt = time.Now()
			q.mu.Unlock()

			var result TaskResult
			finalized := false
			defer func() {
				if r := recover(); r != nil {
					log.Printf("task %s panic: %v", task.ID, r)
					if finalized {
						return
					}
					result = TaskResult{Success: false, Message: fmt.Sprintf("task execution panic: %v", r)}
					q.mu.Lock()
					task.Status = TaskStatusFailed
					task.UpdatedAt = time.Now()
					task.Result = &result
					q.mu.Unlock()
					task.ResultCh <- result
					close(task.ResultCh)
					safeLogOp(task, result)
					q.mu.Lock()
					q.taskCount--
					q.mu.Unlock()
					q.running.Store(false)
				}
			}()
			switch task.Type {
			case TaskCreateSite:
				result = executeCreateSite(task)
			case TaskDeleteSite:
				result = executeDeleteSite(task)
			case TaskPauseSite:
				result = executePauseSite(task)
			case TaskEnableSite:
				result = executeEnableSite(task)
			case TaskRefreshWhitelist:
				result = executeRefreshWhitelist(task)
			case TaskUnbanIP:
				result = executeUnbanIP(task)
			case TaskEnableSSL:
				result = executeEnableSSL(task)
			case TaskRemoveSSL:
				result = executeRemoveSSL(task)
			case TaskChangeDBPassword:
				result = executeChangeDBPassword(task)
			case TaskUpdateDomains:
				result = executeUpdateDomains(task)
			case TaskSaveNginxCustom:
				result = executeSaveNginxCustom(task)
			case TaskSetAccessLogMode:
				result = executeSetAccessLogMode(task)
			case TaskSetCDNRealIP:
				result = executeSetCDNRealIP(task)
			case TaskSetDocumentRoot:
				result = executeSetDocumentRoot(task)
			case TaskRenewSSL:
				result = executeRenewSSL(task)
			case TaskRenderCron:
				result = executeRenderCron(task)
			case TaskRunCron:
				result = executeRunCron(task)
			case TaskManualBan:
				result = executeManualBan(task)
			case TaskCreateBackup:
				result = executeCreateBackup(task)
			case TaskRestoreBackup:
				result = executeRestoreBackup(task)
			case TaskSetFileLock:
				result = executeSetFileLock(task)
			default:
				result = TaskResult{Success: false, Message: "未知任务类型: " + string(task.Type)}
			}

			if result.Success {
				q.mu.Lock()
				task.Status = TaskStatusSuccess
			} else {
				q.mu.Lock()
				task.Status = TaskStatusFailed
			}
			task.UpdatedAt = time.Now()
			task.Result = &result
			q.mu.Unlock()

			safeLogOp(task, result)

			task.ResultCh <- result
			close(task.ResultCh)
			finalized = true

			q.mu.Lock()
			q.taskCount--
			q.mu.Unlock()

			q.running.Store(false)
		}()
	}
}

func safeLogOp(task *Task, result TaskResult) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("record operation log for task %s panic: %v", task.ID, r)
		}
	}()
	logOp(task, result)
}

func logOp(task *Task, result TaskResult) {
	status := "success"
	if !result.Success {
		status = "failed"
	}
	target := ""
	switch task.Type {
	case TaskCreateSite:
		if p, ok := task.Payload.(*CreateSitePayload); ok {
			target = p.Domain
		}
	case TaskDeleteSite:
		if p, ok := task.Payload.(*DeleteSitePayload); ok && p.Site != nil {
			target = p.Site.Domain
		}
	case TaskPauseSite:
		if p, ok := task.Payload.(*PauseSitePayload); ok && p.Site != nil {
			target = p.Site.Domain
		}
	case TaskEnableSite:
		if p, ok := task.Payload.(*EnableSitePayload); ok && p.Site != nil {
			target = p.Site.Domain
		}
	case TaskEnableSSL:
		if p, ok := task.Payload.(*EnableSSLPayload); ok && p.Site != nil {
			target = p.Site.Domain
		}
	case TaskRemoveSSL:
		if p, ok := task.Payload.(*RemoveSSLPayload); ok && p.Site != nil {
			target = p.Site.Domain
		}
	case TaskChangeDBPassword:
		if p, ok := task.Payload.(*ChangeDBPasswordPayload); ok && p.Site != nil {
			target = p.Site.Domain
		}
	case TaskUpdateDomains:
		if p, ok := task.Payload.(*UpdateDomainsPayload); ok && p.Site != nil {
			target = p.Site.Domain
		}
	case TaskSaveNginxCustom:
		if p, ok := task.Payload.(*SaveNginxCustomPayload); ok && p.Site != nil {
			target = p.Site.Domain
		}
	case TaskSetAccessLogMode:
		if p, ok := task.Payload.(*SetAccessLogModePayload); ok && p.Site != nil {
			target = p.Site.Domain
		}
	case TaskSetCDNRealIP:
		if p, ok := task.Payload.(*SetCDNRealIPPayload); ok && p.Site != nil {
			target = p.Site.Domain
		}
	case TaskSetDocumentRoot:
		if p, ok := task.Payload.(*SetDocumentRootPayload); ok && p.Site != nil {
			target = p.Site.Domain
		}
	case TaskRenewSSL:
		target = "ssl_renewal"
	case TaskManualBan:
		if p, ok := task.Payload.(*ManualBanPayload); ok {
			target = p.IP
		}
	case TaskRenderCron:
		target = "cron_config"
	case TaskRunCron:
		if p, ok := task.Payload.(*RunCronPayload); ok {
			target = p.Name
		}
	case TaskCreateBackup:
		if p, ok := task.Payload.(*CreateBackupPayload); ok && p.Site != nil {
			target = p.Site.Domain
		}
	case TaskRestoreBackup:
		if p, ok := task.Payload.(*RestoreBackupPayload); ok && p.Site != nil {
			target = p.Site.Domain
		}
	case TaskSetFileLock:
		if p, ok := task.Payload.(*SetFileLockPayload); ok && p.Site != nil {
			target = p.Site.Domain
		}
	}

	recordOperationLog(string(task.Type), target, status, result.Message)
}

func buildSiteName(domain string) string {
	normalized := strings.TrimSpace(domain)
	normalized = strings.TrimSuffix(normalized, ".")
	normalized = strings.ToLower(normalized)
	sum := sha1.Sum([]byte(normalized))
	suffix := hex.EncodeToString(sum[:])[:8]

	var b strings.Builder
	lastUnderscore := false
	for _, c := range normalized {
		isAlphaNum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if isAlphaNum {
			b.WriteRune(c)
			lastUnderscore = false
		} else if c == '.' || c == '-' {
			if b.Len() > 0 && !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}

	name := strings.Trim(b.String(), "_")
	if name == "" {
		name = "site"
	}

	const maxSiteNameLen = 27
	const hashSeparatorLen = 1
	maxReadableLen := maxSiteNameLen - hashSeparatorLen - len(suffix)
	if len(name) > maxReadableLen {
		name = strings.TrimRight(name[:maxReadableLen], "_")
		if name == "" {
			name = "site"
		}
	}

	return name + "_" + suffix
}

func fileExists(path string) bool {
	_, err := executeCommand("test", "-f", path)
	return err == nil
}

func dirExists(path string) bool {
	_, err := executeCommand("test", "-d", path)
	return err == nil
}

var shellExec = func(binary string, args ...string) (string, error) {
	result, err := Execute(binary, args...)
	if err != nil {
		if result != nil && result.Stderr != "" {
			log.Printf("命令 %s stderr: %s", binary, result.Stderr)
		}
		return "", fmt.Errorf("命令 %s 执行失败", binary)
	}
	return result.Stdout, nil
}

func executeCommand(binary string, args ...string) (string, error) {
	return shellExec(binary, args...)
}

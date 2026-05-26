package executor

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"

	"github.com/google/uuid"
)

type TaskQueue struct {
	queue     chan *Task
	running   atomic.Bool
	mu        sync.Mutex
	taskCount int
}

var GlobalQueue *TaskQueue

func InitQueue(cfg *config.Config) *TaskQueue {
	q := &TaskQueue{
		queue: make(chan *Task, 100),
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
		ResultCh:  make(chan TaskResult, 1),
	}

	q.mu.Lock()
	q.taskCount++
	q.mu.Unlock()

	q.queue <- task

	return task
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
		q.running.Store(true)
		task.Status = TaskStatusRunning

		var result TaskResult
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
		default:
			result = TaskResult{Success: false, Message: "未知任务类型: " + string(task.Type)}
		}

		if result.Success {
			task.Status = TaskStatusSuccess
		} else {
			task.Status = TaskStatusFailed
		}

		logOp(task, result)

		task.ResultCh <- result
		close(task.ResultCh)

		q.mu.Lock()
		q.taskCount--
		q.mu.Unlock()

		q.running.Store(false)
	}
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
	}

	db := database.GetDB()
	if db != nil {
		_, _ = db.Exec(
			"INSERT INTO operation_logs (operation, target, status, message) VALUES (?, ?, ?, ?)",
			string(task.Type), target, status, result.Message,
		)
	}
}

func buildSiteName(domain string) string {
	name := ""
	for _, c := range domain {
		if c == '.' {
			name += "_"
		} else if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			name += string(c)
		} else if c >= 'A' && c <= 'Z' {
			name += string(c + 32)
		}
	}
	return name
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

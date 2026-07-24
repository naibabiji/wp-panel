package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"
)

const wpPluginUpdateControlTimeout = 30 * time.Second

var errUpdateTaskOwnershipLost = errors.New("update task ownership lost")

type wpPluginUpdateExecution struct {
	Task           WPUpdateTask
	WebRoot        string
	SystemUser     string
	Domain         string
	DatabaseName   string
	PackagePath    string
	DatabaseBackup string
	PluginBackup   string
	FileLockMode   string
	FileLockActive bool
}

// Each mutating method owns the atomic or compensating cleanup for its own
// substage. Once called, it must not leave a half-committed substage merely
// because ctx is cancelled; ownership is checked again only at boundaries.
type wpPluginUpdateOperations interface {
	// Prepare is read-only. It must not modify website files, options, locks,
	// maintenance state, or any other server state.
	Prepare(context.Context, wpPluginUpdateExecution) (bool, error)
	Unlock(context.Context, wpPluginUpdateExecution) error
	ApplyPluginUpdate(context.Context, wpPluginUpdateExecution) error
	ReactivatePlugin(context.Context, wpPluginUpdateExecution) error
	CheckTargetHealth(context.Context, wpPluginUpdateExecution, bool) error
	SetMaintenance(context.Context, wpPluginUpdateExecution, bool) error
	RestoreDatabase(context.Context, wpPluginUpdateExecution) error
	RestorePluginFiles(context.Context, wpPluginUpdateExecution) error
	CheckRollbackHealth(context.Context, wpPluginUpdateExecution) error
	RestoreFileLock(context.Context, wpPluginUpdateExecution) error
}

type wpPluginUpdateFinalizer interface {
	Finalize(context.Context, wpPluginUpdateExecution, bool) error
}

type wpPluginUpdateExecutor struct {
	store *wpUpdateStore
	root  string
	ops   wpPluginUpdateOperations
	now   func() time.Time
}

func newWPPluginUpdateExecutor(store *wpUpdateStore, artifactRoot string, ops wpPluginUpdateOperations) (*wpPluginUpdateExecutor, error) {
	if store == nil || store.db == nil || !filepath.IsAbs(artifactRoot) || ops == nil {
		return nil, errors.New("invalid plugin update executor")
	}
	return &wpPluginUpdateExecutor{store: store, root: filepath.Clean(artifactRoot), ops: ops, now: time.Now}, nil
}

func (e *wpPluginUpdateExecutor) Execute(ctx context.Context, taskID, owner string) error {
	execution, err := e.loadExecution(ctx, taskID, owner, wpUpdateRunning)
	if err != nil {
		return err
	}
	return e.execute(ctx, execution, owner)
}

func (e *wpPluginUpdateExecutor) execute(ctx context.Context, execution wpPluginUpdateExecution, owner string) error {
	taskID := execution.Task.ID
	wasActive, err := e.ops.Prepare(ctx, execution)
	if err != nil {
		if handled, interruptErr := e.interruptForSupervision(ctx, taskID, owner, err); handled {
			if interruptErr != nil {
				return interruptErr
			}
			return err
		}
		if finishErr := e.markFailure(ctx, taskID, owner, "precheck", false); finishErr != nil {
			return finishErr
		}
		return errors.New("plugin update operations precheck failed")
	}
	if err := ctx.Err(); err != nil {
		if finishErr := e.markFailure(ctx, taskID, owner, "precheck", false); finishErr != nil {
			return finishErr
		}
		return err
	}
	controlCtx, cancel := e.controlContext(ctx)
	if execution.Task.ComponentType == "theme" {
		err = e.store.recordThemePrepared(controlCtx, taskID, owner, e.now().UTC())
	} else {
		err = e.store.recordPluginPrepared(controlCtx, taskID, owner, wasActive, e.now().UTC())
	}
	cancel()
	if err != nil {
		return err
	}
	if err := e.runWriteSubstage(ctx, func(stageCtx context.Context) error { return e.ops.Unlock(stageCtx, execution) }); err != nil {
		return e.failBeforePluginWrite(ctx, execution, owner, "unlock")
	}
	if err := e.stage(ctx, taskID, owner, "unlocking", "updating_component"); err != nil {
		return err
	}
	if err := e.runWriteSubstage(ctx, func(stageCtx context.Context) error { return e.ops.ApplyPluginUpdate(stageCtx, execution) }); err != nil {
		if handled, interruptErr := e.interruptForSupervision(ctx, taskID, owner, err); handled {
			if interruptErr != nil {
				return interruptErr
			}
			return err
		}
		e.recordFailureEvent(ctx, taskID, owner, "plugin_update", "plugin_update_failed")
		return e.rollbackOrHalt(ctx, execution, owner, "plugin_update")
	}
	if wasActive {
		if err := e.stage(ctx, taskID, owner, "updating_component", "reactivating"); err != nil {
			return err
		}
		if err := e.runWriteSubstage(ctx, func(stageCtx context.Context) error { return e.ops.ReactivatePlugin(stageCtx, execution) }); err != nil {
			if handled, interruptErr := e.interruptForSupervision(ctx, taskID, owner, err); handled {
				if interruptErr != nil {
					return interruptErr
				}
				return err
			}
			e.recordFailureEvent(ctx, taskID, owner, "reactivation", "reactivation_failed")
			return e.rollbackOrHalt(ctx, execution, owner, "reactivation")
		}
		if err := e.stage(ctx, taskID, owner, "reactivating", "health_check"); err != nil {
			return err
		}
	} else if err := e.stage(ctx, taskID, owner, "updating_component", "health_check"); err != nil {
		return err
	}
	if err := e.runProbe(ctx, func(probeCtx context.Context) error { return e.ops.CheckTargetHealth(probeCtx, execution, wasActive) }); err != nil {
		if handled, interruptErr := e.interruptForSupervision(ctx, taskID, owner, err); handled {
			if interruptErr != nil {
				return interruptErr
			}
			return err
		}
		e.recordFailureEvent(ctx, taskID, owner, "health_check", pluginHealthCheckFailureCode(err))
		return e.rollbackOrHalt(ctx, execution, owner, "health_check")
	}
	if err := e.stage(ctx, taskID, owner, "health_check", "restoring_file_lock"); err != nil {
		return err
	}
	if err := e.runWriteSubstage(ctx, func(stageCtx context.Context) error { return e.ops.RestoreFileLock(stageCtx, execution) }); err != nil {
		e.recordFailureEvent(ctx, taskID, owner, "file_lock_restore", "file_lock_restore_failed")
		return e.rollbackOrHalt(ctx, execution, owner, "file_lock_restore")
	}
	if err := e.finalize(ctx, execution, false); err != nil {
		return err
	}
	controlCtx, cancel = e.controlContext(ctx)
	err = e.store.markSuccess(controlCtx, taskID, owner, e.now().UTC())
	cancel()
	return err
}

func (e *wpPluginUpdateExecutor) runWriteSubstage(ctx context.Context, run func(context.Context) error) error {
	stageCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wpUpdateLease)
	defer cancel()
	return run(stageCtx)
}

func (e *wpPluginUpdateExecutor) runProbe(ctx context.Context, run func(context.Context) error) error {
	probeCtx, cancel := e.controlContext(ctx)
	defer cancel()
	return run(probeCtx)
}

func (e *wpPluginUpdateExecutor) controlContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), wpPluginUpdateControlTimeout)
}

func (e *wpPluginUpdateExecutor) failBeforePluginWrite(ctx context.Context, execution wpPluginUpdateExecution, owner, failureStage string) error {
	owned, err := e.ownsRunningTask(ctx, execution.Task.ID, owner)
	if err != nil || !owned {
		if err != nil {
			return err
		}
		return errUpdateTaskOwnershipLost
	}
	restoreErr := e.runWriteSubstage(ctx, func(stageCtx context.Context) error { return e.ops.RestoreFileLock(stageCtx, execution) })
	finalizeErr := e.finalize(ctx, execution, restoreErr != nil)
	if err := e.markFailure(ctx, execution.Task.ID, owner, failureStage, restoreErr != nil); err != nil {
		return err
	}
	if restoreErr != nil {
		return errors.New("plugin update pre-write failure and file lock restoration failed")
	}
	if finalizeErr != nil {
		return finalizeErr
	}
	return fmt.Errorf("plugin update failed at %s before plugin write", failureStage)
}

// rollbackOrHalt 是更新失败后统一的分流点：auto_rollback=1（单个更新的默认行为）立即执行
// 自动回滚；auto_rollback=0（批量更新任务）不做任何自动改动，直接终态化等待用户决定。
func (e *wpPluginUpdateExecutor) rollbackOrHalt(ctx context.Context, execution wpPluginUpdateExecution, owner, failureStage string) error {
	if !execution.Task.AutoRollback {
		return e.haltForManualDecision(ctx, execution, owner, failureStage)
	}
	return e.rollback(ctx, execution, owner, failureStage)
}

func (e *wpPluginUpdateExecutor) rollback(ctx context.Context, execution wpPluginUpdateExecution, owner, failureStage string) error {
	log.Printf("插件更新触发自动回滚 site=%s 组件=%s task=%s 失败阶段=%s", execution.Domain, execution.Task.ComponentKey, execution.Task.ID, failureStage)
	controlCtx, cancel := e.controlContext(ctx)
	err := e.store.beginAutomaticRollback(controlCtx, execution.Task.ID, owner, failureStage, e.now().UTC())
	cancel()
	if err != nil {
		return err
	}
	succeeded, rollbackErr, ownershipErr := e.runRollbackSequence(ctx, execution, owner, wpUpdateRunning, e.interruptForSupervision)
	if ownershipErr != nil {
		return ownershipErr
	}
	controlCtx, cancel = e.controlContext(ctx)
	err = e.store.finishAutomaticRollback(controlCtx, execution.Task.ID, owner, succeeded, e.now().UTC())
	cancel()
	if err != nil {
		return err
	}
	if rollbackErr != nil {
		return errors.New("plugin update failed and automatic rollback failed")
	}
	return fmt.Errorf("plugin update failed at %s and was rolled back", failureStage)
}

// haltForManualDecision 用于 auto_rollback=0 的任务（批量更新）：健康检查/更新失败时不做任何
// 自动回滚，直接把任务终态化为 failed 并等待用户稍后选择回滚或忽略。
func (e *wpPluginUpdateExecutor) haltForManualDecision(ctx context.Context, execution wpPluginUpdateExecution, owner, failureStage string) error {
	log.Printf("插件更新失败但 auto_rollback 已关闭，等待人工决定 site=%s 组件=%s task=%s 失败阶段=%s", execution.Domain, execution.Task.ComponentKey, execution.Task.ID, failureStage)
	if err := e.finalize(ctx, execution, false); err != nil {
		return err
	}
	controlCtx, cancel := e.controlContext(ctx)
	err := e.store.haltForManualRollback(controlCtx, execution.Task.ID, owner, failureStage, e.now().UTC())
	cancel()
	if err != nil {
		return err
	}
	return fmt.Errorf("plugin update failed at %s and is awaiting a manual rollback decision", failureStage)
}

func newWPPluginManualRollbackOwner() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "manual-rollback-" + hex.EncodeToString(raw[:]), nil
}

// ManualRollback 由用户对一个 auto_rollback=0 且失败挂起（rollback_status='pending'）的任务
// 触发，执行与自动回滚完全相同的步骤序列，但运行在任务已终态（status='failed'）之上，
// 且重新 Prepare 以获得一个新的 PHP runner 会话（原会话可能在失败当时的进程里就已释放）。
func (e *wpPluginUpdateExecutor) ManualRollback(ctx context.Context, taskID string) error {
	owner, err := newWPPluginManualRollbackOwner()
	if err != nil {
		return err
	}
	if err := e.store.beginManualRollback(ctx, taskID, owner, e.now().UTC()); err != nil {
		return err
	}
	execution, err := e.loadExecution(ctx, taskID, owner, wpUpdateFailed)
	if err != nil {
		e.abandonManualRollbackCleanup(ctx, taskID, owner)
		return err
	}
	if _, err := e.ops.Prepare(ctx, execution); err != nil {
		e.abandonManualRollbackCleanup(ctx, taskID, owner)
		return errors.New("manual rollback preparation failed")
	}
	stopHeartbeat := e.startManualRollbackHeartbeat(taskID, owner)
	succeeded, rollbackErr, ownershipErr := e.runRollbackSequence(ctx, execution, owner, wpUpdateFailed, nil)
	stopHeartbeat()
	if ownershipErr != nil {
		return ownershipErr
	}
	if err := e.finishManualRollback(ctx, taskID, owner, succeeded); err != nil {
		return err
	}
	if rollbackErr != nil {
		return errors.New("manual rollback failed")
	}
	return nil
}

// startManualRollbackHeartbeat 启动一个独立于步骤边界的定时续租协程，返回值用于停止
// 心跳并等待协程退出。人工回滚是同步执行的，不像 worker 那样把执行放在子协程、外层
// 独立心跳；这里反过来：把心跳放到子协程，续租频率与自动回滚路径的 worker 心跳一致
// （wpCoreUpdateWorkerHeartbeatInterval，30 秒），这样哪怕单个步骤（比如恢复大数据库）
// 跑满了整个 wpUpdateLease 时长，也不会因为只在步骤边界续租一次而在临界点被第二个
// 请求抢占——这是 runRollbackStep 里按步骤续租的补充，不是替代，两者同时生效无害。
func (e *wpPluginUpdateExecutor) startManualRollbackHeartbeat(taskID, owner string) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(wpCoreUpdateWorkerHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				renewed, err := e.store.renewManualRollbackClaim(context.Background(), taskID, owner, e.now().UTC())
				if err != nil || !renewed {
					return
				}
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

func (e *wpPluginUpdateExecutor) finishManualRollback(ctx context.Context, taskID, owner string, succeeded bool) error {
	controlCtx, cancel := e.controlContext(ctx)
	defer cancel()
	return e.store.finishManualRollback(controlCtx, taskID, owner, succeeded, e.now().UTC())
}

// abandonManualRollbackCleanup 用 controlContext（基于 context.WithoutCancel，不受调用方
// ctx 取消/超时影响）执行放弃认领，而不是直接复用可能已经取消的原始 ctx——loadExecution/
// Prepare 失败往往正是因为请求断开或超时，如果放弃认领也用同一个 ctx，会在同一个原因下
// 立刻再次失败，导致 lease_owner 永远清不掉，用户既不能重试回滚也不能 Ignore。
func (e *wpPluginUpdateExecutor) abandonManualRollbackCleanup(ctx context.Context, taskID, owner string) {
	controlCtx, cancel := e.controlContext(ctx)
	defer cancel()
	if err := e.store.abandonManualRollback(controlCtx, taskID, owner, e.now().UTC()); err != nil {
		log.Printf("放弃人工回滚认领失败 task=%s: %v", taskID, err)
	}
}

// runRollbackSequence 执行完整的回滚步骤序列：开启维护模式 → 恢复数据库（database_backup_mode='fresh'
// 时）→ 恢复插件文件 → 关闭维护模式 → 回滚后健康检查 → 恢复文件锁。expectedStatus 决定每一步执行前
// 的持有权校验针对 status='running'（自动回滚）还是 status='failed'（用户手动触发回滚）。
// handleSupervisionUncertain 仅自动回滚传入，用于把「运行状态不确定」的健康检查错误转为
// interrupted_unknown；人工触发的回滚任务本就已是终态，这类错误按普通回滚失败处理即可。
// 返回 (是否全部成功, 汇总的回滚错误, 持有权丢失错误)：只有第三个非 nil 时调用方才需要立即返回。
func (e *wpPluginUpdateExecutor) runRollbackSequence(ctx context.Context, execution wpPluginUpdateExecution, owner, expectedStatus string,
	handleSupervisionUncertain func(context.Context, string, string, error) (bool, error)) (succeeded bool, rollbackErr, ownershipErr error) {
	taskID := execution.Task.ID
	if stepErr, abort := e.runRollbackStep(ctx, taskID, owner, expectedStatus, "maintenance_on",
		func(stepCtx context.Context) error { return e.ops.SetMaintenance(stepCtx, execution, true) }); abort != nil {
		return false, nil, abort
	} else if stepErr != nil {
		rollbackErr = errors.Join(rollbackErr, stepErr)
	}
	if execution.Task.DatabaseBackupMode == "fresh" {
		if stepErr, abort := e.runRollbackStep(ctx, taskID, owner, expectedStatus, "restore_database",
			func(stepCtx context.Context) error { return e.ops.RestoreDatabase(stepCtx, execution) }); abort != nil {
			return false, nil, abort
		} else if stepErr != nil {
			rollbackErr = errors.Join(rollbackErr, stepErr)
		}
	}
	if stepErr, abort := e.runRollbackStep(ctx, taskID, owner, expectedStatus, "restore_plugin_files",
		func(stepCtx context.Context) error { return e.ops.RestorePluginFiles(stepCtx, execution) }); abort != nil {
		return false, nil, abort
	} else if stepErr != nil {
		rollbackErr = errors.Join(rollbackErr, stepErr)
	}
	if rollbackErr == nil {
		if stepErr, abort := e.runRollbackStep(ctx, taskID, owner, expectedStatus, "maintenance_off",
			func(stepCtx context.Context) error { return e.ops.SetMaintenance(stepCtx, execution, false) }); abort != nil {
			return false, nil, abort
		} else if stepErr != nil {
			rollbackErr = stepErr
		} else {
			if err := e.requireOwnershipWithStatus(ctx, taskID, owner, expectedStatus); err != nil {
				return false, nil, err
			}
			if err := e.runProbe(ctx, func(probeCtx context.Context) error { return e.ops.CheckRollbackHealth(probeCtx, execution) }); err != nil {
				if handleSupervisionUncertain != nil {
					if handled, interruptErr := handleSupervisionUncertain(ctx, taskID, owner, err); handled {
						if interruptErr != nil {
							return false, nil, interruptErr
						}
						return false, nil, err
					}
				}
				e.recordFailureEventWithStatus(ctx, taskID, owner, expectedStatus, "rollback", pluginRollbackStepCode("rollback_health", err))
				rollbackErr = err
			}
		}
	}
	if stepErr, abort := e.runRollbackStep(ctx, taskID, owner, expectedStatus, "restore_file_lock",
		func(stepCtx context.Context) error { return e.ops.RestoreFileLock(stepCtx, execution) }); abort != nil {
		return false, nil, abort
	} else if stepErr != nil {
		rollbackErr = errors.Join(rollbackErr, stepErr)
	}
	succeeded = rollbackErr == nil
	if err := e.finalize(ctx, execution, !succeeded); err != nil {
		rollbackErr = errors.Join(rollbackErr, err)
		succeeded = false
	}
	return succeeded, rollbackErr, nil
}

// recordFailureEvent 把关键步骤的失败原因落到事件表，供前端日志复制；记录失败不影响主流程。
func (e *wpPluginUpdateExecutor) recordFailureEvent(ctx context.Context, taskID, owner, stage, code string) {
	e.recordFailureEventWithStatus(ctx, taskID, owner, wpUpdateRunning, stage, code)
}

// recordFailureEventWithStatus 与 recordFailureEvent 相同，但可指定任务当前应处于的 status，
// 供批量更新失败后（status='failed'）的人工回滚复用同一套事件记录逻辑。
func (e *wpPluginUpdateExecutor) recordFailureEventWithStatus(ctx context.Context, taskID, owner, status, stage, code string) {
	if code == "" {
		code = stage + "_failed"
	}
	if err := e.store.recordEventWithStatus(ctx, taskID, owner, status, stage, "failed", code, e.now().UTC()); err != nil {
		log.Printf("record %s failure event: %v", stage, err)
	}
}

// pluginHealthCheckFailureCode 把健康检查错误映射为稳定的 error_code。
func pluginHealthCheckFailureCode(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "site health probe detected fatal response"):
		return "site_fatal_error"
	case strings.Contains(msg, "site health probe failed"):
		return "site_unreachable"
	case strings.Contains(msg, "site health response invalid"):
		return "site_response_invalid"
	case strings.Contains(msg, "invalid probe domain"):
		return "invalid_probe_domain"
	case strings.Contains(msg, "plugin health plan mismatch"), strings.Contains(msg, "active expectation changed"):
		return "plugin_health_plan_mismatch"
	default:
		return "health_check_failed"
	}
}

// pluginRollbackStepCode 根据回滚步骤和错误生成 error_code。
func pluginRollbackStepCode(step string, err error) string {
	if err == nil {
		return ""
	}
	base := step + "_failed"
	switch step {
	case "maintenance_on":
		base = "rollback_maintenance_failed"
	case "restore_database":
		base = "rollback_restore_database_failed"
	case "restore_plugin_files":
		base = "rollback_restore_plugin_failed"
	case "maintenance_off":
		base = "rollback_maintenance_off_failed"
	case "rollback_health":
		base = "rollback_health_check_failed"
	case "restore_file_lock":
		base = "rollback_restore_lock_failed"
	}
	msg := err.Error()
	if strings.Contains(msg, "site health") {
		return base + "_site"
	}
	return base
}

func (e *wpPluginUpdateExecutor) interruptForSupervision(ctx context.Context, taskID, owner string, cause error) (bool, error) {
	if !errors.Is(cause, errWPPluginScopeSupervisionUncertain) {
		return false, nil
	}
	controlCtx, cancel := e.controlContext(ctx)
	defer cancel()
	changed, err := e.store.interruptOwned(controlCtx, taskID, owner, "runner_supervision_uncertain", e.now().UTC())
	if err != nil {
		return true, err
	}
	if !changed {
		return true, errUpdateTaskOwnershipLost
	}
	return true, nil
}

func (e *wpPluginUpdateExecutor) finalize(ctx context.Context, execution wpPluginUpdateExecution, preserve bool) error {
	finalizer, ok := e.ops.(wpPluginUpdateFinalizer)
	if !ok {
		return nil
	}
	controlCtx, cancel := e.controlContext(ctx)
	defer cancel()
	return finalizer.Finalize(controlCtx, execution, preserve)
}

func (e *wpPluginUpdateExecutor) requireOwnership(ctx context.Context, taskID, owner string) error {
	return e.requireOwnershipWithStatus(ctx, taskID, owner, wpUpdateRunning)
}

// requireOwnershipWithStatus 与 requireOwnership 相同，但可指定任务当前应处于的 status，
// 供批量更新失败后（status='failed'）的人工回滚复用同一套持有权校验。
func (e *wpPluginUpdateExecutor) requireOwnershipWithStatus(ctx context.Context, taskID, owner, status string) error {
	// 人工回滚（status='failed'）没有 worker 那样的独立心跳协程续租，借每一步开始前
	// 都要做的持有权校验顺带续租，避免耗时步骤跑到一半时租约过期被第二个请求抢占。
	if status == wpUpdateFailed {
		renewed, err := e.store.renewManualRollbackClaim(ctx, taskID, owner, e.now().UTC())
		if err != nil {
			return err
		}
		if !renewed {
			return errUpdateTaskOwnershipLost
		}
		return nil
	}
	owned, err := e.ownsTaskWithStatus(ctx, taskID, owner, status)
	if err != nil {
		return err
	}
	if !owned {
		return errUpdateTaskOwnershipLost
	}
	return nil
}

func (e *wpPluginUpdateExecutor) ownsRunningTask(ctx context.Context, taskID, owner string) (bool, error) {
	return e.ownsTaskWithStatus(ctx, taskID, owner, wpUpdateRunning)
}

func (e *wpPluginUpdateExecutor) ownsTaskWithStatus(ctx context.Context, taskID, owner, status string) (bool, error) {
	controlCtx, cancel := e.controlContext(ctx)
	defer cancel()
	return e.store.ownsTaskWithStatus(controlCtx, taskID, owner, status)
}

// runRollbackStep 在指定 expectedStatus 下校验任务仍由 owner 持有，执行一个回滚子步骤并在失败时
// 记录事件；供自动回滚（status='running'）与用户手动触发回滚（status='failed'）共用同一套步骤实现。
// runRollbackStep 校验 owner 仍持有 taskID（expectedStatus 下），执行一个回滚子步骤。
// abort 非 nil 时调用方必须立即返回（持有权校验本身出错，或已确认丢失持有权），不应计入
// 回滚汇总错误；stepErr 非 nil 而 abort 为 nil 时，仅表示该步骤自身失败，应计入汇总错误后继续下一步。
func (e *wpPluginUpdateExecutor) runRollbackStep(ctx context.Context, taskID, owner, expectedStatus, stepName string, run func(context.Context) error) (stepErr, abort error) {
	if err := e.requireOwnershipWithStatus(ctx, taskID, owner, expectedStatus); err != nil {
		return nil, err
	}
	if err := e.runWriteSubstage(ctx, run); err != nil {
		e.recordFailureEventWithStatus(ctx, taskID, owner, expectedStatus, "rollback", pluginRollbackStepCode(stepName, err))
		return err, nil
	}
	return nil, nil
}

func (e *wpPluginUpdateExecutor) markFailure(ctx context.Context, taskID, owner, stage string, attention bool) error {
	controlCtx, cancel := e.controlContext(ctx)
	defer cancel()
	return e.store.markFailure(controlCtx, taskID, owner, stage, attention, e.now().UTC())
}

func (e *wpPluginUpdateExecutor) stage(ctx context.Context, taskID, owner, expectedStage, nextStage string) error {
	controlCtx, cancel := e.controlContext(ctx)
	err := e.store.advanceOwnedStage(controlCtx, taskID, owner, expectedStage, nextStage, e.now().UTC())
	cancel()
	if err != nil {
		return err
	}
	if ctx.Err() == nil {
		return nil
	}
	controlCtx, cancel = e.controlContext(ctx)
	_, interruptErr := e.store.interruptOwned(controlCtx, taskID, owner, "execution_cancelled", e.now().UTC())
	cancel()
	if interruptErr != nil {
		return interruptErr
	}
	return ctx.Err()
}

func (e *wpPluginUpdateExecutor) loadExecution(ctx context.Context, taskID, owner, expectedStatus string) (wpPluginUpdateExecution, error) {
	if !wpUpdateTaskIDPattern.MatchString(taskID) || owner == "" {
		return wpPluginUpdateExecution{}, errors.New("invalid plugin update execution")
	}
	task, err := e.store.getTask(ctx, taskID)
	if err != nil || task.Status != expectedStatus || task.TaskKind != "update" || task.ComponentType != "plugin" ||
		task.LeaseOwner != owner || !task.BackupReady || !validWPPluginComponentKey(task.ComponentKey) {
		return wpPluginUpdateExecution{}, errors.New("plugin update task is not executable")
	}
	taskDir := filepath.Join(e.root, taskID)
	packagePath := filepath.Join(taskDir, "package.zip")
	if task.PackageSnapshotPath != packagePath {
		return wpPluginUpdateExecution{}, errors.New("plugin update package path is not controlled")
	}
	sha, _, err := hashRegularFile(packagePath)
	if err != nil || sha != task.DownloadedSHA256 {
		return wpPluginUpdateExecution{}, errors.New("plugin update package digest mismatch")
	}
	slug := strings.Split(task.ComponentKey, "/")[0]
	if _, err := ValidateWPComponentPackage(ctx, packagePath, WPComponentPackageExpectation{
		ComponentType: "plugin", ComponentKey: task.ComponentKey, OfficialSlug: slug, TargetVersion: task.TargetVersion,
	}); err != nil {
		return wpPluginUpdateExecution{}, errors.New("plugin update package validation failed")
	}
	validatedSHA, _, err := hashRegularFile(packagePath)
	if err != nil || validatedSHA != sha {
		return wpPluginUpdateExecution{}, errors.New("plugin update package changed during validation")
	}
	var execution wpPluginUpdateExecution
	execution.Task, execution.PackagePath = task, packagePath
	var lockEnabled int
	err = e.store.db.QueryRowContext(ctx, `SELECT domain,system_user,web_root,db_name,file_lock_mode,file_lock_enabled
		FROM websites WHERE id=? AND site_type='wordpress' AND status='active'`, task.SiteID).
		Scan(&execution.Domain, &execution.SystemUser, &execution.WebRoot, &execution.DatabaseName, &execution.FileLockMode, &lockEnabled)
	if err != nil || !filepath.IsAbs(execution.WebRoot) {
		return wpPluginUpdateExecution{}, errors.New("plugin update site is not executable")
	}
	execution.FileLockActive = lockEnabled != 0
	rows, err := e.store.db.QueryContext(ctx, `SELECT kind,file_path,sha256 FROM wp_update_task_backups
		WHERE task_id=? AND protected=1 AND deleted_at IS NULL AND kind IN ('database','plugin_files')`, taskID)
	if err != nil {
		return wpPluginUpdateExecution{}, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var kind, name, digest string
		if err := rows.Scan(&kind, &name, &digest); err != nil {
			return wpPluginUpdateExecution{}, err
		}
		expected := filepath.Join(taskDir, map[string]string{"database": "database.sql.gz", "plugin_files": "plugin-files.tar.gz"}[kind])
		if name != expected || seen[kind] {
			return wpPluginUpdateExecution{}, errors.New("plugin update backup path is not controlled")
		}
		actual, _, err := hashRegularFile(name)
		if err != nil || actual != digest {
			return wpPluginUpdateExecution{}, errors.New("plugin update backup digest mismatch")
		}
		if kind == "database" {
			execution.DatabaseBackup = name
		} else {
			execution.PluginBackup = name
		}
		seen[kind] = true
	}
	if err := rows.Err(); err != nil {
		return wpPluginUpdateExecution{}, err
	}
	if (task.DatabaseBackupMode == "fresh" && !seen["database"]) || !seen["plugin_files"] {
		return wpPluginUpdateExecution{}, errors.New("plugin update backups are incomplete")
	}
	return execution, nil
}

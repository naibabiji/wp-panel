package executor

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type phpFPMPoolBatchPlan struct {
	domain        string
	configContent string
	targetPath    string
	logDir        string
	socketPath    string
}

type phpFPMPoolBatchResult struct {
	skipped         int
	changed         int
	recovery        int
	reloadAttempts  int
	restartAttempts int
	fallback        bool
}

type preparedPHPFPMPool struct {
	phpFPMPoolBatchPlan
	oldContent []byte
	hadOld     bool
	changed    bool
}

var applyPHPFPMPoolIndividually = func(engine *TemplateEngine, plan phpFPMPoolBatchPlan) error {
	return engine.ApplyPHPFPMPool(plan.configContent, plan.targetPath, plan.logDir, plan.socketPath)
}

func (e *TemplateEngine) applyPHPFPMPoolBatch(plans []phpFPMPoolBatchPlan) (phpFPMPoolBatchResult, error) {
	result := phpFPMPoolBatchResult{}
	prepared := make([]preparedPHPFPMPool, 0, len(plans))
	for _, plan := range plans {
		if err := os.MkdirAll(plan.logDir, 0755); err != nil {
			return result, fmt.Errorf("%s: 创建日志目录失败: %w", plan.domain, err)
		}
		oldContent, oldErr := os.ReadFile(plan.targetPath)
		hadOld := oldErr == nil
		if oldErr != nil && !os.IsNotExist(oldErr) {
			return result, fmt.Errorf("%s: 读取旧 Pool 配置失败: %w", plan.domain, oldErr)
		}
		changed := !hadOld || !bytes.Equal(oldContent, []byte(plan.configContent))
		if !changed {
			if err := checkPHPFPMSocket(plan.socketPath, 1, 0); err == nil {
				result.skipped++
				continue
			}
			result.recovery++
		} else {
			result.changed++
		}
		prepared = append(prepared, preparedPHPFPMPool{
			phpFPMPoolBatchPlan: plan,
			oldContent:          oldContent,
			hadOld:              hadOld,
			changed:             changed,
		})
	}
	if len(prepared) == 0 {
		return result, nil
	}

	written := make([]preparedPHPFPMPool, 0, result.changed)
	for _, pool := range prepared {
		if !pool.changed {
			continue
		}
		if err := writePHPFPMPoolFile(pool.targetPath, []byte(pool.configContent), 0644); err != nil {
			if restoreErr := restorePreparedPHPFPMPools(written); restoreErr != nil {
				return result, fmt.Errorf("%s: 写入 PHP-FPM 配置失败: %v；批量旧配置恢复不完整，需要人工检查: %w", pool.domain, err, restoreErr)
			}
			return e.fallbackPHPFPMPoolBatch(prepared, result, fmt.Errorf("%s: 写入 PHP-FPM 配置失败: %w", pool.domain, err))
		}
		written = append(written, pool)
	}

	if len(written) > 0 {
		if testOut, err := testPHPFPMConfig(); err != nil {
			applyErr := fmt.Errorf("PHP-FPM 批量配置检查失败: %s", strings.TrimSpace(string(testOut)))
			if restoreErr := restorePreparedPHPFPMPools(written); restoreErr != nil {
				return result, fmt.Errorf("%v；批量旧配置恢复不完整，需要人工检查: %w", applyErr, restoreErr)
			}
			return e.fallbackPHPFPMPoolBatch(prepared, result, applyErr)
		}
	}

	// PHP-FPM reload 会沿用继承的监听 fd；若 socket 路径已被删除，reload
	// 不会重新创建该路径，因此恢复集合必须直接 restart（失败后再 start）。
	reloadAttempts, restartAttempts, activateErr := activatePHPFPM(result.recovery > 0)
	result.reloadAttempts += reloadAttempts
	result.restartAttempts += restartAttempts
	if activateErr != nil {
		if restoreErr := restorePreparedPHPFPMPools(written); restoreErr != nil {
			return result, fmt.Errorf("%v；批量旧配置恢复不完整，需要人工检查: %w", activateErr, restoreErr)
		}
		result.restartAttempts++
		if restoreErr := restartAndCheckPHPFPMPools(prepared); restoreErr != nil {
			return result, fmt.Errorf("%v；旧 Pool 配置和 PHP-FPM 自动恢复不完整，需要人工检查: %w", activateErr, restoreErr)
		}
		return result, fmt.Errorf("%v；旧 Pool 配置和 PHP-FPM 已恢复", activateErr)
	}

	var failed []preparedPHPFPMPool
	for _, pool := range prepared {
		if err := checkPHPFPMSocket(pool.socketPath, 30, 100*time.Millisecond); err != nil {
			failed = append(failed, pool)
		}
	}
	if len(failed) == 0 {
		return result, nil
	}

	failedNames := make([]string, 0, len(failed))
	for _, pool := range failed {
		failedNames = append(failedNames, pool.domain)
	}
	if restoreErr := restorePreparedPHPFPMPools(failed); restoreErr != nil {
		return result, fmt.Errorf("站点 PHP-FPM Pool 未就绪 (%s)；失败站点旧配置恢复不完整，需要人工检查: %w", strings.Join(failedNames, ", "), restoreErr)
	}
	result.restartAttempts++
	if restoreErr := restartAndCheckPHPFPMPools(failed); restoreErr != nil {
		return result, fmt.Errorf("站点 PHP-FPM Pool 未就绪 (%s)；失败站点自动恢复不完整，需要人工检查: %w", strings.Join(failedNames, ", "), restoreErr)
	}
	return result, fmt.Errorf("站点 PHP-FPM Pool 未就绪 (%s)；失败站点旧配置和 PHP-FPM 已恢复", strings.Join(failedNames, ", "))
}

func (e *TemplateEngine) fallbackPHPFPMPoolBatch(pools []preparedPHPFPMPool, result phpFPMPoolBatchResult, cause error) (phpFPMPoolBatchResult, error) {
	result.fallback = true
	var failures []error
	for _, pool := range pools {
		if err := applyPHPFPMPoolIndividually(e, pool.phpFPMPoolBatchPlan); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", pool.domain, err))
		}
	}
	if len(failures) > 0 {
		return result, fmt.Errorf("%v；已退回逐站应用以定位失败: %w", cause, errors.Join(failures...))
	}
	return result, nil
}

func restorePreparedPHPFPMPools(pools []preparedPHPFPMPool) error {
	var failures []error
	for _, pool := range pools {
		if !pool.changed {
			continue
		}
		if pool.hadOld {
			if err := writePHPFPMPoolFile(pool.targetPath, pool.oldContent, 0644); err != nil {
				failures = append(failures, fmt.Errorf("%s: 恢复旧 Pool 文件失败: %w", pool.domain, err))
			}
		} else if err := removePHPFPMPoolFile(pool.targetPath); err != nil && !os.IsNotExist(err) {
			failures = append(failures, fmt.Errorf("%s: 删除本次新建 Pool 文件失败: %w", pool.domain, err))
		}
	}
	return errors.Join(failures...)
}

func restartAndCheckPHPFPMPools(pools []preparedPHPFPMPool) error {
	if err := runPHPFPMServiceAction("restart"); err != nil {
		return fmt.Errorf("恢复旧配置后重启 PHP-FPM 失败: %w", err)
	}
	var failures []error
	for _, pool := range pools {
		if err := checkPHPFPMSocket(pool.socketPath, 30, 100*time.Millisecond); err != nil {
			failures = append(failures, fmt.Errorf("%s: 恢复旧配置后网站 Pool 仍未就绪: %w", pool.domain, err))
		}
	}
	return errors.Join(failures...)
}

func activatePHPFPM(restartFirst bool) (reloadAttempts, restartAttempts int, err error) {
	if !restartFirst {
		reloadAttempts++
		if err := runPHPFPMServiceAction("reload"); err == nil {
			return reloadAttempts, restartAttempts, nil
		}
	}
	restartAttempts++
	if err := runPHPFPMServiceAction("restart"); err == nil {
		return reloadAttempts, restartAttempts, nil
	}
	if err := runPHPFPMServiceAction("start"); err != nil {
		if restartFirst {
			return reloadAttempts, restartAttempts, fmt.Errorf("PHP-FPM restart 和 start 均失败: %w", err)
		}
		return reloadAttempts, restartAttempts, fmt.Errorf("PHP-FPM reload、restart 和 start 均失败: %w", err)
	}
	return reloadAttempts, restartAttempts, nil
}

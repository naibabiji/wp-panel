package executor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	wpPluginScopeRuntime = 9 * time.Minute
	wpPluginScopeStop    = 10 * time.Second
	wpPluginScopePoll    = 250 * time.Millisecond
	wpPluginScopeOutput  = 64 << 10
)

var errWPPluginScopeSupervisionUncertain = errors.New("plugin update scope supervision uncertain")

type wpPluginScopeCommand func(context.Context, string, ...string) ([]byte, error)

type wpPluginScopeState struct {
	LoadState   string
	ActiveState string
	SubState    string
	Result      string
	MainPID     int
}

type wpPluginUpdateScope struct {
	systemdRun string
	systemctl  string
	runnerPath string
	run        wpPluginScopeCommand
	mu         sync.Mutex
	completed  map[string]bool
}

func newWPPluginUpdateScope(systemdRun, systemctl, runnerPath string, run wpPluginScopeCommand) (*wpPluginUpdateScope, error) {
	if !filepath.IsAbs(systemdRun) || !filepath.IsAbs(systemctl) || !filepath.IsAbs(runnerPath) || run == nil {
		return nil, errors.New("invalid plugin update scope")
	}
	return &wpPluginUpdateScope{systemdRun: systemdRun, systemctl: systemctl, runnerPath: runnerPath, run: run, completed: map[string]bool{}}, nil
}

func newDefaultWPPluginUpdateScope(runnerPath string) (*wpPluginUpdateScope, error) {
	return newWPPluginUpdateScope("/usr/bin/systemd-run", "/usr/bin/systemctl", runnerPath, runWPPluginScopeCommand)
}

func runWPPluginScopeCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	stdout := newCountingSink(wpPluginScopeOutput, true)
	stderr := newCountingSink(wpPluginScopeOutput, false)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	_, stdoutExceeded, raw := stdout.snapshot()
	_, stderrExceeded, _ := stderr.snapshot()
	if stdoutExceeded || stderrExceeded {
		return nil, errors.New("plugin update scope output exceeded")
	}
	return raw, err
}

func (s *wpPluginUpdateScope) Run(ctx context.Context, taskID string, runnerArgs ...string) error {
	unit, err := wpPluginUpdateUnitName(taskID)
	if err != nil || len(runnerArgs) == 0 {
		return errors.New("invalid plugin update scope request")
	}
	s.mu.Lock()
	previousCompleted := s.completed[taskID]
	s.mu.Unlock()
	if previousCompleted {
		waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wpPluginUpdateControlTimeout)
		err := s.waitCollected(waitCtx, taskID)
		cancel()
		if err != nil {
			return err
		}
		s.mu.Lock()
		delete(s.completed, taskID)
		s.mu.Unlock()
	}
	args := []string{
		"--quiet", "--wait", "--collect", "--unit=" + unit,
		"--property=RuntimeMaxSec=" + strconv.FormatInt(int64(wpPluginScopeRuntime/time.Second), 10) + "s",
		"--property=TimeoutStopSec=" + strconv.FormatInt(int64(wpPluginScopeStop/time.Second), 10) + "s",
		"--property=KillMode=control-group", "--", s.runnerPath,
	}
	args = append(args, runnerArgs...)
	started := time.Now()
	if _, err := s.run(ctx, s.systemdRun, args...); err != nil {
		remaining := wpPluginScopeRuntime + wpPluginScopeStop + wpPluginUpdateControlTimeout - time.Since(started)
		if remaining <= 0 {
			remaining = wpPluginUpdateControlTimeout
		}
		waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), remaining)
		defer cancel()
		if waitErr := s.waitInactive(waitCtx, taskID); waitErr != nil {
			return errWPPluginScopeSupervisionUncertain
		}
		s.markCompleted(taskID)
		return errors.New("plugin update scope failed")
	}
	s.markCompleted(taskID)
	return nil
}

func (s *wpPluginUpdateScope) markCompleted(taskID string) {
	s.mu.Lock()
	s.completed[taskID] = true
	s.mu.Unlock()
}

func (s *wpPluginUpdateScope) waitCollected(ctx context.Context, taskID string) error {
	ticker := time.NewTicker(wpPluginScopePoll)
	defer ticker.Stop()
	for {
		state, err := s.Inspect(ctx, taskID)
		if err == nil {
			if state.LoadState == "not-found" {
				return nil
			}
			if state.ActiveState == "active" || state.ActiveState == "activating" || state.ActiveState == "reloading" {
				return errWPPluginScopeSupervisionUncertain
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("plugin update scope was not collected")
		case <-ticker.C:
		}
	}
}

func (s *wpPluginUpdateScope) waitInactive(ctx context.Context, taskID string) error {
	ticker := time.NewTicker(wpPluginScopePoll)
	defer ticker.Stop()
	for {
		state, err := s.Inspect(ctx, taskID)
		if err == nil && (state.LoadState == "not-found" || state.ActiveState == "inactive" || state.ActiveState == "failed") {
			return nil
		}
		select {
		case <-ctx.Done():
			return errWPPluginScopeSupervisionUncertain
		case <-ticker.C:
		}
	}
}

func (s *wpPluginUpdateScope) Inspect(ctx context.Context, taskID string) (wpPluginScopeState, error) {
	unit, err := wpPluginUpdateUnitName(taskID)
	if err != nil {
		return wpPluginScopeState{}, err
	}
	out, err := s.run(ctx, s.systemctl, "show", unit,
		"--property=LoadState", "--property=ActiveState", "--property=SubState", "--property=Result", "--property=MainPID", "--no-pager")
	if err != nil {
		return wpPluginScopeState{}, errors.New("plugin update scope inspection failed")
	}
	state := wpPluginScopeState{}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch key {
		case "LoadState":
			state.LoadState = value
		case "ActiveState":
			state.ActiveState = value
		case "SubState":
			state.SubState = value
		case "Result":
			state.Result = value
		case "MainPID":
			state.MainPID, err = strconv.Atoi(value)
			if err != nil || state.MainPID < 0 {
				return wpPluginScopeState{}, errors.New("invalid plugin update scope state")
			}
		}
	}
	if state.LoadState == "" || state.ActiveState == "" || state.SubState == "" {
		return wpPluginScopeState{}, errors.New("incomplete plugin update scope state")
	}
	return state, nil
}

func wpPluginUpdateUnitName(taskID string) (string, error) {
	if !wpUpdateTaskIDPattern.MatchString(taskID) {
		return "", errors.New("invalid plugin update task id")
	}
	return fmt.Sprintf("wp-panel-plugin-%s.service", strings.TrimPrefix(taskID, "wpu_")), nil
}

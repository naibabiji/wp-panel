package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

var (
	aiDevelopmentSSHDefaultConfig  = "/etc/ssh/sshd_config"
	aiDevelopmentSSHCommandTimeout = 15 * time.Second
	aiDevelopmentSSHReload         = reloadAIDevelopmentSSH
	runAIDevelopmentSSHCommand     = runAIDevelopmentSSHCommandImpl
	resolveAIDevelopmentSSHRuntime = resolveAIDevelopmentSSHRuntimeImpl
	atomicWriteAIDevelopmentSSH    = atomicWriteAIDevelopmentSSHConfigImpl
	aiDevelopmentSSHMu             sync.Mutex
)

var ErrAIDevelopmentSSHEnvironment = errors.New("SSH environment cannot be inspected")

type aiDevelopmentSSHRuntime struct {
	service    string
	configPath string
}

// EnsureAIDevelopmentSSH is a preflight for the AI SSH flow. It never changes
// the server unless repair is explicitly true.
func EnsureAIDevelopmentSSH(ctx context.Context, systemUser, host string, repair bool) error {
	_, err := EnsureAIDevelopmentSSHResult(ctx, systemUser, host, repair)
	return err
}

// EnsureAIDevelopmentSSHResult reports whether this invocation actually wrote
// the managed SSH repair block. A successful no-op check returns false.
func EnsureAIDevelopmentSSHResult(ctx context.Context, systemUser, host string, repair bool) (bool, error) {
	return (productionAIDevelopmentSystem{}).EnsureSSHResult(ctx, systemUser, host, repair)
}

func (productionAIDevelopmentSystem) EnsureSSH(ctx context.Context, systemUser, host string, repair bool) error {
	_, err := (productionAIDevelopmentSystem{}).EnsureSSHResult(ctx, systemUser, host, repair)
	return err
}

func (productionAIDevelopmentSystem) EnsureSSHResult(ctx context.Context, systemUser, host string, repair bool) (bool, error) {
	aiDevelopmentSSHMu.Lock()
	defer aiDevelopmentSSHMu.Unlock()

	if !aiDevelopmentUserPattern.MatchString(systemUser) || strings.TrimSpace(host) == "" {
		return false, ErrAIDevelopmentSSHEnvironment
	}
	runtime, err := resolveAIDevelopmentSSHRuntime(ctx)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrAIDevelopmentSSHEnvironment, err)
	}
	enabled, err := effectiveAIDevelopmentSSHPublicKeyAuth(ctx, runtime, systemUser, host)
	if err != nil {
		return false, fmt.Errorf("%w: inspect public-key authentication: %v", ErrAIDevelopmentSSHEnvironment, err)
	}
	if enabled {
		return false, nil
	}
	if !repair {
		return false, ErrAIDevelopmentSSHPublicKeyAuthDisabled
	}
	if err := repairAIDevelopmentSSHPublicKeyAuth(ctx, runtime, systemUser, host); err != nil {
		return false, err
	}
	return true, nil
}

func effectiveAIDevelopmentSSHPublicKeyAuth(ctx context.Context, runtime aiDevelopmentSSHRuntime, systemUser, host string) (bool, error) {
	output, err := runAIDevelopmentSSHCommand(ctx, "/usr/sbin/sshd", "-T", "-f", runtime.configPath, "-C", "user="+systemUser+",host="+host+",addr=127.0.0.1")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.EqualFold(fields[0], "pubkeyauthentication") {
			return strings.EqualFold(fields[1], "yes"), nil
		}
	}
	return false, errors.New("sshd -T did not report PubkeyAuthentication")
}

func repairAIDevelopmentSSHPublicKeyAuth(ctx context.Context, runtime aiDevelopmentSSHRuntime, systemUser, host string) (err error) {
	backupPath := ""
	defer func() {
		status := "success"
		message := "已确认并修复 SSH 公钥认证"
		if err != nil {
			status = "failed"
			message = "SSH 公钥认证修复失败: " + err.Error()
		}
		if backupPath != "" {
			message += "; backup=" + backupPath
		}
		recordOperationLog("ssh_config_pubkey_authentication_repair", host, status, message)
	}()
	info, err := os.Lstat(runtime.configPath)
	if err != nil {
		return fmt.Errorf("read SSH configuration metadata: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("SSH configuration is not a regular file")
	}
	oldContent, err := os.ReadFile(runtime.configPath)
	if err != nil {
		return fmt.Errorf("read SSH configuration: %w", err)
	}
	backupPath, err = backupAIDevelopmentSSHConfig(oldContent)
	if err != nil {
		return fmt.Errorf("backup SSH configuration: %w", err)
	}

	managedHeader := "# WP Panel managed: enable SSH public-key authentication for AI development access.\n"
	newContent := ensureManagedAIDevelopmentSSHPubkeyBlock(oldContent, managedHeader)
	if err := atomicWriteAIDevelopmentSSH(runtime.configPath, newContent, info.Mode().Perm()); err != nil {
		return fmt.Errorf("write SSH configuration (backup %s): %w", backupPath, err)
	}
	if _, err := runAIDevelopmentSSHCommand(ctx, "/usr/sbin/sshd", "-t", "-f", runtime.configPath); err != nil {
		if restoreErr := restoreAIDevelopmentSSHConfig(runtime.configPath, oldContent, info.Mode().Perm()); restoreErr != nil {
			return fmt.Errorf("SSH syntax check failed and configuration restore failed: %v; restore: %w", err, restoreErr)
		}
		return fmt.Errorf("SSH syntax check failed; configuration restored: %w", err)
	}
	if err := aiDevelopmentSSHReload(ctx, runtime.service); err != nil {
		if restoreErr := restoreAIDevelopmentSSHConfig(runtime.configPath, oldContent, info.Mode().Perm()); restoreErr != nil {
			return fmt.Errorf("SSH reload failed and configuration restore failed: %v; restore: %w", err, restoreErr)
		}
		if reloadErr := aiDevelopmentSSHReload(ctx, runtime.service); reloadErr != nil {
			return fmt.Errorf("SSH reload failed; configuration restored but reload failed: %v; recovery reload: %w", err, reloadErr)
		}
		return fmt.Errorf("SSH reload failed; configuration restored: %w", err)
	}
	enabled, err := effectiveAIDevelopmentSSHPublicKeyAuth(ctx, runtime, systemUser, host)
	if err == nil && enabled {
		return nil
	}
	verificationErr := err
	if verificationErr == nil {
		verificationErr = errors.New("effective sshd configuration still disables public-key authentication")
	}
	if restoreErr := restoreAIDevelopmentSSHConfig(runtime.configPath, oldContent, info.Mode().Perm()); restoreErr != nil {
		return fmt.Errorf("SSH public-key verification failed and configuration restore failed: %v; restore: %w", verificationErr, restoreErr)
	}
	if reloadErr := aiDevelopmentSSHReload(ctx, runtime.service); reloadErr != nil {
		return fmt.Errorf("SSH public-key verification failed; configuration restored but recovery reload failed: %v; recovery reload: %w", verificationErr, reloadErr)
	}
	return errors.New("SSH public-key authentication remained disabled; configuration restored")
}

func backupAIDevelopmentSSHConfig(content []byte) (string, error) {
	backupDir := config.DefaultBackupDir
	if config.AppConfig != nil && strings.TrimSpace(config.AppConfig.Panel.BackupDir) != "" {
		backupDir = config.AppConfig.Panel.BackupDir
	}
	backupDir = filepath.Join(backupDir, "ssh-config")
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return "", err
	}
	if err := os.Chmod(backupDir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(backupDir, fmt.Sprintf("sshd_config.%s.%d.bak", time.Now().UTC().Format("20060102T150405.000000000Z"), os.Getpid()))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return path, nil
}

func atomicWriteAIDevelopmentSSHConfigImpl(path string, content []byte, mode os.FileMode) error {
	var uid, gid int = -1, -1
	if info, err := os.Stat(path); err == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(stat.Uid), int(stat.Gid)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sshd_config.wp-panel-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if uid >= 0 && gid >= 0 {
		if err := os.Chown(tmpPath, uid, gid); err != nil {
			tmp.Close()
			return err
		}
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func restoreAIDevelopmentSSHConfig(path string, content []byte, mode os.FileMode) error {
	return atomicWriteAIDevelopmentSSH(path, content, mode)
}

func runAIDevelopmentSSHCommandImpl(ctx context.Context, name string, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, aiDevelopmentSSHCommandTimeout)
	defer cancel()
	output, err := exec.CommandContext(commandCtx, name, args...).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return nil, errors.New(message)
	}
	return output, nil
}

func reloadAIDevelopmentSSH(ctx context.Context, service string) error {
	if _, err := runAIDevelopmentSSHCommand(ctx, "/usr/bin/systemctl", "reload", service); err != nil {
		return err
	}
	return nil
}

func resolveAIDevelopmentSSHRuntimeImpl(ctx context.Context) (aiDevelopmentSSHRuntime, error) {
	var failures []string
	for _, service := range []string{"ssh", "sshd"} {
		output, err := runAIDevelopmentSSHCommand(ctx, "/usr/bin/systemctl", "show", "-p", "ExecStart", "--value", service)
		if err != nil {
			failures = append(failures, service+": systemctl show: "+err.Error())
			continue
		}
		configPath, err := sshConfigPathFromExecStart(string(output))
		if err != nil {
			failures = append(failures, service+": "+err.Error())
			continue
		}
		return aiDevelopmentSSHRuntime{service: service, configPath: configPath}, nil
	}
	if len(failures) == 0 {
		return aiDevelopmentSSHRuntime{}, errors.New("no loaded SSH service returned ExecStart")
	}
	return aiDevelopmentSSHRuntime{}, fmt.Errorf("no usable SSH service runtime found: %s", strings.Join(failures, "; "))
}

func sshConfigPathFromExecStart(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("SSH service ExecStart is empty")
	}
	fields := strings.Fields(value)
	for i, field := range fields {
		if field == "-f" {
			if i+1 >= len(fields) || strings.Contains(fields[i+1], "$") {
				return "", errors.New("SSH service ExecStart has an unresolved -f config path")
			}
			if strings.HasPrefix(fields[i+1], "/") {
				return fields[i+1], nil
			}
			return "", errors.New("SSH service ExecStart has an invalid -f config path")
		}
		if strings.Contains(field, "$") && field != "$SSHD_OPTS" && field != "${SSHD_OPTS}" && field != "$SSH_OPTS" && field != "${SSH_OPTS}" {
			return "", errors.New("SSH service ExecStart contains an unexpanded variable")
		}
	}
	return aiDevelopmentSSHDefaultConfig, nil
}

func ensureManagedAIDevelopmentSSHPubkeyBlock(content []byte, header string) []byte {
	const endMarker = "# WP Panel managed end: enable SSH public-key authentication for AI development access.\n"
	text := string(content)
	for {
		start := strings.Index(text, header)
		if start < 0 {
			break
		}
		blockEnd := start + len(header)
		if strings.HasPrefix(text[blockEnd:], "PubkeyAuthentication ") {
			if lineEnd := strings.IndexByte(text[blockEnd:], '\n'); lineEnd >= 0 {
				blockEnd += lineEnd + 1
			}
		}
		if strings.HasPrefix(text[blockEnd:], endMarker) {
			blockEnd += len(endMarker)
			if strings.HasPrefix(text[blockEnd:], "\n") {
				blockEnd++
			}
		}
		text = text[:start] + text[blockEnd:]
	}
	return []byte(header + "PubkeyAuthentication yes\n" + endMarker + "\n" + text)
}

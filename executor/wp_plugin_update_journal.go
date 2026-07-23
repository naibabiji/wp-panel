package executor

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const (
	wpPluginJournalName = "runner.journal"
	wpPluginJournalMax  = 64 << 10
)

var wpPluginJournalCheckpoints = []string{
	"before_upgrade",
	"upgrader_entered",
	"upgrader_returned",
	"reactivate_started",
	"reactivate_completed",
}

type wpPluginUpdateJournalReport struct {
	Checkpoints []string
	Truncated   bool
}

func createWPPluginUpdateJournal(taskDir string, ownerUID, ownerGID int) (string, error) {
	if ownerUID < 0 || ownerGID < 0 {
		return "", errors.New("invalid plugin update journal owner")
	}
	root, err := openWPPluginJournalRoot(taskDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	f, err := root.OpenFile(wpPluginJournalName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0640)
	if err != nil {
		return "", err
	}
	keep := false
	defer func() {
		if !keep {
			_ = root.Remove(wpPluginJournalName)
		}
	}()
	if err := f.Chmod(0640); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Chown(ownerUID, ownerGID); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	dir, err := os.Open(taskDir)
	if err != nil {
		return "", err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return "", syncErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	keep = true
	return filepath.Join(taskDir, wpPluginJournalName), nil
}

func readWPPluginUpdateJournal(taskDir string) (wpPluginUpdateJournalReport, error) {
	root, err := openWPPluginJournalRoot(taskDir)
	if err != nil {
		return wpPluginUpdateJournalReport{}, err
	}
	defer root.Close()
	info, err := root.Lstat(wpPluginJournalName)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > wpPluginJournalMax {
		return wpPluginUpdateJournalReport{}, errors.New("invalid plugin update journal")
	}
	f, err := root.Open(wpPluginJournalName)
	if err != nil {
		return wpPluginUpdateJournalReport{}, err
	}
	defer f.Close()
	reader := bufio.NewReader(io.LimitReader(f, wpPluginJournalMax+1))
	var checkpoints []string
	expectedIndex := 0
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) != 0 {
			if line[len(line)-1] != '\n' {
				if errors.Is(readErr, io.EOF) {
					return wpPluginUpdateJournalReport{Checkpoints: checkpoints, Truncated: true}, nil
				}
				return wpPluginUpdateJournalReport{}, errors.New("invalid plugin update journal entry")
			}
			if expectedIndex >= len(wpPluginJournalCheckpoints) || line[:len(line)-1] != wpPluginJournalCheckpoints[expectedIndex] {
				return wpPluginUpdateJournalReport{}, errors.New("invalid plugin update journal entry")
			}
			checkpoints = append(checkpoints, line[:len(line)-1])
			expectedIndex++
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return wpPluginUpdateJournalReport{}, readErr
		}
	}
	return wpPluginUpdateJournalReport{Checkpoints: checkpoints}, nil
}

func openWPPluginJournalRoot(taskDir string) (*os.Root, error) {
	info, err := os.Lstat(taskDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("invalid plugin update task directory")
	}
	return os.OpenRoot(taskDir)
}

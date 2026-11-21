package executor

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

type maintenanceBudgetReader interface {
	QueryRow(string, ...any) *sql.Row
	Query(string, ...any) (*sql.Rows, error)
}

// A compound reboot reason may shrink after a failed reboot (for example its
// marker disappeared but GRUB still selected the old kernel). A different
// target string is not, by itself, authorization for another physical reboot.
func freshMaintenanceRebootIntent(db maintenanceBudgetReader, target string, evidence []maintenanceRuntimeEvidence, boot string) (bool, error) {
	parts := strings.Split(target, "/")
	var goals []string
	switch parts[0] {
	case "runtime":
		if len(parts) != 1 {
			return false, errors.New("invalid runtime reboot target")
		}
	case "kernel":
		if len(parts) != 2 && len(parts) != 4 {
			return false, errors.New("invalid kernel reboot target")
		}
		goals = append(goals, strings.Join(parts[:2], "/"))
		if len(parts) == 4 {
			if parts[2] != "marker" {
				return false, errors.New("invalid reboot marker")
			}
			goals = append(goals, strings.Join(parts[2:], "/"))
		}
	case "marker":
		if len(parts) != 2 {
			return false, errors.New("invalid marker reboot target")
		}
		goals = append(goals, target)
	default:
		return false, errors.New("invalid reboot target")
	}
	for _, goal := range goals {
		var count int
		var err error
		if strings.HasPrefix(goal, "kernel/") {
			err = db.QueryRow(`SELECT COUNT(*) FROM system_maintenance_actions WHERE kind='reboot' AND attempts>0 AND (target=? OR substr(target,1,length(?)+1)=?||'/')`, goal, goal, goal).Scan(&count)
		} else {
			suffix := "/" + goal
			err = db.QueryRow(`SELECT COUNT(*) FROM system_maintenance_actions WHERE kind='reboot' AND attempts>0 AND (target=? OR substr(target,-length(?))=?)`, goal, suffix, suffix).Scan(&count)
		}
		if err != nil {
			return false, err
		}
		if count == 0 {
			return true, nil
		}
	}
	unknown := map[string]bool{}
	for _, e := range evidence {
		unknown[e.fileKey()] = true
	}
	if len(unknown) == 0 {
		return false, nil
	}
	// Inodes from an earlier boot cannot represent surviving old processes.
	// Compare only this boot's file budgets; goal/version budgets span boots.
	rows, err := db.Query(`SELECT evidence_json,last_success_json FROM system_maintenance_actions WHERE kind='reboot' AND attempts>0 AND boot_id_before=? LIMIT 257`, boot)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if count > 256 {
			return false, errors.New("reboot budget history exceeds limit")
		}
		var failed, completed string
		if err := rows.Scan(&failed, &completed); err != nil {
			return false, err
		}
		for _, encoded := range []string{failed, completed} {
			var files []maintenanceRuntimeEvidence
			if err := json.Unmarshal([]byte(encoded), &files); err != nil {
				return false, err
			}
			for _, e := range files {
				delete(unknown, e.fileKey())
			}
		}
		if len(unknown) == 0 {
			return false, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return len(unknown) > 0, nil
}

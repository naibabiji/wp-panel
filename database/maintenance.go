package database

import (
	"database/sql"
	"encoding/json"
)

// MaintenanceWindowActive deliberately reads the private column rather than
// adding credentials or recovery state to the serializable Website model.
func MaintenanceWindowActive(db *sql.DB, siteID int) (bool, error) {
	var raw string
	if err := db.QueryRow(`SELECT maintenance_security FROM websites WHERE id=?`, siteID).Scan(&raw); err != nil {
		return false, err
	}
	var state struct {
		Window *json.RawMessage `json:"window"`
	}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return false, err
	}
	return state.Window != nil, nil
}

func ensureMaintenanceSecurityColumn() error {
	rows, err := DB.Query(`PRAGMA table_info(websites)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var def sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "maintenance_security" {
			found = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || found {
		return err
	}
	_, err = DB.Exec(`ALTER TABLE websites ADD COLUMN maintenance_security TEXT NOT NULL DEFAULT '{}'`)
	return err
}

package database

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrAIDevelopmentAccessNotFound = errors.New("AI development access not found")

type AIDevelopmentAccess struct {
	SiteID         int64      `json:"site_id"`
	Status         string     `json:"status"`
	Operation      string     `json:"operation"`
	SystemUser     string     `json:"system_user"`
	WebRoot        string     `json:"web_root"`
	OriginalShell  string     `json:"-"`
	OriginalHome   string     `json:"-"`
	PublicKey      string     `json:"-"`
	KeyFingerprint string     `json:"key_fingerprint"`
	RequestedBy    string     `json:"requested_by"`
	LastError      string     `json:"last_error"`
	EnabledAt      *time.Time `json:"enabled_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func GetAIDevelopmentAccess(ctx context.Context, db *sql.DB, siteID int64) (*AIDevelopmentAccess, error) {
	var item AIDevelopmentAccess
	var enabledAt sql.NullTime
	err := db.QueryRowContext(ctx, `SELECT site_id,status,operation,system_user,web_root,original_shell,original_home,
		public_key,key_fingerprint,requested_by,last_error,enabled_at,created_at,updated_at
		FROM website_ai_development_access WHERE site_id=?`, siteID).Scan(
		&item.SiteID, &item.Status, &item.Operation, &item.SystemUser, &item.WebRoot,
		&item.OriginalShell, &item.OriginalHome, &item.PublicKey, &item.KeyFingerprint,
		&item.RequestedBy, &item.LastError, &enabledAt, &item.CreatedAt, &item.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAIDevelopmentAccessNotFound
	}
	if err != nil {
		return nil, err
	}
	if enabledAt.Valid {
		item.EnabledAt = &enabledAt.Time
	}
	return &item, nil
}

func IsAIDevelopmentAccessBlocking(ctx context.Context, db *sql.DB, siteID int64) (bool, error) {
	// A nil handle is only expected in isolated handler unit tests. The running
	// panel completes database initialization before registering routes.
	if db == nil {
		return false, nil
	}
	var exists int
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM website_ai_development_access WHERE site_id=?)`, siteID).Scan(&exists)
	return exists == 1, err
}

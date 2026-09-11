package executor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const siteMigrationProtocolVersion = 2

var siteMigrationIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{15,95}$`)

var (
	errSiteMigrationBusy       = errors.New("site migration lock already held")
	errSiteMigrationNotClaimed = errors.New("site migration task not claimed")
)

type siteMigrationSite struct {
	ID             string
	BatchID        string
	SourceSiteID   sql.NullInt64
	TargetSiteID   sql.NullInt64
	SourceDomain   string
	TargetDomain   string
	SiteType       string
	Status         string
	Stage          string
	LeaseOwner     string
	LeaseExpiresAt sql.NullTime
	AttemptCount   int
}

type siteMigrationStore struct {
	db *sql.DB
}

func newSiteMigrationStore(db *sql.DB) (*siteMigrationStore, error) {
	if db == nil {
		return nil, errors.New("site migration database unavailable")
	}
	return &siteMigrationStore{db: db}, nil
}

func validSiteMigrationID(value string) bool {
	return siteMigrationIDPattern.MatchString(value)
}

func (s *siteMigrationStore) acquireLock(ctx context.Context, migrationSiteID string, siteID *int, domain, direction string, now time.Time) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if !validSiteMigrationID(migrationSiteID) || !IsValidDomain(domain) || (direction != "source" && direction != "target") || now.IsZero() {
		return errors.New("invalid site migration lock")
	}
	var nullableSiteID any
	if siteID != nil {
		if *siteID <= 0 {
			return errors.New("invalid site migration lock site")
		}
		if !TryAcquireSiteOpLock(*siteID, "migration_reservation") {
			return errSiteMigrationBusy
		}
		defer ReleaseSiteOpLock(*siteID)
		nullableSiteID = *siteID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO site_migration_locks
		(domain,site_id,migration_site_id,direction,status,created_at,updated_at)
		VALUES (?,?,?,?,'active',?,?)`, domain, nullableSiteID, migrationSiteID, direction, now.UTC(), now.UTC())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
			return errSiteMigrationBusy
		}
		return err
	}
	return nil
}

func (s *siteMigrationStore) releaseLock(ctx context.Context, migrationSiteID string, now time.Time) error {
	if !validSiteMigrationID(migrationSiteID) || now.IsZero() {
		return errors.New("invalid site migration lock release")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE site_migration_locks
		SET status='released',released_at=?,updated_at=?
		WHERE migration_site_id=? AND status='active'`, now.UTC(), now.UTC(), migrationSiteID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return errSiteMigrationNotClaimed
	}
	return nil
}

func (s *siteMigrationStore) isLocked(ctx context.Context, siteID int, domain string) (bool, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if siteID <= 0 && !IsValidDomain(domain) {
		return false, errors.New("invalid site migration lock lookup")
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_locks
		WHERE status='active' AND ((? > 0 AND site_id=?) OR (? <> '' AND domain=?))`, siteID, siteID, domain, domain).Scan(&count)
	return count > 0, err
}

func (s *siteMigrationStore) claimNext(ctx context.Context, owner string, now time.Time, lease time.Duration) (*siteMigrationSite, error) {
	if !validSiteMigrationID(owner) || now.IsZero() || lease <= 0 {
		return nil, errors.New("invalid site migration claim")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM site_migration_sites
		WHERE status='queued' AND (lease_expires_at IS NULL OR lease_expires_at<=?)
		ORDER BY created_at,id LIMIT 1`, now.UTC()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	expires := now.UTC().Add(lease)
	result, err := tx.ExecContext(ctx, `UPDATE site_migration_sites
		SET status='running',lease_owner=?,lease_expires_at=?,attempt_count=attempt_count+1,updated_at=?
		WHERE id=? AND status='queued'`, owner, expires, now.UTC(), id)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return nil, errSiteMigrationNotClaimed
	}
	job, err := getSiteMigrationSite(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return job, nil
}

func (s *siteMigrationStore) heartbeat(ctx context.Context, id, owner string, now time.Time, lease time.Duration) error {
	if !validSiteMigrationID(id) || !validSiteMigrationID(owner) || now.IsZero() || lease <= 0 {
		return errors.New("invalid site migration heartbeat")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE site_migration_sites SET lease_expires_at=?,updated_at=?
		WHERE id=? AND status='running' AND lease_owner=? AND lease_expires_at>?`, now.UTC().Add(lease), now.UTC(), id, owner, now.UTC())
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return errSiteMigrationNotClaimed
	}
	return nil
}

func (s *siteMigrationStore) finish(ctx context.Context, id, owner, status, errorCode string, now time.Time) error {
	if !validSiteMigrationID(id) || !validSiteMigrationID(owner) || now.IsZero() ||
		(status != "completed" && status != "failed_manual") {
		return errors.New("invalid site migration finish")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE site_migration_sites
		SET status=?,error_code=?,lease_owner='',lease_expires_at=NULL,finished_at=?,updated_at=?
		WHERE id=? AND status='running' AND lease_owner=?`, status, errorCode, now.UTC(), now.UTC(), id, owner)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return errSiteMigrationNotClaimed
	}
	return nil
}

func (s *siteMigrationStore) releaseClaim(ctx context.Context, id, owner string, processErr error, now time.Time) error {
	if !validSiteMigrationID(id) || !validSiteMigrationID(owner) || now.IsZero() {
		return errors.New("invalid site migration claim release")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status, stage, direction string
	if err := tx.QueryRowContext(ctx, `SELECT ms.status,ms.stage,mb.direction FROM site_migration_sites ms
		JOIN site_migration_batches mb ON mb.id=ms.batch_id WHERE ms.id=? AND ms.lease_owner=?`, id, owner).Scan(&status, &stage, &direction); err != nil {
		return errSiteMigrationNotClaimed
	}
	if status == "running" {
		if processErr == nil {
			return errors.New("site migration processor left task running")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE site_migration_sites SET status='failed_retryable',error_code='worker_stage_failed',updated_at=? WHERE id=? AND status='running' AND lease_owner=?`, now.UTC(), id, owner); err != nil {
			return err
		}
	} else if status == "awaiting_cutover" && stage == "awaiting_cutover" && direction == "target" && processErr != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE site_migration_sites SET status='failed_retryable',error_code='worker_stage_failed',updated_at=?
			WHERE id=? AND status='awaiting_cutover' AND stage='awaiting_cutover' AND lease_owner=?`, now.UTC(), id, owner); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE site_migration_sites SET lease_owner='',lease_expires_at=NULL,updated_at=? WHERE id=? AND lease_owner=?`, now.UTC(), id, owner)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errSiteMigrationNotClaimed
	}
	return tx.Commit()
}

func (s *siteMigrationStore) recoverExpired(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		return 0, errors.New("invalid site migration recovery time")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// A target that crashes after local health committed awaiting_cutover has
	// no remaining preparation work. Requeue it with the exact stage so the
	// normal processor can finish automatic activation. Source tasks use the
	// same status while waiting for the user's local decision and must not move.
	activationResult, err := tx.ExecContext(ctx, `UPDATE site_migration_sites
		SET status='queued',error_code='',lease_owner='',lease_expires_at=NULL,updated_at=?
		WHERE status='awaiting_cutover' AND stage='awaiting_cutover'
		AND (lease_expires_at IS NULL OR lease_expires_at<=?)
		AND EXISTS (SELECT 1 FROM site_migration_batches mb
			WHERE mb.id=site_migration_sites.batch_id AND mb.direction='target' AND mb.status='active')`, now.UTC(), now.UTC())
	if err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE site_migration_sites
		SET status='interrupted_unknown',error_code='lease_expired',
			lease_owner='',lease_expires_at=NULL,updated_at=?
		WHERE status='running' AND lease_expires_at IS NOT NULL AND lease_expires_at<=?`, now.UTC(), now.UTC())
	if err != nil {
		return 0, err
	}
	activationRows, _ := activationResult.RowsAffected()
	runningRows, _ := result.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return activationRows + runningRows, nil
}

type siteMigrationQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getSiteMigrationSite(ctx context.Context, q siteMigrationQueryer, id string) (*siteMigrationSite, error) {
	var job siteMigrationSite
	err := q.QueryRowContext(ctx, `SELECT id,batch_id,source_site_id,target_site_id,source_domain,target_domain,
		site_type,status,stage,lease_owner,lease_expires_at,attempt_count
		FROM site_migration_sites WHERE id=?`, id).Scan(
		&job.ID, &job.BatchID, &job.SourceSiteID, &job.TargetSiteID, &job.SourceDomain, &job.TargetDomain,
		&job.SiteType, &job.Status, &job.Stage, &job.LeaseOwner, &job.LeaseExpiresAt, &job.AttemptCount)
	if err != nil {
		return nil, fmt.Errorf("load site migration task: %w", err)
	}
	return &job, nil
}

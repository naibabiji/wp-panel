package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
)

type SiteMigrationBatchSitePlan struct {
	Domain        string   `json:"domain"`
	Aliases       []string `json:"aliases"`
	SiteType      string   `json:"site_type"`
	FileBytes     int64    `json:"file_bytes"`
	DatabaseBytes int64    `json:"database_bytes"`
}

type SiteMigrationBatchPlanResult struct {
	BatchID string                             `json:"batch_id"`
	Sites   []SiteMigrationBatchPlanSiteResult `json:"sites"`
}

type SiteMigrationBatchPlanSiteResult struct {
	ID            string `json:"id"`
	Domain        string `json:"domain"`
	Status        string `json:"status"`
	ErrorCode     string `json:"error_code,omitempty"`
	ReservedBytes int64  `json:"reserved_bytes"`
}

type SiteMigrationBatchPlanner struct {
	db        *sql.DB
	available func(string) (uint64, error)
	root      string
	now       func() time.Time
}

func NewSiteMigrationBatchPlanner(db *sql.DB, stagingRoot string) (*SiteMigrationBatchPlanner, error) {
	if db == nil || strings.TrimSpace(stagingRoot) == "" {
		return nil, errors.New("invalid site migration batch planner")
	}
	return &SiteMigrationBatchPlanner{db: db, available: migrationAvailableBytes, root: stagingRoot, now: time.Now}, nil
}

func (p *SiteMigrationBatchPlanner) CreateTargetBatch(ctx context.Context, peerID, requestedBy string, plans []SiteMigrationBatchSitePlan) (*SiteMigrationBatchPlanResult, error) {
	batchID, err := randomMigrationID("batch_", 12)
	if err != nil {
		return nil, err
	}
	return p.CreateTargetBatchWithID(ctx, peerID, batchID, requestedBy, plans)
}

func (p *SiteMigrationBatchPlanner) CreateTargetBatchWithID(ctx context.Context, peerID, batchID, requestedBy string, plans []SiteMigrationBatchSitePlan) (*SiteMigrationBatchPlanResult, error) {
	requestedBy = strings.TrimSpace(requestedBy)
	if !validSiteMigrationID(peerID) || !validSiteMigrationID(batchID) || !validSiteMigrationRequester(requestedBy) || len(plans) == 0 || len(plans) > 500 {
		return nil, errors.New("invalid site migration batch plan")
	}
	if replay, exists, err := p.loadTargetBatchReplay(ctx, peerID, batchID, requestedBy, plans); err != nil || exists {
		return replay, err
	}
	available, err := p.available(p.root)
	if err != nil {
		return nil, err
	}
	type prepared struct {
		plan                    SiteMigrationBatchSitePlan
		id, snapshot, errorCode string
		reserved                int64
	}
	items := make([]prepared, 0, len(plans))
	seen := make(map[string]struct{})
	for _, plan := range plans {
		plan.Domain = strings.ToLower(strings.TrimSpace(plan.Domain))
		item := prepared{plan: plan}
		item.id, err = randomMigrationID("migration_", 12)
		if err != nil {
			return nil, err
		}
		if !IsValidDomain(plan.Domain) || (plan.SiteType != "wordpress" && plan.SiteType != "php") || plan.FileBytes < 0 || plan.DatabaseBytes < 0 {
			return nil, errors.New("invalid source site migration plan")
		} else {
			all := append([]string{plan.Domain}, plan.Aliases...)
			for i := range all {
				all[i] = strings.ToLower(strings.TrimSpace(all[i]))
				if !IsValidDomain(all[i]) {
					return nil, errors.New("invalid source site migration alias")
				}
			}
			item.plan.Aliases = append([]string(nil), all[1:]...)
			localSeen := make(map[string]struct{}, len(all))
			for _, domain := range all {
				if _, duplicate := localSeen[domain]; duplicate {
					item.errorCode = "batch_domain_conflict"
					break
				}
				localSeen[domain] = struct{}{}
				if _, exists := seen[domain]; exists {
					item.errorCode = "batch_domain_conflict"
					break
				}
			}
			if item.errorCode == "" {
				for _, domain := range all {
					seen[domain] = struct{}{}
				}
				item.reserved, err = migrationReservationBytes(plan.FileBytes, plan.DatabaseBytes)
				if err != nil {
					return nil, err
				}
			}
		}
		raw, marshalErr := json.Marshal(map[string]any{"source_aliases": item.plan.Aliases})
		if marshalErr != nil {
			return nil, marshalErr
		}
		item.snapshot = string(raw)
		items = append(items, item)
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var peerStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM site_migration_peers WHERE id=?`, peerID).Scan(&peerStatus); err != nil || peerStatus != "paired" {
		return nil, errors.New("site migration peer unavailable")
	}
	// A target draft is only a reservation created before the source commits
	// its matching task. A later request from the same peer for the same domain
	// supersedes such an unclaimed draft; it is not migration history.
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, `DELETE FROM site_migration_sites WHERE id IN (
			SELECT ms.id FROM site_migration_sites ms JOIN site_migration_batches mb ON mb.id=ms.batch_id
			WHERE mb.peer_id=? AND mb.direction='target' AND ms.source_domain=? AND ms.status='draft' AND ms.stage='preflight_passed'
			AND NOT EXISTS (SELECT 1 FROM site_migration_locks ml WHERE ml.migration_site_id=ms.id AND ml.status='active')
			AND NOT EXISTS (SELECT 1 FROM site_migration_resources r WHERE r.migration_site_id=ms.id AND r.status IN ('created','published','remove_failed'))
		)`, peerID, item.plan.Domain); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM site_migration_batches WHERE direction='target' AND peer_id=? AND NOT EXISTS (SELECT 1 FROM site_migration_sites ms WHERE ms.batch_id=site_migration_batches.id)`, peerID); err != nil {
		return nil, err
	}
	var requested int64
	for i := range items {
		if items[i].errorCode != "" {
			continue
		}
		for _, domain := range append([]string{items[i].plan.Domain}, items[i].plan.Aliases...) {
			domain = strings.ToLower(strings.TrimSpace(domain))
			var conflict int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM websites WHERE lower(domain)=? OR (char(10)||lower(aliases)||char(10)) LIKE ('%'||char(10)||?||char(10)||'%')`, domain, domain).Scan(&conflict); err != nil {
				return nil, err
			}
			if conflict != 0 {
				items[i].errorCode, items[i].reserved = "target_domain_conflict", 0
				break
			}
		}
		if requested > math.MaxInt64-items[i].reserved {
			return nil, errors.New("site migration reservation overflow")
		}
		requested += items[i].reserved
	}
	var already int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(reserved_bytes),0) FROM site_migration_sites WHERE status NOT IN ('completed','abandoned','failed_manual')`).Scan(&already); err != nil {
		return nil, err
	}
	if already < 0 || uint64(already) > available || uint64(requested) > available-uint64(already) {
		return nil, errors.New("insufficient site migration reserved space")
	}
	now := p.now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_batches(id,peer_id,direction,status,requested_by,created_at,updated_at) VALUES (?,?,'target','active',?,?,?)`, batchID, peerID, requestedBy, now, now); err != nil {
		return nil, err
	}
	result := &SiteMigrationBatchPlanResult{BatchID: batchID, Sites: make([]SiteMigrationBatchPlanSiteResult, 0, len(items))}
	for _, item := range items {
		status, stage := "draft", "preflight_passed"
		if item.errorCode != "" {
			status, stage, item.reserved = "failed_manual", "draft", 0
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO site_migration_sites(id,batch_id,source_domain,target_domain,site_type,status,stage,settings_snapshot,error_code,estimated_file_bytes,estimated_database_bytes,reserved_bytes,created_at,updated_at,finished_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,CASE WHEN ?='failed_manual' THEN ? ELSE NULL END)`, item.id, batchID, item.plan.Domain, item.plan.Domain, item.plan.SiteType, status, stage, item.snapshot, item.errorCode, item.plan.FileBytes, item.plan.DatabaseBytes, item.reserved, now, now, status, now)
		if err != nil {
			return nil, err
		}
		result.Sites = append(result.Sites, SiteMigrationBatchPlanSiteResult{ID: item.id, Domain: item.plan.Domain, Status: status, ErrorCode: item.errorCode, ReservedBytes: item.reserved})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (p *SiteMigrationBatchPlanner) loadTargetBatchReplay(ctx context.Context, peerID, batchID, requestedBy string, plans []SiteMigrationBatchSitePlan) (*SiteMigrationBatchPlanResult, bool, error) {
	var storedPeer, direction, status, actor string
	err := p.db.QueryRowContext(ctx, `SELECT peer_id,direction,status,requested_by FROM site_migration_batches WHERE id=?`, batchID).Scan(&storedPeer, &direction, &status, &actor)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil || storedPeer != peerID || direction != "target" || status != "active" || actor != requestedBy {
		return nil, true, errors.New("target migration batch replay conflicts")
	}
	rows, err := p.db.QueryContext(ctx, `SELECT id,source_domain,site_type,status,error_code,reserved_bytes,estimated_file_bytes,estimated_database_bytes,settings_snapshot FROM site_migration_sites WHERE batch_id=? ORDER BY created_at,id`, batchID)
	if err != nil {
		return nil, true, err
	}
	defer rows.Close()
	result := &SiteMigrationBatchPlanResult{BatchID: batchID}
	wants := make(map[string]SiteMigrationBatchSitePlan, len(plans))
	for _, plan := range plans {
		plan.Domain = strings.ToLower(strings.TrimSpace(plan.Domain))
		wants[plan.Domain] = plan
	}
	seen := 0
	for rows.Next() {
		var item SiteMigrationBatchPlanSiteResult
		var siteType, snapshot string
		var fileBytes, databaseBytes int64
		if err := rows.Scan(&item.ID, &item.Domain, &siteType, &item.Status, &item.ErrorCode, &item.ReservedBytes, &fileBytes, &databaseBytes, &snapshot); err != nil {
			return nil, true, err
		}
		plan, ok := wants[item.Domain]
		if !ok {
			return nil, true, errors.New("target migration batch replay conflicts")
		}
		aliases := make([]string, len(plan.Aliases))
		for i := range plan.Aliases {
			aliases[i] = strings.ToLower(strings.TrimSpace(plan.Aliases[i]))
		}
		var saved struct {
			Aliases []string `json:"source_aliases"`
		}
		if json.Unmarshal([]byte(snapshot), &saved) != nil || item.Domain != plan.Domain || siteType != plan.SiteType || fileBytes != plan.FileBytes || databaseBytes != plan.DatabaseBytes || !equalMigrationAliases(saved.Aliases, aliases) {
			return nil, true, errors.New("target migration batch replay conflicts")
		}
		result.Sites = append(result.Sites, item)
		delete(wants, item.Domain)
		seen++
	}
	if err := rows.Err(); err != nil || seen != len(plans) || len(wants) != 0 {
		return nil, true, errors.New("target migration batch replay conflicts")
	}
	return result, true, nil
}

func equalMigrationAliases(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func validSiteMigrationRequester(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// QueueTargetBatch makes only the preflight-passed children claimable. It is
// intentionally not exposed by an HTTP handler until the G4 worker processor
// is wired to the single-site migration primitives.
func (p *SiteMigrationBatchPlanner) QueueTargetBatch(ctx context.Context, batchID string) error {
	return p.queueTargetBatch(ctx, "", batchID)
}

func (p *SiteMigrationBatchPlanner) QueueTargetBatchForPeer(ctx context.Context, peerID, batchID string) error {
	if !validSiteMigrationID(peerID) {
		return errors.New("invalid site migration peer")
	}
	return p.queueTargetBatch(ctx, peerID, batchID)
}

func (p *SiteMigrationBatchPlanner) QueueTargetSiteForPeer(ctx context.Context, peerID, batchID, taskID string) error {
	if !validSiteMigrationID(peerID) || !validSiteMigrationID(batchID) || !validSiteMigrationID(taskID) {
		return errors.New("invalid site migration target queue")
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status, stage string
	if err := tx.QueryRowContext(ctx, `SELECT ms.status,ms.stage FROM site_migration_sites ms
		JOIN site_migration_batches mb ON mb.id=ms.batch_id AND mb.peer_id=? AND mb.direction='target' AND mb.status='active'
		WHERE ms.id=? AND ms.batch_id=?`, peerID, taskID, batchID).Scan(&status, &stage); err != nil {
		return errors.New("site migration target task unavailable")
	}
	if status == "draft" && stage == "preflight_passed" {
		result, err := tx.ExecContext(ctx, `UPDATE site_migration_sites SET status='queued',updated_at=?
			WHERE id=? AND batch_id=? AND status='draft' AND stage='preflight_passed'`, p.now().UTC(), taskID, batchID)
		if err != nil {
			return err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return errors.New("site migration target queue state changed")
		}
	} else if status == "failed_manual" {
		return errors.New("site migration target task is not ready")
	}
	return tx.Commit()
}

func (p *SiteMigrationBatchPlanner) queueTargetBatch(ctx context.Context, peerID, batchID string) error {
	if !validSiteMigrationID(batchID) {
		return errors.New("invalid site migration batch")
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var direction, status string
	var storedPeer string
	if err := tx.QueryRowContext(ctx, `SELECT peer_id,direction,status FROM site_migration_batches WHERE id=?`, batchID).Scan(&storedPeer, &direction, &status); err != nil || direction != "target" || status != "active" || peerID != "" && storedPeer != peerID {
		return errors.New("site migration batch unavailable")
	}
	result, err := tx.ExecContext(ctx, `UPDATE site_migration_sites SET status='queued',updated_at=?
		WHERE batch_id=? AND status='draft' AND stage='preflight_passed'`, p.now().UTC(), batchID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		var previouslyQueued int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_sites WHERE batch_id=? AND status!='failed_manual'`, batchID).Scan(&previouslyQueued); err != nil || previouslyQueued == 0 {
			return errors.New("site migration batch has no ready sites")
		}
	}
	return tx.Commit()
}

func migrationReservationBytes(fileBytes, databaseBytes int64) (int64, error) {
	if fileBytes < 0 || databaseBytes < 0 || fileBytes > math.MaxInt64-databaseBytes {
		return 0, errors.New("invalid site migration size estimate")
	}
	total := fileBytes + databaseBytes
	if total > (math.MaxInt64-9)/22 {
		return 0, errors.New("site migration reservation overflow")
	}
	base := (total*22 + 9) / 10
	shard := fileBytes
	if shard > SiteMigrationFileShardBytes {
		shard = SiteMigrationFileShardBytes
	}
	archive := int64(0)
	if shard > 0 {
		if shard > (math.MaxInt64-siteMigrationShardMaxExtra)/101*100 {
			return 0, errors.New("site migration reservation overflow")
		}
		archive = (shard*101+99)/100 + siteMigrationShardMaxExtra
	}
	if base > math.MaxInt64-archive {
		return 0, errors.New("site migration reservation overflow")
	}
	return base + archive, nil
}

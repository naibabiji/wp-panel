package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/naibabiji/wp-panel/config"
)

// SiteMigrationControlService exposes the short, user-triggered operations
// that sit above the durable G2/G3 primitives. Long-running migration stages
// remain owned by the leased worker.
type SiteMigrationControlService struct {
	db               *sql.DB
	pairing          *SiteMigrationPairingService
	workflow         *SiteMigrationWorkflowService
	cutover          *SiteMigrationCutoverService
	rollback         *SiteMigrationTargetRollbackService
	freezer          *siteMigrationFreezer
	client           *http.Client
	now              func() time.Time
	resumeActivation func(context.Context, string) error
	stagingRoot      string
}

func NewSiteMigrationControlService(db *sql.DB, cfg *config.Config, pairing *SiteMigrationPairingService, workflow *SiteMigrationWorkflowService, stagingRoot string) (*SiteMigrationControlService, error) {
	if db == nil || cfg == nil || pairing == nil || workflow == nil {
		return nil, errors.New("site migration control unavailable")
	}
	cutover, err := NewSiteMigrationCutoverService(db)
	if err != nil {
		return nil, err
	}
	rollback, err := NewSiteMigrationTargetRollbackService(db, cfg, stagingRoot)
	if err != nil {
		return nil, err
	}
	freezer, err := newSiteMigrationFreezer(db, cfg, productionSiteMigrationNginxRunner{})
	if err != nil {
		freezer = nil
	}
	service := &SiteMigrationControlService{db: db, pairing: pairing, workflow: workflow, cutover: cutover, rollback: rollback, freezer: freezer,
		client: &http.Client{Timeout: 15 * time.Second}, now: time.Now, resumeActivation: cutover.ResumeActivation, stagingRoot: stagingRoot}
	workflow.prepareStart = service.prepareStart
	return service, nil
}

func (s *SiteMigrationControlService) taskScope(ctx context.Context, taskID string) (peerID, direction, domain string, err error) {
	if !validSiteMigrationID(taskID) {
		return "", "", "", errors.New("invalid site migration task")
	}
	err = s.db.QueryRowContext(ctx, `SELECT mb.peer_id,mb.direction,ms.target_domain FROM site_migration_sites ms
		JOIN site_migration_batches mb ON mb.id=ms.batch_id AND mb.status='active'
		WHERE ms.id=?`, taskID).Scan(&peerID, &direction, &domain)
	if err != nil || !validSiteMigrationID(peerID) || (direction != "source" && direction != "target") || !IsValidDomain(domain) {
		return "", "", "", errors.New("site migration task unavailable")
	}
	return peerID, direction, domain, nil
}

func (s *SiteMigrationControlService) AuthorizeMachineTask(ctx context.Context, peerID, taskID, direction string) error {
	var count int
	if !validSiteMigrationID(peerID) || !validSiteMigrationID(taskID) || (direction != "source" && direction != "target") {
		return ErrSiteMigrationPairRejected
	}
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_migration_sites ms JOIN site_migration_batches mb ON mb.id=ms.batch_id
		WHERE ms.id=? AND mb.peer_id=? AND mb.direction=? AND mb.status='active'`, taskID, peerID, direction).Scan(&count)
	if err != nil || count != 1 {
		return ErrSiteMigrationPairRejected
	}
	return nil
}

func (s *SiteMigrationControlService) TargetStatuses(ctx context.Context, peerID string, taskIDs []string) ([]SiteMigrationRemoteTaskStatus, error) {
	if !validSiteMigrationID(peerID) || len(taskIDs) == 0 || len(taskIDs) > 500 {
		return nil, ErrSiteMigrationPairRejected
	}
	seen := map[string]struct{}{}
	for _, taskID := range taskIDs {
		if !validSiteMigrationID(taskID) {
			return nil, ErrSiteMigrationPairRejected
		}
		if _, exists := seen[taskID]; exists {
			return nil, ErrSiteMigrationPairRejected
		}
		seen[taskID] = struct{}{}
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(taskIDs)), ",")
	args := make([]any, 0, len(taskIDs)+1)
	args = append(args, peerID)
	for _, taskID := range taskIDs {
		args = append(args, taskID)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ms.id,ms.status,ms.stage,ms.error_code,ms.updated_at FROM site_migration_sites ms
		JOIN site_migration_batches mb ON mb.id=ms.batch_id AND mb.peer_id=? AND mb.direction='target' AND mb.status='active'
		WHERE ms.id IN (`+placeholders+`) ORDER BY ms.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]SiteMigrationRemoteTaskStatus, 0, len(taskIDs))
	for rows.Next() {
		var item SiteMigrationRemoteTaskStatus
		if err := rows.Scan(&item.ID, &item.Status, &item.Stage, &item.ErrorCode, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if _, ok := seen[item.ID]; ok {
			item.TransferReceivedBytes, item.TransferExtracting = siteMigrationTransferTelemetry(s.stagingRoot, item.ID, item.Status, item.Stage)
			result = append(result, item)
		}
	}
	if err := rows.Err(); err != nil || len(result) != len(seen) {
		return nil, ErrSiteMigrationPairRejected
	}
	return result, nil
}

func (s *SiteMigrationControlService) ListTasks(ctx context.Context) ([]SiteMigrationTaskSummary, error) {
	tasks, err := s.pairing.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	byPeer := map[string][]int{}
	for i := range tasks {
		if tasks[i].Direction == "target" {
			tasks[i].TransferReceivedBytes, tasks[i].TransferExtracting = siteMigrationTransferTelemetry(s.stagingRoot, tasks[i].ID, tasks[i].Status, tasks[i].Stage)
		}
		if tasks[i].Direction == "source" {
			byPeer[tasks[i].PeerID] = append(byPeer[tasks[i].PeerID], i)
		}
	}
	for peerID, indexes := range byPeer {
		ids := make([]string, 0, len(indexes))
		for _, index := range indexes {
			ids = append(ids, tasks[index].ID)
		}
		remote, err := s.pairing.RemoteTargetStatuses(ctx, peerID, ids)
		if err != nil {
			for _, index := range indexes {
				tasks[index].RemoteStateUnavailable = true
			}
			continue
		}
		remoteByID := make(map[string]SiteMigrationRemoteTaskStatus, len(remote))
		for _, item := range remote {
			remoteByID[item.ID] = item
		}
		for _, index := range indexes {
			if item, ok := remoteByID[tasks[index].ID]; ok {
				mergeSiteMigrationRemoteTargetStatus(&tasks[index], item)
			}
		}
	}
	return tasks, nil
}

func mergeSiteMigrationRemoteTargetStatus(task *SiteMigrationTaskSummary, remote SiteMigrationRemoteTaskStatus) {
	if task == nil || task.Status == "abandoned" {
		return
	}
	if remote.Status == "abandoned" {
		task.ErrorCode = "source_restore_pending"
		return
	}
	// Source preparation and target queueing failures belong to this panel.
	// The target becomes authoritative only after the source is fully prepared.
	if task.Status != "awaiting_cutover" {
		return
	}
	if remote.Status == "completed" && remote.Stage == "completed" {
		task.SourceDecisionPending = true
	}
	task.Status = remote.Status
	task.Stage = remote.Stage
	task.ErrorCode = remote.ErrorCode
	task.TransferReceivedBytes = remote.TransferReceivedBytes
	task.TransferExtracting = remote.TransferExtracting
	task.UpdatedAt = remote.UpdatedAt
}

func siteMigrationTransferTelemetry(stagingRoot, taskID, status, stage string) (int64, bool) {
	if strings.TrimSpace(stagingRoot) == "" || !validSiteMigrationID(taskID) || (status != "running" && status != "awaiting_cutover") || stage != "transferring_files" {
		return 0, false
	}
	entries, err := os.ReadDir(filepath.Join(stagingRoot, taskID, "shards"))
	if err != nil {
		return 0, false
	}
	var received int64
	extracting := false
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "extract-") && entry.IsDir() {
			extracting = true
			continue
		}
		if !strings.HasPrefix(name, "shard-") || !strings.HasSuffix(name, ".tar.zst.partial") || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			received += info.Size()
		}
	}
	return received, extracting
}

func (s *SiteMigrationControlService) Retry(ctx context.Context, taskID string) error {
	peerID, direction, _, err := s.taskScope(ctx, taskID)
	if err != nil {
		return err
	}
	if direction == "source" {
		var localStatus string
		if err := s.db.QueryRowContext(ctx, `SELECT status FROM site_migration_sites WHERE id=?`, taskID).Scan(&localStatus); err != nil {
			return errors.New("site migration retry unavailable")
		}
		if localStatus == "failed_retryable" {
			return s.workflow.Retry(ctx, taskID)
		}
		if localStatus != "awaiting_cutover" {
			return errors.New("site migration retry unavailable")
		}
		remote, err := s.pairing.RemoteTargetStatuses(ctx, peerID, []string{taskID})
		if err == nil && len(remote) == 1 && remote[0].Status == "failed_retryable" {
			return s.pairing.RemoteTargetRetry(ctx, peerID, taskID)
		}
		return errors.New("site migration retry unavailable")
	}
	return s.RetryTarget(ctx, taskID)
}

func (s *SiteMigrationControlService) RetryTarget(ctx context.Context, taskID string) error {
	var status, stage string
	if err := s.db.QueryRowContext(ctx, `SELECT status,stage FROM site_migration_sites WHERE id=?`, taskID).Scan(&status, &stage); err != nil {
		return errors.New("site migration retry unavailable")
	}
	if status == "failed_retryable" && (stage == "activating_target" || stage == "activation_runtime_sync") {
		if s.resumeActivation == nil {
			return errors.New("site migration activation retry unavailable")
		}
		return s.resumeActivation(ctx, taskID)
	}
	return s.workflow.Retry(ctx, taskID)
}

func (s *SiteMigrationControlService) ProbeSource(ctx context.Context, peerID, taskID string) (SiteMigrationProbeAttestation, error) {
	var domain, snapshotRaw, markerKey string
	err := s.db.QueryRowContext(ctx, `SELECT ms.source_domain,ms.settings_snapshot,p.inbound_credential_hash
		FROM site_migration_sites ms JOIN site_migration_batches mb ON mb.id=ms.batch_id AND mb.peer_id=? AND mb.direction='source' AND mb.status='active'
		JOIN site_migration_peers p ON p.id=mb.peer_id AND p.status='paired'
		JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.direction='source' AND ml.status='active'
		WHERE ms.id=? AND ms.status='awaiting_cutover'`, peerID, taskID).Scan(&domain, &snapshotRaw, &markerKey)
	if err != nil || !IsValidDomain(domain) || len(markerKey) != 64 {
		return SiteMigrationProbeAttestation{}, ErrSiteMigrationPairRejected
	}
	var snapshot struct {
		MarkerToken string `json:"source_marker_token"`
	}
	if json.Unmarshal([]byte(snapshotRaw), &snapshot) != nil || !siteMigrationMarkerPattern.MatchString(snapshot.MarkerToken) {
		return SiteMigrationProbeAttestation{}, errors.New("source migration marker unavailable")
	}
	observedAt := s.now().UTC()
	probeURL := "https://" + domain + "/.well-known/wp-panel-migration/" + snapshot.MarkerToken + "?probe=" + NewAPIKey()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	req.Header.Set("Cache-Control", "no-cache, no-store")
	resp, err := s.client.Do(req)
	if err != nil {
		return NewSiteMigrationProbeFailureAttestation(taskID, markerKey, observedAt)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if readErr != nil || resp.StatusCode != http.StatusOK || !strings.Contains(strings.ToLower(resp.Header.Get("Cache-Control")), "no-store") {
		return NewSiteMigrationProbeFailureAttestation(taskID, markerKey, observedAt)
	}
	var marker SiteMigrationMarkerResponse
	if json.Unmarshal(body, &marker) != nil || VerifySiteMigrationTargetMarker(taskID, markerKey, snapshot.MarkerToken, marker) != nil {
		return NewSiteMigrationProbeFailureAttestation(taskID, markerKey, observedAt)
	}
	return NewSiteMigrationProbeAttestation(taskID, markerKey, snapshot.MarkerToken, observedAt, marker)
}

func (s *SiteMigrationControlService) Probe(ctx context.Context, taskID string) error {
	peerID, direction, _, err := s.taskScope(ctx, taskID)
	if err != nil {
		return err
	}
	if direction == "source" {
		attestation, err := s.ProbeSource(ctx, peerID, taskID)
		if err != nil {
			return err
		}
		return s.pairing.RemoteTargetProbe(ctx, peerID, taskID, attestation)
	}
	attestation, err := s.pairing.RemoteSourceProbe(ctx, peerID, taskID)
	if err != nil {
		return err
	}
	if err := s.cutover.AcceptSourceProbe(ctx, taskID, attestation); err != nil {
		return err
	}
	return s.cutover.ProbePublic(ctx, taskID, "target")
}

func (s *SiteMigrationControlService) DNS(ctx context.Context, taskID string) ([]string, error) {
	_, _, domain, err := s.taskScope(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return ResolveSiteMigrationDNS(ctx, domain)
}

func (s *SiteMigrationControlService) AcceptProbe(ctx context.Context, taskID string, attestation SiteMigrationProbeAttestation) error {
	if err := s.cutover.AcceptSourceProbe(ctx, taskID, attestation); err != nil {
		return err
	}
	return s.cutover.ProbePublic(ctx, taskID, "target")
}

func (s *SiteMigrationControlService) Confirm(ctx context.Context, taskID, typedDomain, operator, reason string, force bool) error {
	if !validSiteMigrationRequester(operator) {
		return errors.New("invalid cutover operator")
	}
	peerID, direction, _, err := s.taskScope(ctx, taskID)
	if err != nil {
		return err
	}
	if direction == "source" {
		return s.pairing.RemoteTargetConfirm(ctx, peerID, taskID, typedDomain, operator, reason, force)
	}
	return s.cutover.Confirm(ctx, taskID, strings.ToLower(strings.TrimSpace(typedDomain)), operator, reason, force)
}

func (s *SiteMigrationControlService) Abandon(ctx context.Context, taskID, typedDomain, operator, reason string, allowActivated bool) error {
	if !validSiteMigrationRequester(operator) {
		return errors.New("invalid rollback operator")
	}
	peerID, direction, _, err := s.taskScope(ctx, taskID)
	if err != nil {
		return err
	}
	typedDomain = strings.ToLower(strings.TrimSpace(typedDomain))
	if direction == "source" {
		if s.freezer == nil {
			return errors.New("source migration restore unavailable")
		}
		if err := s.pairing.RemoteTargetAbandon(ctx, peerID, taskID, typedDomain, operator, reason, allowActivated); err != nil {
			return err
		}
		return s.freezer.AbandonSource(ctx, taskID)
	}
	if err := s.rollback.Abandon(ctx, taskID, typedDomain, operator, reason, allowActivated); err != nil {
		return err
	}
	if err := s.pairing.RemoteSourceAbandon(ctx, peerID, taskID); err != nil {
		_, _ = s.db.ExecContext(context.Background(), `UPDATE site_migration_sites SET error_code='source_restore_pending',updated_at=? WHERE id=? AND status='abandoned'`, s.now().UTC(), taskID)
		return err
	}
	_, _ = s.db.ExecContext(context.Background(), `UPDATE site_migration_sites SET error_code='',updated_at=? WHERE id=? AND status='abandoned'`, s.now().UTC(), taskID)
	return nil
}

func (s *SiteMigrationControlService) AbandonTarget(ctx context.Context, taskID, domain, operator, reason string, allowActivated bool) error {
	if !validSiteMigrationRequester(operator) {
		return errors.New("invalid rollback operator")
	}
	return s.rollback.Abandon(ctx, taskID, strings.ToLower(strings.TrimSpace(domain)), operator, reason, allowActivated)
}

func (s *SiteMigrationControlService) AbandonSource(ctx context.Context, taskID string) error {
	if s.freezer == nil {
		return errors.New("source migration restore unavailable")
	}
	return s.freezer.AbandonSource(ctx, taskID)
}

func (s *SiteMigrationControlService) CompleteSource(ctx context.Context, taskID string) error {
	peerID, direction, _, err := s.taskScope(ctx, taskID)
	if err != nil || direction != "source" || s.freezer == nil {
		return errors.New("source migration completion unavailable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.freezer.completeSourceTx(ctx, tx, taskID); err != nil {
		return err
	}
	if err := releaseSiteMigrationTaskReferencesTx(ctx, tx, taskID); err != nil {
		return err
	}
	if err := forgetSiteMigrationTaskTx(ctx, tx, taskID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if s.pairing != nil {
		_ = s.pairing.RemoteDeleteTargetTask(context.Background(), peerID, taskID)
	}
	return nil
}

func (s *SiteMigrationControlService) RestoreSource(ctx context.Context, taskID string) error {
	_, direction, _, err := s.taskScope(ctx, taskID)
	if err != nil || direction != "source" || s.freezer == nil {
		return errors.New("source migration restore unavailable")
	}
	return s.deleteSourceTask(ctx, taskID, true)
}

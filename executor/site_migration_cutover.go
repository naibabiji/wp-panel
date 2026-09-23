package executor

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	siteMigrationCutoverProbeCount  = 3
	siteMigrationCutoverProbeSpan   = 2 * time.Minute
	siteMigrationCutoverProbeMaxAge = 10 * time.Minute
)

type SiteMigrationMarkerResponse struct {
	Task      string `json:"task"`
	Role      string `json:"role"`
	IssuedAt  int64  `json:"issued_at"`
	Signature string `json:"signature"`
}

type SiteMigrationProbeAttestation struct {
	Task            string `json:"task"`
	Observer        string `json:"observer"`
	ObservedAt      int64  `json:"observed_at"`
	MarkerSignature string `json:"marker_signature"`
	Success         bool   `json:"success"`
	Signature       string `json:"signature"`
}

type siteMigrationCutoverOps interface {
	ApplyMarker(string, string, string) error
	RemoveMarker(string, string) error
	RestoreWordPressCron(string, string, string) error
	ReloadCron() error
}

type productionSiteMigrationCutoverOps struct{}

func (productionSiteMigrationCutoverOps) ApplyMarker(nginxPath, enabledPath, block string) error {
	content, err := os.ReadFile(nginxPath)
	if err != nil {
		return err
	}
	updated, err := injectSiteMigrationTargetMarker(string(content), block)
	if err != nil {
		return err
	}
	return applyMigrationNginxContent(nginxPath, enabledPath, updated)
}

func (productionSiteMigrationCutoverOps) RemoveMarker(nginxPath, enabledPath string) error {
	content, err := os.ReadFile(nginxPath)
	if err != nil {
		return err
	}
	updated, err := removeSiteMigrationTargetMarker(string(content))
	if err != nil {
		return err
	}
	return applyMigrationNginxContent(nginxPath, enabledPath, updated)
}

func (productionSiteMigrationCutoverOps) RestoreWordPressCron(stagingRoot, webRoot, systemUser string) error {
	source, err := os.ReadFile(filepath.Join(stagingRoot, "files", "wp-config.php"))
	if err != nil {
		return err
	}
	target, err := readWPConfigSecure(webRoot)
	if err != nil {
		return err
	}
	re := regexp.MustCompile(`(?m)^define\('DISABLE_WP_CRON', true\); // WP Panel migration freeze\s*$`)
	sourceDefine := regexp.MustCompile(`(?m)define\(\s*['"]DISABLE_WP_CRON['"]\s*,\s*(?:true|false)\s*\)\s*;?`).Find(source)
	replacement := ""
	if len(sourceDefine) > 0 {
		replacement = string(sourceDefine)
	}
	updated := re.ReplaceAllString(string(target), replacement)
	if strings.Contains(updated, "WP Panel migration freeze") {
		return errors.New("migration WP-Cron freeze marker remained")
	}
	return writeWPConfigSecure(webRoot, []byte(updated))
}

func (productionSiteMigrationCutoverOps) ReloadCron() error {
	result := renderCronConfig()
	if !result.Success {
		return errors.New(result.Message)
	}
	return nil
}

type SiteMigrationCutoverService struct {
	db     *sql.DB
	ops    siteMigrationCutoverOps
	now    func() time.Time
	client *http.Client
}

func NewSiteMigrationCutoverService(db *sql.DB) (*SiteMigrationCutoverService, error) {
	if db == nil {
		return nil, errors.New("site migration cutover service unavailable")
	}
	return &SiteMigrationCutoverService{db: db, ops: productionSiteMigrationCutoverOps{}, now: time.Now, client: &http.Client{Timeout: 15 * time.Second}}, nil
}

func (s *SiteMigrationCutoverService) InstallTargetMarker(ctx context.Context, migrationSiteID string) error {
	state, err := s.loadCutoverState(ctx, migrationSiteID, "awaiting_cutover")
	if err != nil {
		return err
	}
	issued := s.now().UTC().Unix()
	marker := SiteMigrationMarkerResponse{Task: migrationSiteID, Role: "target", IssuedAt: issued}
	marker.Signature = signSiteMigrationMarker(state.MarkerKey, state.MarkerToken, marker)
	marker, err = s.recordMarkerIntent(ctx, migrationSiteID, state.NginxPath, marker)
	if err != nil {
		return err
	}
	if marker.Task != migrationSiteID || marker.Role != "target" || !verifySiteMigrationMarker(state.MarkerKey, state.MarkerToken, marker) {
		return errors.New("target marker intent invalid")
	}
	payload, _ := json.Marshal(marker)
	block := renderSiteMigrationTargetMarkerBlock(state.MarkerToken, string(payload))
	if err := s.ops.ApplyMarker(state.NginxPath, state.EnabledPath, block); err != nil {
		return s.cutoverFailed(migrationSiteID, "target_marker_publish_failed", err)
	}
	return nil
}

func (s *SiteMigrationCutoverService) ProbePublic(ctx context.Context, migrationSiteID, observer string) error {
	if observer != "target" {
		return errors.New("invalid cutover observer")
	}
	state, err := s.loadCutoverState(ctx, migrationSiteID, "awaiting_cutover")
	if err != nil {
		return err
	}
	err = s.probePublicMarker(ctx, migrationSiteID, state)
	if err != nil {
		_ = s.recordProbeResult(ctx, migrationSiteID, observer, false)
		return err
	}
	return s.recordProbeResult(ctx, migrationSiteID, observer, true)
}

func (s *SiteMigrationCutoverService) probePublicMarker(ctx context.Context, migrationSiteID string, state siteMigrationCutoverState) error {
	probeURL := "https://" + state.Domain + "/.well-known/wp-panel-migration/" + state.MarkerToken + "?probe=" + NewAPIKey()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	req.Header.Set("Cache-Control", "no-cache, no-store")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("public cutover probe failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(strings.ToLower(resp.Header.Get("Cache-Control")), "no-store") {
		return errors.New("public cutover marker unavailable")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return err
	}
	var marker SiteMigrationMarkerResponse
	if json.Unmarshal(body, &marker) != nil || marker.Task != migrationSiteID || marker.Role != "target" || !verifySiteMigrationMarker(state.MarkerKey, state.MarkerToken, marker) {
		return errors.New("public cutover marker invalid")
	}
	return nil
}

func (s *SiteMigrationCutoverService) AcceptSourceProbe(ctx context.Context, migrationSiteID string, attestation SiteMigrationProbeAttestation) error {
	state, err := s.loadCutoverState(ctx, migrationSiteID, "awaiting_cutover")
	if err != nil {
		return err
	}
	now := s.now().UTC()
	if attestation.Task != migrationSiteID || attestation.Observer != "source" || attestation.ObservedAt < now.Add(-time.Minute).Unix() || attestation.ObservedAt > now.Add(time.Minute).Unix() || !verifySiteMigrationProbeAttestation(state.MarkerKey, attestation) {
		return errors.New("source cutover probe attestation invalid")
	}
	return s.recordProbeResult(ctx, migrationSiteID, "source", attestation.Success)
}

func NewSiteMigrationProbeAttestation(taskID, markerKey, token string, observedAt time.Time, marker SiteMigrationMarkerResponse) (SiteMigrationProbeAttestation, error) {
	if observedAt.IsZero() || VerifySiteMigrationTargetMarker(taskID, markerKey, token, marker) != nil {
		return SiteMigrationProbeAttestation{}, errors.New("invalid source probe attestation")
	}
	attestation := SiteMigrationProbeAttestation{Task: taskID, Observer: "source", ObservedAt: observedAt.UTC().Unix(), MarkerSignature: marker.Signature, Success: true}
	attestation.Signature = signSiteMigrationProbeAttestation(markerKey, attestation)
	return attestation, nil
}

// VerifySiteMigrationTargetMarker lets the source observer authenticate a
// marker fetched through the public domain before it signs an attestation.
// token is required in production; an empty token is rejected.
func VerifySiteMigrationTargetMarker(taskID, markerKey, token string, marker SiteMigrationMarkerResponse) error {
	if !validSiteMigrationID(taskID) || markerKey == "" || !siteMigrationMarkerPattern.MatchString(token) || marker.Task != taskID || marker.Role != "target" || !verifySiteMigrationMarker(markerKey, token, marker) {
		return errors.New("public cutover marker invalid")
	}
	return nil
}

func NewSiteMigrationProbeFailureAttestation(taskID, markerKey string, observedAt time.Time) (SiteMigrationProbeAttestation, error) {
	if !validSiteMigrationID(taskID) || markerKey == "" || observedAt.IsZero() {
		return SiteMigrationProbeAttestation{}, errors.New("invalid source probe failure attestation")
	}
	attestation := SiteMigrationProbeAttestation{Task: taskID, Observer: "source", ObservedAt: observedAt.UTC().Unix(), Success: false}
	attestation.Signature = signSiteMigrationProbeAttestation(markerKey, attestation)
	return attestation, nil
}

func (s *SiteMigrationCutoverService) Confirm(ctx context.Context, migrationSiteID, typedDomain, operator, reason string, force bool) error {
	state, err := s.loadCutoverState(ctx, migrationSiteID, "awaiting_cutover")
	if err != nil {
		return err
	}
	if typedDomain != state.Domain {
		return errors.New("cutover domain confirmation mismatch")
	}
	if strings.TrimSpace(operator) == "" {
		return errors.New("cutover operator required")
	}
	if force {
		if strings.TrimSpace(reason) == "" {
			return errors.New("forced cutover reason required")
		}
	} else if err := s.requireProbeWindow(ctx, migrationSiteID); err != nil {
		return err
	}
	decision := "verified"
	if force {
		decision = "forced"
	}
	return s.beginActivation(ctx, migrationSiteID, decision, operator, reason)
}

// ActivateAfterHealth makes a fully published and locally healthy target a
// normal website without waiting for DNS or public marker observations.
func (s *SiteMigrationCutoverService) ActivateAfterHealth(ctx context.Context, migrationSiteID string) error {
	if _, err := s.loadCutoverState(ctx, migrationSiteID, "awaiting_cutover"); err != nil {
		return err
	}
	return s.beginActivation(ctx, migrationSiteID, "automatic", "system", "")
}

func (s *SiteMigrationCutoverService) beginActivation(ctx context.Context, migrationSiteID, decision, operator, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE site_migration_sites SET status='running',stage='activating_target',updated_at=? WHERE id=? AND status='awaiting_cutover' AND stage='awaiting_cutover'`, s.now().UTC(), migrationSiteID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("cutover activation state changed")
	}
	decisionPayload, _ := json.Marshal(struct {
		Decision string `json:"decision"`
		Operator string `json:"operator"`
		Reason   string `json:"reason,omitempty"`
	}{Decision: decision, Operator: strings.TrimSpace(operator), Reason: strings.TrimSpace(reason)})
	if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_events (migration_site_id,stage,result,message,created_at) VALUES (?,'cutover_decision','info',?,?)`, migrationSiteID, string(decisionPayload), s.now().UTC()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.ResumeActivation(ctx, migrationSiteID)
}

// ResumeActivation replays only idempotent activation steps after a crash or
// retryable service failure. It never repeats the user's cutover decision.
func (s *SiteMigrationCutoverService) ResumeActivation(ctx context.Context, migrationSiteID string) error {
	var stage string
	if err := s.db.QueryRowContext(ctx, `SELECT stage FROM site_migration_sites WHERE id=?`, migrationSiteID).Scan(&stage); err != nil {
		return errors.New("cutover activation unavailable")
	}
	if stage == "activation_runtime_sync" {
		return s.resumeActivationRuntime(ctx, migrationSiteID)
	}
	if stage != "activating_target" {
		return errors.New("cutover activation unavailable")
	}
	state, err := s.loadCutoverState(ctx, migrationSiteID, "activating_target")
	if err != nil {
		return err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE site_migration_sites SET status='running',error_code='',updated_at=? WHERE id=? AND stage='activating_target' AND status='failed_retryable'`, s.now().UTC(), migrationSiteID)
	if state.SiteType == "wordpress" {
		if err := s.ops.RestoreWordPressCron(state.StagingRoot, state.WebRoot, state.SystemUser); err != nil {
			return s.activationFailed(migrationSiteID, "wp_cron_restore_failed", err)
		}
	}
	if err := s.ops.RemoveMarker(state.NginxPath, state.EnabledPath); err != nil {
		return s.activationFailed(migrationSiteID, "target_marker_remove_failed", err)
	}
	var decisionPayload string
	var decision struct {
		Decision string `json:"decision"`
		Operator string `json:"operator"`
		Reason   string `json:"reason"`
	}
	if err := s.db.QueryRowContext(ctx, `SELECT message FROM site_migration_events WHERE migration_site_id=? AND stage='cutover_decision' ORDER BY id DESC LIMIT 1`, migrationSiteID).Scan(&decisionPayload); err != nil || json.Unmarshal([]byte(decisionPayload), &decision) != nil || strings.TrimSpace(decision.Operator) == "" || (decision.Decision != "verified" && decision.Decision != "forced" && decision.Decision != "automatic") {
		return s.activationFailed(migrationSiteID, "cutover_decision_missing", errors.New("cutover decision unavailable"))
	}
	if err := s.commitActivation(ctx, migrationSiteID, state.SiteID); err != nil {
		return s.activationFailed(migrationSiteID, "activation_commit_failed", err)
	}
	return s.resumeActivationRuntime(ctx, migrationSiteID)
}

type siteMigrationCutoverState struct {
	Domain, SiteType, SystemUser, WebRoot string
	NginxPath, EnabledPath, StagingRoot   string
	MarkerToken, MarkerKey                string
	SiteID                                int64
}

func (s *SiteMigrationCutoverService) loadCutoverState(ctx context.Context, migrationSiteID, stage string) (siteMigrationCutoverState, error) {
	if !validSiteMigrationID(migrationSiteID) {
		return siteMigrationCutoverState{}, errors.New("invalid cutover task")
	}
	var state siteMigrationCutoverState
	var snapshotRaw, outbound string
	err := s.db.QueryRowContext(ctx, `SELECT ms.target_domain,ms.site_type,ms.target_site_id,ms.settings_snapshot,w.system_user,w.web_root,w.nginx_conf_path,p.outbound_credential
		FROM site_migration_sites ms JOIN site_migration_batches mb ON mb.id=ms.batch_id AND mb.direction='target' AND mb.status='active'
		JOIN site_migration_peers p ON p.id=mb.peer_id AND p.status='paired'
		JOIN websites w ON w.id=ms.target_site_id
		JOIN site_migration_locks ml ON ml.migration_site_id=ms.id AND ml.site_id=w.id AND ml.direction='target' AND ml.status='active'
		WHERE ms.id=? AND ms.stage=?`, migrationSiteID, stage).Scan(&state.Domain, &state.SiteType, &state.SiteID, &snapshotRaw, &state.SystemUser, &state.WebRoot, &state.NginxPath, &outbound)
	if err != nil || !IsValidDomain(state.Domain) || len(outbound) < 40 {
		return siteMigrationCutoverState{}, errors.New("cutover scope unavailable")
	}
	var snapshot struct {
		RuntimeSettings SiteMigrationRuntimeSettings `json:"runtime_settings"`
		TargetSpec      siteMigrationPublishSpec     `json:"target_spec"`
	}
	if json.Unmarshal([]byte(snapshotRaw), &snapshot) != nil || !siteMigrationMarkerPattern.MatchString(snapshot.RuntimeSettings.MarkerToken) {
		return siteMigrationCutoverState{}, errors.New("cutover marker metadata unavailable")
	}
	state.MarkerToken = snapshot.RuntimeSettings.MarkerToken
	state.EnabledPath = snapshot.TargetSpec.NginxEnabledPath
	var stagingIdentifier, stagingOwner string
	if err := s.db.QueryRowContext(ctx, `SELECT identifier,ownership_tag FROM site_migration_resources WHERE migration_site_id=? AND resource_type='target_staging_root' AND status='created'`, migrationSiteID).Scan(&stagingIdentifier, &stagingOwner); err != nil || stagingOwner != migrationSiteID {
		return siteMigrationCutoverState{}, errors.New("cutover staging ownership unavailable")
	}
	state.StagingRoot = filepath.Clean(stagingIdentifier)
	if state.EnabledPath == "" || state.NginxPath != snapshot.TargetSpec.NginxConfPath {
		return siteMigrationCutoverState{}, errors.New("cutover target configuration mismatch")
	}
	state.MarkerKey = hashMigrationSecret(outbound)
	return state, nil
}

func (s *SiteMigrationCutoverService) recordMarkerIntent(ctx context.Context, taskID, nginxPath string, marker SiteMigrationMarkerResponse) (SiteMigrationMarkerResponse, error) {
	message, _ := json.Marshal(marker)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SiteMigrationMarkerResponse{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO site_migration_resources (migration_site_id,resource_type,identifier,ownership_tag,status,created_at,updated_at) VALUES (?,'target_marker_config',?,?,'created',?,?)`, taskID, nginxPath, taskID, s.now().UTC(), s.now().UTC())
	if err != nil {
		return SiteMigrationMarkerResponse{}, err
	}
	inserted, _ := result.RowsAffected()
	var owner, status string
	if err := tx.QueryRowContext(ctx, `SELECT ownership_tag,status FROM site_migration_resources WHERE migration_site_id=? AND resource_type='target_marker_config' AND identifier=?`, taskID, nginxPath).Scan(&owner, &status); err != nil || owner != taskID || status != "created" {
		return SiteMigrationMarkerResponse{}, errors.New("target marker intent conflicts")
	}
	if inserted == 0 {
		var storedMessage string
		if err := tx.QueryRowContext(ctx, `SELECT message FROM site_migration_events WHERE migration_site_id=? AND stage='target_marker' AND result='info' ORDER BY id ASC LIMIT 1`, taskID).Scan(&storedMessage); err != nil {
			return SiteMigrationMarkerResponse{}, errors.New("target marker intent unavailable")
		}
		var stored SiteMigrationMarkerResponse
		if json.Unmarshal([]byte(storedMessage), &stored) != nil {
			return SiteMigrationMarkerResponse{}, errors.New("target marker intent invalid")
		}
		if err := tx.Commit(); err != nil {
			return SiteMigrationMarkerResponse{}, err
		}
		return stored, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO site_migration_events (migration_site_id,stage,result,message,created_at) VALUES (?,'target_marker','info',?,?)`, taskID, string(message), s.now().UTC()); err != nil {
		return SiteMigrationMarkerResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return SiteMigrationMarkerResponse{}, err
	}
	return marker, nil
}

func (s *SiteMigrationCutoverService) recordProbe(ctx context.Context, taskID, observer string) error {
	return s.recordProbeResult(ctx, taskID, observer, true)
}

func (s *SiteMigrationCutoverService) recordProbeResult(ctx context.Context, taskID, observer string, success bool) error {
	result := "failed"
	if success {
		result = "success"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO site_migration_events (migration_site_id,stage,result,message,created_at) VALUES (?,'cutover_probe',?,?,?)`, taskID, result, "observer="+observer, s.now().UTC())
	return err
}

func (s *SiteMigrationCutoverService) requireProbeWindow(ctx context.Context, taskID string) error {
	now := s.now().UTC()
	for _, observer := range []string{"source", "target"} {
		rows, err := s.db.QueryContext(ctx, `SELECT result,created_at FROM site_migration_events WHERE migration_site_id=? AND stage='cutover_probe' AND message=? AND created_at>=? ORDER BY created_at DESC,id DESC LIMIT ?`, taskID, "observer="+observer, now.Add(-siteMigrationCutoverProbeMaxAge), siteMigrationCutoverProbeCount)
		if err != nil {
			return err
		}
		var times []time.Time
		allSuccess := true
		for rows.Next() {
			var result string
			var at time.Time
			if rows.Scan(&result, &at) == nil {
				times = append(times, at.UTC())
				allSuccess = allSuccess && result == "success"
			}
		}
		rows.Close()
		if len(times) != siteMigrationCutoverProbeCount || !allSuccess || times[0].Sub(times[len(times)-1]) < siteMigrationCutoverProbeSpan {
			return errors.New("public cutover verification incomplete")
		}
	}
	return nil
}

func (s *SiteMigrationCutoverService) commitActivation(ctx context.Context, taskID string, siteID int64) error {
	var snapshotRaw string
	if err := s.db.QueryRowContext(ctx, `SELECT settings_snapshot FROM site_migration_sites WHERE id=? AND stage='activating_target'`, taskID).Scan(&snapshotRaw); err != nil {
		return err
	}
	var snapshot struct {
		RuntimeSettings SiteMigrationRuntimeSettings `json:"runtime_settings"`
	}
	if json.Unmarshal([]byte(snapshotRaw), &snapshot) != nil {
		return errors.New("activation settings unavailable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT cj.id FROM cron_jobs cj
		JOIN site_migration_resources smr ON smr.migration_site_id=? AND smr.resource_type='cron_job' AND smr.identifier=CAST(cj.id AS TEXT) AND smr.ownership_tag=? AND smr.status='published'
		WHERE cj.site_id=? ORDER BY cj.id`, taskID, taskID, siteID)
	if err != nil {
		return err
	}
	var cronIDs []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			cronIDs = append(cronIDs, id)
		}
	}
	rows.Close()
	if len(cronIDs) != len(snapshot.RuntimeSettings.CronJobs) {
		return errors.New("activation cron identity mismatch")
	}
	for index, id := range cronIDs {
		result, err := tx.ExecContext(ctx, `UPDATE cron_jobs SET enabled=?,updated_at=? WHERE id=? AND site_id=? AND enabled=0`, boolInt(snapshot.RuntimeSettings.CronJobs[index].Enabled), s.now().UTC(), id, siteID)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return errors.New("activation cron state changed")
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE websites SET monitoring_enabled=?,updated_at=? WHERE id=?`, boolInt(snapshot.RuntimeSettings.MonitoringEnabled), s.now().UTC(), siteID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE site_migration_resources SET status='removed',updated_at=? WHERE migration_site_id=? AND resource_type='target_marker_config' AND status='created'`, s.now().UTC(), taskID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE site_migration_locks SET status='released',released_at=?,updated_at=? WHERE migration_site_id=? AND direction='target' AND site_id=? AND status='active'`, s.now().UTC(), s.now().UTC(), taskID, siteID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("activation lock state changed")
	}
	result, err = tx.ExecContext(ctx, `UPDATE site_migration_sites SET status='running',stage='activation_runtime_sync',error_code='',updated_at=? WHERE id=? AND stage='activating_target'`, s.now().UTC(), taskID)
	if err != nil {
		return err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return errors.New("activation task state changed")
	}
	return tx.Commit()
}

func (s *SiteMigrationCutoverService) resumeActivationRuntime(ctx context.Context, taskID string) error {
	_, _ = s.db.ExecContext(ctx, `UPDATE site_migration_sites SET status='running',error_code='',updated_at=? WHERE id=? AND stage='activation_runtime_sync' AND status='failed_retryable'`, s.now().UTC(), taskID)
	var siteID int
	if err := s.db.QueryRowContext(ctx, `SELECT target_site_id FROM site_migration_sites WHERE id=? AND stage='activation_runtime_sync'`, taskID).Scan(&siteID); err != nil {
		return s.activationFailedAtStage(taskID, "activation_runtime_sync", "activation_finalize_failed", err)
	}
	if err := s.ops.ReloadCron(); err != nil {
		return s.activationFailedAtStage(taskID, "activation_runtime_sync", "cron_runtime_sync_failed", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return s.activationFailedAtStage(taskID, "activation_runtime_sync", "activation_finalize_failed", err)
	}
	defer tx.Rollback()
	failFinalize := func(cause error) error {
		_ = tx.Rollback()
		return s.activationFailedAtStage(taskID, "activation_runtime_sync", "activation_finalize_failed", cause)
	}
	result, err := tx.ExecContext(ctx, `UPDATE site_migration_sites SET status='completed',stage='completed',error_code='',finished_at=?,updated_at=? WHERE id=? AND stage='activation_runtime_sync'`, s.now().UTC(), s.now().UTC(), taskID)
	if err != nil {
		return failFinalize(err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return failFinalize(errors.New("activation runtime state changed"))
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO site_migration_events (migration_site_id,stage,result,message,created_at) VALUES (?,'cutover_complete','success','target activated',?)`, taskID, s.now().UTC()); err != nil {
		return failFinalize(err)
	}
	if err := tx.Commit(); err != nil {
		return s.activationFailedAtStage(taskID, "activation_runtime_sync", "activation_finalize_failed", err)
	}
	refreshWPCodeIntegrityBaselineBestEffort(siteID, "网站搬家目标激活成功")
	return nil
}

func (s *SiteMigrationCutoverService) cutoverFailed(taskID, code string, cause error) error {
	_, _ = s.db.Exec(`UPDATE site_migration_sites SET error_code=?,updated_at=? WHERE id=? AND stage='awaiting_cutover'`, code, s.now().UTC(), taskID)
	return fmt.Errorf("cutover marker failed: %w", cause)
}
func (s *SiteMigrationCutoverService) activationFailed(taskID, code string, cause error) error {
	return s.activationFailedAtStage(taskID, "activating_target", code, cause)
}
func (s *SiteMigrationCutoverService) activationFailedAtStage(taskID, stage, code string, cause error) error {
	_, _ = s.db.Exec(`UPDATE site_migration_sites SET status='failed_retryable',error_code=?,updated_at=? WHERE id=? AND stage=?`, code, s.now().UTC(), taskID, stage)
	return fmt.Errorf("cutover activation failed: %w", cause)
}

func renderSiteMigrationTargetMarkerBlock(token, payload string) string {
	return "    # WP Panel migration target marker begin\n    location = /.well-known/wp-panel-migration/" + token + " { access_log off; default_type application/json; add_header Cache-Control \"no-store\" always; return 200 '" + payload + "'; }\n    # WP Panel migration target marker end\n"
}

func loadPersistedSiteMigrationTargetMarkerBlock(ctx context.Context, db *sql.DB, migrationSiteID, stage string) (string, string, error) {
	if stage != "awaiting_cutover" && stage != "activating_target" {
		return "", "", errors.New("target marker recovery stage unavailable")
	}
	service := &SiteMigrationCutoverService{db: db}
	state, err := service.loadCutoverState(ctx, migrationSiteID, stage)
	if err != nil {
		return "", "", err
	}
	var message string
	err = db.QueryRowContext(ctx, `SELECT e.message FROM site_migration_events e
		JOIN site_migration_resources r ON r.migration_site_id=e.migration_site_id AND r.resource_type='target_marker_config' AND r.identifier=? AND r.ownership_tag=? AND r.status='created'
		WHERE e.migration_site_id=? AND e.stage='target_marker' AND e.result='info'
		ORDER BY e.id LIMIT 1`, state.NginxPath, migrationSiteID, migrationSiteID).Scan(&message)
	if err != nil {
		return "", "", errors.New("target marker intent unavailable")
	}
	var marker SiteMigrationMarkerResponse
	if json.Unmarshal([]byte(message), &marker) != nil || marker.Task != migrationSiteID || marker.Role != "target" || !verifySiteMigrationMarker(state.MarkerKey, state.MarkerToken, marker) {
		return "", "", errors.New("target marker intent invalid")
	}
	payload, _ := json.Marshal(marker)
	return renderSiteMigrationTargetMarkerBlock(state.MarkerToken, string(payload)), state.EnabledPath, nil
}

func injectSiteMigrationTargetMarker(content, block string) (string, error) {
	serverCount := strings.Count(content, "server {")
	if serverCount == 0 {
		return "", errors.New("target marker config unavailable")
	}
	if strings.Contains(content, "WP Panel migration target marker") {
		if strings.Count(content, block) != serverCount || strings.Count(content, "WP Panel migration target marker begin") != serverCount || strings.Count(content, "WP Panel migration target marker end") != serverCount {
			return "", errors.New("target marker config conflicts")
		}
		return content, nil
	}
	return strings.ReplaceAll(content, "server {", "server {\n"+block), nil
}
func removeSiteMigrationTargetMarker(content string) (string, error) {
	re := regexp.MustCompile(`(?ms)\s*# WP Panel migration target marker begin\n.*?# WP Panel migration target marker end\n`)
	updated := re.ReplaceAllString(content, "\n")
	if updated == content && !strings.Contains(content, "WP Panel migration target marker") {
		return content, nil
	}
	if strings.Contains(updated, "WP Panel migration target marker") {
		return "", errors.New("target marker config unavailable")
	}
	return updated, nil
}
func applyMigrationNginxContent(path, enabledPath, content string) error {
	engine := NewTemplateEngine(filepath.Join(filepath.Dir(path), ".migration-backups"))
	if err := engine.writeNginxConfigFile(content, path); err != nil {
		return err
	}
	if target, err := os.Readlink(enabledPath); err != nil || filepath.Clean(target) != filepath.Clean(path) {
		return errors.New("target Nginx symlink changed")
	}
	out, err := exec.Command("nginx", "-s", "reload").CombinedOutput()
	if err != nil {
		return fmt.Errorf("reload target marker: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
func signSiteMigrationMarker(key, token string, marker SiteMigrationMarkerResponse) string {
	mac := hmac.New(sha256.New, []byte(key))
	fmt.Fprintf(mac, "%s|%s|%d|%s", marker.Task, marker.Role, marker.IssuedAt, token)
	return hex.EncodeToString(mac.Sum(nil))
}
func verifySiteMigrationMarker(key, token string, marker SiteMigrationMarkerResponse) bool {
	expected := signSiteMigrationMarker(key, token, marker)
	if len(expected) != len(marker.Signature) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(marker.Signature)) == 1
}

func signSiteMigrationProbeAttestation(key string, attestation SiteMigrationProbeAttestation) string {
	mac := hmac.New(sha256.New, []byte(key))
	fmt.Fprintf(mac, "%s|%s|%d|%s|%t", attestation.Task, attestation.Observer, attestation.ObservedAt, attestation.MarkerSignature, attestation.Success)
	return hex.EncodeToString(mac.Sum(nil))
}

func verifySiteMigrationProbeAttestation(key string, attestation SiteMigrationProbeAttestation) bool {
	expected := signSiteMigrationProbeAttestation(key, attestation)
	return len(expected) == len(attestation.Signature) && subtle.ConstantTimeCompare([]byte(expected), []byte(attestation.Signature)) == 1
}

func ResolveSiteMigrationDNS(ctx context.Context, domain string) ([]string, error) {
	if !IsValidDomain(domain) {
		return nil, errors.New("invalid migration DNS domain")
	}
	resolver := net.DefaultResolver
	addresses, err := resolver.LookupHost(ctx, domain)
	if err != nil {
		return nil, err
	}
	sort.Strings(addresses)
	return addresses, nil
}

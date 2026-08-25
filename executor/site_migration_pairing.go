package executor

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	siteMigrationPairTTL      = 15 * time.Minute
	siteMigrationPairMaxFails = 5
	siteMigrationMaxResponse  = 64 << 10
)

var (
	ErrSiteMigrationPairRejected = errors.New("site migration pairing rejected")
	ErrSiteMigrationPairConflict = errors.New("site migration peer already exists")
)

type SiteMigrationPairingPackage struct {
	PeerID            string    `json:"peer_id"`
	BaseURL           string    `json:"base_url"`
	CertificateSHA256 string    `json:"certificate_sha256"`
	Token             string    `json:"token"`
	ProtocolVersion   int       `json:"protocol_version"`
	PanelVersion      string    `json:"panel_version"`
	ExpiresAt         time.Time `json:"expires_at"`
}

type SiteMigrationRedeemRequest struct {
	PeerID            string `json:"peer_id"`
	Token             string `json:"token"`
	SourceBaseURL     string `json:"source_base_url"`
	CertificateSHA256 string `json:"certificate_sha256"`
	Challenge         string `json:"challenge"`
	ProtocolVersion   int    `json:"protocol_version"`
	PanelVersion      string `json:"panel_version"`
}

type SiteMigrationChallengeRequest struct {
	PeerID          string `json:"peer_id"`
	Challenge       string `json:"challenge"`
	ProtocolVersion int    `json:"protocol_version"`
	PanelVersion    string `json:"panel_version"`
}

type SiteMigrationPreflightRequest struct {
	PeerID  string   `json:"peer_id"`
	Domains []string `json:"domains"`
}

type SiteMigrationPreflightConflict struct {
	Domain   string `json:"domain"`
	SiteType string `json:"site_type"`
}

type SiteMigrationPreflightResponse struct {
	Success      bool                             `json:"success"`
	PanelVersion string                           `json:"panel_version"`
	Conflicts    []SiteMigrationPreflightConflict `json:"conflicts"`
}

type SiteMigrationPeer struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	BaseURL           string     `json:"base_url"`
	CertificateSHA256 string     `json:"certificate_sha256"`
	ProtocolVersion   int        `json:"protocol_version"`
	Status            string     `json:"status"`
	PairedAt          *time.Time `json:"paired_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
}

type SiteMigrationTaskSummary struct {
	ID                      string    `json:"id"`
	BatchID                 string    `json:"batch_id"`
	PeerID                  string    `json:"peer_id"`
	Direction               string    `json:"direction"`
	SourceDomain            string    `json:"source_domain"`
	TargetDomain            string    `json:"target_domain"`
	SiteType                string    `json:"site_type"`
	Status                  string    `json:"status"`
	Stage                   string    `json:"stage"`
	ErrorCode               string    `json:"error_code"`
	ReservedBytes           int64     `json:"reserved_bytes"`
	RemoteBackupReconfigure bool      `json:"remote_backup_reconfigure"`
	SkippedCustomCommands   int       `json:"skipped_custom_commands"`
	RemoteStateUnavailable  bool      `json:"remote_state_unavailable"`
	SourceSitePresent       bool      `json:"source_site_present"`
	SourceDecisionPending   bool      `json:"source_decision_pending"`
	SourceCanRestore        bool      `json:"source_can_restore"`
	TransferReceivedBytes   int64     `json:"transfer_received_bytes"`
	TransferExtracting      bool      `json:"transfer_extracting"`
	UpdatedAt               time.Time `json:"updated_at"`
}

type SiteMigrationRemoteTaskStatus struct {
	ID                    string    `json:"id"`
	Status                string    `json:"status"`
	Stage                 string    `json:"stage"`
	ErrorCode             string    `json:"error_code"`
	TransferReceivedBytes int64     `json:"transfer_received_bytes"`
	TransferExtracting    bool      `json:"transfer_extracting"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type SiteMigrationPairingService struct {
	db          *sql.DB
	version     string
	certPath    string
	now         func() time.Time
	httpTimeout time.Duration
}

func NewSiteMigrationPairingService(db *sql.DB, version, certPath string) (*SiteMigrationPairingService, error) {
	if db == nil || strings.TrimSpace(version) == "" || strings.TrimSpace(certPath) == "" {
		return nil, errors.New("invalid site migration pairing configuration")
	}
	return &SiteMigrationPairingService{db: db, version: version, certPath: certPath, now: time.Now, httpTimeout: 10 * time.Second}, nil
}

func (s *SiteMigrationPairingService) GeneratePackage(ctx context.Context, baseURL string) (*SiteMigrationPairingPackage, error) {
	baseURL, err := normalizeMigrationBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	fingerprint, err := migrationCertificateFingerprint(s.certPath)
	if err != nil {
		return nil, err
	}
	id, err := randomMigrationID("peer_", 18)
	if err != nil {
		return nil, err
	}
	token, err := randomMigrationValue("", 32)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	expires := now.Add(siteMigrationPairTTL)
	_, err = s.db.ExecContext(ctx, `INSERT INTO site_migration_peers
		(id,base_url,certificate_sha256,local_certificate_sha256,pair_token_hash,pair_token_expires_at,protocol_version,status,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,'pending',?,?)`, id, baseURL, fingerprint, fingerprint, hashMigrationSecret(token), expires, siteMigrationProtocolVersion, now, now)
	if err != nil {
		return nil, fmt.Errorf("create site migration pairing: %w", err)
	}
	return &SiteMigrationPairingPackage{PeerID: id, BaseURL: baseURL, CertificateSHA256: fingerprint, Token: token,
		ProtocolVersion: siteMigrationProtocolVersion, PanelVersion: s.version, ExpiresAt: expires}, nil
}

func (s *SiteMigrationPairingService) Connect(ctx context.Context, pkg SiteMigrationPairingPackage, sourceBaseURL string) error {
	now := s.now().UTC()
	if !validSiteMigrationID(pkg.PeerID) || pkg.ProtocolVersion != siteMigrationProtocolVersion || pkg.PanelVersion != s.version ||
		!pkg.ExpiresAt.After(now) || len(pkg.Token) < 40 || !validMigrationFingerprint(pkg.CertificateSHA256) {
		return ErrSiteMigrationPairRejected
	}
	targetURL, err := normalizeMigrationBaseURL(pkg.BaseURL)
	if err != nil {
		return ErrSiteMigrationPairRejected
	}
	sourceBaseURL, err = normalizeMigrationBaseURL(sourceBaseURL)
	if err != nil {
		return err
	}
	localFingerprint, err := migrationCertificateFingerprint(s.certPath)
	if err != nil {
		return err
	}

	challenge, err := s.prepareLocalPeer(ctx, pkg, targetURL, localFingerprint, now)
	if err != nil {
		return err
	}
	aToB, bToA := deriveMigrationCredentials(pkg.Token, challenge, localFingerprint, pkg.CertificateSHA256)
	req := SiteMigrationRedeemRequest{PeerID: pkg.PeerID, Token: pkg.Token, SourceBaseURL: sourceBaseURL,
		CertificateSHA256: localFingerprint, Challenge: challenge, ProtocolVersion: siteMigrationProtocolVersion, PanelVersion: s.version}
	if err := postPinnedJSON(ctx, targetURL+"/api/site-migration/v1/pair/redeem", pkg.CertificateSHA256, "", req, nil, s.httpTimeout); err != nil {
		return fmt.Errorf("connect site migration peer: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE site_migration_peers SET status='paired',paired_at=?,updated_at=?
		WHERE id=? AND status='pending' AND outbound_credential=? AND inbound_credential_hash=?`,
		now, now, pkg.PeerID, aToB, hashMigrationSecret(bToA))
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrSiteMigrationPairConflict
	}
	return nil
}

func (s *SiteMigrationPairingService) prepareLocalPeer(ctx context.Context, pkg SiteMigrationPairingPackage, targetURL, localFingerprint string, now time.Time) (string, error) {
	var status, baseURL, fingerprint, challenge, outbound, inboundHash string
	err := s.db.QueryRowContext(ctx, `SELECT status,base_url,certificate_sha256,pairing_challenge,outbound_credential,inbound_credential_hash
		FROM site_migration_peers WHERE id=?`, pkg.PeerID).Scan(&status, &baseURL, &fingerprint, &challenge, &outbound, &inboundHash)
	if err == nil {
		if status != "pending" || baseURL != targetURL || !sameMigrationSecret(fingerprint, pkg.CertificateSHA256) || challenge == "" {
			return "", ErrSiteMigrationPairConflict
		}
		aToB, bToA := deriveMigrationCredentials(pkg.Token, challenge, localFingerprint, pkg.CertificateSHA256)
		if !sameMigrationSecret(outbound, aToB) || !sameMigrationSecret(inboundHash, hashMigrationSecret(bToA)) {
			return "", ErrSiteMigrationPairConflict
		}
		return challenge, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	challenge, err = randomMigrationValue("", 32)
	if err != nil {
		return "", err
	}
	aToB, bToA := deriveMigrationCredentials(pkg.Token, challenge, localFingerprint, pkg.CertificateSHA256)
	_, err = s.db.ExecContext(ctx, `INSERT INTO site_migration_peers
		(id,base_url,certificate_sha256,local_certificate_sha256,inbound_credential_hash,outbound_credential,pairing_challenge,protocol_version,status,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,'pending',?,?)`, pkg.PeerID, targetURL, strings.ToLower(pkg.CertificateSHA256), localFingerprint,
		hashMigrationSecret(bToA), aToB, challenge, siteMigrationProtocolVersion, now, now)
	if err != nil {
		return "", ErrSiteMigrationPairConflict
	}
	return challenge, nil
}

func (s *SiteMigrationPairingService) Redeem(ctx context.Context, req SiteMigrationRedeemRequest) error {
	if !validSiteMigrationID(req.PeerID) {
		return ErrSiteMigrationPairRejected
	}
	fail := func() error {
		_, _ = s.db.ExecContext(ctx, `UPDATE site_migration_peers SET pair_attempts=pair_attempts+1,updated_at=?
			WHERE id=? AND status='pending' AND pair_attempts<?`, s.now().UTC(), req.PeerID, siteMigrationPairMaxFails)
		return ErrSiteMigrationPairRejected
	}
	var tokenHash, ownFingerprint, status, boundURL, boundFingerprint, inboundHash, outbound, storedChallenge string
	var expires sql.NullTime
	var attempts, protocol int
	err := s.db.QueryRowContext(ctx, `SELECT pair_token_hash,pair_token_expires_at,pair_attempts,protocol_version,status,
		local_certificate_sha256,certificate_sha256,base_url,inbound_credential_hash,outbound_credential,pairing_challenge
		FROM site_migration_peers WHERE id=?`, req.PeerID).Scan(&tokenHash, &expires, &attempts, &protocol, &status,
		&ownFingerprint, &boundFingerprint, &boundURL, &inboundHash, &outbound, &storedChallenge)
	if err != nil || attempts >= siteMigrationPairMaxFails || !expires.Valid || !expires.Time.After(s.now().UTC()) ||
		protocol != siteMigrationProtocolVersion || req.ProtocolVersion != protocol || req.PanelVersion != s.version ||
		!sameMigrationSecret(tokenHash, hashMigrationSecret(req.Token)) || !validMigrationFingerprint(req.CertificateSHA256) {
		return fail()
	}
	sourceURL, err := normalizeMigrationBaseURL(req.SourceBaseURL)
	if err != nil || len(req.Challenge) < 40 {
		return fail()
	}
	aToB, bToA := deriveMigrationCredentials(req.Token, req.Challenge, req.CertificateSHA256, ownFingerprint)
	if status == "paired" && (boundURL != sourceURL || !sameMigrationSecret(boundFingerprint, req.CertificateSHA256) ||
		!sameMigrationSecret(inboundHash, hashMigrationSecret(aToB)) || !sameMigrationSecret(outbound, bToA) || storedChallenge != req.Challenge) {
		return ErrSiteMigrationPairRejected
	}
	if status != "pending" && status != "paired" {
		return ErrSiteMigrationPairRejected
	}
	challengeReq := SiteMigrationChallengeRequest{PeerID: req.PeerID, Challenge: req.Challenge,
		ProtocolVersion: siteMigrationProtocolVersion, PanelVersion: s.version}
	if err := postPinnedJSON(ctx, sourceURL+"/api/site-migration/v1/pair/challenge", req.CertificateSHA256, bToA, challengeReq, nil, s.httpTimeout); err != nil {
		return fail()
	}
	if status == "paired" {
		return nil
	}
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE site_migration_peers SET base_url=?,certificate_sha256=?,inbound_credential_hash=?,
		outbound_credential=?,pairing_challenge=?,status='paired',paired_at=?,updated_at=? WHERE id=? AND status='pending'`,
		sourceURL, strings.ToLower(req.CertificateSHA256), hashMigrationSecret(aToB), bToA, req.Challenge, now, now, req.PeerID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrSiteMigrationPairConflict
	}
	return nil
}

func (s *SiteMigrationPairingService) VerifyChallenge(ctx context.Context, peerID, bearer string, req SiteMigrationChallengeRequest) error {
	if peerID != req.PeerID || !validSiteMigrationID(peerID) || req.ProtocolVersion != siteMigrationProtocolVersion || req.PanelVersion != s.version {
		return ErrSiteMigrationPairRejected
	}
	var inboundHash, challenge, status string
	err := s.db.QueryRowContext(ctx, `SELECT inbound_credential_hash,pairing_challenge,status FROM site_migration_peers WHERE id=?`, peerID).
		Scan(&inboundHash, &challenge, &status)
	if err != nil || (status != "pending" && status != "paired") || challenge != req.Challenge || !sameMigrationSecret(inboundHash, hashMigrationSecret(bearer)) {
		return ErrSiteMigrationPairRejected
	}
	return nil
}

func (s *SiteMigrationPairingService) AuthorizePeer(ctx context.Context, peerID, bearer string) error {
	if !validSiteMigrationID(peerID) || bearer == "" {
		return ErrSiteMigrationPairRejected
	}
	var inboundHash, status string
	var protocol int
	if err := s.db.QueryRowContext(ctx, `SELECT inbound_credential_hash,status,protocol_version FROM site_migration_peers WHERE id=?`, peerID).
		Scan(&inboundHash, &status, &protocol); err != nil || status != "paired" || protocol != siteMigrationProtocolVersion || !sameMigrationSecret(inboundHash, hashMigrationSecret(bearer)) {
		return ErrSiteMigrationPairRejected
	}
	return nil
}

func (s *SiteMigrationPairingService) RemotePreflight(ctx context.Context, peerID string, domains []string) (*SiteMigrationPreflightResponse, error) {
	if !validSiteMigrationID(peerID) || len(domains) == 0 || len(domains) > 500 {
		return nil, ErrSiteMigrationPairRejected
	}
	for i := range domains {
		domains[i] = strings.ToLower(strings.TrimSpace(domains[i]))
		if !IsValidDomain(domains[i]) {
			return nil, ErrSiteMigrationPairRejected
		}
	}
	var baseURL, fingerprint, outbound, status string
	var protocol int
	if err := s.db.QueryRowContext(ctx, `SELECT base_url,certificate_sha256,outbound_credential,status,protocol_version
		FROM site_migration_peers WHERE id=?`, peerID).Scan(&baseURL, &fingerprint, &outbound, &status, &protocol); err != nil || status != "paired" || protocol != siteMigrationProtocolVersion {
		return nil, ErrSiteMigrationPairRejected
	}
	request := SiteMigrationPreflightRequest{PeerID: peerID, Domains: domains}
	var response SiteMigrationPreflightResponse
	if err := postPinnedJSON(ctx, baseURL+"/api/site-migration/v1/preflight", fingerprint, outbound, request, &response, s.httpTimeout); err != nil {
		return nil, err
	}
	if !response.Success || response.PanelVersion != s.version {
		return nil, ErrSiteMigrationPairRejected
	}
	return &response, nil
}

func (s *SiteMigrationPairingService) RemoteCreateTargetBatch(ctx context.Context, peerID, batchID, requestedBy string, sites []SiteMigrationBatchSitePlan) (*SiteMigrationBatchPlanResult, error) {
	var baseURL, fingerprint, outbound, status string
	var protocol int
	if !validSiteMigrationID(peerID) || !validSiteMigrationID(batchID) || len(sites) == 0 || len(sites) > 500 || s.db.QueryRowContext(ctx, `SELECT base_url,certificate_sha256,outbound_credential,status,protocol_version FROM site_migration_peers WHERE id=?`, peerID).Scan(&baseURL, &fingerprint, &outbound, &status, &protocol) != nil || status != "paired" || protocol != siteMigrationProtocolVersion {
		return nil, ErrSiteMigrationPairRejected
	}
	request := struct {
		PeerID      string                       `json:"peer_id"`
		BatchID     string                       `json:"batch_id"`
		RequestedBy string                       `json:"requested_by"`
		Sites       []SiteMigrationBatchSitePlan `json:"sites"`
	}{peerID, batchID, requestedBy, sites}
	var response struct {
		Success bool                          `json:"success"`
		Batch   *SiteMigrationBatchPlanResult `json:"batch"`
	}
	if err := postPinnedJSON(ctx, baseURL+"/api/site-migration/v1/target/batches", fingerprint, outbound, request, &response, s.httpTimeout); err != nil || !response.Success || response.Batch == nil {
		return nil, ErrSiteMigrationPairRejected
	}
	return response.Batch, nil
}

func (s *SiteMigrationPairingService) RemoteQueueTargetSite(ctx context.Context, peerID, batchID, taskID string) error {
	var baseURL, fingerprint, outbound, status string
	var protocol int
	if !validSiteMigrationID(peerID) || !validSiteMigrationID(batchID) || !validSiteMigrationID(taskID) || s.db.QueryRowContext(ctx, `SELECT base_url,certificate_sha256,outbound_credential,status,protocol_version FROM site_migration_peers WHERE id=?`, peerID).Scan(&baseURL, &fingerprint, &outbound, &status, &protocol) != nil || status != "paired" || protocol != siteMigrationProtocolVersion {
		return ErrSiteMigrationPairRejected
	}
	request := map[string]string{"peer_id": peerID, "batch_id": batchID, "migration_site_id": taskID}
	var response struct {
		Success bool `json:"success"`
	}
	if err := postPinnedJSON(ctx, baseURL+"/api/site-migration/v1/target/batches/queue", fingerprint, outbound, request, &response, s.httpTimeout); err != nil || !response.Success {
		return ErrSiteMigrationPairRejected
	}
	return nil
}

func (s *SiteMigrationPairingService) remoteMigrationAction(ctx context.Context, peerID, route string, request, response any) error {
	var baseURL, fingerprint, outbound, status string
	var protocol int
	if !validSiteMigrationID(peerID) || s.db.QueryRowContext(ctx, `SELECT base_url,certificate_sha256,outbound_credential,status,protocol_version FROM site_migration_peers WHERE id=?`, peerID).Scan(&baseURL, &fingerprint, &outbound, &status, &protocol) != nil || status != "paired" || protocol != siteMigrationProtocolVersion {
		return ErrSiteMigrationPairRejected
	}
	if err := postPinnedJSONLimit(ctx, baseURL+route, fingerprint, outbound, request, response, s.httpTimeout, 1<<20); err != nil {
		return ErrSiteMigrationPairRejected
	}
	return nil
}

func (s *SiteMigrationPairingService) RemoteSourceProbe(ctx context.Context, peerID, taskID string) (SiteMigrationProbeAttestation, error) {
	var response struct {
		Success     bool                          `json:"success"`
		Attestation SiteMigrationProbeAttestation `json:"attestation"`
	}
	err := s.remoteMigrationAction(ctx, peerID, "/api/site-migration/v1/source/probe", map[string]string{"peer_id": peerID, "migration_site_id": taskID}, &response)
	if err != nil || !response.Success {
		return SiteMigrationProbeAttestation{}, ErrSiteMigrationPairRejected
	}
	return response.Attestation, nil
}

func (s *SiteMigrationPairingService) RemoteTargetProbe(ctx context.Context, peerID, taskID string, attestation SiteMigrationProbeAttestation) error {
	var response struct {
		Success bool `json:"success"`
	}
	err := s.remoteMigrationAction(ctx, peerID, "/api/site-migration/v1/target/probe", map[string]any{"peer_id": peerID, "migration_site_id": taskID, "attestation": attestation}, &response)
	if err != nil || !response.Success {
		return ErrSiteMigrationPairRejected
	}
	return nil
}

func (s *SiteMigrationPairingService) RemoteTargetConfirm(ctx context.Context, peerID, taskID, domain, operator, reason string, force bool) error {
	var response struct {
		Success bool `json:"success"`
	}
	err := s.remoteMigrationAction(ctx, peerID, "/api/site-migration/v1/target/confirm", map[string]any{"peer_id": peerID, "migration_site_id": taskID, "domain": domain, "operator": operator, "reason": reason, "force": force}, &response)
	if err != nil || !response.Success {
		return ErrSiteMigrationPairRejected
	}
	return nil
}

func (s *SiteMigrationPairingService) RemoteTargetAbandon(ctx context.Context, peerID, taskID, domain, operator, reason string, allowActivated bool) error {
	var response struct {
		Success bool `json:"success"`
	}
	err := s.remoteMigrationAction(ctx, peerID, "/api/site-migration/v1/target/abandon", map[string]any{"peer_id": peerID, "migration_site_id": taskID, "domain": domain, "operator": operator, "reason": reason, "allow_activated": allowActivated}, &response)
	if err != nil || !response.Success {
		return ErrSiteMigrationPairRejected
	}
	return nil
}

func (s *SiteMigrationPairingService) RemoteSourceAbandon(ctx context.Context, peerID, taskID string) error {
	var response struct {
		Success bool `json:"success"`
	}
	err := s.remoteMigrationAction(ctx, peerID, "/api/site-migration/v1/source/abandon", map[string]string{"peer_id": peerID, "migration_site_id": taskID}, &response)
	if err != nil || !response.Success {
		return ErrSiteMigrationPairRejected
	}
	return nil
}

func (s *SiteMigrationPairingService) RemoteTargetRetry(ctx context.Context, peerID, taskID string) error {
	var response struct {
		Success bool `json:"success"`
	}
	err := s.remoteMigrationAction(ctx, peerID, "/api/site-migration/v1/target/retry", map[string]string{"peer_id": peerID, "migration_site_id": taskID}, &response)
	if err != nil || !response.Success {
		return ErrSiteMigrationPairRejected
	}
	return nil
}

func (s *SiteMigrationPairingService) RemoteDeleteTargetTask(ctx context.Context, peerID, taskID string) error {
	var response struct {
		Success bool `json:"success"`
	}
	err := s.remoteMigrationAction(ctx, peerID, "/api/site-migration/v1/target/delete-task", map[string]string{"peer_id": peerID, "migration_site_id": taskID}, &response)
	if err != nil || !response.Success {
		return ErrSiteMigrationPairRejected
	}
	return nil
}

func (s *SiteMigrationPairingService) RemoteDeleteSourceTask(ctx context.Context, peerID, taskID string) error {
	var response struct {
		Success bool `json:"success"`
	}
	err := s.remoteMigrationAction(ctx, peerID, "/api/site-migration/v1/source/delete-task", map[string]string{"peer_id": peerID, "migration_site_id": taskID}, &response)
	if err != nil || !response.Success {
		return ErrSiteMigrationPairRejected
	}
	return nil
}

func (s *SiteMigrationPairingService) RemoteRevokePeer(ctx context.Context, peerID string) error {
	var response struct {
		Success bool `json:"success"`
	}
	err := s.remoteMigrationAction(ctx, peerID, "/api/site-migration/v1/peer/revoke", map[string]string{"peer_id": peerID}, &response)
	if err != nil || !response.Success {
		return ErrSiteMigrationPairRejected
	}
	return nil
}

func (s *SiteMigrationPairingService) RemoteTargetStatuses(ctx context.Context, peerID string, taskIDs []string) ([]SiteMigrationRemoteTaskStatus, error) {
	var response struct {
		Success bool                            `json:"success"`
		Tasks   []SiteMigrationRemoteTaskStatus `json:"tasks"`
	}
	err := s.remoteMigrationAction(ctx, peerID, "/api/site-migration/v1/target/status", map[string]any{"peer_id": peerID, "migration_site_ids": taskIDs}, &response)
	if err != nil || !response.Success {
		return nil, ErrSiteMigrationPairRejected
	}
	return response.Tasks, nil
}

func (s *SiteMigrationPairingService) ListPeers(ctx context.Context) ([]SiteMigrationPeer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,base_url,certificate_sha256,protocol_version,status,paired_at,created_at
		FROM site_migration_peers WHERE status IN ('pending','paired') ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	peers := make([]SiteMigrationPeer, 0)
	for rows.Next() {
		var peer SiteMigrationPeer
		var paired sql.NullTime
		if err := rows.Scan(&peer.ID, &peer.Name, &peer.BaseURL, &peer.CertificateSHA256, &peer.ProtocolVersion, &peer.Status, &paired, &peer.CreatedAt); err != nil {
			return nil, err
		}
		if paired.Valid {
			value := paired.Time
			peer.PairedAt = &value
		}
		peers = append(peers, peer)
	}
	return peers, rows.Err()
}

func (s *SiteMigrationPairingService) ListTasks(ctx context.Context) ([]SiteMigrationTaskSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ms.id,ms.batch_id,mb.peer_id,mb.direction,ms.source_domain,ms.target_domain,
		ms.site_type,ms.status,ms.stage,ms.error_code,ms.reserved_bytes,ms.settings_snapshot,ms.source_site_id IS NOT NULL,
		EXISTS(SELECT 1 FROM site_migration_resources r WHERE r.migration_site_id=ms.id AND r.resource_type='source_maintenance_config' AND r.status='created'),ms.updated_at
		FROM site_migration_sites ms JOIN site_migration_batches mb ON mb.id=ms.batch_id
		WHERE ms.status NOT IN ('completed','abandoned','failed_manual')
		ORDER BY ms.updated_at DESC,ms.id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := make([]SiteMigrationTaskSummary, 0)
	for rows.Next() {
		var task SiteMigrationTaskSummary
		var snapshotRaw string
		if err := rows.Scan(&task.ID, &task.BatchID, &task.PeerID, &task.Direction, &task.SourceDomain, &task.TargetDomain, &task.SiteType, &task.Status, &task.Stage, &task.ErrorCode, &task.ReservedBytes, &snapshotRaw, &task.SourceSitePresent, &task.SourceCanRestore, &task.UpdatedAt); err != nil {
			return nil, err
		}
		var snapshot struct {
			RuntimeSettings   SiteMigrationRuntimeSettings `json:"runtime_settings"`
			MigrationWarnings struct {
				RemoteBackupReconfigure bool `json:"remote_backup_reconfigure"`
				SkippedCustomCommands   int  `json:"skipped_custom_commands"`
			} `json:"migration_warnings"`
		}
		if json.Unmarshal([]byte(snapshotRaw), &snapshot) == nil {
			task.RemoteBackupReconfigure = snapshot.RuntimeSettings.RemoteBackupReconfigure || snapshot.MigrationWarnings.RemoteBackupReconfigure
			task.SkippedCustomCommands = snapshot.RuntimeSettings.SkippedCustomCommands
			if task.SkippedCustomCommands == 0 {
				task.SkippedCustomCommands = snapshot.MigrationWarnings.SkippedCustomCommands
			}
		}
		if task.Direction != "source" || (task.Status != "awaiting_cutover" && task.Status != "failed_retryable" && task.Status != "interrupted_unknown" && task.Status != "cleanup_failed") {
			task.SourceCanRestore = false
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *SiteMigrationPairingService) RevokePeer(ctx context.Context, peerID string) error {
	if !validSiteMigrationID(peerID) {
		return ErrSiteMigrationPairRejected
	}
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE site_migration_peers SET status='revoked',inbound_credential_hash='',
		outbound_credential='',pair_token_hash='',pairing_challenge='',revoked_at=?,updated_at=?
		WHERE id=? AND status IN ('pending','paired')`, now, now, peerID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrSiteMigrationPairRejected
	}
	return nil
}

func (s *SiteMigrationPairingService) RevokePeerEverywhere(ctx context.Context, peerID string) error {
	_ = s.RemoteRevokePeer(ctx, peerID)
	return s.RevokePeer(ctx, peerID)
}

func randomMigrationValue(prefix string, size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

func randomMigrationID(prefix string, size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(buf), nil
}

func hashMigrationSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func sameMigrationSecret(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(strings.ToLower(a)), []byte(strings.ToLower(b))) == 1
}

func deriveMigrationCredentials(token, challenge, sourceFingerprint, targetFingerprint string) (string, string) {
	contextValue := challenge + "\x00" + strings.ToLower(sourceFingerprint) + "\x00" + strings.ToLower(targetFingerprint)
	derive := func(label string) string {
		mac := hmac.New(sha256.New, []byte(token))
		_, _ = mac.Write([]byte(label + "\x00" + contextValue))
		return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	}
	return derive("source-to-target"), derive("target-to-source")
}

func normalizeMigrationBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid HTTPS panel address")
	}
	return "https://" + u.Host, nil
}

func validMigrationFingerprint(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func migrationCertificateFingerprint(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read panel certificate: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("invalid panel certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse panel certificate: %w", err)
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:]), nil
}

func pinnedMigrationClient(fingerprint string, timeout time.Duration) (*http.Client, error) {
	tlsConfig, err := pinnedMigrationTLSConfig(fingerprint)
	if err != nil {
		return nil, err
	}
	return &http.Client{Timeout: timeout, Transport: &http.Transport{TLSClientConfig: tlsConfig}}, nil
}

func pinnedMigrationStreamingClient(fingerprint string) (*http.Client, error) {
	tlsConfig, err := pinnedMigrationTLSConfig(fingerprint)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig:       tlsConfig,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}}, nil
}

func pinnedMigrationTLSConfig(fingerprint string) (*tls.Config, error) {
	if !validMigrationFingerprint(fingerprint) {
		return nil, errors.New("invalid certificate fingerprint")
	}
	tlsConfig := &tls.Config{ // The explicit leaf fingerprint check below is the trust policy for self-signed panel certificates.
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec -- required for self-signed certificates; VerifyConnection is mandatory.
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("missing peer certificate")
			}
			sum := sha256.Sum256(state.PeerCertificates[0].Raw)
			if !sameMigrationSecret(hex.EncodeToString(sum[:]), fingerprint) {
				return errors.New("panel certificate fingerprint mismatch")
			}
			return nil
		},
	}
	return tlsConfig, nil
}

func postPinnedJSON(ctx context.Context, endpoint, fingerprint, bearer string, body, response any, timeout time.Duration) error {
	return postPinnedJSONLimit(ctx, endpoint, fingerprint, bearer, body, response, timeout, siteMigrationMaxResponse)
}

func postPinnedJSONLimit(ctx context.Context, endpoint, fingerprint, bearer string, body, response any, timeout time.Duration, responseLimit int64) error {
	if responseLimit <= 0 || responseLimit > 1<<20 {
		return errors.New("invalid peer response limit")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	client, err := pinnedMigrationClient(fingerprint, timeout)
	if err != nil {
		return err
	}
	resp, err := client.Do(request)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, responseLimit+1)
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, limited)
		return fmt.Errorf("peer returned HTTP %d", resp.StatusCode)
	}
	if response == nil {
		_, err = io.Copy(io.Discard, limited)
		return err
	}
	data, err := io.ReadAll(limited)
	if err != nil || int64(len(data)) > responseLimit {
		return errors.New("peer response exceeded limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(response); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("peer response contains trailing data")
	}
	return nil
}

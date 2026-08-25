package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type SiteMigrationRemoteSource struct {
	pairing *SiteMigrationPairingService
}

func (s *SiteMigrationRemoteSource) WriteFileShard(ctx context.Context, peerID, taskID string, index, expectedEntries int, expectedBytes int64, dst io.Writer) error {
	if index < 0 || expectedEntries <= 0 || expectedBytes <= 0 || expectedBytes > SiteMigrationFileShardBytes || dst == nil {
		return errors.New("invalid remote migration file shard")
	}
	peer, err := s.peer(ctx, peerID)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{"peer_id": peerID, "migration_site_id": taskID, "shard_index": index})
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, peer.baseURL+"/api/site-migration/v1/source/file-shard", strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+peer.credential)
	client, err := pinnedMigrationStreamingClient(peer.fingerprint)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/zstd" {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, siteMigrationMaxResponse))
		return fmt.Errorf("peer returned invalid file shard response")
	}
	shardIndex, indexErr := strconv.Atoi(response.Header.Get("X-WP-Panel-Shard-Index"))
	entryCount, countErr := strconv.Atoi(response.Header.Get("X-WP-Panel-Shard-Entries"))
	bytes, bytesErr := strconv.ParseInt(response.Header.Get("X-WP-Panel-Shard-Uncompressed-Bytes"), 10, 64)
	if indexErr != nil || countErr != nil || bytesErr != nil || shardIndex != index || entryCount != expectedEntries || bytes != expectedBytes {
		return errors.New("remote file shard metadata changed")
	}
	_, err = io.Copy(dst, response.Body)
	return err
}

func NewSiteMigrationRemoteSource(pairing *SiteMigrationPairingService) (*SiteMigrationRemoteSource, error) {
	if pairing == nil {
		return nil, errors.New("site migration remote source unavailable")
	}
	return &SiteMigrationRemoteSource{pairing: pairing}, nil
}

type siteMigrationRemotePeer struct {
	baseURL, fingerprint, credential string
}

func (s *SiteMigrationRemoteSource) peer(ctx context.Context, peerID string) (siteMigrationRemotePeer, error) {
	var peer siteMigrationRemotePeer
	var status string
	var protocol int
	if !validSiteMigrationID(peerID) || s.pairing.db.QueryRowContext(ctx, `SELECT base_url,certificate_sha256,outbound_credential,status,protocol_version FROM site_migration_peers WHERE id=?`, peerID).Scan(&peer.baseURL, &peer.fingerprint, &peer.credential, &status, &protocol) != nil || status != "paired" || protocol != siteMigrationProtocolVersion {
		return peer, ErrSiteMigrationPairRejected
	}
	return peer, nil
}

func (s *SiteMigrationRemoteSource) ListManifest(ctx context.Context, peerID, taskID string, after int64, limit int) ([]SiteMigrationManifestEntry, int64, bool, error) {
	peer, err := s.peer(ctx, peerID)
	if err != nil {
		return nil, 0, false, err
	}
	request := map[string]any{"peer_id": peerID, "migration_site_id": taskID, "after_id": after, "limit": limit}
	var response struct {
		Success bool                         `json:"success"`
		Entries []SiteMigrationManifestEntry `json:"entries"`
		Next    int64                        `json:"next_after_id"`
		HasMore bool                         `json:"has_more"`
	}
	if err := postPinnedJSON(ctx, peer.baseURL+"/api/site-migration/v1/source/manifest", peer.fingerprint, peer.credential, request, &response, s.pairing.httpTimeout); err != nil || !response.Success {
		return nil, 0, false, ErrSiteMigrationPairRejected
	}
	return response.Entries, response.Next, response.HasMore, nil
}

func (s *SiteMigrationRemoteSource) DatabaseArtifact(ctx context.Context, peerID, taskID string) (SiteMigrationManifestEntry, error) {
	peer, err := s.peer(ctx, peerID)
	if err != nil {
		return SiteMigrationManifestEntry{}, err
	}
	var response struct {
		Success  bool                       `json:"success"`
		Artifact SiteMigrationManifestEntry `json:"artifact"`
	}
	request := map[string]string{"peer_id": peerID, "migration_site_id": taskID}
	if err := postPinnedJSON(ctx, peer.baseURL+"/api/site-migration/v1/source/database", peer.fingerprint, peer.credential, request, &response, s.pairing.httpTimeout); err != nil || !response.Success {
		return SiteMigrationManifestEntry{}, ErrSiteMigrationPairRejected
	}
	return response.Artifact, nil
}

func (s *SiteMigrationRemoteSource) CertificateArtifacts(ctx context.Context, peerID, taskID string) ([]SiteMigrationCertificateArtifact, error) {
	peer, err := s.peer(ctx, peerID)
	if err != nil {
		return nil, err
	}
	var response struct {
		Success   bool                               `json:"success"`
		Artifacts []SiteMigrationCertificateArtifact `json:"artifacts"`
	}
	request := map[string]string{"peer_id": peerID, "migration_site_id": taskID}
	if err := postPinnedJSON(ctx, peer.baseURL+"/api/site-migration/v1/source/certificates", peer.fingerprint, peer.credential, request, &response, s.pairing.httpTimeout); err != nil || !response.Success {
		return nil, ErrSiteMigrationPairRejected
	}
	return response.Artifacts, nil
}

func (s *SiteMigrationRemoteSource) RuntimeSettings(ctx context.Context, peerID, taskID string) (SiteMigrationRuntimeSettings, error) {
	peer, err := s.peer(ctx, peerID)
	if err != nil {
		return SiteMigrationRuntimeSettings{}, err
	}
	var response struct {
		Success  bool                         `json:"success"`
		Settings SiteMigrationRuntimeSettings `json:"settings"`
	}
	request := map[string]string{"peer_id": peerID, "migration_site_id": taskID}
	if err := postPinnedJSONLimit(ctx, peer.baseURL+"/api/site-migration/v1/source/settings", peer.fingerprint, peer.credential, request, &response, s.pairing.httpTimeout, 1<<20); err != nil || !response.Success || validateSiteMigrationRuntimeSettings(&response.Settings) != nil {
		return SiteMigrationRuntimeSettings{}, ErrSiteMigrationPairRejected
	}
	return response.Settings, nil
}

func (s *SiteMigrationRemoteSource) ReadFileChunk(ctx context.Context, peerID, taskID, relativePath string, offset, length int64) (*SiteMigrationFileChunk, error) {
	return s.readChunk(ctx, peerID, "/api/site-migration/v1/source/chunk", map[string]any{"peer_id": peerID, "migration_site_id": taskID, "relative_path": relativePath, "offset": offset, "length": length}, relativePath, offset, length)
}

func (s *SiteMigrationRemoteSource) ReadDatabaseChunk(ctx context.Context, peerID, taskID string, offset, length int64) (*SiteMigrationFileChunk, error) {
	return s.readChunk(ctx, peerID, "/api/site-migration/v1/source/database-chunk", map[string]any{"peer_id": peerID, "migration_site_id": taskID, "offset": offset, "length": length}, siteMigrationDatabaseArtifactName, offset, length)
}

func (s *SiteMigrationRemoteSource) ReadCertificateChunk(ctx context.Context, peerID, taskID, artifactType string, offset, length int64) (*SiteMigrationFileChunk, error) {
	path := "certificate.pem"
	if artifactType == "private_key" {
		path = "private-key.pem"
	} else if artifactType != "certificate" {
		return nil, errors.New("invalid remote certificate artifact type")
	}
	return s.readChunk(ctx, peerID, "/api/site-migration/v1/source/certificate-chunk", map[string]any{"peer_id": peerID, "migration_site_id": taskID, "artifact_type": artifactType, "offset": offset, "length": length}, path, offset, length)
}

func (s *SiteMigrationRemoteSource) readChunk(ctx context.Context, peerID, route string, body map[string]any, relativePath string, offset, length int64) (*SiteMigrationFileChunk, error) {
	if offset < 0 || length <= 0 || length > SiteMigrationMaxChunkSize {
		return nil, errors.New("invalid remote migration chunk")
	}
	peer, err := s.peer(ctx, peerID)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, peer.baseURL+route, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+peer.credential)
	client, err := pinnedMigrationClient(peer.fingerprint, s.pairing.httpTimeout)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, siteMigrationMaxResponse))
		return nil, fmt.Errorf("peer returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, length+1))
	if err != nil || int64(len(data)) != length {
		return nil, errors.New("invalid remote migration chunk length")
	}
	chunkOffset, err := strconv.ParseInt(response.Header.Get("X-WP-Panel-Chunk-Offset"), 10, 64)
	if err != nil {
		return nil, errors.New("invalid remote migration chunk offset")
	}
	totalSize, err := strconv.ParseInt(response.Header.Get("X-WP-Panel-File-Size"), 10, 64)
	if err != nil || totalSize < 0 {
		return nil, errors.New("invalid remote migration file size")
	}
	fileHash, chunkHash := response.Header.Get("X-WP-Panel-File-SHA256"), response.Header.Get("X-WP-Panel-Chunk-SHA256")
	if !validMigrationSHA256(fileHash) || !validMigrationSHA256(chunkHash) {
		return nil, errors.New("invalid remote migration hash")
	}
	return &SiteMigrationFileChunk{RelativePath: relativePath, Offset: chunkOffset, TotalSize: totalSize, FileSHA256: fileHash, ChunkSHA256: chunkHash, Data: data}, nil
}

package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/executor"
)

type fakeSiteMigrationSource struct {
	manifestErr error
	chunkErr    error
}

type fakeSiteMigrationWorkflow struct {
	called bool
}

func (f *fakeSiteMigrationWorkflow) Start(_ context.Context, peerID, batchID, requestedBy string, siteIDs []int64) (*executor.SiteMigrationBatchPlanResult, error) {
	if peerID != "peer_00000000001" || batchID != "batch_0000000001" || requestedBy != "admin" || len(siteIDs) != 1 || siteIDs[0] != 7 {
		return nil, errors.New("unexpected start request")
	}
	f.called = true
	return &executor.SiteMigrationBatchPlanResult{BatchID: batchID}, nil
}

func (f *fakeSiteMigrationWorkflow) Estimate(context.Context, []int64) (executor.SiteMigrationEstimate, error) {
	return executor.SiteMigrationEstimate{}, nil
}

func TestSiteMigrationStartHandlerUsesAuthenticatedOperatorAndStrictJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	workflow := &fakeSiteMigrationWorkflow{}
	handler := &SiteMigrationHandler{Workflow: workflow}
	router := gin.New()
	router.POST("/start", func(c *gin.Context) { c.Set("session_username", "admin"); handler.Start(c) })
	req := httptest.NewRequest(http.MethodPost, "/start", strings.NewReader(`{"peer_id":"peer_00000000001","batch_id":"batch_0000000001","site_ids":[7]}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || !workflow.called {
		t.Fatalf("status=%d called=%v body=%s", recorder.Code, workflow.called, recorder.Body.String())
	}
	bad := httptest.NewRequest(http.MethodPost, "/start", strings.NewReader(`{"peer_id":"peer_00000000001","batch_id":"batch_0000000001","site_ids":[7],"unknown":true}`))
	bad.Header.Set("Content-Type", "application/json")
	badRecorder := httptest.NewRecorder()
	router.ServeHTTP(badRecorder, bad)
	if badRecorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d", badRecorder.Code)
	}
}

func (f *fakeSiteMigrationSource) ListManifest(_ context.Context, peerID, migrationSiteID, bearer string, afterID int64, limit int) ([]executor.SiteMigrationManifestEntry, int64, error) {
	if f.manifestErr != nil {
		return nil, 0, f.manifestErr
	}
	if peerID != "peer_00000000001" || migrationSiteID != "migration_0000001" || bearer != "secret" || afterID != 0 || limit != 1 {
		return nil, 0, errors.New("unexpected manifest request")
	}
	return []executor.SiteMigrationManifestEntry{{RelativePath: "index.php", EntryType: "file", Size: 3}}, 7, nil
}

func (f *fakeSiteMigrationSource) ReadFileChunk(_ context.Context, peerID, migrationSiteID, bearer, relativePath string, offset, length int64) (*executor.SiteMigrationFileChunk, error) {
	if f.chunkErr != nil {
		return nil, f.chunkErr
	}
	if peerID != "peer_00000000001" || migrationSiteID != "migration_0000001" || bearer != "secret" || relativePath != "index.php" || offset != 0 || length != 3 {
		return nil, errors.New("unexpected chunk request")
	}
	data := []byte("php")
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	return &executor.SiteMigrationFileChunk{Offset: 0, TotalSize: 3, FileSHA256: hash, ChunkSHA256: hash, Data: data}, nil
}

func (f *fakeSiteMigrationSource) FileShard(_ context.Context, peerID, migrationSiteID, bearer string, index int) (*executor.SiteMigrationFileShard, error) {
	if peerID != "peer_00000000001" || migrationSiteID != "migration_0000001" || bearer != "secret" || index != 0 {
		return nil, errors.New("unexpected shard request")
	}
	return &executor.SiteMigrationFileShard{Index: 0, EntryCount: 1, UncompressedBytes: 3, WriteTo: func(w io.Writer) error { _, err := w.Write([]byte("zstd")); return err }}, nil
}

func (f *fakeSiteMigrationSource) ReadDatabaseChunk(_ context.Context, peerID, migrationSiteID, bearer string, offset, length int64) (*executor.SiteMigrationFileChunk, error) {
	return f.ReadFileChunk(context.Background(), peerID, migrationSiteID, bearer, "index.php", offset, length)
}

func (f *fakeSiteMigrationSource) GetDatabaseArtifact(_ context.Context, peerID, migrationSiteID, bearer string) (executor.SiteMigrationManifestEntry, error) {
	if peerID != "peer_00000000001" || migrationSiteID != "migration_0000001" || bearer != "secret" {
		return executor.SiteMigrationManifestEntry{}, errors.New("unexpected database request")
	}
	return executor.SiteMigrationManifestEntry{RelativePath: "database.sql.gz", EntryType: "file", Size: 13, SHA256: strings.Repeat("a", 64)}, nil
}

func (f *fakeSiteMigrationSource) ListCertificateArtifacts(_ context.Context, peerID, migrationSiteID, bearer string) ([]executor.SiteMigrationCertificateArtifact, error) {
	if peerID != "peer_00000000001" || migrationSiteID != "migration_0000001" || bearer != "secret" {
		return nil, errors.New("unexpected certificate request")
	}
	return []executor.SiteMigrationCertificateArtifact{{ArtifactType: "certificate", RelativePath: "certificate.pem", Size: 3, SHA256: strings.Repeat("a", 64)}}, nil
}

func (f *fakeSiteMigrationSource) ReadCertificateChunk(_ context.Context, peerID, migrationSiteID, bearer, artifactType string, offset, length int64) (*executor.SiteMigrationFileChunk, error) {
	if artifactType != "certificate" {
		return nil, errors.New("unexpected certificate type")
	}
	return f.ReadFileChunk(context.Background(), peerID, migrationSiteID, bearer, "index.php", offset, length)
}

func (f *fakeSiteMigrationSource) GetRuntimeSettings(context.Context, string, string, string) (executor.SiteMigrationRuntimeSettings, error) {
	return executor.SiteMigrationRuntimeSettings{}, nil
}

func TestSiteMigrationSourceManifestHandler(t *testing.T) {
	recorder := performSiteMigrationSourceRequest(t, "/manifest", `{"peer_id":"peer_00000000001","migration_site_id":"migration_0000001","after_id":0,"limit":1}`, &fakeSiteMigrationSource{})
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%s", recorder.Code, recorder.Header().Get("Cache-Control"), recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"relative_path":"index.php"`) || !strings.Contains(recorder.Body.String(), `"next_after_id":7`) {
		t.Fatalf("unexpected body: %s", recorder.Body.String())
	}
}

func TestSiteMigrationSourceChunkHandler(t *testing.T) {
	recorder := performSiteMigrationSourceRequest(t, "/chunk", `{"peer_id":"peer_00000000001","migration_site_id":"migration_0000001","relative_path":"index.php","offset":0,"length":3}`, &fakeSiteMigrationSource{})
	if recorder.Code != http.StatusOK || recorder.Body.String() != "php" || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%q", recorder.Code, recorder.Header().Get("Cache-Control"), recorder.Body.String())
	}
	if recorder.Header().Get("X-WP-Panel-File-SHA256") == "" || recorder.Header().Get("X-WP-Panel-Chunk-SHA256") == "" {
		t.Fatal("missing integrity headers")
	}
}

func TestSiteMigrationSourceFileShardHandler(t *testing.T) {
	recorder := performSiteMigrationSourceRequest(t, "/file-shard", `{"peer_id":"peer_00000000001","migration_site_id":"migration_0000001","shard_index":0}`, &fakeSiteMigrationSource{})
	if recorder.Code != http.StatusOK || recorder.Body.String() != "zstd" || recorder.Header().Get("Content-Type") != "application/zstd" || recorder.Header().Get("X-WP-Panel-Shard-Entries") != "1" {
		t.Fatalf("status=%d headers=%v body=%q", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestSiteMigrationSourceDatabaseChunkHandler(t *testing.T) {
	recorder := performSiteMigrationSourceRequest(t, "/database-chunk", `{"peer_id":"peer_00000000001","migration_site_id":"migration_0000001","offset":0,"length":3}`, &fakeSiteMigrationSource{})
	if recorder.Code != http.StatusOK || recorder.Body.String() != "php" || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%q", recorder.Code, recorder.Header().Get("Cache-Control"), recorder.Body.String())
	}
}

func TestSiteMigrationSourceDatabaseHandler(t *testing.T) {
	recorder := performSiteMigrationSourceRequest(t, "/database", `{"peer_id":"peer_00000000001","migration_site_id":"migration_0000001"}`, &fakeSiteMigrationSource{})
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" || !strings.Contains(recorder.Body.String(), `"size":13`) {
		t.Fatalf("status=%d cache=%q body=%q", recorder.Code, recorder.Header().Get("Cache-Control"), recorder.Body.String())
	}
}

func TestSiteMigrationSourceCertificateHandlers(t *testing.T) {
	metadata := performSiteMigrationSourceRequest(t, "/certificates", `{"peer_id":"peer_00000000001","migration_site_id":"migration_0000001"}`, &fakeSiteMigrationSource{})
	if metadata.Code != http.StatusOK || metadata.Header().Get("Cache-Control") != "no-store" || !strings.Contains(metadata.Body.String(), `"artifact_type":"certificate"`) {
		t.Fatalf("status=%d cache=%q body=%q", metadata.Code, metadata.Header().Get("Cache-Control"), metadata.Body.String())
	}
	chunk := performSiteMigrationSourceRequest(t, "/certificate-chunk", `{"peer_id":"peer_00000000001","migration_site_id":"migration_0000001","artifact_type":"certificate","offset":0,"length":3}`, &fakeSiteMigrationSource{})
	if chunk.Code != http.StatusOK || chunk.Body.String() != "php" || chunk.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%q", chunk.Code, chunk.Header().Get("Cache-Control"), chunk.Body.String())
	}
}

func TestSiteMigrationSourceHandlersRejectInvalidOrUnauthorizedRequests(t *testing.T) {
	invalid := performSiteMigrationSourceRequest(t, "/manifest", `{"peer_id":"peer_00000000001","unknown":true}`, &fakeSiteMigrationSource{})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON status=%d", invalid.Code)
	}
	denied := performSiteMigrationSourceRequest(t, "/chunk", `{"peer_id":"peer_00000000001","migration_site_id":"migration_0000001","relative_path":"index.php","offset":0,"length":3}`, &fakeSiteMigrationSource{chunkErr: errors.New("denied")})
	if denied.Code != http.StatusUnauthorized || strings.Contains(denied.Body.String(), "denied") {
		t.Fatalf("authorization response leaked detail: status=%d body=%s", denied.Code, denied.Body.String())
	}
}

func performSiteMigrationSourceRequest(t *testing.T, path, body string, source siteMigrationSourceAPI) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &SiteMigrationHandler{Source: source}
	router.POST("/manifest", handler.SourceManifest)
	router.POST("/chunk", handler.SourceChunk)
	router.POST("/file-shard", handler.SourceFileShard)
	router.POST("/database-chunk", handler.SourceDatabaseChunk)
	router.POST("/database", handler.SourceDatabase)
	router.POST("/certificates", handler.SourceCertificates)
	router.POST("/certificate-chunk", handler.SourceCertificateChunk)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

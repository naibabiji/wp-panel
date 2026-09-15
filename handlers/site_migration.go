package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"
)

const siteMigrationPairBodyLimit = 32 << 10
const siteMigrationBatchBodyLimit = 1 << 20

type SiteMigrationHandler struct {
	Service  *executor.SiteMigrationPairingService
	Source   siteMigrationSourceAPI
	Planner  siteMigrationTargetBatchAPI
	Workflow interface {
		Start(context.Context, string, string, string, []int64) (*executor.SiteMigrationBatchPlanResult, error)
		Estimate(context.Context, []int64) (executor.SiteMigrationEstimate, error)
	}
	Control *executor.SiteMigrationControlService
	DB      *sql.DB
	Version string
}

func (h *SiteMigrationHandler) Estimate(c *gin.Context) {
	var req struct {
		SiteIDs []int64 `json:"site_ids"`
	}
	if !decodeSiteMigrationJSONLimit(c, &req, siteMigrationBatchBodyLimit) {
		return
	}
	if h.Workflow == nil {
		siteMigrationError(c, http.StatusServiceUnavailable, "common.operation_failed")
		return
	}
	estimate, err := h.Workflow.Estimate(c.Request.Context(), req.SiteIDs)
	if err != nil {
		siteMigrationError(c, http.StatusBadRequest, "common.operation_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "estimate": estimate})
}

func (h *SiteMigrationHandler) CompleteSource(c *gin.Context) {
	if h.Control == nil || h.Control.CompleteSource(c.Request.Context(), c.Param("id")) != nil {
		siteMigrationError(c, http.StatusConflict, "common.operation_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *SiteMigrationHandler) RestoreSource(c *gin.Context) {
	if h.Control == nil || h.Control.RestoreSource(c.Request.Context(), c.Param("id")) != nil {
		siteMigrationError(c, http.StatusConflict, "common.operation_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *SiteMigrationHandler) DeleteTask(c *gin.Context) {
	if h.Control == nil || h.Control.DeleteTask(c.Request.Context(), c.Param("id")) != nil {
		siteMigrationError(c, http.StatusConflict, "common.operation_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *SiteMigrationHandler) MachineDeleteTargetTask(c *gin.Context) {
	var req struct {
		PeerID          string `json:"peer_id"`
		MigrationSiteID string `json:"migration_site_id"`
	}
	if !decodeSiteMigrationJSON(c, &req) {
		return
	}
	if h.Control == nil || h.Service == nil || h.Service.AuthorizePeer(c.Request.Context(), req.PeerID, bearerFromMigrationRequest(c)) != nil ||
		h.Control.AuthorizeMachineTask(c.Request.Context(), req.PeerID, req.MigrationSiteID, "target") != nil ||
		h.Control.DeleteTargetTask(c.Request.Context(), req.MigrationSiteID) != nil {
		siteMigrationError(c, http.StatusUnauthorized, "common.operation_failed")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *SiteMigrationHandler) MachineDeleteSourceTask(c *gin.Context) {
	var req struct {
		PeerID          string `json:"peer_id"`
		MigrationSiteID string `json:"migration_site_id"`
	}
	if !decodeSiteMigrationJSON(c, &req) {
		return
	}
	if h.Control == nil || h.Service == nil || h.Service.AuthorizePeer(c.Request.Context(), req.PeerID, bearerFromMigrationRequest(c)) != nil ||
		h.Control.AuthorizeMachineTask(c.Request.Context(), req.PeerID, req.MigrationSiteID, "source") != nil ||
		h.Control.DeleteSourceTask(c.Request.Context(), req.MigrationSiteID) != nil {
		siteMigrationError(c, http.StatusUnauthorized, "common.operation_failed")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *SiteMigrationHandler) MachineTargetStatus(c *gin.Context) {
	var req struct {
		PeerID           string   `json:"peer_id"`
		MigrationSiteIDs []string `json:"migration_site_ids"`
	}
	if !decodeSiteMigrationJSONLimit(c, &req, siteMigrationBatchBodyLimit) {
		return
	}
	if h.Control == nil || h.Service == nil || h.Service.AuthorizePeer(c.Request.Context(), req.PeerID, bearerFromMigrationRequest(c)) != nil {
		siteMigrationError(c, http.StatusUnauthorized, "common.operation_failed")
		return
	}
	tasks, err := h.Control.TargetStatuses(c.Request.Context(), req.PeerID, req.MigrationSiteIDs)
	if err != nil {
		siteMigrationError(c, http.StatusNotFound, "common.operation_failed")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"success": true, "tasks": tasks})
}

func (h *SiteMigrationHandler) MachineTargetRetry(c *gin.Context) {
	var req struct {
		PeerID          string `json:"peer_id"`
		MigrationSiteID string `json:"migration_site_id"`
	}
	if !decodeSiteMigrationJSON(c, &req) {
		return
	}
	if h.Control == nil || h.Service == nil || h.Service.AuthorizePeer(c.Request.Context(), req.PeerID, bearerFromMigrationRequest(c)) != nil || h.Control.AuthorizeMachineTask(c.Request.Context(), req.PeerID, req.MigrationSiteID, "target") != nil {
		siteMigrationError(c, http.StatusUnauthorized, "common.operation_failed")
		return
	}
	if h.Control.RetryTarget(c.Request.Context(), req.MigrationSiteID) != nil {
		siteMigrationError(c, http.StatusConflict, "common.operation_failed")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *SiteMigrationHandler) Retry(c *gin.Context) {
	if h.Control == nil {
		siteMigrationError(c, http.StatusServiceUnavailable, "common.operation_failed")
		return
	}
	if err := h.Control.Retry(c.Request.Context(), c.Param("id")); err != nil {
		siteMigrationError(c, http.StatusConflict, "common.operation_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *SiteMigrationHandler) Start(c *gin.Context) {
	var req struct {
		PeerID  string  `json:"peer_id"`
		BatchID string  `json:"batch_id"`
		SiteIDs []int64 `json:"site_ids"`
	}
	if !decodeSiteMigrationJSONLimit(c, &req, siteMigrationBatchBodyLimit) {
		return
	}
	for _, siteID := range req.SiteIDs {
		if siteID <= 0 || rejectIfAIDevelopmentAccessActive(c, int(siteID)) {
			return
		}
	}
	username, _ := c.Get("session_username")
	actor, _ := username.(string)
	if h.Workflow == nil || strings.TrimSpace(actor) == "" {
		siteMigrationError(c, http.StatusUnauthorized, "common.operation_failed")
		return
	}
	result, err := h.Workflow.Start(c.Request.Context(), req.PeerID, req.BatchID, actor, req.SiteIDs)
	if err != nil {
		siteMigrationError(c, http.StatusBadRequest, "common.operation_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "batch": result})
}

type siteMigrationTargetBatchAPI interface {
	CreateTargetBatch(context.Context, string, string, []executor.SiteMigrationBatchSitePlan) (*executor.SiteMigrationBatchPlanResult, error)
	CreateTargetBatchWithID(context.Context, string, string, string, []executor.SiteMigrationBatchSitePlan) (*executor.SiteMigrationBatchPlanResult, error)
	QueueTargetBatchForPeer(context.Context, string, string) error
	QueueTargetSiteForPeer(context.Context, string, string, string) error
}

func (h *SiteMigrationHandler) MachineQueueTargetBatch(c *gin.Context) {
	var req struct {
		PeerID          string `json:"peer_id"`
		BatchID         string `json:"batch_id"`
		MigrationSiteID string `json:"migration_site_id"`
	}
	if !decodeSiteMigrationJSON(c, &req) {
		return
	}
	if h.Planner == nil || h.Service.AuthorizePeer(c.Request.Context(), req.PeerID, bearerFromMigrationRequest(c)) != nil {
		siteMigrationError(c, http.StatusUnauthorized, "common.operation_failed")
		return
	}
	if err := h.Planner.QueueTargetSiteForPeer(c.Request.Context(), req.PeerID, req.BatchID, req.MigrationSiteID); err != nil {
		siteMigrationError(c, http.StatusBadRequest, "common.operation_failed")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"success": true})
}

type siteMigrationSourceAPI interface {
	ListManifest(context.Context, string, string, string, int64, int) ([]executor.SiteMigrationManifestEntry, int64, error)
	ReadFileChunk(context.Context, string, string, string, string, int64, int64) (*executor.SiteMigrationFileChunk, error)
	FileShard(context.Context, string, string, string, int) (*executor.SiteMigrationFileShard, error)
	ReadDatabaseChunk(context.Context, string, string, string, int64, int64) (*executor.SiteMigrationFileChunk, error)
	GetDatabaseArtifact(context.Context, string, string, string) (executor.SiteMigrationManifestEntry, error)
	ListCertificateArtifacts(context.Context, string, string, string) ([]executor.SiteMigrationCertificateArtifact, error)
	ReadCertificateChunk(context.Context, string, string, string, string, int64, int64) (*executor.SiteMigrationFileChunk, error)
	GetRuntimeSettings(context.Context, string, string, string) (executor.SiteMigrationRuntimeSettings, error)
}

func (h *SiteMigrationHandler) SourceSettings(c *gin.Context) {
	var req struct {
		PeerID          string `json:"peer_id"`
		MigrationSiteID string `json:"migration_site_id"`
	}
	if !decodeSiteMigrationJSON(c, &req) {
		return
	}
	settings, err := h.Source.GetRuntimeSettings(c.Request.Context(), req.PeerID, req.MigrationSiteID, bearerFromMigrationRequest(c))
	if err != nil {
		siteMigrationError(c, http.StatusUnauthorized, "common.operation_failed")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"success": true, "settings": settings})
}

type siteMigrationManifestRequest struct {
	PeerID          string `json:"peer_id"`
	MigrationSiteID string `json:"migration_site_id"`
	AfterID         int64  `json:"after_id"`
	Limit           int    `json:"limit"`
}

type siteMigrationChunkRequest struct {
	PeerID          string `json:"peer_id"`
	MigrationSiteID string `json:"migration_site_id"`
	RelativePath    string `json:"relative_path"`
	Offset          int64  `json:"offset"`
	Length          int64  `json:"length"`
}

type siteMigrationDatabaseChunkRequest struct {
	PeerID          string `json:"peer_id"`
	MigrationSiteID string `json:"migration_site_id"`
	Offset          int64  `json:"offset"`
	Length          int64  `json:"length"`
}

func (h *SiteMigrationHandler) MachineCreateTargetBatch(c *gin.Context) {
	var req struct {
		PeerID      string                                `json:"peer_id"`
		BatchID     string                                `json:"batch_id"`
		RequestedBy string                                `json:"requested_by"`
		Sites       []executor.SiteMigrationBatchSitePlan `json:"sites"`
	}
	if !decodeSiteMigrationJSONLimit(c, &req, siteMigrationBatchBodyLimit) {
		return
	}
	if h.Planner == nil || h.Service.AuthorizePeer(c.Request.Context(), req.PeerID, bearerFromMigrationRequest(c)) != nil {
		siteMigrationError(c, http.StatusUnauthorized, "common.operation_failed")
		return
	}
	result, err := h.Planner.CreateTargetBatchWithID(c.Request.Context(), req.PeerID, req.BatchID, req.RequestedBy, req.Sites)
	if err != nil {
		siteMigrationError(c, http.StatusBadRequest, "common.operation_failed")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"success": true, "batch": result})
}

func (h *SiteMigrationHandler) SourceManifest(c *gin.Context) {
	var req siteMigrationManifestRequest
	if !decodeSiteMigrationJSON(c, &req) {
		return
	}
	entries, next, err := h.Source.ListManifest(c.Request.Context(), req.PeerID, req.MigrationSiteID, bearerFromMigrationRequest(c), req.AfterID, req.Limit)
	if err != nil {
		siteMigrationError(c, http.StatusUnauthorized, "common.operation_failed")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"success": true, "entries": entries, "next_after_id": next, "has_more": len(entries) == req.Limit})
}

func (h *SiteMigrationHandler) SourceChunk(c *gin.Context) {
	var req siteMigrationChunkRequest
	if !decodeSiteMigrationJSON(c, &req) {
		return
	}
	chunk, err := h.Source.ReadFileChunk(c.Request.Context(), req.PeerID, req.MigrationSiteID, bearerFromMigrationRequest(c), req.RelativePath, req.Offset, req.Length)
	if err != nil {
		siteMigrationError(c, http.StatusUnauthorized, "common.operation_failed")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-WP-Panel-File-SHA256", chunk.FileSHA256)
	c.Header("X-WP-Panel-Chunk-SHA256", chunk.ChunkSHA256)
	c.Header("X-WP-Panel-Chunk-Offset", strconv.FormatInt(chunk.Offset, 10))
	c.Header("X-WP-Panel-File-Size", strconv.FormatInt(chunk.TotalSize, 10))
	c.Data(http.StatusOK, "application/octet-stream", chunk.Data)
}

func (h *SiteMigrationHandler) SourceFileShard(c *gin.Context) {
	var req struct {
		PeerID          string `json:"peer_id"`
		MigrationSiteID string `json:"migration_site_id"`
		ShardIndex      int    `json:"shard_index"`
	}
	if !decodeSiteMigrationJSON(c, &req) {
		return
	}
	shard, err := h.Source.FileShard(c.Request.Context(), req.PeerID, req.MigrationSiteID, bearerFromMigrationRequest(c), req.ShardIndex)
	if err != nil {
		siteMigrationError(c, http.StatusUnauthorized, "common.operation_failed")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-WP-Panel-Shard-Index", strconv.Itoa(shard.Index))
	c.Header("X-WP-Panel-Shard-Entries", strconv.Itoa(shard.EntryCount))
	c.Header("X-WP-Panel-Shard-Uncompressed-Bytes", strconv.FormatInt(shard.UncompressedBytes, 10))
	c.Header("Content-Type", "application/zstd")
	c.Status(http.StatusOK)
	if err := shard.WriteTo(c.Writer); err != nil {
		_ = c.Error(err)
	}
}

func (h *SiteMigrationHandler) SourceDatabaseChunk(c *gin.Context) {
	var req siteMigrationDatabaseChunkRequest
	if !decodeSiteMigrationJSON(c, &req) {
		return
	}
	chunk, err := h.Source.ReadDatabaseChunk(c.Request.Context(), req.PeerID, req.MigrationSiteID, bearerFromMigrationRequest(c), req.Offset, req.Length)
	if err != nil {
		siteMigrationError(c, http.StatusUnauthorized, "common.operation_failed")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-WP-Panel-File-SHA256", chunk.FileSHA256)
	c.Header("X-WP-Panel-Chunk-SHA256", chunk.ChunkSHA256)
	c.Header("X-WP-Panel-Chunk-Offset", strconv.FormatInt(chunk.Offset, 10))
	c.Header("X-WP-Panel-File-Size", strconv.FormatInt(chunk.TotalSize, 10))
	c.Data(http.StatusOK, "application/octet-stream", chunk.Data)
}

func (h *SiteMigrationHandler) SourceDatabase(c *gin.Context) {
	var req struct {
		PeerID          string `json:"peer_id"`
		MigrationSiteID string `json:"migration_site_id"`
	}
	if !decodeSiteMigrationJSON(c, &req) {
		return
	}
	artifact, err := h.Source.GetDatabaseArtifact(c.Request.Context(), req.PeerID, req.MigrationSiteID, bearerFromMigrationRequest(c))
	if err != nil {
		siteMigrationError(c, http.StatusUnauthorized, "common.operation_failed")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"success": true, "artifact": artifact})
}

func (h *SiteMigrationHandler) SourceCertificates(c *gin.Context) {
	var req struct {
		PeerID          string `json:"peer_id"`
		MigrationSiteID string `json:"migration_site_id"`
	}
	if !decodeSiteMigrationJSON(c, &req) {
		return
	}
	artifacts, err := h.Source.ListCertificateArtifacts(c.Request.Context(), req.PeerID, req.MigrationSiteID, bearerFromMigrationRequest(c))
	if err != nil {
		siteMigrationError(c, http.StatusUnauthorized, "common.operation_failed")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"success": true, "artifacts": artifacts})
}

func (h *SiteMigrationHandler) SourceCertificateChunk(c *gin.Context) {
	var req struct {
		PeerID          string `json:"peer_id"`
		MigrationSiteID string `json:"migration_site_id"`
		ArtifactType    string `json:"artifact_type"`
		Offset          int64  `json:"offset"`
		Length          int64  `json:"length"`
	}
	if !decodeSiteMigrationJSON(c, &req) {
		return
	}
	chunk, err := h.Source.ReadCertificateChunk(c.Request.Context(), req.PeerID, req.MigrationSiteID, bearerFromMigrationRequest(c), req.ArtifactType, req.Offset, req.Length)
	if err != nil {
		siteMigrationError(c, http.StatusUnauthorized, "common.operation_failed")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-WP-Panel-File-SHA256", chunk.FileSHA256)
	c.Header("X-WP-Panel-Chunk-SHA256", chunk.ChunkSHA256)
	c.Header("X-WP-Panel-Chunk-Offset", strconv.FormatInt(chunk.Offset, 10))
	c.Header("X-WP-Panel-File-Size", strconv.FormatInt(chunk.TotalSize, 10))
	c.Data(http.StatusOK, "application/octet-stream", chunk.Data)
}

func (h *SiteMigrationHandler) MachinePreflight(c *gin.Context) {
	var req executor.SiteMigrationPreflightRequest
	if !decodeSiteMigrationJSON(c, &req) {
		return
	}
	if len(req.Domains) == 0 || len(req.Domains) > 500 {
		siteMigrationError(c, http.StatusBadRequest, "common.invalid_params")
		return
	}
	if h.Service.AuthorizePeer(c.Request.Context(), req.PeerID, bearerFromMigrationRequest(c)) != nil {
		siteMigrationError(c, http.StatusUnauthorized, "common.operation_failed")
		return
	}
	conflicts := make([]executor.SiteMigrationPreflightConflict, 0)
	for _, domain := range req.Domains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if !executor.IsValidDomain(domain) {
			siteMigrationError(c, http.StatusBadRequest, "common.invalid_params")
			return
		}
		var siteType string
		err := h.DB.QueryRowContext(c.Request.Context(), `SELECT site_type FROM websites
			WHERE lower(domain)=? OR (char(10)||lower(aliases)||char(10)) LIKE ('%'||char(10)||?||char(10)||'%') ESCAPE '\' LIMIT 1`,
			domain, escapeLike(domain)).Scan(&siteType)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			siteMigrationError(c, http.StatusInternalServerError, "common.operation_failed")
			return
		}
		if err == nil {
			conflicts = append(conflicts, executor.SiteMigrationPreflightConflict{Domain: domain, SiteType: siteType})
		}
	}
	c.JSON(http.StatusOK, executor.SiteMigrationPreflightResponse{Success: true, PanelVersion: h.Version, Conflicts: conflicts})
}

func (h *SiteMigrationHandler) RemotePreflight(c *gin.Context) {
	var req executor.SiteMigrationPreflightRequest
	if !decodeSiteMigrationJSON(c, &req) {
		return
	}
	domains := make([]string, 0, len(req.Domains)*2)
	seen := make(map[string]struct{})
	for _, requested := range req.Domains {
		requested = strings.ToLower(strings.TrimSpace(requested))
		if !executor.IsValidDomain(requested) {
			siteMigrationError(c, http.StatusBadRequest, "common.operation_failed")
			return
		}
		var primary, aliases string
		if err := h.DB.QueryRowContext(c.Request.Context(), `SELECT domain,aliases FROM websites WHERE lower(domain)=? LIMIT 1`, requested).
			Scan(&primary, &aliases); err != nil {
			siteMigrationError(c, http.StatusBadRequest, "common.operation_failed")
			return
		}
		for _, value := range append([]string{primary}, strings.Split(aliases, "\n")...) {
			value = strings.ToLower(strings.TrimSpace(value))
			if executor.IsValidDomain(value) {
				if _, exists := seen[value]; !exists {
					seen[value] = struct{}{}
					domains = append(domains, value)
				}
			}
		}
	}
	response, err := h.Service.RemotePreflight(c.Request.Context(), c.Param("id"), domains)
	if err != nil {
		siteMigrationError(c, http.StatusBadRequest, "common.operation_failed")
		return
	}
	c.JSON(http.StatusOK, response)
}

type siteMigrationPackageRequest struct {
	BaseURL string `json:"base_url"`
}

type siteMigrationConnectRequest struct {
	Package       executor.SiteMigrationPairingPackage `json:"package"`
	SourceBaseURL string                               `json:"source_base_url"`
}

func (h *SiteMigrationHandler) GeneratePackage(c *gin.Context) {
	var req siteMigrationPackageRequest
	if !decodeSiteMigrationJSON(c, &req) {
		return
	}
	pkg, err := h.Service.GeneratePackage(c.Request.Context(), req.BaseURL)
	if err != nil {
		siteMigrationError(c, http.StatusBadRequest, "common.invalid_params")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "package": pkg})
}

func (h *SiteMigrationHandler) Connect(c *gin.Context) {
	var req siteMigrationConnectRequest
	if !decodeSiteMigrationJSON(c, &req) {
		return
	}
	if err := h.Service.Connect(c.Request.Context(), req.Package, req.SourceBaseURL); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, executor.ErrSiteMigrationPairConflict) {
			status = http.StatusConflict
		}
		siteMigrationError(c, status, "common.operation_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *SiteMigrationHandler) Redeem(c *gin.Context) {
	var req executor.SiteMigrationRedeemRequest
	if !decodeSiteMigrationJSON(c, &req) {
		return
	}
	if err := h.Service.Redeem(c.Request.Context(), req); err != nil {
		siteMigrationError(c, http.StatusUnauthorized, "common.operation_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *SiteMigrationHandler) Challenge(c *gin.Context) {
	var req executor.SiteMigrationChallengeRequest
	if !decodeSiteMigrationJSON(c, &req) {
		return
	}
	authorization := c.GetHeader("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") ||
		h.Service.VerifyChallenge(c.Request.Context(), req.PeerID, strings.TrimPrefix(authorization, "Bearer "), req) != nil {
		siteMigrationError(c, http.StatusUnauthorized, "common.operation_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *SiteMigrationHandler) ListPeers(c *gin.Context) {
	peers, err := h.Service.ListPeers(c.Request.Context())
	if err != nil {
		siteMigrationError(c, http.StatusInternalServerError, "common.operation_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "peers": peers})
}

func (h *SiteMigrationHandler) ListTasks(c *gin.Context) {
	var tasks []executor.SiteMigrationTaskSummary
	var err error
	if h.Control != nil {
		tasks, err = h.Control.ListTasks(c.Request.Context())
	} else {
		tasks, err = h.Service.ListTasks(c.Request.Context())
	}
	if err != nil {
		siteMigrationError(c, http.StatusInternalServerError, "common.operation_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "tasks": tasks})
}

func (h *SiteMigrationHandler) RevokePeer(c *gin.Context) {
	if err := h.Service.RevokePeerEverywhere(c.Request.Context(), c.Param("id")); err != nil {
		siteMigrationError(c, http.StatusNotFound, "common.operation_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *SiteMigrationHandler) MachineRevokePeer(c *gin.Context) {
	var req struct {
		PeerID string `json:"peer_id"`
	}
	if !decodeSiteMigrationJSON(c, &req) {
		return
	}
	if h.Service.AuthorizePeer(c.Request.Context(), req.PeerID, bearerFromMigrationRequest(c)) != nil {
		siteMigrationError(c, http.StatusUnauthorized, "common.operation_failed")
		return
	}
	if err := h.Service.RevokePeer(c.Request.Context(), req.PeerID); err != nil {
		siteMigrationError(c, http.StatusConflict, "common.operation_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func decodeSiteMigrationJSON(c *gin.Context, target any) bool {
	return decodeSiteMigrationJSONLimit(c, target, siteMigrationPairBodyLimit)
}

func decodeSiteMigrationJSONLimit(c *gin.Context, target any, limit int64) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		siteMigrationError(c, http.StatusBadRequest, "common.invalid_params")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		siteMigrationError(c, http.StatusBadRequest, "common.invalid_params")
		return false
	}
	return true
}

func siteMigrationError(c *gin.Context, status int, key string) {
	c.JSON(status, models.ErrorResponse(i18n.TE(c.Request, key)))
}

func bearerFromMigrationRequest(c *gin.Context) string {
	value := c.GetHeader("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(value, "Bearer ")
}

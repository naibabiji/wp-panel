package handlers

import (
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

type SystemUpdateHandler struct {
	Config *config.Config
}

type systemPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Repo    string `json:"repo"`
}

var sysPkgCache struct {
	mu       sync.Mutex
	expireAt time.Time
	pkgs     []systemPackage
}

func (h *SystemUpdateHandler) Check(c *gin.Context) {
	sysPkgCache.mu.Lock()
	if c.Query("fresh") != "1" && time.Now().Before(sysPkgCache.expireAt) {
		pkgs := sysPkgCache.pkgs
		sysPkgCache.mu.Unlock()
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
			"packages": pkgs,
			"count":    len(pkgs),
		}))
		return
	}
	sysPkgCache.mu.Unlock()

	pkgs := getUpgradablePackages()

	sysPkgCache.mu.Lock()
	sysPkgCache.expireAt = time.Now().Add(5 * time.Minute)
	sysPkgCache.pkgs = pkgs
	sysPkgCache.mu.Unlock()

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"packages": pkgs,
		"count":    len(pkgs),
	}))
}

func (h *SystemUpdateHandler) Update(c *gin.Context) {
	status, err := executor.StartSystemPackageUpdate(h.Config)
	if err != nil {
		if strings.Contains(err.Error(), "正在执行") {
			c.JSON(http.StatusConflict, models.ErrorResponse(i18n.TE(c.Request, "settings.system_update_already_running")))
			return
		}
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "settings.system_update_start_failed")))
		return
	}
	c.JSON(http.StatusAccepted, models.SuccessResponse(systemUpdateStatusResponse(c, status)))
}

func (h *SystemUpdateHandler) Status(c *gin.Context) {
	status := executor.ReconcileSystemPackageUpdateStatus(h.Config)
	if status.Status == "success" {
		sysPkgCache.mu.Lock()
		sysPkgCache.expireAt = time.Time{}
		sysPkgCache.pkgs = nil
		sysPkgCache.mu.Unlock()
		executor.ClearSystemUpdateAlertCache()
	}
	c.JSON(http.StatusOK, models.SuccessResponse(systemUpdateStatusResponse(c, status)))
}

func systemUpdateStatusResponse(c *gin.Context, status executor.SystemPackageUpdateStatus) gin.H {
	message := ""
	if status.MessageKey != "" {
		message = i18n.TE(c.Request, status.MessageKey)
	}
	return gin.H{
		"id":          status.ID,
		"status":      status.Status,
		"stage":       status.Stage,
		"message":     message,
		"message_key": status.MessageKey,
		"started_at":  status.StartedAt,
		"updated_at":  status.UpdatedAt,
	}
}

func getUpgradablePackages() []systemPackage {
	out, err := exec.Command("bash", "-c", "apt list --upgradable 2>/dev/null").Output()
	if err != nil {
		return []systemPackage{}
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var pkgs []systemPackage
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Listing...") {
			continue
		}
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 2 {
			continue
		}
		nameRepo := strings.SplitN(parts[0], "/", 2)
		name := nameRepo[0]
		repo := ""
		if len(nameRepo) > 1 {
			repo = nameRepo[1]
		}
		pkgs = append(pkgs, systemPackage{
			Name:    name,
			Version: parts[1],
			Repo:    repo,
		})
	}
	if pkgs == nil {
		pkgs = []systemPackage{}
	}
	return pkgs
}

package handlers

import (
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

type SystemUpdateHandler struct{}

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
	if time.Now().Before(sysPkgCache.expireAt) {
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
	out1, err := exec.Command("bash", "-c", "apt update 2>&1").CombinedOutput()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("apt update 失败: "+string(out1)))
		return
	}

	out2, err := exec.Command("bash", "-c", "apt upgrade -y 2>&1").CombinedOutput()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("apt upgrade 失败: "+string(out2)))
		return
	}

	// clear cache so next check reflects updated state
	sysPkgCache.mu.Lock()
	sysPkgCache.expireAt = time.Time{}
	sysPkgCache.pkgs = nil
	sysPkgCache.mu.Unlock()
	executor.ClearSystemUpdateAlertCache()

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"message": "系统更新完成",
		"output":  string(out2),
	}))
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

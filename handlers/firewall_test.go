package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"
)

func TestListBanHistoryLimitsSearchAndPaginationToLatest300(t *testing.T) {
	setupSecurityTestDB(t)

	base := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 305; i++ {
		ip := fmt.Sprintf("198.51.100.%d", i)
		if i == 1 {
			ip = "old-only"
		}
		if i == 305 {
			ip = "search-target"
		}
		if _, err := database.GetDB().Exec(`INSERT INTO firewall_ban_history
			(ip_address, ban_level, reason, source_jail, banned_at, expires_at)
			VALUES (?, 2, 'test', 'wppanel', ?, ?)`,
			ip, base.Add(time.Duration(i)*time.Second), base.Add(time.Duration(i+600)*time.Second)); err != nil {
			t.Fatalf("insert ban %d: %v", i, err)
		}
	}

	page := requestBanHistory(t, "/firewall/bans?history=1&page=2")
	if page.Data.Total != firewallBanHistoryLimit || page.Data.TotalPages != 10 || page.Data.Page != 2 {
		t.Fatalf("pagination = total %d, pages %d, page %d", page.Data.Total, page.Data.TotalPages, page.Data.Page)
	}
	if !page.Data.HasMore {
		t.Fatal("default history window should advertise older retained records")
	}
	if len(page.Data.Data) != 30 {
		t.Fatalf("page data length = %d, want 30", len(page.Data.Data))
	}

	found := requestBanHistory(t, "/firewall/bans?history=1&search=search-target")
	if found.Data.Total != 1 || len(found.Data.Data) != 1 || found.Data.Data[0].IPAddress != "search-target" {
		t.Fatalf("search result = %+v", found.Data)
	}

	excluded := requestBanHistory(t, "/firewall/bans?history=1&search=old-only")
	if excluded.Data.Total != 0 || len(excluded.Data.Data) != 0 {
		t.Fatalf("old record should be outside latest 300: %+v", excluded.Data)
	}
	if !excluded.Data.HasMore {
		t.Fatal("filtered default window must still allow expanding to older retained history")
	}

	expanded := requestBanHistory(t, "/firewall/bans?history=1&expanded=1&search=old-only")
	if expanded.Data.Total != 1 || len(expanded.Data.Data) != 1 || expanded.Data.Data[0].IPAddress != "old-only" {
		t.Fatalf("expanded history did not include older record: %+v", expanded.Data)
	}
}

type banHistoryResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Data       []models.FirewallBan `json:"data"`
		Total      int                  `json:"total"`
		Page       int                  `json:"page"`
		TotalPages int                  `json:"total_pages"`
		HasMore    bool                 `json:"has_more"`
	} `json:"data"`
}

func requestBanHistory(t *testing.T, path string) banHistoryResponse {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/firewall/bans", (&FirewallHandler{}).ListBans)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var response banHistoryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	if !response.Success {
		t.Fatalf("response was not successful: %s", rec.Body.String())
	}
	return response
}

func TestBuildCurrentBanViewUsesLiveEnforcementAndKeepsAnomaliesSeparate(t *testing.T) {
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	level := models.BanLevelTemp24h
	receipts := []models.FirewallBan{
		{ID: 1, IPAddress: "203.0.113.10", SourceJail: "wppanel", BanLevel: level, Reason: "web", BannedAt: now, BanCount: 2},
		{ID: 2, IPAddress: "203.0.113.20", SourceJail: "panel_scan", BanLevel: level, Reason: "scan", BannedAt: now.Add(-time.Minute), BanCount: 1},
	}
	state := executor.CurrentBanEnforcement{
		Fail2ban: map[executor.CurrentBanKey]bool{{IP: "203.0.113.10", Source: "wppanel"}: true},
		Persist:  map[string]bool{},
		Nginx:    map[string]bool{"203.0.113.10": true},
		Status: executor.CurrentBanReadStatus{
			Fail2ban: map[string]bool{"wppanel": true, "wppanel-404": true, "wppanel-login": true, "wppanel-sshd": true},
			Nftables: true,
			Nginx:    true,
		},
	}
	current, anomalies := buildCurrentBanView(receipts, state)
	if len(current) != 1 || current[0].IPAddress != "203.0.113.10" || current[0].SourceJail != "wppanel" {
		t.Fatalf("current = %+v", current)
	}
	if len(anomalies) != 1 || anomalies[0].IPAddress != "203.0.113.20" || anomalies[0].Verification != "missing" {
		t.Fatalf("anomalies = %+v", anomalies)
	}
}

func TestBuildCurrentBanViewPreservesUnknownLiveIPAndReadFailure(t *testing.T) {
	receipt := models.FirewallBan{ID: 3, IPAddress: "203.0.113.30", SourceJail: "wppanel-sshd", BanLevel: models.BanLevelTemp10m, BannedAt: time.Now()}
	state := executor.CurrentBanEnforcement{
		Fail2ban: map[executor.CurrentBanKey]bool{},
		Persist:  map[string]bool{"203.0.113.40": true},
		Nginx:    map[string]bool{},
		Status:   executor.CurrentBanReadStatus{Fail2ban: map[string]bool{}, Nftables: true, Nginx: true},
	}
	current, anomalies := buildCurrentBanView([]models.FirewallBan{receipt}, state)
	if len(current) != 1 || current[0].IPAddress != "203.0.113.40" || current[0].SourceJail != "nftables" || current[0].BanLevel != nil {
		t.Fatalf("current = %+v", current)
	}
	if len(anomalies) != 1 || anomalies[0].Verification != "unverified" {
		t.Fatalf("anomalies = %+v", anomalies)
	}
}

func TestBuildCurrentBanViewMergesFail2banAndNginxWithoutReceipt(t *testing.T) {
	state := executor.CurrentBanEnforcement{
		Fail2ban: map[executor.CurrentBanKey]bool{{IP: "203.0.113.50", Source: "wppanel"}: true},
		Persist:  map[string]bool{},
		Nginx:    map[string]bool{"203.0.113.50": true},
		Status: executor.CurrentBanReadStatus{
			Fail2ban: map[string]bool{"wppanel": true},
			Nftables: true,
			Nginx:    true,
		},
	}
	current, anomalies := buildCurrentBanView(nil, state)
	if len(current) != 1 || current[0].SourceJail != "wppanel" || current[0].IPAddress != "203.0.113.50" {
		t.Fatalf("current = %+v", current)
	}
	if len(anomalies) != 0 {
		t.Fatalf("anomalies = %+v", anomalies)
	}
}

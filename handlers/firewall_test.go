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
		if _, err := database.GetDB().Exec(`INSERT INTO firewall_bans
			(ip_address, ban_level, reason, source_jail, banned_at, unbanned_at)
			VALUES (?, 2, 'test', 'wppanel', ?, ?)`,
			ip, base.Add(time.Duration(i)*time.Second), base.Add(time.Duration(i+600)*time.Second)); err != nil {
			t.Fatalf("insert ban %d: %v", i, err)
		}
	}

	page := requestBanHistory(t, "/firewall/bans?history=1&page=2")
	if page.Data.Total != firewallBanHistoryLimit || page.Data.TotalPages != 10 || page.Data.Page != 2 {
		t.Fatalf("pagination = total %d, pages %d, page %d", page.Data.Total, page.Data.TotalPages, page.Data.Page)
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
}

type banHistoryResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Data       []models.FirewallBan `json:"data"`
		Total      int                  `json:"total"`
		Page       int                  `json:"page"`
		TotalPages int                  `json:"total_pages"`
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

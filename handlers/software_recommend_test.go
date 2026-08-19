package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type recommendResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Facts struct {
			TotalMemoryMB uint64 `json:"total_memory_mb"`
			CPUCores      int    `json:"cpu_cores"`
			SiteCount     int    `json:"site_count"`
		} `json:"facts"`
		Reason          string            `json:"reason"`
		Recommendations map[string]string `json:"recommendations"`
	} `json:"data"`
}

func requestRecommend(t *testing.T, name string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/software/recommend", (&SoftwareHandler{}).Recommend)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/software/recommend?name="+name, nil))
	return rec
}

func TestRecommendReturnsOpcacheKeysForPHP(t *testing.T) {
	rec := requestRecommend(t, "PHP")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp recommendResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	if !resp.Success {
		t.Fatalf("response was not successful: %s", rec.Body.String())
	}
	for _, key := range []string{"opcache.memory_consumption", "opcache.max_accelerated_files"} {
		if _, ok := resp.Data.Recommendations[key]; !ok {
			t.Fatalf("expected recommendation for %q, got %+v", key, resp.Data.Recommendations)
		}
	}
	if resp.Data.Reason == "" {
		t.Fatal("expected a non-empty reason explaining the recommendation")
	}
	// PHP 卡片有 8 个配置项但只有 2 项会被这个按钮更新，理由文案必须明确点名
	// 这两个 key，避免管理员误以为整张卡片的值都被重新计算了。
	for _, key := range []string{"opcache.memory_consumption", "opcache.max_accelerated_files"} {
		if !strings.Contains(resp.Data.Reason, key) {
			t.Fatalf("expected PHP reason to explicitly name %q, got: %s", key, resp.Data.Reason)
		}
	}
}

func TestRecommendReturnsBufferPoolSizeForMariaDB(t *testing.T) {
	rec := requestRecommend(t, "MariaDB")
	var resp recommendResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	value, ok := resp.Data.Recommendations["innodb_buffer_pool_size"]
	if !ok {
		t.Fatalf("expected innodb_buffer_pool_size recommendation, got %+v", resp.Data.Recommendations)
	}
	if value == "" || value[len(value)-1] != 'M' {
		t.Fatalf("expected innodb_buffer_pool_size to end with M suffix, got %q", value)
	}
}

func TestRecommendReturnsMaxmemoryForRedis(t *testing.T) {
	rec := requestRecommend(t, "Redis")
	var resp recommendResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	value, ok := resp.Data.Recommendations["maxmemory"]
	if !ok {
		t.Fatalf("expected maxmemory recommendation, got %+v", resp.Data.Recommendations)
	}
	if len(value) < 2 || value[len(value)-2:] != "mb" {
		t.Fatalf("expected maxmemory to end with mb suffix, got %q", value)
	}
}

func TestRecommendRejectsUnsupportedSoftware(t *testing.T) {
	rec := requestRecommend(t, "Nginx")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unsupported software, got %d: %s", rec.Code, rec.Body.String())
	}
}

package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestFormatMetricLabelUsesServerLocalTime(t *testing.T) {
	oldLocal := time.Local
	time.Local = time.FixedZone("Asia/Shanghai", 8*60*60)
	t.Cleanup(func() { time.Local = oldLocal })

	ts := time.Date(2026, 8, 28, 7, 45, 0, 0, time.UTC)
	tests := []struct {
		rangeName string
		want      string
	}{
		{rangeName: "24h", want: "15:45"},
		{rangeName: "7d", want: "08-28 15:45"},
		{rangeName: "15d", want: "08-28"},
	}

	for _, tt := range tests {
		if got := formatMetricLabel(ts, tt.rangeName); got != tt.want {
			t.Errorf("formatMetricLabel(%q) = %q, want %q", tt.rangeName, got, tt.want)
		}
	}
}

func TestMetricsRejectsUnimplemented30DayRange(t *testing.T) {
	router := gin.New()
	router.GET("/metrics", (&DashboardHandler{}).GetMetrics)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics?range=30d", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

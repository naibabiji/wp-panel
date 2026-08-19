package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestClearOpcacheReturnsWellFormedResponse 不要求本机真的装了 php8.3-fpm（开发机上
// 大概率没有），只确认 handler 不会 panic，并且不管成功还是失败都返回结构完整的
// JSON 响应——真正的"清空成功"路径由真机验证覆盖（跟 MariaDBReady/RedisReady 的
// 测试方式一致，这些都是薄封装的系统命令调用，不引入 mock）。
func TestClearOpcacheReturnsWellFormedResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/software/opcache/clear", (&SoftwareHandler{}).ClearOpcache)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/software/opcache/clear", nil))

	if rec.Code != http.StatusOK && rec.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    struct {
			Message string `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	if resp.Success && resp.Data.Message == "" {
		t.Fatal("expected a non-empty success message")
	}
	if !resp.Success && resp.Message == "" {
		t.Fatal("expected a non-empty error message")
	}
}

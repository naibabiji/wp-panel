package handlers

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/executor"
)

func TestGetWPPackageReportsLocalVersionAndAutoCheckStatus(t *testing.T) {
	oldDB := database.DB
	if err := database.Open(filepath.Join(t.TempDir(), "panel.db")); err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.RunMigrations(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
		database.DB = oldDB
	})

	target := filepath.Join(t.TempDir(), "wordpress.zip")
	if err := os.WriteFile(target, buildHandlerWordPressZIP(t), 0644); err != nil {
		t.Fatal(err)
	}
	oldCfg := config.AppConfig
	config.AppConfig = &config.Config{Paths: config.PathsConfig{WordPressPackage: target}}
	t.Cleanup(func() { config.AppConfig = oldCfg })

	handler := &SettingsHandler{}
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/settings/wp-package", nil)
	handler.GetWPPackage(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Data struct {
			Available bool   `json:"available"`
			Version   string `json:"version"`
			AutoCheck struct {
				Enabled string `json:"wp_package_auto_check_enabled"`
			} `json:"auto_check"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON response: %v; body=%s", err, recorder.Body.String())
	}
	if !body.Data.Available || body.Data.Version != "7.0.2" {
		t.Fatalf("available=%v version=%q, want true/7.0.2", body.Data.Available, body.Data.Version)
	}
	// Unset in security_settings must default to enabled (not an empty/false-ish string).
	if body.Data.AutoCheck.Enabled != "" {
		t.Fatalf("auto_check.enabled = %q, want empty (unset) before any toggle save", body.Data.AutoCheck.Enabled)
	}
}

func TestUploadWPPackageRejectsInvalidPackageAndPreservesTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "wordpress.zip")
	if err := os.WriteFile(target, []byte("old-package"), 0644); err != nil {
		t.Fatal(err)
	}
	service, err := executor.NewWPPackageService(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := &SettingsHandler{WPPackageService: service}
	recorder := performPackageUpload(t, handler, "wordpress.zip", []byte("not-a-zip"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
	}
	assertAPIErrorDoesNotLeak(t, recorder.Body.Bytes(), "not-a-zip")
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old-package" {
		t.Fatalf("target changed to %q", got)
	}
}

func TestUploadWPPackagePublishesValidatedPackage(t *testing.T) {
	target := filepath.Join(t.TempDir(), "wordpress.zip")
	service, err := executor.NewWPPackageService(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := &SettingsHandler{WPPackageService: service}
	recorder := performPackageUpload(t, handler, "wordpress.zip", buildHandlerWordPressZIP(t))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if _, err := executor.ValidateWordPressPackage(t.Context(), target); err != nil {
		t.Fatalf("published package is invalid: %v", err)
	}
}

func TestUploadWPPackageUsesRequestEntityTooLarge(t *testing.T) {
	handler := &SettingsHandler{}
	recorder := performPackageUploadWithDeclaredSize(t, handler, "wordpress.zip", []byte("x"), 100<<20+1)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", recorder.Code)
	}
}

func TestDownloadWPPackageMapsInvalidUpstreamPackageToBadGateway(t *testing.T) {
	client := &http.Client{Transport: handlerRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString("not-a-zip")),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}
	service, err := executor.NewWPPackageService(filepath.Join(t.TempDir(), "wordpress.zip"), client)
	if err != nil {
		t.Fatal(err)
	}
	handler := &SettingsHandler{WPPackageService: service}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/settings/wp-package/download", nil)
	handler.DownloadWPPackage(c)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", recorder.Code, recorder.Body.String())
	}
}

type handlerRoundTripFunc func(*http.Request) (*http.Response, error)

func (f handlerRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func performPackageUpload(t *testing.T, handler *SettingsHandler, filename string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	return performPackageUploadWithDeclaredSize(t, handler, filename, body, int64(len(body)))
}

func performPackageUploadWithDeclaredSize(t *testing.T, handler *SettingsHandler, filename string, body []byte, declaredSize int64) *httptest.ResponseRecorder {
	t.Helper()
	var requestBody bytes.Buffer
	w := multipart.NewWriter(&requestBody)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/settings/wp-package/upload", &requestBody)
	req.Header.Set("Content-Type", w.FormDataContentType())
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if err := req.ParseMultipartForm(101 << 20); err != nil {
		t.Fatal(err)
	}
	req.MultipartForm.File["file"][0].Size = declaredSize
	recorder := httptest.NewRecorder()
	c, _ = gin.CreateTestContext(recorder)
	c.Request = req
	handler.UploadWPPackage(c)
	return recorder
}

func buildHandlerWordPressZIP(t *testing.T) []byte {
	t.Helper()
	var body bytes.Buffer
	zw := zip.NewWriter(&body)
	entries := map[string]string{
		"wordpress/wp-admin/index.php":      "<?php",
		"wordpress/wp-includes/load.php":    "<?php",
		"wordpress/wp-includes/version.php": "<?php\n$wp_version = '7.0.2';\n",
		"wordpress/wp-settings.php":         "<?php",
		"wordpress/wp-load.php":             "<?php",
	}
	for name, contents := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func assertAPIErrorDoesNotLeak(t *testing.T, body []byte, forbidden string) {
	t.Helper()
	if bytes.Contains(body, []byte(forbidden)) {
		t.Fatalf("response leaked untrusted input: %s", body)
	}
	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
}

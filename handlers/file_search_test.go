package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/config"
)

func TestFileSearchHandlerUsesBackupRootAndRejectsEscapes(t *testing.T) {
	oldConfig := config.AppConfig
	backupRoot := t.TempDir()
	config.AppConfig = &config.Config{Panel: config.PanelConfig{BackupDir: backupRoot}}
	t.Cleanup(func() { config.AppConfig = oldConfig })
	mustWriteSearchFile(t, filepath.Join(backupRoot, "nested", "Cache.php"))

	router := gin.New()
	router.GET("/api/files/search", (&FileHandler{}).Search)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/files/search?site_id=0&path=/&q=cache&scope=subtree", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Data struct {
			Files []fileSearchEntry `json:"files"`
			Total int               `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.Total != 1 || len(response.Data.Files) != 1 || response.Data.Files[0].Path != "/nested/Cache.php" {
		t.Fatalf("search response = %#v", response.Data)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/files/search?site_id=0&path=/../../&q=cache&scope=current", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("escape status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if err := os.Symlink(filepath.Join(backupRoot, "nested"), filepath.Join(backupRoot, "nested-link")); err == nil {
		rec = httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/files/search?site_id=0&path=/nested-link&q=cache&scope=subtree", nil))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("symlink status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}
}

func TestFileSearchHandlerValidatesQueryAndScope(t *testing.T) {
	router := gin.New()
	router.GET("/api/files/search", (&FileHandler{}).Search)
	for _, target := range []string{
		"/api/files/search?site_id=0&path=/&scope=current",
		"/api/files/search?site_id=0&path=/&q=test&scope=invalid",
		"/api/files/search?site_id=-1&path=/&q=test&scope=current",
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, body = %s", target, rec.Code, rec.Body.String())
		}
	}
}

func TestFileSearchHandlerLimitsOnlyRecursiveConcurrency(t *testing.T) {
	oldConfig := config.AppConfig
	oldSlots := fileSearchSlots
	backupRoot := t.TempDir()
	config.AppConfig = &config.Config{Panel: config.PanelConfig{BackupDir: backupRoot}}
	fileSearchSlots = make(chan struct{}, 1)
	fileSearchSlots <- struct{}{}
	t.Cleanup(func() {
		config.AppConfig = oldConfig
		fileSearchSlots = oldSlots
	})
	mustWriteSearchFile(t, filepath.Join(backupRoot, "match.txt"))

	router := gin.New()
	router.GET("/api/files/search", (&FileHandler{}).Search)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/files/search?site_id=0&path=/&q=match&scope=subtree", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("subtree status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/files/search?site_id=0&path=/&q=match&scope=current", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("current status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestSearchFileTreeCurrentAndSubtreeScopes(t *testing.T) {
	root := t.TempDir()
	mustWriteSearchFile(t, filepath.Join(root, "cache.php"))
	mustWriteSearchFile(t, filepath.Join(root, "other.txt"))
	mustWriteSearchFile(t, filepath.Join(root, "nested", "CACHE.log"))

	current, err := searchFileTree(context.Background(), root, root, "cache", false, "name", "asc", 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if got := searchEntryPaths(current.Files); !reflect.DeepEqual(got, []string{"/cache.php"}) {
		t.Fatalf("current search paths = %v", got)
	}

	subtree, err := searchFileTree(context.Background(), root, root, "cache", true, "name", "asc", 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if got := searchEntryPaths(subtree.Files); !reflect.DeepEqual(got, []string{"/nested/CACHE.log", "/cache.php"}) {
		t.Fatalf("subtree search paths = %v", got)
	}
}

func TestSearchFileTreeReturnsDirectoriesAndDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWriteSearchFile(t, filepath.Join(root, "cache-dir", "inside.txt"))
	mustWriteSearchFile(t, filepath.Join(outside, "cache-secret.txt"))
	if err := os.Symlink(outside, filepath.Join(root, "cache-link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	result, err := searchFileTree(context.Background(), root, root, "cache", true, "name", "asc", 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if got := searchEntryPaths(result.Files); !reflect.DeepEqual(got, []string{"/cache-dir", "/cache-link"}) {
		t.Fatalf("search paths = %v", got)
	}
}

func TestSearchFileTreePaginatesAfterDeterministicSort(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"z-match.txt", "a-match.txt", "m-match.txt"} {
		mustWriteSearchFile(t, filepath.Join(root, name))
	}

	result, err := searchFileTree(context.Background(), root, root, "match", false, "name", "asc", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 3 || result.Page != 2 || result.TotalPages != 2 {
		t.Fatalf("pagination = total %d page %d pages %d", result.Total, result.Page, result.TotalPages)
	}
	if got := searchEntryPaths(result.Files); !reflect.DeepEqual(got, []string{"/z-match.txt"}) {
		t.Fatalf("second page = %v", got)
	}
}

func TestSearchFileTreeReportsScanAndMatchLimits(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		mustWriteSearchFile(t, filepath.Join(root, name))
	}

	scanLimited, err := searchFileTreeWithLimits(context.Background(), root, root, "nomatch", false, "name", "asc", 1, 50, fileSearchLimits{MaxScanned: 2, MaxMatches: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !scanLimited.Truncated || scanLimited.TruncatedReason != "scan_limit" {
		t.Fatalf("scan limit = truncated %v reason %q", scanLimited.Truncated, scanLimited.TruncatedReason)
	}

	matchLimited, err := searchFileTreeWithLimits(context.Background(), root, root, ".txt", false, "name", "asc", 1, 50, fileSearchLimits{MaxScanned: 10, MaxMatches: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !matchLimited.Truncated || matchLimited.TruncatedReason != "match_limit" || matchLimited.Total != 2 {
		t.Fatalf("match limit = truncated %v reason %q total %d", matchLimited.Truncated, matchLimited.TruncatedReason, matchLimited.Total)
	}
}

func TestSearchFileTreeHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := searchFileTree(ctx, t.TempDir(), t.TempDir(), "x", true, "name", "asc", 1, 50)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("search error = %v, want context canceled", err)
	}
}

func mustWriteSearchFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
}

func searchEntryPaths(entries []fileSearchEntry) []string {
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}
	return paths
}

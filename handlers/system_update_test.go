package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/executor"
)

func TestSystemUpdateStatusResponseIncludesRemovalSafetyDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/", nil)
	packages := []string{"old-kernel", "unused-library"}

	response := systemUpdateStatusResponse(c, executor.SystemPackageUpdateStatus{
		ID:              "test",
		Status:          "blocked",
		Stage:           "preflight",
		MessageKey:      "settings.system_update_status_removal_blocked",
		RemovalPackages: packages,
	})

	if response["status"] != "blocked" {
		t.Fatalf("unexpected status: %#v", response["status"])
	}
	if got, ok := response["removal_packages"].([]string); !ok || !reflect.DeepEqual(got, packages) {
		t.Fatalf("unexpected removal packages: %#v", response["removal_packages"])
	}
	if response["message"] == "" {
		t.Fatal("blocked status should include a localized message")
	}
}

func TestSystemUpdateRefreshFetchesSourcesAndReplacesCachedPackages(t *testing.T) {
	oldRefresh := refreshSystemPackageSources
	oldList := listUpgradableSystemPackages
	oldExpireAt, oldPkgs := snapshotSystemPackageCache(t)
	t.Cleanup(func() {
		refreshSystemPackageSources = oldRefresh
		listUpgradableSystemPackages = oldList
		restoreSystemPackageCache(oldExpireAt, oldPkgs)
	})

	refreshed := false
	refreshSystemPackageSources = func() error {
		refreshed = true
		return nil
	}
	listUpgradableSystemPackages = func() ([]systemPackage, error) {
		return []systemPackage{{Name: "php8.3-fpm", Version: "8.3.36", Repo: "sury"}}, nil
	}
	sysPkgCache.mu.Lock()
	sysPkgCache.expireAt = time.Now().Add(time.Hour)
	sysPkgCache.pkgs = []systemPackage{{Name: "old-cached-package", Version: "1.0", Repo: "old"}}
	sysPkgCache.mu.Unlock()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	router := gin.New()
	router.POST("/refresh", (&SystemUpdateHandler{}).Refresh)
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/refresh", nil))

	if recorder.Code != http.StatusOK || !refreshed {
		t.Fatalf("refresh status=%d refreshed=%v body=%s", recorder.Code, refreshed, recorder.Body.String())
	}
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Count    int             `json:"count"`
			Packages []systemPackage `json:"packages"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Success || response.Data.Count != 1 || len(response.Data.Packages) != 1 || response.Data.Packages[0].Version != "8.3.36" {
		t.Fatalf("unexpected refresh response: %+v", response)
	}
}

func TestSystemUpdateRefreshListFailureIsNotReportedAsUpToDate(t *testing.T) {
	oldRefresh := refreshSystemPackageSources
	oldList := listUpgradableSystemPackages
	t.Cleanup(func() {
		refreshSystemPackageSources = oldRefresh
		listUpgradableSystemPackages = oldList
	})
	refreshSystemPackageSources = func() error { return nil }
	listUpgradableSystemPackages = func() ([]systemPackage, error) { return nil, errSystemUpdateTest }

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	router := gin.New()
	router.POST("/refresh", (&SystemUpdateHandler{}).Refresh)
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/refresh", nil))
	if recorder.Code != http.StatusBadGateway || strings.Contains(recorder.Body.String(), "已是最新") {
		t.Fatalf("list failure status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSystemUpdateRefreshFailureDoesNotReturnCachedUpToDateResult(t *testing.T) {
	oldRefresh := refreshSystemPackageSources
	oldList := listUpgradableSystemPackages
	oldExpireAt, oldPkgs := snapshotSystemPackageCache(t)
	t.Cleanup(func() {
		refreshSystemPackageSources = oldRefresh
		listUpgradableSystemPackages = oldList
		restoreSystemPackageCache(oldExpireAt, oldPkgs)
	})

	refreshSystemPackageSources = func() error { return errSystemUpdateTest }
	listCalled := false
	listUpgradableSystemPackages = func() ([]systemPackage, error) {
		listCalled = true
		return nil, nil
	}
	sysPkgCache.mu.Lock()
	sysPkgCache.expireAt = time.Now().Add(time.Hour)
	sysPkgCache.pkgs = []systemPackage{}
	sysPkgCache.mu.Unlock()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	router := gin.New()
	router.POST("/refresh", (&SystemUpdateHandler{}).Refresh)
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/refresh", nil))

	if recorder.Code != http.StatusBadGateway || listCalled || !strings.Contains(recorder.Body.String(), "无法刷新软件源") {
		t.Fatalf("refresh failure status=%d listCalled=%v body=%s", recorder.Code, listCalled, recorder.Body.String())
	}
}

func TestSystemUpdateCheckUsesQuickCachedReadWithoutRefreshingSources(t *testing.T) {
	oldRefresh := refreshSystemPackageSources
	oldExpireAt, oldPkgs := snapshotSystemPackageCache(t)
	t.Cleanup(func() {
		refreshSystemPackageSources = oldRefresh
		restoreSystemPackageCache(oldExpireAt, oldPkgs)
	})
	refreshSystemPackageSources = func() error { t.Fatal("quick check must not refresh package sources"); return nil }
	sysPkgCache.mu.Lock()
	sysPkgCache.expireAt = time.Now().Add(time.Hour)
	sysPkgCache.pkgs = []systemPackage{{Name: "cached", Version: "1.1", Repo: "test"}}
	sysPkgCache.mu.Unlock()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	router := gin.New()
	router.GET("/check", (&SystemUpdateHandler{}).Check)
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/check", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "cached") {
		t.Fatalf("quick check status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

var errSystemUpdateTest = errors.New("test refresh failure")

func snapshotSystemPackageCache(t *testing.T) (time.Time, []systemPackage) {
	t.Helper()
	sysPkgCache.mu.Lock()
	defer sysPkgCache.mu.Unlock()
	return sysPkgCache.expireAt, append([]systemPackage(nil), sysPkgCache.pkgs...)
}

func restoreSystemPackageCache(expireAt time.Time, pkgs []systemPackage) {
	sysPkgCache.mu.Lock()
	sysPkgCache.expireAt = expireAt
	sysPkgCache.pkgs = pkgs
	sysPkgCache.mu.Unlock()
}

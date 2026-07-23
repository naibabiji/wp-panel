//go:build linux

package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/models"
)

func TestWPInventoryEmbeddedSource(t *testing.T) {
	if len(wpInventoryRunnerSource) == 0 || len(wpInventoryRunnerSource) > wpInventorySourceLimit {
		t.Fatalf("embedded runner source length = %d", len(wpInventoryRunnerSource))
	}
	if strings.Contains(string(wpInventoryRunnerSource), "WP Panel Inventory Adversarial Fixture") {
		t.Fatal("production runner contains adversarial fixture")
	}
	sum := sha256.Sum256(wpInventoryRunnerSource)
	if len(hex.EncodeToString(sum[:])) != 64 {
		t.Fatal("embedded runner hash is not a SHA-256 hex value")
	}
}

func TestWPInventoryRunErrorDoesNotLeakCause(t *testing.T) {
	err := runError(WPInventoryProtocolInvalid, WPInventoryStageProtocol, 17, false, errors.New("/secret/path plugin output"))
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "plugin") {
		t.Fatalf("public error leaks private cause: %s", err)
	}
}

func TestWPInventoryPolicyMismatchReportsEachField(t *testing.T) {
	want := wpInventoryDiagnostics{
		SAPI: "cli", EffectiveUID: 1001, EffectiveGID: 1002, OpenBaseDir: "/site:/runner",
		DisableFunctions: sitePHPDisabledFunctions(), AllowURLInclude: "0", MemoryLimit: wpInventoryRunnerMemoryLimit,
	}
	if got := wpInventoryPolicyMismatch(want, 1001, 1002, "/site:/runner"); got != "" {
		t.Fatalf("matching diagnostics reported a mismatch: %q", got)
	}

	cases := []struct {
		name   string
		mutate func(d wpInventoryDiagnostics) wpInventoryDiagnostics
		want   string
	}{
		{"sapi", func(d wpInventoryDiagnostics) wpInventoryDiagnostics { d.SAPI = "fpm-fcgi"; return d }, "sapi="},
		{"uid", func(d wpInventoryDiagnostics) wpInventoryDiagnostics { d.EffectiveUID = 0; return d }, "effective_uid="},
		{"gid", func(d wpInventoryDiagnostics) wpInventoryDiagnostics { d.EffectiveGID = 0; return d }, "effective_gid="},
		{"open_basedir", func(d wpInventoryDiagnostics) wpInventoryDiagnostics { d.OpenBaseDir = "/other"; return d }, "open_basedir="},
		{"disable_functions", func(d wpInventoryDiagnostics) wpInventoryDiagnostics { d.DisableFunctions = "exec"; return d }, "disable_functions mismatch"},
		{"allow_url_include", func(d wpInventoryDiagnostics) wpInventoryDiagnostics { d.AllowURLInclude = "1"; return d }, "allow_url_include="},
		{"memory_limit", func(d wpInventoryDiagnostics) wpInventoryDiagnostics { d.MemoryLimit = "64M"; return d }, "memory_limit="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wpInventoryPolicyMismatch(tc.mutate(want), 1001, 1002, "/site:/runner")
			if !strings.Contains(got, tc.want) {
				t.Fatalf("mismatch=%q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestParseWPInventoryProtocol(t *testing.T) {
	token := "0123456789abcdef0123456789abcdef"
	body := validInventoryEnvelopeJSON(t, token, 1001, 1002, "/site:/runner")
	env, err := parseWPInventoryProtocol(body, token)
	if err != nil {
		t.Fatalf("parse valid protocol: %v", err)
	}
	if !env.OK || env.Data == nil || env.Data.WordPress.Version != "7.0" {
		t.Fatalf("unexpected envelope: %#v", env)
	}

	cases := map[string][]byte{
		"wrong token":     bytesReplace(body, token, strings.Repeat("a", 32), 1),
		"missing end":     body[:len(body)-len("WP_PANEL_INVENTORY_END "+token+"\n")],
		"outside data":    append([]byte("noise"), body...),
		"trailing json":   protocolFrame(token, `{"protocol":"wp-panel-inventory","runner_version":"1","inventory_schema_version":1,"ok":false,"error":{"code":"fatal_error"},"diagnostics":{"sapi":"cli","effective_uid":1,"effective_gid":1,"open_basedir":"x","disable_functions":"x","allow_url_include":"0","memory_limit":"256M","bootstrap_output_bytes":0}} {}`),
		"unknown field":   protocolFrame(token, `{"protocol":"wp-panel-inventory","runner_version":"1","inventory_schema_version":1,"ok":false,"error":{"code":"fatal_error","message":"leak"},"diagnostics":{"sapi":"cli","effective_uid":1,"effective_gid":1,"open_basedir":"x","disable_functions":"x","allow_url_include":"0","memory_limit":"256M","bootstrap_output_bytes":0}}`),
		"bad versions":    protocolFrame(token, `{"protocol":"other","runner_version":"1","inventory_schema_version":1,"ok":false,"error":{"code":"fatal_error"},"diagnostics":{"sapi":"cli","effective_uid":1,"effective_gid":1,"open_basedir":"x","disable_functions":"x","allow_url_include":"0","memory_limit":"256M","bootstrap_output_bytes":0}}`),
		"success no data": protocolFrame(token, `{"protocol":"wp-panel-inventory","runner_version":"1","inventory_schema_version":1,"ok":true,"diagnostics":{"sapi":"cli","effective_uid":1,"effective_gid":1,"open_basedir":"x","disable_functions":"x","allow_url_include":"0","memory_limit":"256M","bootstrap_output_bytes":0}}`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseWPInventoryProtocol(raw, token); err == nil {
				t.Fatal("invalid protocol accepted")
			}
		})
	}
}

func TestValidateWPInventoryRejectsOrderingDuplicatesAndPaths(t *testing.T) {
	base := validInventory()
	base.Plugins = []WPInventoryPlugin{{File: "z/z.php"}, {File: "a/a.php"}}
	if validateWPInventory(&base) == nil {
		t.Fatal("unsorted plugins accepted")
	}
	base = validInventory()
	base.Themes = []WPInventoryTheme{{Stylesheet: "same"}, {Stylesheet: "same"}}
	if validateWPInventory(&base) == nil {
		t.Fatal("duplicate themes accepted")
	}
	base = validInventory()
	base.Updates.Plugins.Items = []WPInventoryComponentUpdate{{ID: "../escape", Version: "1"}}
	if validateWPInventory(&base) == nil {
		t.Fatal("escaping update id accepted")
	}
}

func TestWPInventoryRunnerPrepareIsAtomicAndStable(t *testing.T) {
	runner, _ := newTestInventoryRunner(t, []byte("<?php echo 'fixed';"))
	if err := runner.ensureRunnerRoot(); err != nil {
		t.Fatalf("ensure runner root: %v", err)
	}
	lock, err := wpInventoryAcquireFileLock(context.Background(), filepath.Join(runner.runnerRoot, ".lock"), runner.ownerUID, runner.ownerGID)
	if err != nil {
		t.Fatalf("lock runner root: %v", err)
	}
	defer lock.Close()
	path, _, err := runner.prepareRunner()
	if err != nil {
		t.Fatalf("prepare runner: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0444 {
		t.Fatalf("runner file mode: info=%v err=%v", info, err)
	}
	dirInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil || dirInfo.Mode().Perm() != 0555 {
		t.Fatalf("runner dir mode: info=%v err=%v", dirInfo, err)
	}
	mtime := info.ModTime()
	time.Sleep(10 * time.Millisecond)
	path2, _, err := runner.prepareRunner()
	if err != nil || path2 != path {
		t.Fatalf("repeat prepare: path=%q err=%v", path2, err)
	}
	info2, _ := os.Stat(path2)
	if !info2.ModTime().Equal(mtime) {
		t.Fatalf("repeat prepare rewrote runner: %s -> %s", mtime, info2.ModTime())
	}
}

func TestWPInventoryRunnerRejectsPublishedCorruption(t *testing.T) {
	runner, _ := newTestInventoryRunner(t, []byte("<?php echo 'fixed';"))
	if err := runner.ensureRunnerRoot(); err != nil {
		t.Fatal(err)
	}
	path, _, err := runner.prepareRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	_, _, err = runner.prepareRunner()
	assertInventoryErrorCode(t, err, WPInventoryRunnerIntegrityFailed)
}

func TestWPInventoryRunnerPreservesUnknownEntries(t *testing.T) {
	runner, _ := newTestInventoryRunner(t, []byte("<?php echo 'fixed';"))
	if err := runner.ensureRunnerRoot(); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(runner.runnerRoot, "operator-note")
	if err := os.WriteFile(unknown, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runner.prepareRunner(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(unknown); err != nil || string(data) != "keep" {
		t.Fatalf("unknown entry changed: %q %v", data, err)
	}
}

func TestValidateInventorySitePathRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	www := filepath.Join(root, "www")
	site := filepath.Join(www, "example.com")
	if err := os.MkdirAll(site, 0755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.php")
	if err := os.WriteFile(outside, []byte("<?php"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(site, "wp-load.php")); err != nil {
		t.Fatal(err)
	}
	if _, err := validateInventorySitePath(www, site); err == nil {
		t.Fatal("wp-load symlink accepted")
	}
}

func TestWPInventoryCollectUsesFixedArgsAndMinimalEnvironment(t *testing.T) {
	runner, fixture := newTestInventoryRunner(t, []byte("<?php // embedded"))
	openBase := sitePHPRunnerOpenBaseDir(fixture.site.WebRoot, fixture.site.Domain, filepath.Join(runner.runnerRoot, runner.hash))
	writeProtocolWrapper(t, runner.runuserPath, fixture.auditPath, fixture.uid, fixture.gid, openBase, false)
	t.Setenv("PARENT_SECRET", "must-not-leak")
	result, err := runner.Collect(context.Background(), fixture.cfg, fixture.site)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if result.Inventory.WordPress.Version != "7.0" || result.Meta.ExitCode != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	audit, err := os.ReadFile(fixture.auditPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(audit)
	for _, want := range []string{"-u wp_test --", "-d open_basedir=" + openBase, "-d disable_functions=" + sitePHPDisabledFunctions(), "-d allow_url_include=0", "-d memory_limit=256M", "PARENT_SECRET=unset"} {
		if !strings.Contains(text, want) {
			t.Fatalf("audit missing %q:\n%s", want, text)
		}
	}
}

func TestWPInventoryCollectTimeoutRecovers(t *testing.T) {
	runner, fixture := newTestInventoryRunner(t, []byte("<?php // embedded"))
	writeProtocolWrapper(t, runner.runuserPath, fixture.auditPath, fixture.uid, fixture.gid, "", true)
	_, err := runner.Collect(context.Background(), fixture.cfg, fixture.site)
	assertInventoryErrorCode(t, err, WPInventoryRunnerTimeout)
	openBase := sitePHPRunnerOpenBaseDir(fixture.site.WebRoot, fixture.site.Domain, filepath.Join(runner.runnerRoot, runner.hash))
	writeProtocolWrapper(t, runner.runuserPath, fixture.auditPath, fixture.uid, fixture.gid, openBase, false)
	if _, err := runner.Collect(context.Background(), fixture.cfg, fixture.site); err != nil {
		t.Fatalf("collect after timeout: %v", err)
	}
}

func TestWPInventoryCollectGlobalSlotAcrossRunnerInstances(t *testing.T) {
	runner, fixture := newTestInventoryRunner(t, []byte("<?php // embedded"))
	second := *runner
	openBase := sitePHPRunnerOpenBaseDir(fixture.site.WebRoot, fixture.site.Domain, filepath.Join(runner.runnerRoot, runner.hash))
	guardPath := filepath.Join(filepath.Dir(fixture.auditPath), "active-guard")
	overlapPath := filepath.Join(filepath.Dir(fixture.auditPath), "overlap")
	writeConcurrentProtocolWrapper(t, runner.runuserPath, fixture.uid, fixture.gid, openBase, guardPath, overlapPath)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, candidate := range []*WPInventoryRunner{runner, &second} {
		wg.Add(1)
		go func(candidate *WPInventoryRunner) {
			defer wg.Done()
			_, err := candidate.Collect(context.Background(), fixture.cfg, fixture.site)
			errs <- err
		}(candidate)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent collect: %v", err)
		}
	}
	if _, err := os.Stat(overlapPath); !os.IsNotExist(err) {
		t.Fatalf("multiple Runner instances overlapped; marker stat error = %v", err)
	}
}

type inventoryFixture struct {
	cfg       *config.Config
	site      *models.Website
	uid       int
	gid       int
	auditPath string
}

func newTestInventoryRunner(t *testing.T, source []byte) (*WPInventoryRunner, inventoryFixture) {
	t.Helper()
	base := t.TempDir()
	binDir := filepath.Join(base, "bin")
	sbinDir := filepath.Join(base, "sbin")
	www := filepath.Join(base, "www")
	siteRoot := filepath.Join(www, "example.com")
	for _, dir := range []string{binDir, sbinDir, siteRoot} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(siteRoot, "wp-load.php"), []byte("<?php"), 0644); err != nil {
		t.Fatal(err)
	}
	phpPath := filepath.Join(binDir, "php8.3")
	runuserPath := filepath.Join(sbinDir, "runuser")
	if err := os.WriteFile(phpPath, []byte("#!/bin/sh\nexit 0\n"), 0555); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runuserPath, []byte("#!/bin/sh\nexit 1\n"), 0555); err != nil {
		t.Fatal(err)
	}
	uid, gid := os.Geteuid(), os.Getegid()
	if uid == 0 {
		uid = 1001
	}
	if gid == 0 {
		gid = 1002
	}
	runner, err := newWPInventoryRunner(wpInventoryRunnerOptions{
		source: source, runnerRoot: filepath.Join(base, "runners", "wp-inventory"), trustedRoot: base,
		phpPath: phpPath, runuserPath: runuserPath, phpDir: binDir, runuserDir: sbinDir,
		requireRoot: false, ownerUID: os.Geteuid(), ownerGID: os.Getegid(), now: time.Now,
		lookupUser: func(name string) (*user.User, error) {
			if name != "wp_test" {
				return nil, user.UnknownUserError(name)
			}
			return &user.User{Username: name, Uid: fmt.Sprint(uid), Gid: fmt.Sprint(gid), HomeDir: base}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(runner.runnerRoot, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr == nil && entry.IsDir() {
				_ = os.Chmod(path, 0755)
			}
			return nil
		})
	})
	return runner, inventoryFixture{
		cfg:  &config.Config{Paths: config.PathsConfig{WWWRoot: www}},
		site: &models.Website{ID: 1, Domain: "example.com", SystemUser: "wp_test", WebRoot: siteRoot, SiteType: "wordpress", Status: models.StatusActive},
		uid:  uid, gid: gid, auditPath: filepath.Join(base, "audit.txt"),
	}
}

func writeProtocolWrapper(t *testing.T, path, auditPath string, uid, gid int, openBase string, timeout bool) {
	t.Helper()
	var script string
	if timeout {
		script = "#!/bin/sh\nsleep 30\n"
	} else {
		jsonBody := validSuccessEnvelopeBody(uid, gid, openBase)
		script = fmt.Sprintf("#!/bin/sh\nprintf '%%s\\nPARENT_SECRET=%%s\\n' \"$*\" \"${PARENT_SECRET-unset}\" > %q\nprintf 'WP_PANEL_INVENTORY_BEGIN %%s\\n%%s\\nWP_PANEL_INVENTORY_END %%s\\n' \"$WP_PANEL_RUNNER_TOKEN\" %q \"$WP_PANEL_RUNNER_TOKEN\" >&3\n", auditPath, jsonBody)
	}
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(script), 0555); err != nil {
		t.Fatal(err)
	}
}

func writeConcurrentProtocolWrapper(t *testing.T, wrapperPath string, uid, gid int, openBase, guardPath, overlapPath string) {
	t.Helper()
	body := validSuccessEnvelopeBody(uid, gid, openBase)
	script := fmt.Sprintf("#!/bin/sh\nif ! mkdir %q 2>/dev/null; then touch %q; fi\nsleep 0.2\nrmdir %q 2>/dev/null || true\nprintf 'WP_PANEL_INVENTORY_BEGIN %%s\\n%%s\\nWP_PANEL_INVENTORY_END %%s\\n' \"$WP_PANEL_RUNNER_TOKEN\" %q \"$WP_PANEL_RUNNER_TOKEN\" >&3\n", guardPath, overlapPath, guardPath, body)
	if err := os.Chmod(wrapperPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrapperPath, []byte(script), 0555); err != nil {
		t.Fatal(err)
	}
}

func validSuccessEnvelopeBody(uid, gid int, openBase string) string {
	return fmt.Sprintf(`{"protocol":"%s","runner_version":"%s","inventory_schema_version":%d,"ok":true,"data":{"wordpress":{"version":"7.0","locale":"en_US","multisite":false},"plugins":[],"themes":[],"current_theme":null,"updates":{"core":{"transient_present":false,"last_checked":0,"version_checked":"","items":[]},"plugins":{"transient_present":false,"last_checked":0,"items":[]},"themes":{"transient_present":false,"last_checked":0,"items":[]}}},"diagnostics":{"sapi":"cli","effective_uid":%d,"effective_gid":%d,"open_basedir":"%s","disable_functions":"%s","allow_url_include":"0","memory_limit":"256M","bootstrap_output_bytes":0}}`, wpInventoryProtocol, wpInventoryRunnerVersion, wpInventorySchemaVersion, uid, gid, openBase, sitePHPDisabledFunctions())
}

func validInventoryEnvelopeJSON(t *testing.T, token string, uid, gid int, openBase string) []byte {
	t.Helper()
	inv, err := jsonMarshal(validInventory())
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"protocol":"%s","runner_version":"%s","inventory_schema_version":%d,"ok":true,"data":%s,"diagnostics":{"sapi":"cli","effective_uid":%d,"effective_gid":%d,"open_basedir":"%s","disable_functions":"%s","allow_url_include":"0","memory_limit":"256M","bootstrap_output_bytes":0}}`, wpInventoryProtocol, wpInventoryRunnerVersion, wpInventorySchemaVersion, inv, uid, gid, openBase, sitePHPDisabledFunctions())
	return protocolFrame(token, body)
}

func validInventory() WPInventory {
	return WPInventory{
		WordPress: WPInventoryWordPress{Version: "7.0", Locale: "en_US"},
		Plugins:   []WPInventoryPlugin{}, Themes: []WPInventoryTheme{},
		Updates: WPInventoryUpdates{
			Core:    WPInventoryCoreUpdates{Items: []WPInventoryCoreUpdate{}},
			Plugins: WPInventoryComponentUpdates{Items: []WPInventoryComponentUpdate{}},
			Themes:  WPInventoryComponentUpdates{Items: []WPInventoryComponentUpdate{}},
		},
	}
}

func protocolFrame(token, body string) []byte {
	return []byte("WP_PANEL_INVENTORY_BEGIN " + token + "\n" + body + "\nWP_PANEL_INVENTORY_END " + token + "\n")
}

func bytesReplace(raw []byte, old, replacement string, count int) []byte {
	return []byte(strings.Replace(string(raw), old, replacement, count))
}

func assertInventoryErrorCode(t *testing.T, err error, want WPInventoryErrorCode) {
	t.Helper()
	var runErr *WPInventoryRunError
	if !errors.As(err, &runErr) || runErr.Code != want {
		t.Fatalf("error = %v, want code %s", err, want)
	}
}

func jsonMarshal(value any) ([]byte, error) {
	return json.Marshal(value)
}

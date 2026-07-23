package executor

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCreateWPCoreNoContentPackageExcludesWPContent(t *testing.T) {
	source := writeTestZIP(t, map[string]string{"wordpress/wp-admin/index.php": "admin", "wordpress/wp-includes/version.php": "version", "wordpress/wp-content/plugins/bundled.php": "plugin", "wordpress/wp-content/languages/core.mo": "language"})
	target := filepath.Join(t.TempDir(), "core.zip")
	sha, err := createWPCoreNoContentPackage(source, target)
	if err != nil || !wpUpdateSHA256Pattern.MatchString(sha) {
		t.Fatalf("sha=%q err=%v", sha, err)
	}
	zr, err := zip.OpenReader(target)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, entry := range zr.File {
		if strings.HasPrefix(entry.Name, "wordpress/wp-content") {
			t.Fatalf("wp-content entry remains: %s", entry.Name)
		}
	}
}

func TestWPCoreUpdatePHPSourceParses(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php unavailable")
	}
	source := filepath.Join(t.TempDir(), "runner.php")
	if err := os.WriteFile(source, []byte("<?php\n"+wpCoreUpdatePHPSource), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(php, "-l", source).CombinedOutput(); err != nil {
		t.Fatalf("php -l: %v: %s", err, output)
	}
	if !strings.Contains(wpCoreUpdatePHPSource, "delete_site_transient('update_core')") {
		t.Fatal("successful core update does not clear the stale core update transient")
	}
}

func TestWPCoreUpdatePHPSourceKeepsInputsAcrossBootstrap(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php unavailable")
	}
	root := t.TempDir()
	bootstrap := `<?php
$action = 'clobbered';
$root = __DIR__ . '/poison-root';
$package = '/tmp/clobbered.zip';
$target = '9.9.9';
$expected = 'clobbered';
$token = 'clobbered';
$sent = true;
$send = null;
$wp_version = '7.0.1';
`
	if err := os.WriteFile(filepath.Join(root, "wp-load.php"), []byte(bootstrap), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := os.CreateTemp(t.TempDir(), "result-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer result.Close()
	cmd := exec.Command(php, "-d", "display_errors=0", "-r", wpCoreUpdatePHPSource, "check", root, "", "7.0.1", "")
	cmd.Env = append(os.Environ(), "WP_PANEL_RUNNER_TOKEN=0123456789abcdef0123456789abcdef")
	cmd.ExtraFiles = []*os.File{result}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("check failed after bootstrap clobbering: %v output=%s", err, output)
	}
	if _, err := result.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var envelope wpCoreRunnerEnvelope
	if err := json.NewDecoder(result).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK || envelope.Version != "7.0.1" || envelope.ErrorCode != "" {
		t.Fatalf("envelope=%+v", envelope)
	}
}

func TestWPCoreUpdatePHPSourceNamespacesBootstrapSensitiveInputs(t *testing.T) {
	for _, name := range []string{"action", "root", "package", "target", "expected", "token", "sent", "send"} {
		if strings.Contains(wpCoreUpdatePHPSource, "$"+name) {
			t.Fatalf("bootstrap-sensitive input $%s is not namespaced", name)
		}
		if !strings.Contains(wpCoreUpdatePHPSource, "$wp_panel_"+name) {
			t.Fatalf("namespaced input $wp_panel_%s is missing", name)
		}
	}
}

func TestWPCorePHPRunnerUsesSiteUserAndCleansRuntimePackage(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	uid, err := strconv.Atoi(current.Uid)
	if err != nil || uid <= 0 {
		t.Skip("requires non-root test user")
	}
	gid, err := strconv.Atoi(current.Gid)
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	wwwRoot := filepath.Join(base, "www")
	siteRoot := filepath.Join(wwwRoot, "example.com")
	binDir := filepath.Join(base, "bin")
	sbinDir := filepath.Join(base, "sbin")
	runtimeRoot := filepath.Join(base, "runtime")
	for _, dir := range []string{siteRoot, binDir, sbinDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(siteRoot, "wp-load.php"), []byte("<?php"), 0644); err != nil {
		t.Fatal(err)
	}
	phpPath := filepath.Join(binDir, "php8.3")
	phpScript := "#!/bin/bash\ntarget=${@: -2:1}\nexpected=${@: -1}\nif [[ -n \"$expected\" ]]; then package=${@: -3:1}; test -r \"$package\" || exit 9; fi\nprintf '{\"token\":\"%s\",\"ok\":true,\"version\":\"%s\",\"error_code\":\"\"}' \"$WP_PANEL_RUNNER_TOKEN\" \"$target\" >&3\n"
	if err := os.WriteFile(phpPath, []byte(phpScript), 0555); err != nil {
		t.Fatal(err)
	}
	runuserPath := filepath.Join(sbinDir, "runuser")
	if err := os.WriteFile(runuserPath, []byte("#!/bin/bash\nshift 3\nexec \"$@\"\n"), 0555); err != nil {
		t.Fatal(err)
	}
	packagePath := validWordPressZIP(t)
	sha, _, err := hashRegularFile(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := newWPCorePHPRunner(wpCorePHPRunnerOptions{wwwRoot: wwwRoot, runtimeRoot: runtimeRoot, phpPath: phpPath, runuserPath: runuserPath, phpDir: binDir, runuserDir: sbinDir, ownerUID: uid, ownerGID: gid, lookupUser: func(name string) (*user.User, error) {
		if name != "wp_test" {
			return nil, fmt.Errorf("unexpected user")
		}
		return &user.User{Username: name, Uid: current.Uid, Gid: current.Gid, HomeDir: base}, nil
	}, chown: func(string, int, int) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	execution := wpCoreUpdateExecution{Task: WPUpdateTask{ID: "wpu_0123456789abcdef0123456789abcdef", TargetVersion: "7.0.2", DownloadedSHA256: sha}, WebRoot: siteRoot, SystemUser: "wp_test", PackagePath: packagePath}
	if err := runner.Update(context.Background(), execution); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("runtime entries remain: %v", entries)
	}
	if err := runner.CheckLoad(context.Background(), execution, "7.0.2"); err != nil {
		t.Fatal(err)
	}
}

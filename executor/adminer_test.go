package executor

import (
	"bytes"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

func TestReadWebsiteDatabasePassword(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "single quotes", content: `<?php define('DB_PASSWORD', 'secret-value');`, want: "secret-value"},
		{name: "double quotes", content: `<?php define( "DB_PASSWORD" , "double-secret" );`, want: "double-secret"},
		{name: "escaped quote", content: `<?php define('DB_PASSWORD', 'it\'s-secret');`, want: "it's-secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "wp-config.php"), []byte(tt.content), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := ReadWebsiteDatabasePassword(&models.Website{SiteType: "wordpress", WebRoot: root})
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("password = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadWebsiteDatabasePasswordRejectsGenericPHPSite(t *testing.T) {
	if _, err := ReadWebsiteDatabasePassword(&models.Website{SiteType: "php", WebRoot: t.TempDir()}); err == nil {
		t.Fatal("expected generic PHP site to require an explicit password")
	}
}

// TestEmbeddedAdminerVersion just pins the embedded build's version string.
// If you bump it, that's not the whole upgrade: see the WARNING comment
// above the go:embed in adminer.go, and re-run
// TestAdminerLauncherAutoLoginSurvivesRedirect before calling it done.
func TestEmbeddedAdminerVersion(t *testing.T) {
	if len(adminerPHP) < 200_000 {
		t.Fatalf("embedded Adminer looks truncated: %d bytes", len(adminerPHP))
	}
	if want := []byte("@version 6.0.1"); !bytes.Contains(adminerPHP, want) {
		t.Fatalf("embedded Adminer does not contain %q", want)
	}
}

func TestAdminerLauncherPHPSyntax(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php is not installed")
	}
	path := filepath.Join(t.TempDir(), "index.php")
	if err := os.WriteFile(path, adminerIndexPHP, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(php, "-l", path).CombinedOutput(); err != nil {
		t.Fatalf("Adminer launcher PHP syntax failed: %s", output)
	}
}

func TestAdminerAutomaticDisableStopsProcessAndRemovesRuntime(t *testing.T) {
	runtimeDir := t.TempDir()
	marker := filepath.Join(runtimeDir, "runtime-marker")
	if err := os.WriteFile(marker, []byte("active"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	const siteID = 1
	manager := &adminerManager{instances: map[int]*adminerInstance{}}
	inst := &adminerInstance{cmd: cmd, runtimeDir: runtimeDir, siteID: siteID, expiresAt: time.Now().Add(30 * time.Millisecond)}
	inst.timer = time.AfterFunc(30*time.Millisecond, func() { manager.Disable(siteID) })
	manager.instances[siteID] = inst
	deadline := time.Now().Add(time.Second)
	for manager.Status(siteID).Enabled && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if manager.Status(siteID).Enabled {
		t.Fatal("Adminer remained enabled after its deadline")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Adminer runtime was not removed: %v", err)
	}
	_ = cmd.Wait()
}

func TestAdminerManagerSupportsConcurrentSites(t *testing.T) {
	manager := &adminerManager{instances: map[int]*adminerInstance{}}
	for _, siteID := range []int{1, 2} {
		cmd := exec.Command("sleep", "5")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		manager.instances[siteID] = &adminerInstance{cmd: cmd, runtimeDir: t.TempDir(), siteID: siteID}
	}
	defer manager.DisableAll()

	if !manager.Status(1).Enabled || !manager.Status(2).Enabled {
		t.Fatal("expected both sites to have an enabled Adminer instance")
	}

	manager.Disable(1)
	if manager.Status(1).Enabled {
		t.Fatal("expected site 1 to be disabled")
	}
	if !manager.Status(2).Enabled {
		t.Fatal("disabling site 1 should not affect site 2")
	}
}

func TestAdminerManagerEnforcesInstanceCap(t *testing.T) {
	manager := &adminerManager{instances: map[int]*adminerInstance{}}
	for i := 1; i <= maxAdminerInstances; i++ {
		manager.instances[i] = &adminerInstance{siteID: i}
	}
	if manager.canEnableLocked(maxAdminerInstances + 1) {
		t.Fatal("expected the cap to reject a new site once the limit is reached")
	}
	if !manager.canEnableLocked(1) {
		t.Fatal("re-enabling an already-running site should not be blocked by the cap")
	}
}

// TestAdminerLauncherAutoLoginSurvivesRedirect exercises the real login flow
// against a live "php -S" instance: it forges the login POST the same way
// Enable() does and checks that Adminer's own redirect back out (which reads
// the password back out of $_SESSION) actually happens. This is what would
// have caught the Adminer 6 CSRF-token/session-name regression that made
// auto-login silently fall back to the login form — TestAdminerLauncherPHPSyntax
// only checks that the script parses, not that it behaves.
func TestAdminerLauncherAutoLoginSurvivesRedirect(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php is not installed")
	}

	runtimeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(runtimeDir, "adminer.php"), adminerPHP, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "index.php"), []byte(adminerIndexPHP), 0600); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	cmd := exec.Command(php, "-d", "expose_php=0", "-d", "display_errors=0", "-S", address, "-t", runtimeDir)
	cmd.Env = append(os.Environ(),
		// The MySQL server here is deliberately unreachable: the login POST
		// this test checks only writes the password into $_SESSION and
		// redirects, it does not need a real database connection.
		"WP_PANEL_ADMINER_SERVER=127.0.0.1:1",
		"WP_PANEL_ADMINER_USER=tester",
		"WP_PANEL_ADMINER_PASSWORD=secret",
		"WP_PANEL_ADMINER_DATABASE=testdb",
		"WP_PANEL_ADMINER_SITE_ID=42",
		"WP_PANEL_ADMINER_SECURE_COOKIE=0",
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	backend := "http://" + address
	deadline := time.Now().Add(3 * time.Second)
	var ready bool
	for time.Now().Before(deadline) {
		resp, err := http.Get(backend)
		if err == nil {
			_ = resp.Body.Close()
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatal("Adminer instance did not come up in time")
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get(backend + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected the forged login POST to redirect (302 Found), got %d — Adminer's CSRF token or session check most likely rejected it", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if !strings.Contains(location, "username=tester") {
		t.Fatalf("redirect location %q does not carry the logged-in username", location)
	}

	backendURL, _ := url.Parse(backend)
	sawSessionCookie := false
	for _, c := range jar.Cookies(backendURL) {
		if strings.HasPrefix(c.Name, "adminer_sid_") {
			sawSessionCookie = true
		}
	}
	if !sawSessionCookie {
		t.Fatal("expected an adminer_sid_<site id> session cookie to be set on the login response")
	}
}

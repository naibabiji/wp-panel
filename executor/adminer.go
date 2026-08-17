package executor

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/models"
)

// WARNING: upgrading this embedded build is not a drop-in file swap. The
// launcher script (assets/adminer-runner/index.php) forges Adminer's
// internal, undocumented login/session/CSRF mechanics to give WordPress
// sites password-less login — it reverse-engineers implementation details
// that are free to change between Adminer versions (and did: the 5.5.1 ->
// 6.0.1 upgrade silently broke auto-login by adding a CSRF token check and
// changing session setup). After changing this embed, you MUST run
// `go test ./executor/... -run TestAdminerLauncherAutoLoginSurvivesRedirect`
// (it starts a real "php -S" instance and drives the actual login flow) and
// fix the launcher if it fails before considering the upgrade done.
//
//go:embed assets/adminer-6.0.1-mysql.php
var adminerPHP []byte

//go:embed assets/adminer-runner/index.php
var adminerIndexPHP []byte

// maxAdminerInstances caps how many websites can have Adminer enabled at the
// same time. Each instance is its own "php -S" process, temp directory and
// port, so an unbounded number of them is a real resource-exhaustion risk.
const maxAdminerInstances = 5

type AdminerStatus struct {
	Enabled   bool      `json:"enabled"`
	SiteID    int       `json:"site_id,omitempty"`
	Domain    string    `json:"domain,omitempty"`
	DBName    string    `json:"db_name,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Unlimited bool      `json:"unlimited"`
}

// syncBuffer is a concurrency-safe bytes.Buffer. cmd.Start() spawns a
// goroutine that copies the child process's stderr into this buffer for as
// long as the process runs, while the readiness-poll loop in Enable may read
// it from the calling goroutine at the same time; both must go through the
// same mutex.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// adminerInstance is one running "php -S" process serving Adminer for a
// single website's database. Multiple instances can run at once (up to
// maxAdminerInstances) so admins can manage several websites' databases
// concurrently.
type adminerInstance struct {
	cmd        *exec.Cmd
	runtimeDir string
	backend    *url.URL
	siteID     int
	domain     string
	dbName     string
	expiresAt  time.Time
	timer      *time.Timer
}

type adminerManager struct {
	mu        sync.Mutex
	instances map[int]*adminerInstance
}

var GlobalAdminer = &adminerManager{instances: make(map[int]*adminerInstance)}

var wpConfigDefinePattern = regexp.MustCompile(`(?m)define\s*\(\s*['"]DB_PASSWORD['"]\s*,\s*(?:'((?:\\.|[^'\\])*)'|"((?:\\.|[^"\\])*)")\s*\)\s*;`)

func ReadWebsiteDatabasePassword(site *models.Website) (string, error) {
	if site == nil || site.SiteType != "wordpress" {
		return "", errors.New("database password is required")
	}
	data, err := os.ReadFile(filepath.Join(site.WebRoot, "wp-config.php"))
	if err != nil {
		return "", fmt.Errorf("read wp-config.php: %w", err)
	}
	m := wpConfigDefinePattern.FindSubmatch(data)
	if len(m) != 3 {
		return "", errors.New("DB_PASSWORD was not found in wp-config.php")
	}
	password := string(m[1])
	if password != "" {
		password = strings.ReplaceAll(password, `\'`, `'`)
		password = strings.ReplaceAll(password, `\\`, `\`)
	} else {
		password = string(m[2])
		password = strings.ReplaceAll(password, `\"`, `"`)
		password = strings.ReplaceAll(password, `\\`, `\`)
	}
	return password, nil
}

func validateAdminerCredentials(site *models.Website, password string, cfg *config.Config) error {
	if site == nil || !isValidMySQLIdentifier(site.DBName) || !isValidMySQLIdentifier(site.DBUser) {
		return errors.New("invalid website database configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "mysql", "-h", cfg.MariaDB.Host, "-P", strconv.Itoa(cfg.MariaDB.Port), "-u", site.DBUser, "-D", site.DBName, "-N", "-e", "SELECT 1")
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+password)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("database login failed: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

// adminerReadinessClient bounds each individual poll request in Enable so a
// backend that accepts the TCP connection but never responds can't stall
// startup past the overall readiness deadline.
var adminerReadinessClient = &http.Client{Timeout: 500 * time.Millisecond}

func (m *adminerManager) Enable(site *models.Website, password string, duration time.Duration, cfg *config.Config) (AdminerStatus, error) {
	if password == "" {
		var err error
		password, err = ReadWebsiteDatabasePassword(site)
		if err != nil {
			return AdminerStatus{}, err
		}
	}
	if err := validateAdminerCredentials(site, password, cfg); err != nil {
		return AdminerStatus{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.canEnableLocked(site.ID) {
		return AdminerStatus{}, fmt.Errorf("maximum concurrent Adminer instances reached (%d); disable another website's Adminer first", maxAdminerInstances)
	}
	m.stopLocked(site.ID)

	runtimeDir, err := os.MkdirTemp(cfg.Panel.DataDir, "adminer-runtime-")
	if err != nil {
		return AdminerStatus{}, fmt.Errorf("create Adminer runtime: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(runtimeDir) }
	if err := os.WriteFile(filepath.Join(runtimeDir, "adminer.php"), adminerPHP, 0600); err != nil {
		cleanup()
		return AdminerStatus{}, fmt.Errorf("write Adminer: %w", err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "index.php"), adminerIndexPHP, 0600); err != nil {
		cleanup()
		return AdminerStatus{}, fmt.Errorf("write Adminer launcher: %w", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cleanup()
		return AdminerStatus{}, fmt.Errorf("allocate Adminer port: %w", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	secureCookie := "0"
	if cfg.Panel.TLSPort > 0 && cfg.Panel.TLSCertPath != "" && cfg.Panel.TLSKeyPath != "" {
		secureCookie = "1"
	}

	cmd := exec.Command("php", "-d", "expose_php=0", "-d", "display_errors=0", "-S", address, "-t", runtimeDir)
	cmd.Env = append(os.Environ(),
		"WP_PANEL_ADMINER_SERVER="+cfg.MariaDB.Host+":"+strconv.Itoa(cfg.MariaDB.Port),
		"WP_PANEL_ADMINER_USER="+site.DBUser,
		"WP_PANEL_ADMINER_PASSWORD="+password,
		"WP_PANEL_ADMINER_DATABASE="+site.DBName,
		"WP_PANEL_ADMINER_SITE_ID="+strconv.Itoa(site.ID),
		"WP_PANEL_ADMINER_SECURE_COOKIE="+secureCookie,
	)
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		cleanup()
		return AdminerStatus{}, fmt.Errorf("start Adminer: %w", err)
	}

	backend, _ := url.Parse("http://" + address)
	siteID := site.ID
	inst := &adminerInstance{
		cmd:        cmd,
		runtimeDir: runtimeDir,
		backend:    backend,
		siteID:     siteID,
		domain:     site.Domain,
		dbName:     site.DBName,
	}
	if duration > 0 {
		inst.expiresAt = time.Now().Add(duration)
		inst.timer = time.AfterFunc(duration, func() { m.Disable(siteID) })
	}
	m.instances[siteID] = inst

	// Reap the process whenever it exits, whether from an explicit Disable
	// (which already removed the map entry, making this a no-op) or from it
	// dying on its own (a crash, OOM kill, ...). Without this, a instance
	// that dies unprompted leaves a dead entry in the map forever when it
	// has no expiry, since nothing else would notice and clean it up.
	go func() {
		_ = cmd.Wait()
		m.mu.Lock()
		if cur, ok := m.instances[siteID]; ok && cur.cmd == cmd {
			m.stopLocked(siteID)
		}
		m.mu.Unlock()
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, requestErr := adminerReadinessClient.Get(backend.String())
		if requestErr == nil {
			_ = resp.Body.Close()
			if resp.StatusCode < 500 {
				return m.statusLocked(siteID), nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	m.stopLocked(siteID)
	return AdminerStatus{}, fmt.Errorf("Adminer did not become ready: %s", strings.TrimSpace(stderr.String()))
}

// canEnableLocked reports whether a new instance may be started for siteID:
// re-enabling an already-running site (which replaces it) never counts
// against the cap, only genuinely new sites do. Callers must hold m.mu.
func (m *adminerManager) canEnableLocked(siteID int) bool {
	if _, exists := m.instances[siteID]; exists {
		return true
	}
	return len(m.instances) < maxAdminerInstances
}

func (m *adminerManager) Disable(siteID int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked(siteID)
}

// DisableAll stops every running Adminer instance, e.g. on panel shutdown.
func (m *adminerManager) DisableAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for siteID := range m.instances {
		m.stopLocked(siteID)
	}
}

func (m *adminerManager) stopLocked(siteID int) {
	inst, ok := m.instances[siteID]
	if !ok {
		return
	}
	if inst.timer != nil {
		inst.timer.Stop()
	}
	if inst.cmd != nil && inst.cmd.Process != nil {
		if err := inst.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			log.Printf("Adminer: failed to stop process for site %d: %v", siteID, err)
		}
	}
	if inst.runtimeDir != "" {
		if err := os.RemoveAll(inst.runtimeDir); err != nil {
			log.Printf("Adminer: failed to remove runtime dir for site %d: %v", siteID, err)
		}
	}
	delete(m.instances, siteID)
}

func (m *adminerManager) Status(siteID int) AdminerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst, ok := m.instances[siteID]; ok && !inst.expiresAt.IsZero() && time.Now().After(inst.expiresAt) {
		m.stopLocked(siteID)
	}
	return m.statusLocked(siteID)
}

func (m *adminerManager) statusLocked(siteID int) AdminerStatus {
	inst, ok := m.instances[siteID]
	if !ok {
		return AdminerStatus{}
	}
	return AdminerStatus{Enabled: true, SiteID: inst.siteID, Domain: inst.domain, DBName: inst.dbName, ExpiresAt: inst.expiresAt, Unlimited: inst.expiresAt.IsZero()}
}

func (m *adminerManager) ServeHTTP(siteID int, w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	inst, ok := m.instances[siteID]
	if ok && !inst.expiresAt.IsZero() && time.Now().After(inst.expiresAt) {
		m.stopLocked(siteID)
		ok = false
	}
	var backend *url.URL
	if ok {
		backend = inst.backend
	}
	m.mu.Unlock()
	if backend == nil {
		http.NotFound(w, r)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(backend)
	original := proxy.Director
	proxy.Director = func(req *http.Request) {
		original(req)
		req.URL.Path = "/"
	}
	proxy.ServeHTTP(w, r)
}

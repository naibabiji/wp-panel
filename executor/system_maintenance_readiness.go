package executor

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"time"

	"github.com/naibabiji/wp-panel/config"
	"github.com/naibabiji/wp-panel/database"
)

var maintenanceMarkerPackageName = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*(?::[a-z0-9-]+)?$`)

func maintenanceNginxReady(ctx context.Context) error {
	var domain string
	err := database.GetDB().QueryRowContext(ctx, `SELECT domain FROM websites WHERE status='active' ORDER BY id LIMIT 1`).Scan(&domain)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1/", nil)
	if err != nil {
		return err
	}
	req.Host = domain
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return errors.New("local HTTP readiness failed")
	}
	return nil
}

func maintenanceFPMReady(ctx context.Context, cfg *config.Config) error {
	rows, err := database.GetDB().QueryContext(ctx, `SELECT name,php_pool_path FROM websites WHERE status='active'`)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name, pool string
		if err = rows.Scan(&name, &pool); err != nil {
			break
		}
		names = append(names, pathBaseWithoutExt(pool, siteConfigBaseName(name)))
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, name := range names {
		path := filepath.Join(cfg.Paths.PHPFPMSock, siteConfigBaseName(name)+".sock")
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", path)
		if err != nil {
			return err
		}
		conn.Close()
	}
	return nil
}

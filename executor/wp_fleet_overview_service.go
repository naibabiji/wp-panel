package executor

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/naibabiji/wp-panel/models"
)

const (
	wpFleetInventoryStaleAfter = 7 * 24 * time.Hour
	wpFleetSSLExpiringWithin   = 14 * 24 * time.Hour
)

// The plugin_updates/theme_updates/core_upgrade_available subqueries recompute
// update availability from the live candidate rows instead of trusting
// site_wp_inventory_state's cached counters. The site's own WordPress
// update-check transient can lag the live installed version by hours, leaving
// stale site_wp_component_updates rows whose target version equals what is
// already installed; excluding target_version = installed version keeps those
// out of the fleet overview. This stays a single query (see
// TestWPFleetOverviewUsesSingleQuery) so the endpoint's cost stays O(sites)
// regardless of fleet size.
const wpFleetOverviewSQL = `SELECT
	w.id, w.name, w.domain, w.site_type, w.status,
	w.created_at, w.expires_at,
	w.ssl_enabled, w.ssl_expires_at,
	CASE WHEN TRIM(COALESCE(w.ssl_last_error, '')) <> '' THEN 1 ELSE 0 END,
	w.monitoring_enabled, COALESCE(bs.enabled, 0), w.file_lock_enabled,
	w.fastcgi_cache_enabled, w.access_log_mode,
	CASE WHEN s.site_id IS NULL THEN 0 ELSE 1 END,
	COALESCE(s.status, 'unknown'), COALESCE(s.wordpress_version, ''),
	COALESCE(s.collection_id, ''), COALESCE(CAST(s.last_attempt_at AS TEXT), ''),
	COALESCE(CAST(s.last_success_at AS TEXT), ''), COALESCE(s.last_error_code, ''),
	COALESCE(s.last_error_stage, ''), COALESCE(j.status, ''),
	CASE WHEN s.collection_id <> '' THEN (
		SELECT COUNT(*) FROM site_wp_component_updates u
		LEFT JOIN site_wp_components c ON c.site_id = u.site_id AND c.collection_id = u.collection_id
			AND c.component_type = u.component_type AND c.component_key = u.component_key
		WHERE u.site_id = w.id AND u.collection_id = s.collection_id AND u.component_type = 'plugin'
			AND (c.version IS NULL OR c.version <> u.target_version)
	) ELSE 0 END,
	CASE WHEN s.collection_id <> '' THEN (
		SELECT COUNT(*) FROM site_wp_component_updates u
		LEFT JOIN site_wp_components c ON c.site_id = u.site_id AND c.collection_id = u.collection_id
			AND c.component_type = u.component_type AND c.component_key = u.component_key
		WHERE u.site_id = w.id AND u.collection_id = s.collection_id AND u.component_type = 'theme'
			AND (c.version IS NULL OR c.version <> u.target_version)
	) ELSE 0 END,
	CASE WHEN s.collection_id <> '' AND EXISTS (
		SELECT 1 FROM site_wp_component_updates u
		WHERE u.site_id = w.id AND u.collection_id = s.collection_id
		AND u.component_type = 'core' AND u.response = 'upgrade'
		AND u.target_version <> COALESCE(s.wordpress_version, '')
	) THEN 1 ELSE 0 END
	FROM websites w
	LEFT JOIN backup_settings bs ON bs.site_id = w.id
	LEFT JOIN site_wp_inventory_state s ON s.site_id = w.id
	LEFT JOIN site_wp_inventory_jobs j ON j.site_id = w.id AND j.status IN ('queued','running')
	ORDER BY w.created_at DESC, w.id DESC`

type WPFleetOverviewService struct {
	db  *sql.DB
	now func() time.Time
}

type wpFleetOverviewRow struct {
	id                   int
	name                 string
	domain               string
	siteType             string
	status               string
	createdAt            time.Time
	expiresAt            sql.NullTime
	sslEnabled           bool
	sslExpiresAt         sql.NullTime
	sslHasError          bool
	monitoringEnabled    bool
	backupEnabled        bool
	fileLockEnabled      bool
	fastCGICacheEnabled  bool
	accessLogMode        string
	hasInventoryState    bool
	inventoryStatus      string
	wordpressVersion     string
	pluginUpdates        int
	themeUpdates         int
	collectionID         string
	lastAttemptAt        string
	lastSuccessAt        string
	lastErrorCode        string
	lastErrorStage       string
	activeJobStatus      string
	coreUpgradeAvailable bool
}

func NewWPFleetOverviewService(db *sql.DB) (*WPFleetOverviewService, error) {
	return newWPFleetOverviewService(db, func() time.Time { return time.Now().UTC() })
}

func newWPFleetOverviewService(db *sql.DB, now func() time.Time) (*WPFleetOverviewService, error) {
	if db == nil || now == nil {
		return nil, errors.New("wordpress fleet overview dependency is nil")
	}
	return &WPFleetOverviewService{db: db, now: now}, nil
}

func (s *WPFleetOverviewService) Overview(ctx context.Context) (models.WPFleetOverview, error) {
	if s == nil || s.db == nil || s.now == nil {
		return models.WPFleetOverview{}, errors.New("wordpress fleet overview service is not initialized")
	}
	generatedAt := s.now().UTC()
	if generatedAt.IsZero() {
		return models.WPFleetOverview{}, errors.New("wordpress fleet overview time is zero")
	}
	rows, err := s.db.QueryContext(ctx, wpFleetOverviewSQL)
	if err != nil {
		return models.WPFleetOverview{}, err
	}
	defer rows.Close()

	sites := make([]models.WPFleetSite, 0)
	for rows.Next() {
		row, err := scanWPFleetOverviewRow(rows)
		if err != nil {
			return models.WPFleetOverview{}, err
		}
		site, err := wpFleetSiteModel(row, generatedAt)
		if err != nil {
			return models.WPFleetOverview{}, err
		}
		sites = append(sites, site)
	}
	if err := rows.Err(); err != nil {
		return models.WPFleetOverview{}, err
	}
	return models.WPFleetOverview{
		GeneratedAt: generatedAt,
		Counts:      wpFleetOverviewCounts(sites),
		Sites:       sites,
	}, nil
}

func scanWPFleetOverviewRow(rows *sql.Rows) (wpFleetOverviewRow, error) {
	var row wpFleetOverviewRow
	var sslEnabled, sslHasError, monitoringEnabled, backupEnabled int
	var fileLockEnabled, fastCGICacheEnabled, hasInventoryState, coreUpgradeAvailable int
	err := rows.Scan(
		&row.id, &row.name, &row.domain, &row.siteType, &row.status,
		&row.createdAt, &row.expiresAt, &sslEnabled, &row.sslExpiresAt, &sslHasError,
		&monitoringEnabled, &backupEnabled, &fileLockEnabled, &fastCGICacheEnabled,
		&row.accessLogMode, &hasInventoryState, &row.inventoryStatus, &row.wordpressVersion,
		&row.collectionID, &row.lastAttemptAt,
		&row.lastSuccessAt, &row.lastErrorCode, &row.lastErrorStage, &row.activeJobStatus,
		&row.pluginUpdates, &row.themeUpdates, &coreUpgradeAvailable,
	)
	row.sslEnabled = sslEnabled == 1
	row.sslHasError = sslHasError == 1
	row.monitoringEnabled = monitoringEnabled == 1
	row.backupEnabled = backupEnabled == 1
	row.fileLockEnabled = fileLockEnabled == 1
	row.fastCGICacheEnabled = fastCGICacheEnabled == 1
	row.hasInventoryState = hasInventoryState == 1
	row.coreUpgradeAvailable = coreUpgradeAvailable == 1
	return row, err
}

// nullTimeToPointer converts a scanned nullable DATETIME column into the
// *time.Time shape the fleet overview models use, without any string
// formatting/parsing round trip — the sqlite driver already handles the
// underlying storage format for us.
func nullTimeToPointer(v sql.NullTime) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time.UTC()
	return &t
}

func wpFleetSiteModel(row wpFleetOverviewRow, generatedAt time.Time) (models.WPFleetSite, error) {
	if row.id <= 0 || row.domain == "" {
		return models.WPFleetSite{}, errors.New("invalid wordpress fleet site identity")
	}
	switch row.siteType {
	case "wordpress", "php":
	default:
		return models.WPFleetSite{}, errors.New("invalid wordpress fleet site type")
	}
	switch row.status {
	case string(models.StatusActive), string(models.StatusPaused), string(models.StatusError),
		string(models.StatusCreating), string(models.StatusDeleting):
	default:
		return models.WPFleetSite{}, errors.New("invalid wordpress fleet site status")
	}
	switch row.accessLogMode {
	case "off", "error_only", "full":
	default:
		return models.WPFleetSite{}, errors.New("invalid wordpress fleet access log mode")
	}
	if row.createdAt.IsZero() {
		return models.WPFleetSite{}, errors.New("invalid wordpress fleet created_at")
	}
	createdAt := row.createdAt.UTC()
	expiresAt := nullTimeToPointer(row.expiresAt)
	sslExpiresAt := nullTimeToPointer(row.sslExpiresAt)
	sslState := wpFleetSSLState(row.sslEnabled, row.sslHasError, sslExpiresAt, generatedAt)

	var inventory *models.WPFleetInventory
	if row.siteType == "wordpress" {
		var err error
		inventory, err = wpFleetInventoryModel(row, generatedAt)
		if err != nil {
			return models.WPFleetSite{}, err
		}
	}
	site := models.WPFleetSite{
		ID: row.id, Name: row.name, Domain: row.domain, SiteType: row.siteType, Status: row.status,
		CreatedAt: createdAt, ExpiresAt: expiresAt, SSLEnabled: row.sslEnabled,
		SSLExpiresAt: sslExpiresAt, SSLState: sslState, MonitoringEnabled: row.monitoringEnabled,
		BackupEnabled: row.backupEnabled, FileLockEnabled: row.fileLockEnabled,
		FastCGICacheEnabled: row.fastCGICacheEnabled, AccessLogMode: row.accessLogMode,
		Inventory: inventory,
	}
	site.Health = wpFleetHealth(site)
	return site, nil
}

func wpFleetInventoryModel(row wpFleetOverviewRow, generatedAt time.Time) (*models.WPFleetInventory, error) {
	state := wpInventoryState{
		SiteID: row.id, Status: row.inventoryStatus, WordPressVersion: row.wordpressVersion,
		PluginUpdateCount: row.pluginUpdates, ThemeUpdateCount: row.themeUpdates,
		CollectionID: row.collectionID, LastAttemptAt: row.lastAttemptAt, LastSuccessAt: row.lastSuccessAt,
		LastErrorCode: row.lastErrorCode, LastErrorStage: row.lastErrorStage,
	}
	if !row.hasInventoryState {
		state = wpInventoryState{SiteID: row.id, Status: "unknown"}
	}
	if err := validateWPInventoryPublicState(state); err != nil {
		return nil, err
	}
	if row.pluginUpdates < 0 || row.themeUpdates < 0 {
		return nil, errors.New("invalid wordpress fleet update count")
	}
	lastAttemptAt, err := parseOptionalWPInventoryTime(state.LastAttemptAt)
	if err != nil {
		return nil, err
	}
	lastSuccessAt, err := parseOptionalWPInventoryTime(state.LastSuccessAt)
	if err != nil {
		return nil, err
	}
	status := state.Status
	if row.activeJobStatus != "" {
		switch row.activeJobStatus {
		case string(wpInventoryJobQueued), string(wpInventoryJobRunning):
			status = row.activeJobStatus
		default:
			return nil, errors.New("invalid wordpress fleet active job status")
		}
	}
	stale := lastSuccessAt != nil && lastSuccessAt.Before(generatedAt.Add(-wpFleetInventoryStaleAfter))
	updateTotal := row.pluginUpdates + row.themeUpdates
	if row.coreUpgradeAvailable {
		updateTotal++
	}
	return &models.WPFleetInventory{
		Status: status, HasSuccessfulInventory: state.CollectionID != "", WordPressVersion: state.WordPressVersion,
		PluginUpdates: row.pluginUpdates, ThemeUpdates: row.themeUpdates,
		CoreUpgradeAvailable: row.coreUpgradeAvailable, UpdateTotal: updateTotal,
		LastAttemptAt: lastAttemptAt, LastSuccessAt: lastSuccessAt, Stale: stale,
	}, nil
}

func wpFleetSSLState(enabled, hasError bool, expiresAt *time.Time, generatedAt time.Time) string {
	if !enabled {
		if hasError {
			return "pending_error"
		}
		return "disabled"
	}
	if expiresAt == nil {
		return "expiry_unknown"
	}
	if !expiresAt.After(generatedAt) {
		return "expired"
	}
	if !expiresAt.After(generatedAt.Add(wpFleetSSLExpiringWithin)) {
		return "expiring"
	}
	return "valid"
}

func wpFleetHealth(site models.WPFleetSite) models.WPFleetHealth {
	issues := make([]string, 0, 5)
	level := "healthy"
	critical := false
	warning := false
	unknown := false
	if site.Status == string(models.StatusError) {
		issues = append(issues, "site_error")
		critical = true
	}
	switch site.SSLState {
	case "expired":
		issues = append(issues, "ssl_expired")
		critical = true
	case "expiring":
		issues = append(issues, "ssl_expiring")
		warning = true
	case "pending_error":
		issues = append(issues, "ssl_setup_failed")
		warning = true
	case "expiry_unknown":
		issues = append(issues, "ssl_expiry_unknown")
		warning = true
	}
	if site.Inventory != nil {
		if site.Inventory.Status == "failed" {
			issues = append(issues, "inventory_failed")
			warning = true
		}
		if site.Inventory.Stale {
			issues = append(issues, "inventory_stale")
			warning = true
		}
		if site.Inventory.UpdateTotal > 0 {
			issues = append(issues, "updates_available")
			warning = true
		}
		if !site.Inventory.HasSuccessfulInventory {
			issues = append(issues, "inventory_uncollected")
			unknown = true
		}
	}
	switch {
	case critical:
		level = "critical"
	case warning:
		level = "warning"
	case unknown:
		level = "unknown"
	}
	return models.WPFleetHealth{Level: level, Issues: issues}
}

func wpFleetOverviewCounts(sites []models.WPFleetSite) models.WPFleetOverviewCounts {
	counts := models.WPFleetOverviewCounts{TotalSites: len(sites)}
	for _, site := range sites {
		switch site.Health.Level {
		case "critical":
			counts.CriticalSites++
		case "warning":
			counts.WarningSites++
		case "unknown":
			counts.UnknownSites++
		case "healthy":
			counts.HealthySites++
		}
		if site.SiteType != "wordpress" || site.Inventory == nil {
			continue
		}
		counts.WordPressSites++
		if site.Inventory.UpdateTotal > 0 {
			counts.UpdateSites++
		}
		failed := site.Inventory.Status == "failed"
		if failed {
			counts.FailedInventorySites++
		}
		if site.Inventory.Stale {
			counts.StaleInventorySites++
		}
		if failed || site.Inventory.Stale {
			counts.InventoryAttentionSites++
		}
		if !site.Inventory.HasSuccessfulInventory {
			counts.UncollectedSites++
		}
	}
	return counts
}

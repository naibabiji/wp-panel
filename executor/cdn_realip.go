package executor

import (
	"database/sql"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"
)

const (
	CDNProviderCloudflare  = "cloudflare"
	CDNProviderCompatible  = "compatible"
	CDNProviderCustom      = "custom"
	CDNHeaderXForwardedFor = "X-Forwarded-For"
	CDNHeaderXRealIP       = "X-Real-IP"
	CDNHeaderCFConnecting  = "CF-Connecting-IP"
)

var cdnHeaderNameRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

type CDNRealIPRuntime struct {
	Enabled    bool
	HeaderName string
	IPRanges   []string
	Compatible bool
}

func NormalizeCDNRealIPHeader(raw string) (string, error) {
	header := strings.TrimSpace(raw)
	if header == "" {
		return "", fmt.Errorf("真实 IP Header 不能为空")
	}
	if !cdnHeaderNameRe.MatchString(header) {
		return "", fmt.Errorf("真实 IP Header 只能包含英文字母、数字和短横线")
	}
	return header, nil
}

func NormalizeCDNRealIPRanges(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	lines := strings.Split(raw, "\n")
	if len(lines) > 1000 {
		return nil, fmt.Errorf("CDN 回源 IP 段数量过大")
	}
	seen := map[string]bool{}
	var out []string
	for _, line := range lines {
		item := strings.TrimSpace(line)
		if item == "" {
			continue
		}
		if strings.ContainsAny(item, " \t\r") {
			return nil, fmt.Errorf("CDN 回源 IP 格式不正确: %s", item)
		}
		if strings.Contains(item, "/") {
			if _, _, err := net.ParseCIDR(item); err != nil {
				return nil, fmt.Errorf("CDN 回源 IP 格式不正确: %s", item)
			}
		} else if net.ParseIP(item) == nil {
			return nil, fmt.Errorf("CDN 回源 IP 格式不正确: %s", item)
		}
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	return out, nil
}

func JoinCDNRealIPRanges(ranges []string) string {
	return strings.Join(ranges, "\n")
}

func ListCDNRealIPGroups() ([]models.CDNRealIPGroup, error) {
	rows, err := database.GetDB().Query(`SELECT id, name, provider, header_name, ip_ranges, builtin, enabled, description, created_at, updated_at
		FROM cdn_realip_groups ORDER BY builtin DESC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []models.CDNRealIPGroup
	for rows.Next() {
		group, err := scanCDNRealIPGroup(rows.Scan)
		if err != nil {
			continue
		}
		groups = append(groups, group)
	}
	if groups == nil {
		groups = []models.CDNRealIPGroup{}
	}
	return groups, rows.Err()
}

func GetWebsiteCDNRealIPGroups(websiteID int) ([]models.CDNRealIPGroup, error) {
	rows, err := database.GetDB().Query(`SELECT g.id, g.name, g.provider, g.header_name, g.ip_ranges, g.builtin, g.enabled, g.description, g.created_at, g.updated_at
		FROM cdn_realip_groups g
		INNER JOIN website_cdn_realip_groups wg ON wg.group_id = g.id
		WHERE wg.website_id = ?
		ORDER BY g.builtin DESC, g.name ASC`, websiteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []models.CDNRealIPGroup
	for rows.Next() {
		group, err := scanCDNRealIPGroup(rows.Scan)
		if err != nil {
			continue
		}
		groups = append(groups, group)
	}
	if groups == nil {
		groups = []models.CDNRealIPGroup{}
	}
	return groups, rows.Err()
}

func LoadWebsiteCDNRealIPGroups(site *models.Website) {
	if site == nil {
		return
	}
	groups, err := GetWebsiteCDNRealIPGroups(site.ID)
	if err == nil {
		site.CDNRealIPGroups = groups
	}
}

func ResolveCDNRealIPRuntime(site *models.Website) (*CDNRealIPRuntime, error) {
	if site == nil || !site.CDNRealIPEnabled || len(site.CDNRealIPGroups) == 0 {
		return &CDNRealIPRuntime{}, nil
	}
	header := ""
	compatible := false
	enabledGroups := 0
	seen := map[string]bool{}
	var ranges []string

	for _, group := range site.CDNRealIPGroups {
		if !group.Enabled {
			continue
		}
		enabledGroups++
		groupHeader, err := NormalizeCDNRealIPHeader(group.HeaderName)
		if err != nil {
			return nil, err
		}
		if header == "" {
			header = groupHeader
		} else if !strings.EqualFold(header, groupHeader) {
			return nil, fmt.Errorf("同一网站绑定的 CDN 配置组必须使用相同 Header")
		}

		groupRanges := strings.TrimSpace(group.IPRanges)
		if group.Provider == CDNProviderCloudflare && groupRanges == "" {
			groupRanges = cachedCloudflareRealIPRanges()
			if strings.TrimSpace(groupRanges) == "" {
				return nil, fmt.Errorf("Cloudflare 官方 IP 段尚未缓存，请先刷新官方白名单")
			}
		}
		normalized, err := NormalizeCDNRealIPRanges(groupRanges)
		if err != nil {
			return nil, err
		}
		if len(normalized) == 0 {
			compatible = true
			continue
		}
		for _, item := range normalized {
			if !seen[item] {
				seen[item] = true
				ranges = append(ranges, item)
			}
		}
	}
	if compatible && enabledGroups > 1 {
		return nil, fmt.Errorf("兼容模式会信任所有来源，不能与其他 CDN 配置组同时启用")
	}

	if header == "" {
		return &CDNRealIPRuntime{}, nil
	}
	sort.Strings(ranges)
	return &CDNRealIPRuntime{
		Enabled:    true,
		HeaderName: header,
		IPRanges:   ranges,
		Compatible: compatible,
	}, nil
}

func CombinedCDNRealIPRangesForFail2ban() string {
	rows, err := database.GetDB().Query(`SELECT DISTINCT g.id, g.name, g.provider, g.header_name, g.ip_ranges, g.builtin, g.enabled, g.description, g.created_at, g.updated_at
		FROM cdn_realip_groups g
		INNER JOIN website_cdn_realip_groups wg ON wg.group_id = g.id
		INNER JOIN websites w ON w.id = wg.website_id
		WHERE g.enabled = 1 AND w.cdn_realip_enabled = 1`)
	if err != nil {
		return ""
	}
	defer rows.Close()
	seen := map[string]bool{}
	var merged []string
	for rows.Next() {
		group, err := scanCDNRealIPGroup(rows.Scan)
		if err != nil {
			continue
		}
		raw := group.IPRanges
		if group.Provider == CDNProviderCloudflare && strings.TrimSpace(raw) == "" {
			raw = cachedCloudflareRealIPRanges()
		}
		ranges, err := NormalizeCDNRealIPRanges(raw)
		if err != nil {
			continue
		}
		for _, item := range ranges {
			if !seen[item] {
				seen[item] = true
				merged = append(merged, item)
			}
		}
	}
	sort.Strings(merged)
	return strings.Join(merged, "\n")
}

func scanCDNRealIPGroup(scan func(dest ...interface{}) error) (models.CDNRealIPGroup, error) {
	var group models.CDNRealIPGroup
	var builtin, enabled int
	err := scan(&group.ID, &group.Name, &group.Provider, &group.HeaderName, &group.IPRanges, &builtin, &enabled, &group.Description, &group.CreatedAt, &group.UpdatedAt)
	group.Builtin = builtin == 1
	group.Enabled = enabled == 1
	return group, err
}

func cachedCloudflareRealIPRanges() string {
	var raw string
	_ = database.GetDB().QueryRow(`SELECT svalue FROM security_settings WHERE skey = 'cloudflare_realip_ips'`).Scan(&raw)
	return raw
}

func CdnRealIPGroupExists(id int) bool {
	var found int
	err := database.GetDB().QueryRow(`SELECT 1 FROM cdn_realip_groups WHERE id = ? AND enabled = 1`, id).Scan(&found)
	return err == nil && found == 1
}

func GetCDNRealIPGroup(id int) (models.CDNRealIPGroup, error) {
	return scanCDNRealIPGroup(database.GetDB().QueryRow(`SELECT id, name, provider, header_name, ip_ranges, builtin, enabled, description, created_at, updated_at
		FROM cdn_realip_groups WHERE id = ?`, id).Scan)
}

func WebsiteIDsForCDNRealIPGroup(groupID int) ([]int, error) {
	rows, err := database.GetDB().Query(`SELECT website_id FROM website_cdn_realip_groups WHERE group_id = ? ORDER BY website_id`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var websiteIDs []int
	for rows.Next() {
		var websiteID int
		if err := rows.Scan(&websiteID); err != nil {
			return nil, err
		}
		websiteIDs = append(websiteIDs, websiteID)
	}
	return websiteIDs, rows.Err()
}

func RestoreCDNRealIPGroupWithBindings(group models.CDNRealIPGroup, websiteIDs []int) error {
	tx, err := database.GetDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT OR REPLACE INTO cdn_realip_groups
		(id, name, provider, header_name, ip_ranges, builtin, enabled, description, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		group.ID, group.Name, group.Provider, group.HeaderName, group.IPRanges,
		boolToDBInt(group.Builtin), boolToDBInt(group.Enabled), group.Description, group.CreatedAt, group.UpdatedAt); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM website_cdn_realip_groups WHERE group_id = ?`, group.ID); err != nil {
		return err
	}
	for _, websiteID := range websiteIDs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO website_cdn_realip_groups (website_id, group_id) VALUES (?, ?)`, websiteID, group.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func GetEnabledCDNRealIPGroupsByIDs(groupIDs []int) ([]models.CDNRealIPGroup, error) {
	seenIDs := map[int]bool{}
	var groups []models.CDNRealIPGroup
	for _, id := range groupIDs {
		if id <= 0 || seenIDs[id] {
			continue
		}
		seenIDs[id] = true
		group, err := GetCDNRealIPGroup(id)
		if err != nil {
			return nil, fmt.Errorf("CDN 配置组不存在")
		}
		if !group.Enabled {
			return nil, fmt.Errorf("CDN 配置组已禁用: %s", group.Name)
		}
		groups = append(groups, group)
	}
	return groups, nil
}

func UpdateWebsiteCDNRealIPGroups(tx *sql.Tx, websiteID int, groupIDs []int) error {
	if _, err := tx.Exec(`DELETE FROM website_cdn_realip_groups WHERE website_id = ?`, websiteID); err != nil {
		return err
	}
	for _, id := range groupIDs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO website_cdn_realip_groups (website_id, group_id) VALUES (?, ?)`, websiteID, id); err != nil {
			return err
		}
	}
	return nil
}

func SaveWebsiteCDNRealIPSettings(websiteID int, enabled bool, groupIDs []int) error {
	tx, err := database.GetDB().Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE websites SET cdn_realip_enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, boolToDBInt(enabled), websiteID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := UpdateWebsiteCDNRealIPGroups(tx, websiteID, groupIDs); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

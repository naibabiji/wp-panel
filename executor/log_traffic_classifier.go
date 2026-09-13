package executor

import (
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/naibabiji/wp-panel/models"
)

const logTrafficClassificationVersion = 1

const (
	logTrafficSecurityRejected     = "security_rejected"
	logTrafficIdentifiedAutomation = "identified_automation"
	logTrafficHTTPError            = "http_error"
	logTrafficWordPressEndpoint    = "wordpress_endpoint"
	logTrafficStaticAsset          = "static_asset"
	logTrafficPageLike             = "page_like"
	logTrafficOther                = "other"
)

var logTrafficCategoryOrder = []struct {
	key       string
	certainty string
}{
	{logTrafficSecurityRejected, "confirmed"},
	{logTrafficIdentifiedAutomation, "high_confidence"},
	{logTrafficHTTPError, "confirmed"},
	{logTrafficWordPressEndpoint, "confirmed"},
	{logTrafficStaticAsset, "confirmed"},
	{logTrafficPageLike, "heuristic"},
	{logTrafficOther, "confirmed"},
}

func validLogTrafficCategory(value string) bool {
	for _, item := range logTrafficCategoryOrder {
		if item.key == value {
			return true
		}
	}
	return false
}

func classifyLogTraffic(method, rawPath, status, ua, ip string, checker *searchBotIPChecker) string {
	code, _ := strconv.Atoi(status)
	if status == "444" {
		return logTrafficSecurityRejected
	}
	if isIdentifiedAutomation(checker, ua, ip) {
		return logTrafficIdentifiedAutomation
	}
	if code >= 400 && code <= 599 {
		return logTrafficHTTPError
	}
	if isWordPressEndpoint(rawPath) {
		return logTrafficWordPressEndpoint
	}
	if code >= 200 && code <= 399 && isStaticAssetPath(rawPath) {
		return logTrafficStaticAsset
	}
	if (method == "GET" || method == "HEAD") && code >= 200 && code <= 399 {
		return logTrafficPageLike
	}
	return logTrafficOther
}

func isIdentifiedAutomation(checker *searchBotIPChecker, ua, ip string) bool {
	if name, _ := identifySearchBot(checker, ua, ip); name != "" {
		return true
	}
	lower := strings.ToLower(ua)
	for _, token := range []string{"curl/", "wget/", "python-requests", "python-urllib", "go-http-client", "libwww-perl", "scrapy/"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func isWordPressEndpoint(raw string) bool {
	normalized := strings.ToLower(normalizeLogPath(raw))
	if normalized == "/wp-admin" || strings.HasPrefix(normalized, "/wp-admin/") || normalized == "/wp-login.php" || normalized == "/wp-cron.php" || normalized == "/xmlrpc.php" || normalized == "/wp-json" || strings.HasPrefix(normalized, "/wp-json/") {
		return true
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Query().Has("rest_route")
}

func isStaticAssetPath(raw string) bool {
	ext := strings.ToLower(path.Ext(normalizeLogPath(raw)))
	switch ext {
	case ".css", ".js", ".map", ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".svg", ".ico", ".woff", ".woff2", ".ttf", ".eot":
		return true
	default:
		return false
	}
}

func buildLogTrafficCategories(counts map[string]int, ips map[string]map[string]struct{}) []models.LogTrafficCategory {
	items := make([]models.LogTrafficCategory, 0, len(logTrafficCategoryOrder))
	for _, item := range logTrafficCategoryOrder {
		items = append(items, models.LogTrafficCategory{Key: item.key, Count: counts[item.key], UniqueIPs: len(ips[item.key]), Certainty: item.certainty})
	}
	return items
}

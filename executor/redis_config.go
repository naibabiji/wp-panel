package executor

import "strings"

const redisDefaultMaxmemoryPolicy = "allkeys-lru"

// BuildRedisBaselineConfig fills only Redis directives that have no active
// value. Explicit administrator settings, including noeviction, are preserved.
func BuildRedisBaselineConfig(content, recommendedMaxmemory string) (string, bool) {
	var directives []string
	if FindRedisConfigValue(content, "maxmemory") == "" {
		directives = append(directives, "maxmemory "+recommendedMaxmemory)
	}
	if FindRedisConfigValue(content, "maxmemory-policy") == "" && FindRedisConfigValue(content, "include") == "" {
		directives = append(directives, "maxmemory-policy "+redisDefaultMaxmemoryPolicy)
	}
	if len(directives) == 0 {
		return content, false
	}

	prefix := content
	if prefix != "" {
		if !strings.HasSuffix(prefix, "\n") {
			prefix += "\n"
		}
		prefix += "\n"
	}
	next := prefix + "# WP Panel — WordPress object cache baseline\n" + strings.Join(directives, "\n") + "\n"
	return next, true
}

// ReplaceRedisConfigValue updates all active duplicates or appends the key when
// the configuration only contains the documented/commented default.
func ReplaceRedisConfigValue(content, key, value string) string {
	lines := strings.Split(content, "\n")
	// Strip INI-style lines accidentally written by old software-page handling.
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, key+" =") {
			continue
		}
		filtered = append(filtered, line)
	}
	lines = filtered

	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 && fields[0] == key {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = indent + key + " " + value
			found = true
		}
	}
	if !found {
		lines = append(lines, "", "# WP Panel", key+" "+value)
	}
	return strings.Join(lines, "\n")
}

// FindRedisConfigValue returns the last active value for a Redis directive,
// matching Redis' effective handling of repeated directives. Commented examples
// in the stock redis.conf are deliberately ignored.
func FindRedisConfigValue(content, key string) string {
	value := ""
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 && fields[0] == key {
			if fields[1] == "=" && len(fields) >= 3 {
				value = fields[2]
				continue
			}
			value = fields[1]
		}
	}
	return value
}

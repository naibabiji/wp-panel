package executor

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/naibabiji/wp-panel/database"
)

var aiIPCandidatePattern = regexp.MustCompile(`(?i)\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b|(?:[0-9a-f]{0,4}:){2,}[0-9a-f:]*`)
var aiIPAliasPattern = regexp.MustCompile(`\bIP-[0-9]{2,}\b`)

// AnonymizeAIText replaces valid IP addresses with aliases that remain stable
// within one AI session. The private mapping never leaves the panel database.
func AnonymizeAIText(sessionID int, value string) (string, error) {
	if sessionID <= 0 || strings.TrimSpace(value) == "" {
		return value, nil
	}
	db := database.GetDB()
	if db == nil {
		return "", fmt.Errorf("数据库未初始化")
	}
	aliases := map[string]string{}
	rows, err := db.Query(`SELECT ip_address, alias FROM ai_ip_aliases WHERE session_id=?`, sessionID)
	if err != nil {
		return "", err
	}
	next := 1
	for rows.Next() {
		var ip, alias string
		if err := rows.Scan(&ip, &alias); err != nil {
			rows.Close()
			return "", err
		}
		aliases[ip] = alias
		if number, err := strconv.Atoi(strings.TrimPrefix(alias, "IP-")); err == nil && number >= next {
			next = number + 1
		}
	}
	if err := rows.Close(); err != nil {
		return "", err
	}

	for _, candidate := range aiIPCandidatePattern.FindAllString(value, -1) {
		if net.ParseIP(candidate) == nil {
			continue
		}
		if _, exists := aliases[candidate]; exists {
			continue
		}
		alias := fmt.Sprintf("IP-%02d", next)
		if _, err := db.Exec(`INSERT INTO ai_ip_aliases (session_id, alias, ip_address) VALUES (?, ?, ?)`, sessionID, alias, candidate); err != nil {
			return "", err
		}
		aliases[candidate] = alias
		next++
	}

	return aiIPCandidatePattern.ReplaceAllStringFunc(value, func(candidate string) string {
		if alias, exists := aliases[candidate]; exists && net.ParseIP(candidate) != nil {
			return alias
		}
		return candidate
	}), nil
}

// RestoreAIIPAliases turns aliases in an AI response back into the local real
// addresses before the response is stored or shown to the administrator.
func RestoreAIIPAliases(sessionID int, value string) (string, error) {
	if sessionID <= 0 || strings.TrimSpace(value) == "" {
		return value, nil
	}
	db := database.GetDB()
	if db == nil {
		return "", fmt.Errorf("数据库未初始化")
	}
	rows, err := db.Query(`SELECT alias, ip_address FROM ai_ip_aliases WHERE session_id=?`, sessionID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	addresses := map[string]string{}
	for rows.Next() {
		var alias, ip string
		if err := rows.Scan(&alias, &ip); err != nil {
			return "", err
		}
		addresses[alias] = ip
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return aiIPAliasPattern.ReplaceAllStringFunc(value, func(alias string) string {
		if ip, exists := addresses[alias]; exists {
			return ip
		}
		return alias
	}), nil
}

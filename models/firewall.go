package models

import "time"

type BanLevel int

const (
	BanLevelNone          BanLevel = 0
	BanLevelRateLimit     BanLevel = 1
	BanLevelTemp10m       BanLevel = 2
	BanLevelTemp24h       BanLevel = 3
	BanLevelPermCandidate BanLevel = 4
)

type FirewallBan struct {
	ID         int        `json:"id"`
	IPAddress  string     `json:"ip_address"`
	BanLevel   BanLevel   `json:"ban_level"`
	Reason     string     `json:"reason"`
	SourceJail string     `json:"source_jail"`
	BannedAt   time.Time  `json:"banned_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	UnbannedAt *time.Time `json:"unbanned_at"`
	BanCount   int        `json:"ban_count"`
	IsManual   bool       `json:"is_manual"`
}

type UpdateWhitelistRequest struct {
	IPs string `json:"ips" binding:"required"`
}

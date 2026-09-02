package executor

import (
	"net"
	"strings"
)

var currentBanCommand = executeCommand

type CurrentBanKey struct {
	IP     string
	Source string
}

type CurrentBanReadStatus struct {
	Fail2ban map[string]bool `json:"fail2ban"`
	Nftables bool            `json:"nftables"`
	Nginx    bool            `json:"nginx"`
}

type CurrentBanEnforcement struct {
	Fail2ban map[CurrentBanKey]bool
	Persist  map[string]bool
	Nginx    map[string]bool
	Status   CurrentBanReadStatus
}

// ReadCurrentBanEnforcement reads the live enforcement layers. Each layer has
// its own status so callers never mistake a read failure for an empty ban set.
func ReadCurrentBanEnforcement() CurrentBanEnforcement {
	snapshot := readActiveFail2banBans()
	return readCurrentBanEnforcement(snapshot)
}

func readCurrentBanEnforcement(snapshot fail2banSnapshot) CurrentBanEnforcement {
	state := CurrentBanEnforcement{
		Fail2ban: make(map[CurrentBanKey]bool),
		Persist:  make(map[string]bool),
		Nginx:    make(map[string]bool),
		Status: CurrentBanReadStatus{
			Fail2ban: make(map[string]bool),
		},
	}
	for jail, read := range snapshot.jailStatusRead {
		state.Status.Fail2ban[jail] = read
	}
	for pair := range snapshot.active {
		state.Fail2ban[CurrentBanKey{IP: pair.ip, Source: pair.jail}] = true
	}

	EnsurePersistNftables()
	if out, err := currentBanCommand("nft", "list", "set", "ip", "wppanel_persist", "banned_ips"); err == nil {
		state.Status.Nftables = true
		state.Persist = parseNftSetIPs(out)
	}
	if ips, err := readNginxBannedIPs(); err == nil {
		state.Status.Nginx = true
		state.Nginx = ips
	}
	return state
}

func parseNftSetIPs(output string) map[string]bool {
	ips := make(map[string]bool)
	start := strings.Index(output, "elements = {")
	if start < 0 {
		return ips
	}
	rest := output[start+len("elements = {"):]
	end := strings.Index(rest, "}")
	if end < 0 {
		return ips
	}
	for _, entry := range strings.Split(rest[:end], ",") {
		fields := strings.Fields(strings.TrimSpace(entry))
		if len(fields) == 0 {
			continue
		}
		ip := strings.TrimSpace(fields[0])
		if net.ParseIP(ip) != nil {
			ips[ip] = true
		}
	}
	return ips
}

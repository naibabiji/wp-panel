package executor

import (
	"net"
	"strings"
)

var currentBanCommand = executeCommand
var currentBanEnsurePersist = EnsurePersistNftables

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
		ip := pair.ip
		if normalized, ok := NormalizeIP(ip); ok {
			ip = normalized
		}
		state.Fail2ban[CurrentBanKey{IP: ip, Source: pair.jail}] = true
	}

	_ = currentBanEnsurePersist()
	allFamiliesRead := true
	for _, family := range []string{"ip", "ip6"} {
		out, err := currentBanCommand("nft", "list", "set", family, "wppanel_persist", "banned_ips")
		if err != nil {
			allFamiliesRead = false
			continue
		}
		for ip := range parseNftSetIPs(out) {
			state.Persist[ip] = true
		}
	}
	state.Status.Nftables = allFamiliesRead
	if ips, err := readNginxBannedIPs(); err == nil {
		state.Status.Nginx = true
		for ip := range ips {
			if normalized, ok := NormalizeIP(ip); ok {
				ip = normalized
			}
			state.Nginx[ip] = true
		}
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
		if ip, ok := NormalizeIP(fields[0]); ok {
			ips[ip] = true
		}
	}
	return ips
}

// NormalizeIP provides one comparison boundary for database and nftables
// addresses without changing the stored or displayed database value.
func NormalizeIP(value string) (string, bool) {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return "", false
	}
	return ip.String(), true
}

package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var panelCapabilitiesDirectory = "/etc/wp-panel"

type panelManifestPayload struct {
	CatalogSchemaVersion  int               `json:"catalog_schema_version"`
	ManifestSchemaVersion int               `json:"manifest_schema_version"`
	ManifestVersion       int               `json:"manifest_version"`
	Capabilities          []json.RawMessage `json:"capabilities"`
	RetiredCapabilities   []json.RawMessage `json:"retired_capabilities"`
}

type panelManifestOutput struct {
	OK                    bool                `json:"ok"`
	PanelVersion          string              `json:"panel_version"`
	CatalogSchemaVersion  int                 `json:"catalog_schema_version"`
	ManifestSchemaVersion int                 `json:"manifest_schema_version"`
	ManifestVersion       int                 `json:"manifest_version"`
	GeneratedAt           string              `json:"generated_at"`
	ManifestSHA256        string              `json:"manifest_sha256"`
	Capabilities          []json.RawMessage   `json:"capabilities"`
	RetiredCapabilities   []json.RawMessage   `json:"retired_capabilities"`
	CapabilitySync        string              `json:"capability_sync"`
	ServerAIPolicy        panelServerAIPolicy `json:"server_ai_policy"`
	ErrorCode             string              `json:"error_code,omitempty"`
}

type panelServerAIPolicy struct {
	Version                int      `json:"version"`
	Authority              string   `json:"authority"`
	MachineEntryRule       string   `json:"machine_entry_rule"`
	HumanPanelMeaning      string   `json:"human_panel_meaning"`
	OutOfBandAuthorization string   `json:"out_of_band_authorization"`
	OutOfBandRequirements  []string `json:"out_of_band_requirements"`
}

var serverAIPolicy = panelServerAIPolicy{
	Version:                1,
	Authority:              "embedded_manifest_for_supported_workflows",
	MachineEntryRule:       "Only listed machine_entries are supported server-level AI interfaces; this does not remove the administrator's root rights.",
	HumanPanelMeaning:      "No supported server-level AI machine entry; the panel may still provide the capability through its UI.",
	OutOfBandAuthorization: "An explicit user request may authorize an operation outside the supported machine entries, but it must not be described as a WP Panel-supported AI capability.",
	OutOfBandRequirements: []string{
		"confirm exact target and scope",
		"backup relevant state",
		"validate the result",
		"prepare and report rollback",
		"warn about panel overwrite and upgrade impact",
	},
}

// BuildPanelCapabilitiesJSON returns the current binary's embedded capability
// manifest. It intentionally does not open the panel database.
func BuildPanelCapabilitiesJSON(panelVersion string) ([]byte, error) {
	return buildPanelCapabilitiesJSON(panelVersion, "")
}

// PanelCapabilitySyncStatus reports whether the on-disk projection matches
// the currently running binary. The status is derived and never persisted.
func PanelCapabilitySyncStatus(panelVersion string) string {
	return detectPanelCapabilitySync(panelVersion)
}

func buildPanelCapabilitiesJSON(panelVersion, syncStatus string) ([]byte, error) {
	if err := validateEmbeddedPanelManifest(); err != nil {
		return nil, err
	}
	var payload panelManifestPayload
	if err := json.Unmarshal([]byte(aiPanelManifestPayloadJSON), &payload); err != nil {
		return nil, fmt.Errorf("decode embedded panel manifest: %w", err)
	}
	if strings.TrimSpace(panelVersion) == "" {
		panelVersion = "unknown"
	}
	output := panelManifestOutput{
		OK:                    true,
		PanelVersion:          panelVersion,
		CatalogSchemaVersion:  payload.CatalogSchemaVersion,
		ManifestSchemaVersion: payload.ManifestSchemaVersion,
		ManifestVersion:       payload.ManifestVersion,
		GeneratedAt:           time.Now().UTC().Format(time.RFC3339),
		ManifestSHA256:        aiPanelManifestPayloadSHA256,
		Capabilities:          payload.Capabilities,
		RetiredCapabilities:   payload.RetiredCapabilities,
		CapabilitySync:        syncStatus,
		ServerAIPolicy:        serverAIPolicy,
	}
	if output.CapabilitySync == "" {
		output.CapabilitySync = detectPanelCapabilitySync(panelVersion)
	}
	return json.MarshalIndent(output, "", "  ")
}

// RefreshPanelCapabilityDocuments atomically refreshes the root-readable
// server handoff and capability projections. It is intentionally independent
// of the panel database so a database problem cannot erase the AI context.
func RefreshPanelCapabilityDocuments(panelVersion string) error {
	if err := validateEmbeddedPanelManifest(); err != nil {
		return err
	}
	if err := os.MkdirAll(panelCapabilitiesPathDir(), 0755); err != nil {
		return fmt.Errorf("create panel capability directory: %w", err)
	}
	if err := os.Chmod(panelCapabilitiesPathDir(), 0755); err != nil {
		return fmt.Errorf("set panel capability directory mode: %w", err)
	}
	lockPath := filepath.Join(panelCapabilitiesPathDir(), ".refresh.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open panel capability refresh lock: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock panel capability refresh: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	jsonData, err := buildPanelCapabilitiesJSON(panelVersion, "ok")
	if err != nil {
		return err
	}
	var manifest panelManifestOutput
	if err := json.Unmarshal(jsonData, &manifest); err != nil {
		return fmt.Errorf("decode generated panel capabilities: %w", err)
	}
	markdown := renderPanelCapabilitiesMarkdown(manifest)
	handoff := renderPanelAIHandoff(manifest)
	files := map[string][]byte{
		panelCapabilitiesJSONPath():                                  jsonData,
		filepath.Join(panelCapabilitiesPathDir(), "CAPABILITIES.md"): []byte(markdown),
		filepath.Join(panelCapabilitiesPathDir(), "AI-HANDOFF.md"):   []byte(handoff),
	}
	for path, data := range files {
		if err := writePanelCapabilityFile(path, data); err != nil {
			return err
		}
	}
	return nil
}

func panelCapabilitiesPathDir() string { return panelCapabilitiesDirectory }

func panelCapabilitiesJSONPath() string {
	return filepath.Join(panelCapabilitiesPathDir(), "capabilities.json")
}

func writePanelCapabilityFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".wp-panel-capabilities-*")
	if err != nil {
		return fmt.Errorf("create capability temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write capability temp file: %w", err)
	}
	if err := tmp.Chmod(0444); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod capability temp file: %w", err)
	}
	if os.Geteuid() == 0 {
		if err := tmp.Chown(0, 0); err != nil {
			tmp.Close()
			return fmt.Errorf("chown capability temp file: %w", err)
		}
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close capability temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("publish capability file: %w", err)
	}
	return nil
}

func renderPanelCapabilitiesMarkdown(manifest panelManifestOutput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# WP Panel Capabilities\n\nPanel version: %s\nManifest schema version: %d\nManifest version: %d\nManifest SHA256: %s\nCapability sync: %s\n\n", manifest.PanelVersion, manifest.ManifestSchemaVersion, manifest.ManifestVersion, manifest.ManifestSHA256, manifest.CapabilitySync)
	readOnlyEntries, aiWriteEntries := panelMachineEntryCounts(manifest.Capabilities)
	fmt.Fprintf(&b, "Capability entries: %d (interaction counts are capability counts; machine entries: read_only=%d, ai_write=%d). One capability may contain multiple machine entries.\n\n", len(manifest.Capabilities), readOnlyEntries, aiWriteEntries)
	b.WriteString("Machine entries describe supported server-level AI interfaces; they do not remove the administrator's root rights. A human_panel entry means the panel may still provide the capability through its UI, but no supported server-level AI machine entry exists. An explicitly authorized out-of-band change must confirm scope, back up state, validate the result, prepare rollback, and warn about panel overwrite or upgrade impact.\n\n")
	b.WriteString("This is a capability directory, not a live status report. Read `wpp capabilities --json` before relying on this cached document. The current binary manifest is authoritative; this file is only a cached projection. Do not use another panel's menu names or manually edit WP Panel-managed files.\n\n")
	for _, raw := range manifest.Capabilities {
		var item struct {
			ID        string `json:"id"`
			Audiences struct {
				Server struct {
					Title       string `json:"title"`
					Entry       string `json:"entry"`
					Capability  string `json:"capability"`
					Boundary    string `json:"boundary"`
					Interaction string `json:"interaction"`
				} `json:"server"`
			} `json:"audiences"`
		}
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		fmt.Fprintf(&b, "## %s — %s\n\n- Entry: %s\n- Interaction: %s\n- Capability: %s\n- Boundary: %s\n\n", item.ID, item.Audiences.Server.Title, item.Audiences.Server.Entry, item.Audiences.Server.Interaction, item.Audiences.Server.Capability, item.Audiences.Server.Boundary)
	}
	b.WriteString("If no matching capability is listed, report that WP Panel has no confirmed direct entry. Do not invent a command or replace a panel workflow with raw SQL, systemctl, Nginx edits, or SQLite changes.\n")
	return b.String()
}

func renderPanelAIHandoff(manifest panelManifestOutput) string {
	readOnlyEntries, aiWriteEntries := panelMachineEntryCounts(manifest.Capabilities)
	return fmt.Sprintf(`# WP Panel Server AI Handoff

Panel version: %s
Manifest schema version: %d
Manifest version: %d
Manifest SHA256: %s
Capability sync: %s

This is a WP Panel-managed WordPress server. Do not assume aaPanel/BT Panel, 1Panel, cPanel, Plesk, or a generic hand-built LEMP stack.

Capability entries: %d. Machine entries: read_only=%d, ai_write=%d. Interaction counts describe capability entries, not commands; one capability may contain multiple machine entries.

Machine entries describe supported server-level AI interfaces; they do not remove the administrator's root rights. A human_panel entry means the panel may still provide the capability through its UI, but no supported server-level AI machine entry exists. An explicitly authorized out-of-band change must confirm scope, back up state, validate the result, prepare rollback, and warn about panel overwrite or upgrade impact.

First connection safety protocol:
1. Do not start wp-panel just to inspect it. Run 'wpp capabilities --json' first, then 'wpp status --json'.
2. If 'wpp' is unavailable, identify the already-running panel binary and run only 'wp-panel --capabilities --json'.
3. Never run 'wp-panel --json', 'wp-panel -json', or an unqualified panel binary as a discovery command; a bare binary may start a second service instance.
4. Treat the embedded manifest returned by the current binary as the authority. '--help' may list internal flags and is not an AI capability list.
5. A 'human_panel' capability has no supported server-AI machine entry; the panel may still provide the capability through its UI. By default explain its panel entry to the administrator. An explicit user request may authorize an out-of-band operation, but state that it is outside the supported machine interface and may be overwritten or affect upgrades.
6. Read each machine entry's own boundary and gates. Do not generalize one write command's lock or gate rules to another command; an available interface is not authorization or current site health.
7. For every write operation, run the documented dry-run first, show the plan to the user, wait for explicit confirmation, then execute the returned plan digest. The CLI enforces the two-stage protocol but cannot itself prove that a human approved it.
8. Use JSON output, check 'ok' and 'error_code', and retain 'operation_id' for write results. Do not print or store passwords, private keys, full managed paths, or raw command errors. Internal administrative flags such as password reset are not server-AI capabilities unless separately listed in the manifest and explicitly authorized.
9. If 'panel_version' is 'dev', report that the binary is a development build. If 'capability_sync' is 'stale' or 'failed', trust the current binary manifest, not the cached files, and report the degraded state.

Manifest status is a declaration, not proof that every command or resource was just executed successfully. Distinguish declared implementation, verification level, current server status, and an operation actually performed during this session. Read-only commands may fail clearly when their database, site, or service dependency is unavailable.

Root ownership remains the administrator's authority. The manifest describes supported and safer WP Panel workflows; it does not remove the owner's operating-system rights. For an explicitly authorized out-of-band change, confirm the exact target and scope, back up relevant state, validate the result, prepare a rollback, and report that the change may be overwritten by WP Panel or affect future upgrades. Keep panel-generated files, panel SQLite state, site identity, system users, and global service configuration in the high-risk category; prefer the panel workflow whenever one exists.

Read /etc/wp-panel/CAPABILITIES.md and run wpp capabilities --json before recommending or performing a panel-related action. The embedded manifest returned by the current wpp binary is authoritative. The files in /etc/wp-panel are cached projections and must be treated as stale when their version or fingerprint does not match.

Query current state with the panel's documented read-only commands. Capability support does not prove that a site, service, certificate, backup, or setting currently exists or is enabled.

Do not directly edit the panel SQLite database, generated Nginx files, PHP-FPM pools, managed certificate paths, or panel configuration to imitate a WP Panel workflow. If no matching capability is documented, report that WP Panel has no confirmed direct entry and guide the administrator to the appropriate panel page.
`, manifest.PanelVersion, manifest.ManifestSchemaVersion, manifest.ManifestVersion, manifest.ManifestSHA256, manifest.CapabilitySync, len(manifest.Capabilities), readOnlyEntries, aiWriteEntries)
}

func panelMachineEntryCounts(capabilities []json.RawMessage) (readOnly, aiWrite int) {
	for _, raw := range capabilities {
		var item struct {
			MachineEntries []struct {
				Mode string `json:"mode"`
			} `json:"machine_entries"`
		}
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		for _, entry := range item.MachineEntries {
			switch entry.Mode {
			case "read_only":
				readOnly++
			case "ai_write":
				aiWrite++
			}
		}
	}
	return readOnly, aiWrite
}

func validateEmbeddedPanelManifest() error {
	actual := sha256.Sum256([]byte(aiPanelManifestPayloadJSON))
	if hex.EncodeToString(actual[:]) != aiPanelManifestPayloadSHA256 {
		return errors.New("embedded panel manifest integrity check failed")
	}
	var payload panelManifestPayload
	if err := json.Unmarshal([]byte(aiPanelManifestPayloadJSON), &payload); err != nil {
		return fmt.Errorf("embedded panel manifest is invalid: %w", err)
	}
	if payload.CatalogSchemaVersion != 2 || payload.ManifestSchemaVersion != 1 || payload.ManifestVersion <= 0 {
		return errors.New("embedded panel manifest has unsupported schema")
	}
	return nil
}

func manifestVersionFromEmbeddedPayload() int {
	var payload panelManifestPayload
	if err := json.Unmarshal([]byte(aiPanelManifestPayloadJSON), &payload); err != nil {
		return 0
	}
	return payload.ManifestVersion
}

func detectPanelCapabilitySync(panelVersion string) string {
	data, err := os.ReadFile(panelCapabilitiesJSONPath())
	if err != nil {
		return "stale"
	}
	var cached struct {
		PanelVersion   string `json:"panel_version"`
		ManifestSHA256 string `json:"manifest_sha256"`
	}
	if err := json.Unmarshal(data, &cached); err != nil {
		return "failed"
	}
	if cached.PanelVersion != panelVersion || cached.ManifestSHA256 != aiPanelManifestPayloadSHA256 {
		return "stale"
	}
	return "ok"
}

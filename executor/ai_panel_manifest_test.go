package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func useTemporaryPanelCapabilityDirectory(t *testing.T) string {
	t.Helper()
	previous := panelCapabilitiesDirectory
	panelCapabilitiesDirectory = t.TempDir()
	t.Cleanup(func() { panelCapabilitiesDirectory = previous })
	return panelCapabilitiesDirectory
}

func TestBuildPanelCapabilitiesJSONUsesEmbeddedManifest(t *testing.T) {
	data, err := BuildPanelCapabilitiesJSON("dev")
	if err != nil {
		t.Fatalf("BuildPanelCapabilitiesJSON() error = %v", err)
	}
	var got struct {
		OK             bool             `json:"ok"`
		PanelVersion   string           `json:"panel_version"`
		ManifestSHA256 string           `json:"manifest_sha256"`
		CapabilitySync string           `json:"capability_sync"`
		Capabilities   []map[string]any `json:"capabilities"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("manifest JSON invalid: %v", err)
	}
	if !got.OK || got.PanelVersion != "dev" || got.ManifestSHA256 == "" || got.CapabilitySync != "stale" {
		t.Fatalf("unexpected manifest metadata: %+v", got)
	}
	if len(got.Capabilities) == 0 {
		t.Fatal("embedded manifest has no capabilities")
	}
}

func TestValidateEmbeddedPanelManifest(t *testing.T) {
	if err := validateEmbeddedPanelManifest(); err != nil {
		t.Fatalf("validateEmbeddedPanelManifest() error = %v", err)
	}
}

func TestPanelCapabilitySyncReportsFailedForCorruptCache(t *testing.T) {
	directory := useTemporaryPanelCapabilityDirectory(t)
	if err := os.WriteFile(filepath.Join(directory, "capabilities.json"), []byte("{"), 0444); err != nil {
		t.Fatal(err)
	}
	if got := PanelCapabilitySyncStatus("dev"); got != "failed" {
		t.Fatalf("PanelCapabilitySyncStatus()=%q, want failed", got)
	}
}

func TestRefreshPanelCapabilityDocumentsUsesAtomicCacheAndReportsOK(t *testing.T) {
	directory := useTemporaryPanelCapabilityDirectory(t)
	if err := RefreshPanelCapabilityDocuments("dev"); err != nil {
		t.Fatalf("RefreshPanelCapabilityDocuments() error = %v", err)
	}
	for _, name := range []string{"capabilities.json", "CAPABILITIES.md", "AI-HANDOFF.md"} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("missing refreshed %s: %v", name, err)
		}
	}
	handoff, err := os.ReadFile(filepath.Join(directory, "AI-HANDOFF.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"First connection safety protocol:",
		"Never run 'wp-panel --json'",
		"Interaction counts describe capability entries, not commands",
		"cannot itself prove that a human approved it",
		"the panel may still provide the capability through its UI",
		"Do not generalize one write command's lock or gate rules",
		"Internal administrative flags such as password reset are not server-AI capabilities",
		"Manifest status is a declaration",
		"Root ownership remains the administrator's authority",
		"out-of-band change",
		"may be overwritten by WP Panel or affect future upgrades",
		"Machine entries describe supported server-level AI interfaces",
	} {
		if !strings.Contains(string(handoff), required) {
			t.Fatalf("AI-HANDOFF.md missing %q", required)
		}
	}
	if got := PanelCapabilitySyncStatus("dev"); got != "ok" {
		t.Fatalf("PanelCapabilitySyncStatus()=%q, want ok", got)
	}
}

func TestBuildPanelCapabilitiesJSONIncludesServerAIPolicy(t *testing.T) {
	data, err := BuildPanelCapabilitiesJSON("dev")
	if err != nil {
		t.Fatalf("BuildPanelCapabilitiesJSON() error = %v", err)
	}
	var got struct {
		Policy struct {
			Version           int    `json:"version"`
			MachineEntryRule  string `json:"machine_entry_rule"`
			HumanPanelMeaning string `json:"human_panel_meaning"`
		} `json:"server_ai_policy"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("manifest JSON invalid: %v", err)
	}
	if got.Policy.Version != 1 || got.Policy.MachineEntryRule == "" || got.Policy.HumanPanelMeaning == "" {
		t.Fatalf("missing server AI policy: %+v", got.Policy)
	}
}

func TestRefreshPanelCapabilityDocumentsReportsFilesystemFailure(t *testing.T) {
	root := t.TempDir()
	invalidDirectory := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(invalidDirectory, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := panelCapabilitiesDirectory
	panelCapabilitiesDirectory = invalidDirectory
	t.Cleanup(func() { panelCapabilitiesDirectory = previous })
	if err := RefreshPanelCapabilityDocuments("dev"); err == nil {
		t.Fatal("RefreshPanelCapabilityDocuments() succeeded for an invalid directory")
	}
}

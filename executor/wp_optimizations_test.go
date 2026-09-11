package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyWPOptimizationsEnablesDebugAndKeepsDisplayOffByDefault(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "wp-config.php")
	config := "<?php\ndefine('WP_DEBUG', false);\n/* That's all, stop editing! Happy publishing. */\n"
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	if err := ApplyWPOptimizations(dir, WPOptimizations{WPDebug: true, WPPostRevisions: -1}); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(updated)
	for _, want := range []string{
		"define('WP_DEBUG', true);",
		"define('WP_DEBUG_LOG', true);",
		"define('WP_DEBUG_DISPLAY', false);",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in wp-config.php:\n%s", want, got)
		}
	}
}

func TestApplyWPOptimizationsCanDisplayDebugErrors(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "wp-config.php")
	config := "<?php\ndefine('WP_DEBUG', false);\ndefine('WP_DEBUG_DISPLAY', false);\n"
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	if err := ApplyWPOptimizations(dir, WPOptimizations{WPDebug: true, WPDebugDisplay: true, WPPostRevisions: -1}); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(updated)
	if !strings.Contains(got, "define('WP_DEBUG_DISPLAY', true);") {
		t.Fatalf("WP_DEBUG_DISPLAY was not enabled:\n%s", got)
	}
	if !WPDebugDisplayEnabled(dir) {
		t.Fatal("WPDebugDisplayEnabled did not detect the enabled constant")
	}
}

func TestApplyWPOptimizationsHandlesDoubleQuotedDebugDisplay(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "wp-config.php")
	config := "<?php\ndefine('WP_DEBUG', true);\ndefine(\"WP_DEBUG_DISPLAY\", true);\n"
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	if err := ApplyWPOptimizations(dir, WPOptimizations{WPDebug: true, WPPostRevisions: -1}); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(updated)
	if strings.Count(got, "WP_DEBUG_DISPLAY") != 1 || !strings.Contains(got, "define('WP_DEBUG_DISPLAY', false);") {
		t.Fatalf("double-quoted constant was not replaced safely:\n%s", got)
	}
	if WPDebugDisplayEnabled(dir) {
		t.Fatal("WPDebugDisplayEnabled reported the disabled constant as enabled")
	}
}

func TestApplyWPOptimizationsDisablesAllDebugConstants(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "wp-config.php")
	config := "<?php\ndefine('WP_DEBUG', true);\ndefine('WP_DEBUG_LOG', true);\ndefine('WP_DEBUG_DISPLAY', true);\n"
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	if err := ApplyWPOptimizations(dir, WPOptimizations{WPPostRevisions: -1}); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(updated)
	for _, name := range []string{"WP_DEBUG", "WP_DEBUG_LOG", "WP_DEBUG_DISPLAY"} {
		if strings.Contains(got, name) {
			t.Fatalf("%s was not removed:\n%s", name, got)
		}
	}
}

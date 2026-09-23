package executor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyWPOptimizationsReversibleDoesNotOverwriteLaterChange(t *testing.T) {
	dir := newSecureSiteTestRoot(t)
	configPath := filepath.Join(dir, "wp-config.php")
	before := "<?php\ndefine('WP_DEBUG', false);\n"
	if err := os.WriteFile(configPath, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}

	rollback, err := ApplyWPOptimizationsReversible(dir, WPOptimizations{WPDebug: true, WPPostRevisions: -1})
	if err != nil {
		t.Fatal(err)
	}
	external := "<?php\ndefine('WP_DEBUG', true);\n// changed externally\n"
	if err := os.WriteFile(configPath, []byte(external), 0600); err != nil {
		t.Fatal(err)
	}

	if err := rollback(); !errors.Is(err, errWPConfigChanged) {
		t.Fatalf("rollback error = %v, want errWPConfigChanged", err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != external {
		t.Fatalf("rollback overwrote later change:\n%s", got)
	}
}

func TestUpdateWPConfigSkipsUnchangedFile(t *testing.T) {
	dir := newSecureSiteTestRoot(t)
	configPath := filepath.Join(dir, "wp-config.php")
	config := "<?php\ndefine('WP_DEBUG', false);\n"
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	opts := WPOptimizations{WPDebug: true, WPPostRevisions: -1}
	if err := ApplyWPOptimizations(dir, opts); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(configPath, 0400); err != nil {
		t.Fatal(err)
	}

	if err := ApplyWPOptimizations(dir, opts); err != nil {
		t.Fatalf("unchanged read-only config should not be rewritten: %v", err)
	}
}

func TestApplyWPOptimizationsEnablesDebugAndKeepsDisplayOffByDefault(t *testing.T) {
	dir := newSecureSiteTestRoot(t)
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
	dir := newSecureSiteTestRoot(t)
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

func TestWPDebugDisplayEnabledAcceptsUppercaseBoolean(t *testing.T) {
	dir := newSecureSiteTestRoot(t)
	if err := os.WriteFile(filepath.Join(dir, "wp-config.php"), []byte("<?php\ndefine('WP_DEBUG_DISPLAY', TRUE);\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !WPDebugDisplayEnabled(dir) {
		t.Fatal("uppercase TRUE was not detected")
	}
}

func TestApplyWPOptimizationsHandlesDoubleQuotedDebugDisplay(t *testing.T) {
	dir := newSecureSiteTestRoot(t)
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

func TestApplyWPOptimizationsDeduplicatesAndMovesDebugConstantsBeforeMarker(t *testing.T) {
	dir := newSecureSiteTestRoot(t)
	configPath := filepath.Join(dir, "wp-config.php")
	config := "<?php\ndefine( 'WP_DEBUG', false );\n/* That's all, stop editing! Happy publishing. */\nrequire_once ABSPATH . 'wp-settings.php';\ndefine('WP_DEBUG', true);\ndefine('WP_DEBUG_LOG', true);\n"
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
	for _, name := range []string{"WP_DEBUG", "WP_DEBUG_LOG", "WP_DEBUG_DISPLAY"} {
		if count := strings.Count(got, "define('"+name+"'"); count != 1 {
			t.Fatalf("%s definition count=%d, want 1:\n%s", name, count, got)
		}
	}
	if strings.Index(got, "define('WP_DEBUG'") > strings.Index(got, "That's all, stop editing") {
		t.Fatalf("WP_DEBUG was not moved before the stop marker:\n%s", got)
	}
}

func TestApplyWPOptimizationsDisablesAllDebugConstants(t *testing.T) {
	dir := newSecureSiteTestRoot(t)
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

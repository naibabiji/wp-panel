//go:build linux

package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWPAnomalyRunnerQueryUsesExistingIsolation(t *testing.T) {
	runner, fixture := newTestInventoryRunner(t, []byte("<?php // fixture"))
	openBase := sitePHPRunnerOpenBaseDir(fixture.site.WebRoot, fixture.site.Domain, filepath.Join(runner.runnerRoot, runner.hash))
	body := validSuccessEnvelopeBody(fixture.uid, fixture.gid, openBase)
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' \"$WP_PANEL_ANOMALY_QUERY\" > %q\nprintf 'WP_PANEL_INVENTORY_BEGIN %%s\\n%%s\\nWP_PANEL_INVENTORY_END %%s\\n' \"$WP_PANEL_RUNNER_TOKEN\" %q \"$WP_PANEL_RUNNER_TOKEN\" >&3\n", fixture.auditPath, body)
	if err := os.Chmod(runner.runuserPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runner.runuserPath, []byte(script), 0555); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WP_PANEL_ANOMALY_QUERY", "untrusted-parent-query")
	if _, err := runner.Collect(context.Background(), fixture.cfg, fixture.site, false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(fixture.auditPath)
	if len(raw) != 0 {
		t.Fatal("ordinary scan inherited anomaly query")
	}
	runner.anomalyQuery = &wpAnomalyQuery{Since: 100, Until: 200, KnownIDs: []int{1}}
	if _, err := runner.Collect(context.Background(), fixture.cfg, fixture.site, false); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(fixture.auditPath)
	var got wpAnomalyQuery
	if err := json.Unmarshal(raw, &got); err != nil || got.Since != 100 || got.Until != 200 || len(got.KnownIDs) != 1 {
		t.Fatal(string(raw), err)
	}
}

func TestWPAnomalyRunnerRequiresActiveCompatiblePlugin(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php not installed")
	}
	source := string(wpInventoryRunnerSource)
	start := strings.Index(source, "function wp_panel_inventory_anomaly_sample(array $query): array")
	end := strings.Index(source[start:], "\n$token =")
	if start < 0 || end < 0 {
		t.Fatal("missing anomaly entry")
	}
	fn := source[start : start+end]
	for _, test := range []struct{ name, definition, active, multi, want string }{
		{"absent", "", "false", "false", "plugin_required"},
		{"old", "class WP_Panel_Optimizer {}", "true", "false", "plugin_required"},
		{"disabled", "class WP_Panel_Optimizer {static function collect_anomaly_sample($s,$u,$i){return ['error'=>'called'];}}", "false", "false", "plugin_required"},
		{"enabled", "class WP_Panel_Optimizer {static function collect_anomaly_sample($s,$u,$i){return ['error'=>'called'];}}", "true", "false", "called"},
		{"multisite", "", "false", "true", "multisite_unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := fmt.Sprintf(`%s function is_multisite(){return %s;} function get_option($key,$default){return %s ? ['wp-panel-optimizer/wp-panel-optimizer.php'] : [];} echo wp_panel_inventory_anomaly_sample(['since'=>1,'until'=>2,'known_ids'=>[]])['error'];`, test.definition, test.multi, test.active)
			output, err := exec.Command(php, "-r", fn+harness).CombinedOutput()
			if err != nil || string(output) != test.want {
				t.Fatal(string(output), err)
			}
		})
	}
}

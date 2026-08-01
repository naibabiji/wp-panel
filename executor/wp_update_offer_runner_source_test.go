package executor

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestWPUpdateOfferRunnerRefreshesVendorPackageInAdministratorContext(t *testing.T) {
	source := wpUpdateOfferPHPSource
	admin := []byte("wp_set_current_user((int)$administrators[0])")
	refresh := []byte("delete_site_transient($update_transient)")
	lookup := []byte("wp_update_plugins()")
	adminAt := bytes.Index(source, admin)
	refreshAt := bytes.Index(source, refresh)
	lookupAt := bytes.Index(source, lookup)
	if adminAt < 0 || refreshAt < 0 || lookupAt < 0 || adminAt > refreshAt || refreshAt > lookupAt {
		t.Fatalf("offer runner must establish administrator context and clear the update transient before resolving an offer")
	}
}

func TestWPUpdateOfferRunnerMapsThemeToThemeTransient(t *testing.T) {
	want := []byte("$update_transient=$component_type==='plugin'?'update_plugins':'update_themes';")
	if !bytes.Contains(wpUpdateOfferPHPSource, want) {
		t.Fatalf("offer runner must map theme offers to the update_themes transient")
	}
}

func TestWPUpdateOfferRunnerTransientMappingExecutesInPHP(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php is not available")
	}
	line := "$update_transient=$component_type==='plugin'?'update_plugins':'update_themes';"
	if !bytes.Contains(wpUpdateOfferPHPSource, []byte(line)) {
		t.Fatal("transient mapping source missing")
	}
	program := "foreach(['plugin','theme'] as $component_type){" + line + "echo $component_type.'='.$update_transient.\"\\n\";}"
	output, err := exec.Command(php, "-r", program).CombinedOutput()
	if err != nil {
		t.Fatalf("php mapping: %v: %s", err, output)
	}
	if strings.TrimSpace(string(output)) != "plugin=update_plugins\ntheme=update_themes" {
		t.Fatalf("mapping output=%q", output)
	}
}

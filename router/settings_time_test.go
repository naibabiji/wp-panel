package router

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

func TestSettingsTimesUseServerTimezone(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not available")
	}
	page := renderPage(t, "settings.html", "settings_content")
	for _, binding := range []string{"formatSettingsTime(autoUpdate.lastCheck)", "formatSettingsTime(autoUpdate.lastSuccess)", "formatSettingsTime(autoLastCheck)", "formatSettingsTime(autoCheck.lastCheckAt)", "formatSettingsTime(pkg.updated_at, true)", "formatSettingsTime(b.created_at, true)", "formatSettingsTime(log.created_at)"} {
		if !bytes.Contains(page, []byte(binding)) {
			t.Fatalf("timestamp bypasses common formatter: %s", binding)
		}
	}
	var script []byte
	for _, match := range regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindAllSubmatch(page, -1) {
		if bytes.Contains(match[1], []byte("function panelSettings()")) {
			script = match[1]
			break
		}
	}
	if len(script) == 0 {
		t.Fatal("settings script missing")
	}
	harness := []byte(`
function t(key) { return key; }
const page = panelSettings();
page.timezone = 'Asia/Shanghai';
const cases = [
 ['2026-11-22T10:45:18+08:00', false, '2026-11-22 10:45:18'],
 ['2026-11-21 18:29:22', true, '2026-11-21 18:29:22'],
 ['2026-11-22T09:11:29+08:00', false, '2026-11-22 09:11:29'],
 ['2026-11-21T10:22:45Z', false, '2026-11-21 18:22:45'],
 ['2026-11-21 10:22:45', false, '2026-11-21 18:22:45'],
 ['2026-11-21T16:00:00Z', false, '2026-11-22 00:00:00'],
 ['2026-11-21T23:30:00-05:00', false, '2026-11-22 12:30:00'],
 ['', false, ''], [null, false, ''], ['invalid', false, '']
];
for (const [value,local,expected] of cases) {
 const actual=page.formatSettingsTime(value,local);
 if (actual!==expected) throw new Error(JSON.stringify({value,actual,expected}));
}
page.timezone='UTC';
if (page.formatSettingsTime('2026-11-22T09:11:29+08:00')!=='2026-11-22 01:11:29') throw new Error('timezone changes ignored');
page.timezone='invalid';
if (page.formatSettingsTime('2026-11-22T09:11:29Z')!=='') throw new Error('invalid zone fell back to browser time');
page.timezone='Asia/Shanghai';
page.autoUpdate.enabled=true;
page.autoUpdate.lastCheck='2026-11-21T10:22:45Z';
if (page.nextAutoUpdateCheck()!=='2026-11-22 18:22:45') throw new Error('next check uses different zone/format');
page.serverTimeBase=Date.parse('2026-11-21T16:00:00Z');
page.serverTimeFetchedAt=Date.now();page.tickTime();
if (!page.serverTime.startsWith('2026-11-22 00:00:')) throw new Error('live clock format mismatch');
`)
	path := filepath.Join(t.TempDir(), "settings-time.js")
	if err := os.WriteFile(path, append(script, harness...), 0600); err != nil {
		t.Fatal(err)
	}
	// Browser timezone intentionally differs from the server timezone.
	cmd := exec.Command(node, path)
	cmd.Env = append(os.Environ(), "TZ=America/Los_Angeles")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("settings time behavior: %v\n%s", err, out)
	}
}

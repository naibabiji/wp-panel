package tests

import (
	"os"
	"strings"
	"testing"
)

func TestRemoteBackupSetupCommandRequiresSavedEffectivePath(t *testing.T) {
	template := readRemoteBackupTemplate(t)
	for _, required := range []string{
		`:disabled="!canCopyCmd()"`,
		`this.effectiveRemotePath() === this.cfg.remote_path`,
		`showToast(t('settings.save_before_copy_command'), 'error')`,
	} {
		if !strings.Contains(template, required) {
			t.Fatalf("remote backup setup command guard %q is missing", required)
		}
	}
}

func TestRemoteBackupIsolationToggleIsOnlyInRsyncSection(t *testing.T) {
	template := readRemoteBackupTemplate(t)
	rsyncStart := strings.Index(template, `x-show="cfg.backup_type === 'rsync'"`)
	s3Start := strings.Index(template, `x-show="cfg.backup_type === 's3'"`)
	if rsyncStart < 0 || s3Start <= rsyncStart {
		t.Fatal("remote backup rsync/S3 sections not found")
	}
	rsyncSection := template[rsyncStart:s3Start]
	s3End := strings.Index(template[s3Start:], `x-model="cfg.keep_local"`)
	if s3End < 0 {
		t.Fatal("remote backup S3 section end not found")
	}
	s3Section := template[s3Start : s3Start+s3End]
	if !strings.Contains(rsyncSection, `x-model="cfg.isolate_path"`) {
		t.Fatal("rsync advanced isolation toggle is missing")
	}
	if strings.Contains(s3Section, `x-model="cfg.isolate_path"`) {
		t.Fatal("S3 section must not expose the forced isolation toggle")
	}
}

func readRemoteBackupTemplate(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile("../templates/settings.html")
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

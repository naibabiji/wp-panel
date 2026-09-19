package handlers

import (
	"os"
	"path/filepath"
	"testing"
)

func withFakeSystemctlUnits(t *testing.T, units string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"list-unit-files\" ]; then\n" +
		"  for u in $FAKE_SYSTEMCTL_UNITS; do\n" +
		"    if [ \"$u\" = \"$2\" ]; then\n" +
		"      echo \"$2 enabled enabled\"\n" +
		"      exit 0\n" +
		"    fi\n" +
		"  done\n" +
		"  echo \"0 unit files listed.\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "systemctl"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_SYSTEMCTL_UNITS", units)
}

func TestDetectNTPTimeSyncUnitPrefersChrony(t *testing.T) {
	withFakeSystemctlUnits(t, "chrony.service systemd-timesyncd.service")
	if unit := detectNTPTimeSyncUnit(); unit != "chrony.service" {
		t.Fatalf("unit=%q, want chrony.service", unit)
	}
}

func TestDetectNTPTimeSyncUnitFallsBackToTimesyncd(t *testing.T) {
	withFakeSystemctlUnits(t, "systemd-timesyncd.service")
	if unit := detectNTPTimeSyncUnit(); unit != "systemd-timesyncd.service" {
		t.Fatalf("unit=%q, want systemd-timesyncd.service", unit)
	}
}

func TestDetectNTPTimeSyncUnitReturnsEmptyWhenNeitherExists(t *testing.T) {
	withFakeSystemctlUnits(t, "")
	if unit := detectNTPTimeSyncUnit(); unit != "" {
		t.Fatalf("unit=%q, want empty", unit)
	}
}

func withFakeSystemctlMasked(t *testing.T, maskedUnit, availableUnit string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"list-unit-files\" ]; then\n" +
		"  if [ \"$2\" = \"" + maskedUnit + "\" ]; then\n" +
		"    echo \"" + maskedUnit + " masked -\"\n" +
		"    exit 0\n" +
		"  fi\n" +
		"  if [ \"$2\" = \"" + availableUnit + "\" ]; then\n" +
		"    echo \"" + availableUnit + " enabled enabled\"\n" +
		"    exit 0\n" +
		"  fi\n" +
		"  echo \"0 unit files listed.\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "systemctl"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
}

func TestDetectNTPTimeSyncUnitSkipsMaskedChrony(t *testing.T) {
	withFakeSystemctlMasked(t, "chrony.service", "systemd-timesyncd.service")
	if unit := detectNTPTimeSyncUnit(); unit != "systemd-timesyncd.service" {
		t.Fatalf("unit=%q, want systemd-timesyncd.service (chrony is masked)", unit)
	}
}

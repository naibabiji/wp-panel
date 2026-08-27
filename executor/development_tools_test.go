package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWPCLICommandVersionAllowsRootStatusCheck(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wp")
	script := "#!/bin/sh\nif [ \"$1\" != \"--allow-root\" ] || [ \"$2\" != \"--version\" ]; then exit 1; fi\necho 'WP-CLI 2.12.0'\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	version, ok := wpCLICommandVersion(context.Background(), path)
	if !ok || !strings.Contains(version, "WP-CLI 2.12.0") {
		t.Fatalf("version=%q ok=%v", version, ok)
	}
}

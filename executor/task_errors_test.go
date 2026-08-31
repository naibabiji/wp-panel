package executor

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
)

func TestLogRecoveryFailure(t *testing.T) {
	var output bytes.Buffer
	originalWriter := log.Writer()
	originalFlags := log.Flags()
	originalPrefix := log.Prefix()
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(originalWriter)
		log.SetFlags(originalFlags)
		log.SetPrefix(originalPrefix)
	})

	logRecoveryFailure("恢复旧证书", nil)
	if output.Len() != 0 {
		t.Fatalf("nil error produced log output: %q", output.String())
	}

	logRecoveryFailure("恢复旧证书", errors.New("permission denied"))
	got := output.String()
	if !strings.Contains(got, "恢复旧证书失败") || !strings.Contains(got, "permission denied") {
		t.Fatalf("log output = %q", got)
	}
}

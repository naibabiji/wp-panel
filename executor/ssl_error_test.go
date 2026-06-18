package executor

import (
	"errors"
	"strings"
	"testing"
)

func TestFriendlySSLErrorExplainsHTTP404Challenge(t *testing.T) {
	msg := FriendlySSLError(errors.New("获取证书失败: invalid authorization: Invalid response from http://example.com/.well-known/acme-challenge/token: 404"))
	if !strings.Contains(msg, "HTTP-01") || !strings.Contains(msg, "A/AAAA") || !strings.Contains(msg, "CDN") {
		t.Fatalf("message = %q", msg)
	}
}

func TestFriendlySSLErrorExplainsTimeout(t *testing.T) {
	msg := FriendlySSLError(errors.New("context deadline exceeded"))
	if !strings.Contains(msg, "超时") || !strings.Contains(msg, "80") {
		t.Fatalf("message = %q", msg)
	}
}

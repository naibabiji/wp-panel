package executor

import (
	"errors"
	"strings"
	"testing"
)

func TestFriendlySSLError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want []string
	}{
		{
			name: "nil",
			err:  nil,
			want: []string{""},
		},
		{
			name: "http 404 challenge",
			err:  errors.New("获取证书失败: invalid authorization: Invalid response from http://example.com/.well-known/acme-challenge/token: 404"),
			want: []string{"HTTP-01", "CDN", "ACME"},
		},
		{
			name: "dns nxdomain",
			err:  errors.New("NXDOMAIN looking up A for example.com"),
			want: []string{"解析", "A/AAAA"},
		},
		{
			name: "connection refused",
			err:  errors.New("connect: connection refused"),
			want: []string{"80", "防火墙"},
		},
		{
			name: "timeout",
			err:  errors.New("context deadline exceeded"),
			want: []string{"超时", "80"},
		},
		{
			name: "unauthorized",
			err:  errors.New("urn:ietf:params:acme:error:unauthorized"),
			want: []string{"验证未通过", "CDN"},
		},
		{
			name: "default",
			err:  errors.New("unexpected acme failure"),
			want: []string{"申请 Let's Encrypt 证书失败", "unexpected acme failure"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := FriendlySSLError(tt.err)
			for _, want := range tt.want {
				if !strings.Contains(msg, want) {
					t.Fatalf("message = %q, want substring %q", msg, want)
				}
			}
		})
	}
}

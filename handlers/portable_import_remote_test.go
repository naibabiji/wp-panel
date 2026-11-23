package handlers

import (
	"context"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/naibabiji/wp-panel/executor"
)

func TestPortableRestoreLinkPinsTLSConnectionAfterDNSChange(t *testing.T) {
	job := strings.Repeat("a", 32)
	token := strings.Repeat("b", 64)
	link, err := parsePortableRestoreLink("https://example.com/wp-admin/admin-post.php?action=wp_panel_backup_remote&job=" + job + "#" + token)
	if err != nil {
		t.Fatal(err)
	}
	original := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "example.com" || r.TLS.ServerName != "example.com" ||
			r.Header.Get("X-WP-Panel-Backup-Token") != token || strings.Contains(r.URL.String(), token) {
			t.Error("incorrect TLS identity or credential transport")
		}
		if r.URL.Query().Get("artifact") == "redirect.zip" {
			http.Redirect(w, r, "https://example.com/redirect-target", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, r.URL.Query().Get("artifact"))
	}))
	defer original.Close()
	oldIP, newIP := "93.184.215.14", "1.1.1.1"
	dns := oldIP
	dialCount := 0
	client := portableSourceClient(link.endpoint.Host, link.endpoint.Scheme, netip.MustParseAddr(oldIP), func(ctx context.Context, network, address string) (net.Conn, error) {
		dialCount++
		if address != net.JoinHostPort(oldIP, "443") {
			t.Errorf("dialed %s after DNS resolved to %s", address, dns)
		}
		return (&net.Dialer{}).DialContext(ctx, network, original.Listener.Addr().String())
	})
	roots := x509.NewCertPool()
	roots.AddCert(original.Certificate())
	client.Transport.(*http.Transport).TLSClientConfig.RootCAs = roots
	open := link.openFromClient(client)
	for i, name := range []string{"manifest.json", "part-00001.zip", "part-00002.zip"} {
		if i > 0 {
			dns = newIP
		}
		body, err := open(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(body)
		body.Close()
		if err != nil || string(data) != name {
			t.Fatalf("unexpected body: %q, %v", data, err)
		}
	}
	if _, err := open(context.Background(), "redirect.zip"); err == nil || dialCount != 4 {
		t.Fatalf("redirect must not be followed: %v (dials %d)", err, dialCount)
	}
}

func TestPortableHTTPRestoreLinkUsesPinnedPortAndProtocol(t *testing.T) {
	token := strings.Repeat("b", 64)
	path := "/wp-admin/admin-post.php?action=wp_panel_backup_remote&job=" + strings.Repeat("a", 32) + "#" + token
	link, err := parsePortableRestoreLink("http://example.com:80" + path)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"http://example.com:443", "https://example.com:80", "ftp://example.com"} {
		if _, err := parsePortableRestoreLink(invalid + path); err == nil {
			t.Errorf("accepted unsupported link %s", invalid)
		}
	}
	ip := netip.MustParseAddr("93.184.215.14")
	source := executor.PortableRemoteSource{Host: "example.com", IP: ip.String()}
	if link.matchesSource(source) {
		t.Fatal("HTTP link matched a legacy HTTPS source")
	}
	source.HTTP = true
	if !link.matchesSource(source) {
		t.Fatal("HTTP link did not match the explicit HTTP source")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "example.com" || r.TLS != nil || r.Header.Get("X-WP-Panel-Backup-Token") != token || strings.Contains(r.URL.String(), token) {
			t.Error("incorrect HTTP host or credential transport")
		}
		_, _ = io.WriteString(w, "part")
	}))
	defer server.Close()
	client := portableSourceClient(link.endpoint.Host, link.endpoint.Scheme, ip, func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "93.184.215.14:80" {
			t.Errorf("HTTP dialed %s instead of the pinned IP and port", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	})
	body, err := link.openFromClient(client)(context.Background(), "part-00001.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if got, err := io.ReadAll(body); err != nil || string(got) != "part" {
		t.Fatalf("unexpected HTTP part: %q, %v", got, err)
	}
}

func TestPortableSourceRejectsPrivateAndTransitionAddresses(t *testing.T) {
	for _, v := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "100.64.0.1", "::ffff:192.168.1.2", "fe80::1", "64:ff9b::a00:1", "2002:a00:1::1", "198.18.0.1"} {
		if isPublicPortableSource(netip.MustParseAddr(v)) {
			t.Errorf("accepted nonpublic source %s", v)
		}
	}
}

func TestPortableManualSourceAcceptsOnlyPublicIPLiterals(t *testing.T) {
	for input, want := range map[string]string{
		" 93.184.215.14 ":           "93.184.215.14",
		"2606:4700:3034::ac43:9ad1": "2606:4700:3034::ac43:9ad1",
		"::ffff:93.184.215.14":      "93.184.215.14",
	} {
		got, err := parsePortableSourceIP(input)
		if err != nil || got.String() != want {
			t.Errorf("parsePortableSourceIP(%q) = %s, %v; want %s", input, got, err, want)
		}
	}
	for _, input := range []string{"example.com", "93.184.215.14:443", "https://93.184.215.14", "127.0.0.1", "10.0.0.1", "169.254.169.254", "2606:4700::1%eth0"} {
		if _, err := parsePortableSourceIP(input); err == nil {
			t.Errorf("accepted %q as a manual source IP", input)
		}
	}
	if got, err := parsePortableSourceIP(" "); err != nil || got.IsValid() {
		t.Fatalf("empty source IP must keep automatic selection: %v, %v", got, err)
	}
}

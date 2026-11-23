package handlers

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/executor"
)

var portableLinkJob = regexp.MustCompile(`^[0-9a-f]{32}$`)
var portableLinkToken = regexp.MustCompile(`^[0-9a-f]{64}$`)

type portableRestoreLink struct {
	endpoint url.URL
	job      string
	token    string
}

type portableRemoteRequest struct {
	Link         string   `json:"link"`
	TaskID       string   `json:"task_id"`
	SourceIP     string   `json:"source_ip"`
	Aliases      []string `json:"aliases"`
	CleanupToken string   `json:"cleanup_token"`
}

func readPortableRemoteRequest(c *gin.Context) (portableRemoteRequest, portableRestoreLink, error) {
	var input portableRemoteRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	if err := c.ShouldBindJSON(&input); err != nil {
		return input, portableRestoreLink{}, errors.New("无法读取恢复链接设置")
	}
	link, err := parsePortableRestoreLink(input.Link)
	return input, link, err
}

func parsePortableRestoreLink(raw string) (portableRestoreLink, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Opaque != "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Host == "" ||
		(u.Port() != "" && u.Port() != portableLinkPort(u.Scheme)) || !executor.IsValidDomain(u.Hostname()) ||
		!strings.HasSuffix(u.Path, "/wp-admin/admin-post.php") || u.RawPath != "" ||
		!portableLinkToken.MatchString(u.Fragment) {
		return portableRestoreLink{}, errors.New("请选择插件生成的公网 HTTP 或 HTTPS 恢复链接")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != 2 || len(q["action"]) != 1 || len(q["job"]) != 1 ||
		q.Get("action") != "wp_panel_backup_remote" || !portableLinkJob.MatchString(q.Get("job")) {
		return portableRestoreLink{}, errors.New("恢复链接格式无效")
	}
	token := u.Fragment
	u.Fragment, u.RawFragment = "", ""
	u.Host = strings.ToLower(u.Hostname())
	u.RawQuery = q.Encode()
	return portableRestoreLink{endpoint: *u, job: q.Get("job"), token: token}, nil
}

func portableLinkPort(scheme string) string {
	if scheme == "http" {
		return "80"
	}
	return "443"
}

// Use only globally routed addresses and exclude IPv6 transition prefixes
// that can encode other destinations.
func isPublicPortableSource(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.Zone() != "" || isBlockedRemoteImportIP(net.IP(ip.AsSlice())) {
		return false
	}
	if ip.Is6() {
		return netip.MustParsePrefix("2000::/3").Contains(ip) &&
			!netip.MustParsePrefix("2001::/23").Contains(ip) &&
			!netip.MustParsePrefix("2001:db8::/32").Contains(ip) &&
			!netip.MustParsePrefix("2002::/16").Contains(ip) &&
			!netip.MustParsePrefix("3fff::/20").Contains(ip)
	}
	for _, prefix := range []string{"192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4"} {
		if netip.MustParsePrefix(prefix).Contains(ip) {
			return false
		}
	}
	return true
}

func parsePortableSourceIP(raw string) (netip.Addr, error) {
	if strings.TrimSpace(raw) == "" {
		return netip.Addr{}, nil
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil || !isPublicPortableSource(ip) {
		return netip.Addr{}, errors.New("请输入老服务器的公网 IPv4 或 IPv6 地址，无需填写端口")
	}
	return ip.Unmap(), nil
}

func resolvePortableSource(ctx context.Context, host string) ([]netip.Addr, error) {
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("无法解析源站地址")
	}
	for _, ip := range ips {
		if !isPublicPortableSource(ip) {
			return nil, errors.New("源站必须解析到公网地址")
		}
	}
	return ips, nil
}

func portableSourceClient(host, scheme string, ip netip.Addr, dial func(context.Context, string, string) (net.Conn, error)) *http.Client {
	port := portableLinkPort(scheme)
	transport := &http.Transport{
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 25 * time.Second,
		DisableCompression:    true,
		DisableKeepAlives:     true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != net.JoinHostPort(host, port) || !isPublicPortableSource(ip) {
				return nil, errors.New("源站地址无效")
			}
			return dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		},
	}
	return &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (link portableRestoreLink) opener(ip netip.Addr) executor.PortableRemoteOpen {
	dialer := net.Dialer{Timeout: 8 * time.Second}
	return link.openFromClient(portableSourceClient(link.endpoint.Host, link.endpoint.Scheme, ip, dialer.DialContext))
}

func (link portableRestoreLink) matchesSource(source executor.PortableRemoteSource) bool {
	return link.endpoint.Host == source.Host && (link.endpoint.Scheme == "http") == source.HTTP
}

func (link portableRestoreLink) openFromClient(client *http.Client) executor.PortableRemoteOpen {
	return func(ctx context.Context, artifact string) (io.ReadCloser, error) {
		u := link.endpoint
		q := u.Query()
		q.Set("artifact", artifact)
		u.RawQuery = q.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, errors.New("无法创建源站请求")
		}
		request.Header.Set("X-WP-Panel-Backup-Token", link.token)
		request.Header.Set("Accept-Encoding", "identity")
		resp, err := client.Do(request)
		if err != nil {
			if u.Scheme == "http" {
				return nil, errors.New("无法连接固定的源站地址，请检查网络和源站是否可通过 HTTP 访问")
			}
			return nil, errors.New("无法连接固定的源站地址，请核对网络和 HTTPS 证书，必要时重新选择来源")
		}
		if resp.StatusCode != http.StatusOK || (resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity") {
			resp.Body.Close()
			return nil, fmt.Errorf("源站未提供备份材料（HTTP %d），请检查链接是否已过期、撤销或材料不可用", resp.StatusCode)
		}
		return resp.Body, nil
	}
}

func fetchPortableRemoteManifest(ctx context.Context, link portableRestoreLink, ip netip.Addr) ([]byte, error) {
	// Give each public candidate a bounded chance to answer; no redirect or
	// second DNS lookup occurs once the candidate address has been selected.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body, err := link.opener(ip)(ctx, "manifest.json")
	if err != nil {
		return nil, err
	}
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, executor.PortableManifestLimit+1))
	if err != nil || len(raw) > executor.PortableManifestLimit {
		return nil, errors.New("无法读取源站备份清单，或清单超过长度限制")
	}
	var manifest struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil || manifest.ID != link.job {
		return nil, errors.New("源站返回的清单与恢复链接不一致")
	}
	return raw, nil
}

func (h PortableImportHandler) RemotePreview(c *gin.Context) {
	input, link, err := readPortableRemoteRequest(c)
	if err != nil {
		h.respond(c, nil, err)
		return
	}
	manualIP, err := parsePortableSourceIP(input.SourceIP)
	if err != nil {
		h.respond(c, nil, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 18*time.Second)
	defer cancel()
	var ips []netip.Addr
	if manualIP.IsValid() {
		ips = []netip.Addr{manualIP}
	}
	if input.TaskID != "" {
		source, job, err := h.Service.RemoteSource(input.TaskID)
		if err != nil {
			h.respond(c, nil, err)
			return
		}
		if job != link.job {
			h.respond(c, nil, errors.New("请提供本任务原先的同一份备份链接"))
			return
		}
		if source.IP != "" && !manualIP.IsValid() {
			ip, parseErr := netip.ParseAddr(source.IP)
			if !link.matchesSource(source) || parseErr != nil || !isPublicPortableSource(ip) {
				h.respond(c, nil, errors.New("链接与固定来源不符，请在确需更换来源时明确选择更换"))
				return
			}
			ips = []netip.Addr{ip}
		}
	}
	if len(ips) == 0 {
		ips, err = resolvePortableSource(ctx, link.endpoint.Host)
		if err != nil {
			h.respond(c, nil, err)
			return
		}
	}
	for _, ip := range ips {
		raw, fetchErr := fetchPortableRemoteManifest(ctx, link, ip)
		if fetchErr == nil {
			task, bindErr := h.Service.BindRemoteSource(input.TaskID, raw, executor.PortableRemoteSource{
				Host: link.endpoint.Host,
				IP:   ip.Unmap().String(),
				HTTP: link.endpoint.Scheme == "http",
			}, manualIP.IsValid())
			h.respond(c, task, bindErr)
			return
		}
		err = fetchErr
		if ctx.Err() != nil {
			break
		}
	}
	h.respond(c, nil, err)
}

func (h PortableImportHandler) RemoteSubmit(c *gin.Context) {
	input, link, err := readPortableRemoteRequest(c)
	if err != nil {
		h.respond(c, nil, err)
		return
	}
	id := c.Param("id")
	source, job, err := h.Service.RemoteSource(id)
	if err != nil {
		h.respond(c, nil, err)
		return
	}
	ip, parseErr := netip.ParseAddr(source.IP)
	if link.job != job || !link.matchesSource(source) || parseErr != nil || !isPublicPortableSource(ip) {
		h.respond(c, nil, errors.New("请先用这份备份的链接预览来源"))
		return
	}
	h.respond(c, nil, h.Service.SubmitRemote(id, input.Aliases, input.CleanupToken, source, link.opener(ip)))
}

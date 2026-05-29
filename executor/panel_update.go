package executor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	panelRepoOwner = "naibabiji"
	panelRepoName  = "wp-panel"
)

type GithubRelease struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	Body    string `json:"body"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func FetchLatestPanelRelease() (*GithubRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", panelRepoOwner, panelRepoName)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("网络请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API 返回 %d", resp.StatusCode)
	}

	var release GithubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("解析版本信息失败: %w", err)
	}
	return &release, nil
}

func CompareVersions(a, b string) int {
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")
	ap := strings.SplitN(a, "-", 2)
	bp := strings.SplitN(b, "-", 2)
	aNums := strings.Split(ap[0], ".")
	bNums := strings.Split(bp[0], ".")
	for i := 0; i < 3; i++ {
		av, bv := 0, 0
		if i < len(aNums) {
			fmt.Sscanf(aNums[i], "%d", &av)
		}
		if i < len(bNums) {
			fmt.Sscanf(bNums[i], "%d", &bv)
		}
		if av > bv {
			return 1
		}
		if av < bv {
			return -1
		}
	}
	aPre := ""
	bPre := ""
	if len(ap) > 1 {
		aPre = ap[1]
	}
	if len(bp) > 1 {
		bPre = bp[1]
	}
	if aPre == "" && bPre == "" {
		return 0
	}
	if aPre == "" {
		return 1
	}
	if bPre == "" {
		return -1
	}
	return comparePreRelease(aPre, bPre)
}

func comparePreRelease(a, b string) int {
	aParts := splitAlphaNum(a)
	bParts := splitAlphaNum(b)
	n := len(aParts)
	if len(bParts) < n {
		n = len(bParts)
	}
	for i := 0; i < n; i++ {
		aIsNum := isNumeric(aParts[i])
		bIsNum := isNumeric(bParts[i])
		if aIsNum && bIsNum {
			av, bv := 0, 0
			fmt.Sscanf(aParts[i], "%d", &av)
			fmt.Sscanf(bParts[i], "%d", &bv)
			if av > bv {
				return 1
			}
			if av < bv {
				return -1
			}
		} else {
			if aParts[i] > bParts[i] {
				return 1
			}
			if aParts[i] < bParts[i] {
				return -1
			}
		}
	}
	if len(aParts) > len(bParts) {
		return 1
	}
	if len(aParts) < len(bParts) {
		return -1
	}
	return 0
}

func splitAlphaNum(s string) []string {
	var parts []string
	if s == "" {
		return parts
	}
	current := ""
	isDigit := -1
	for _, ch := range s {
		curIsDigit := 0
		if ch >= '0' && ch <= '9' {
			curIsDigit = 1
		}
		if curIsDigit != isDigit && current != "" {
			parts = append(parts, current)
			current = ""
		}
		isDigit = curIsDigit
		current += string(ch)
	}
	parts = append(parts, current)
	return parts
}

func isNumeric(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

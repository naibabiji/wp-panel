package executor

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

var allowedCommands = map[string][]string{
	"systemctl":       {"start", "stop", "reload", "restart", "enable", "disable", "daemon-reload", "status"},
	"nginx":           {"-t", "-s", "-c"},
	"nft":             {"add", "delete", "list", "flush", "create", "insert", "set"},
	"fail2ban-client": {"set", "unban", "reload", "status", "banip", "start", "stop", "add", "get"},
	"useradd":         {"-r", "-s", "-d", "-m", "-g", "-M", "-U"},
	"userdel":         {"-r", "-f"},
	"usermod":         {"-a", "-G", "-g"},
	"groupadd":        {"-r"},
	"getent":          {"group"},
	"chown":           {"-R"},
	"chmod":           {"-R"},
	"mkdir":           {"-p"},
	"ln":              {"-s", "-sf", "-snf"},
	"unlink":          {},
	"cp":              {"-r", "-a", "-f"},
	"mv":              {},
	"unzip":           {"-o", "-q", "-d"},
	"wget":            {"-q", "-O", "-T", "-t"},
	"curl":            {"-s", "-o", "-f", "-L", "-X", "-H", "-d"},
	"runuser":         {"-u", "-g", "--"},
	"mysql":           {"-u", "-p", "-e", "-h", "-P", "--execute", "--host", "--password", "--user"},
	"mysqladmin":      {"-u", "-p", "password", "create", "drop", "status"},
	"test":            {"-f", "-d", "-e", "-r", "-w", "-x", "-n", "-z"},
	"cat":             {},
	"openssl":         {"rand", "-base64", "x509", "-in", "-out", "-days"},
	"tee":             {},
	"head":            {"-c"},
	"sha256sum":       {},
	"base64":          {},
}

func IsCommandAllowed(binary string, args []string) bool {
	allowedArgs, ok := allowedCommands[binary]
	if !ok {
		return false
	}
	if hasUnsafeArgs(binary, args) {
		return false
	}
	if len(allowedArgs) == 0 {
		return len(args) == 0 || binary == "cat" || binary == "tee" || binary == "head" || binary == "sha256sum" || binary == "base64"
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			allowed := false
			for _, allowedArg := range allowedArgs {
				if arg == allowedArg || strings.HasPrefix(arg, allowedArg+"=") {
					allowed = true
					break
				}
			}
			if !allowed {
				return false
			}
		}
	}
	return true
}

func hasUnsafeArgs(binary string, args []string) bool {
	for _, arg := range args {
		if arg == "" || strings.ContainsAny(arg, "\x00\r\n") {
			return true
		}
		if strings.ContainsAny(arg, ";|&`$<>") {
			return true
		}
		if (binary == "wget" || binary == "curl") && strings.HasPrefix(arg, "http") && !isAllowedDownloadURL(arg) {
			return true
		}
	}
	return false
}

func isAllowedDownloadURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "wordpress.org", "downloads.wordpress.org", "api.wordpress.org",
		"www.cloudflare.com", "developers.google.com", "www.bing.com":
		return true
	default:
		return false
	}
}

type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

func Execute(binary string, args ...string) (*ExecResult, error) {
	if !IsCommandAllowed(binary, args) {
		return nil, fmt.Errorf("命令 %s 不在白名单中", binary)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := &ExecResult{
		Stdout: strings.TrimSpace(stdout.String()),
		Stderr: strings.TrimSpace(stderr.String()),
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("命令 %s 执行超时(30秒)", binary)
		}
		result.ExitCode = 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		}
		if result.Stderr != "" {
			log.Printf("命令 %s stderr: %s", binary, result.Stderr)
		}
		return result, fmt.Errorf("命令 %s 执行失败", binary)
	}

	return result, nil
}

func ExecuteWithInput(binary string, input string, args ...string) (*ExecResult, error) {
	if !IsCommandAllowed(binary, args) {
		return nil, fmt.Errorf("命令 %s 不在白名单中", binary)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := &ExecResult{
		Stdout: strings.TrimSpace(stdout.String()),
		Stderr: strings.TrimSpace(stderr.String()),
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("命令 %s 执行超时(30秒)", binary)
		}
		if result.Stderr != "" {
			log.Printf("命令 %s stderr: %s", binary, result.Stderr)
		}
		return result, fmt.Errorf("命令 %s 执行失败", binary)
	}

	return result, nil
}

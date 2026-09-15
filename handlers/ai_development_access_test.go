package handlers

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"
)

func TestAIDevelopmentCredentialPackageUsesConfiguredPortAndPrivateMode(t *testing.T) {
	credential, err := generateAIDevelopmentCredential()
	if err != nil {
		t.Fatal(err)
	}
	knownHosts := "wp-panel-ai-target ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIE3h7V1s95BrjhMuVZ7oBvP3G4XCNiCBN3qO3U1YQ6Ks\n"
	data, err := buildAIDevelopmentCredentialPackage("example.com", "203.0.113.10", 2222, "wp_example", "/var/www/example", knownHosts, credential)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[string]*zip.File, len(archive.File))
	for _, file := range archive.File {
		files[file.Name] = file
	}
	prefix := "example.com-wp-panel-ai/"
	key := files[prefix+".wp-panel-ai/id_ed25519"]
	if key == nil || key.Mode().Perm() != 0600 {
		t.Fatalf("private key mode=%v", key)
	}
	for name, want := range map[string]os.FileMode{
		"AGENTS.md":                  0644,
		"CLAUDE.md":                  0644,
		"README.md":                  0644,
		".gitignore":                 0644,
		".wp-panel-ai/id_ed25519":    0600,
		".wp-panel-ai/known_hosts":   0600,
		".wp-panel-ai/connect.sh":    0700,
		".wp-panel-ai/connect.ps1":   0644,
		".wp-panel-ai/ssh_config":    0600,
		".wp-panel-ai/CONNECTION.md": 0644,
	} {
		file := files[prefix+name]
		if file == nil || file.Mode().Perm() != want {
			t.Fatalf("%s mode=%v, want %v", name, file, want)
		}
	}
	privateKey, err := readZipFile(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ssh.ParseRawPrivateKey(privateKey); err != nil {
		t.Fatalf("generated private key cannot be parsed by Go SSH: %v", err)
	}
	if sshKeygen, err := exec.LookPath("ssh-keygen"); err == nil {
		keyPath := filepath.Join(t.TempDir(), "id_ed25519")
		if err := os.WriteFile(keyPath, privateKey, 0600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command(sshKeygen, "-y", "-f", keyPath).CombinedOutput(); err != nil {
			t.Fatalf("generated private key cannot be parsed by OpenSSH: %v: %s", err, output)
		}
	}
	configFile := files[prefix+".wp-panel-ai/ssh_config"]
	if configFile == nil {
		t.Fatal("ssh_config missing")
	}
	reader, err := configFile.Open()
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "Port 2222") || !strings.Contains(string(content), "HostName 203.0.113.10") {
		t.Fatalf("unexpected ssh_config: %s", content)
	}
	if strings.Contains(string(content), "IdentityFile") {
		t.Fatalf("ssh_config must not rely on a working-directory-relative IdentityFile: %s", content)
	}
	for _, required := range []string{"AGENTS.md", "CLAUDE.md", "README.md", ".gitignore", ".wp-panel-ai/connect.sh", ".wp-panel-ai/connect.ps1", ".wp-panel-ai/CONNECTION.md", ".wp-panel-ai/known_hosts"} {
		if files[prefix+required] == nil {
			t.Fatalf("%s missing", required)
		}
	}
	agentInstructions, err := readZipFile(files[prefix+"AGENTS.md"])
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"WP-PANEL-AI-HANDOFF.md", "WP-PANEL-CAPABILITIES.md", "authoritative", "Work only on this website", "cannot coexist"} {
		if !strings.Contains(string(agentInstructions), required) {
			t.Fatalf("AGENTS.md missing %q: %s", required, agentInstructions)
		}
	}
	for _, forbidden := range []string{"AI-CONTEXT.md", "DEVELOPMENT-PLAN.md", "AI-CHANGELOG.md", "Git is optional", "wait for explicit approval"} {
		if strings.Contains(string(agentInstructions), forbidden) {
			t.Fatalf("AGENTS.md unexpectedly contains development guidance %q: %s", forbidden, agentInstructions)
		}
	}
	gitignore, err := readZipFile(files[prefix+".gitignore"])
	if err != nil {
		t.Fatal(err)
	}
	if string(gitignore) != ".wp-panel-ai/\n" {
		t.Fatalf("unexpected .gitignore: %q", gitignore)
	}
	connectScript, err := readZipFile(files[prefix+".wp-panel-ai/connect.sh"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(connectScript), `-i "$key"`) {
		t.Fatalf("connect.sh does not resolve the private key from its own directory: %s", connectScript)
	}
	if !strings.Contains(string(connectScript), `chmod 600 "$key" "$known_hosts"`) {
		t.Fatalf("connect.sh does not secure the private key: %s", connectScript)
	}
	for _, required := range []string{`UserKnownHostsFile=$known_hosts`, "known_hosts"} {
		if !strings.Contains(string(connectScript), required) {
			t.Fatalf("connect.sh missing %q: %s", required, connectScript)
		}
	}
	powerShellScript, err := readZipFile(files[prefix+".wp-panel-ai/connect.ps1"])
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"icacls.exe", "/inheritance:r", ":(F)", "Position = 0", "ValueFromRemainingArguments", "$RemoteCommand -join ' '", "& ssh.exe @SSHArguments", "UserKnownHostsFile=$KnownHostsPath", "@($KeyPath, $KnownHostsPath)"} {
		if !strings.Contains(string(powerShellScript), required) {
			t.Fatalf("connect.ps1 missing %q: %s", required, powerShellScript)
		}
	}
}

func TestAIDevelopmentConnectScriptSecuresPrivateKey(t *testing.T) {
	credential, err := generateAIDevelopmentCredential()
	if err != nil {
		t.Fatal(err)
	}
	knownHosts := "wp-panel-ai-target ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIE3h7V1s95BrjhMuVZ7oBvP3G4XCNiCBN3qO3U1YQ6Ks\n"
	data, err := buildAIDevelopmentCredentialPackage("example.com", "203.0.113.10", 22, "wp_example", "/var/www/example", knownHosts, credential)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[string]*zip.File, len(archive.File))
	for _, file := range archive.File {
		files[file.Name] = file
	}
	prefix := "example.com-wp-panel-ai/.wp-panel-ai/"
	dir := t.TempDir()
	for _, name := range []string{"connect.sh", "id_ed25519", "ssh_config", "known_hosts"} {
		content, err := readZipFile(files[prefix+name])
		if err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0600)
		if name == "id_ed25519" {
			mode = 0644
		}
		if err := os.WriteFile(filepath.Join(dir, name), content, mode); err != nil {
			t.Fatal(err)
		}
	}
	fakeBin := filepath.Join(dir, "bin")
	if err := os.Mkdir(fakeBin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "ssh"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join(dir, "connect.sh"))
	cmd.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("connect.sh failed: %v: %s", err, output)
	}
	info, err := os.Stat(filepath.Join(dir, "id_ed25519"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("private key mode=%v, want 0600", got)
	}
}

func readZipFile(file *zip.File) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func TestAIDevelopmentPackageResponseCannotBeCached(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	writeAIDevelopmentPackage(ctx, "example.com", []byte("zip"))
	if got := recorder.Header().Get("Cache-Control"); got != "no-store, private" {
		t.Fatalf("Cache-Control=%q", got)
	}
	if got := recorder.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") {
		t.Fatalf("Content-Disposition=%q", got)
	}
	if got := recorder.Header().Get("Content-Disposition"); !strings.Contains(got, `filename="example.com-wp-panel-ai.zip"`) {
		t.Fatalf("Content-Disposition=%q", got)
	}
}

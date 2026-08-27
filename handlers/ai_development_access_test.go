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
	data, err := buildAIDevelopmentCredentialPackage("example.com", "203.0.113.10", 2222, "wp_example", "/var/www/example", credential)
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
	for _, required := range []string{"AGENTS.md", "CLAUDE.md", "README.md", ".gitignore", ".wp-panel-ai/connect.sh", ".wp-panel-ai/connect.ps1", ".wp-panel-ai/CONNECTION.md"} {
		if files[prefix+required] == nil {
			t.Fatalf("%s missing", required)
		}
	}
	agentInstructions, err := readZipFile(files[prefix+"AGENTS.md"])
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"AI-CONTEXT.md", "DEVELOPMENT-PLAN.md", "AI-CHANGELOG.md", "read-only discovery", "Wait for explicit approval", "Do not automatically create backups"} {
		if !strings.Contains(string(agentInstructions), required) {
			t.Fatalf("AGENTS.md missing %q: %s", required, agentInstructions)
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
	if !strings.Contains(string(connectScript), `-i "$credential_dir/id_ed25519"`) {
		t.Fatalf("connect.sh does not resolve the private key from its own directory: %s", connectScript)
	}
	powerShellScript, err := readZipFile(files[prefix+".wp-panel-ai/connect.ps1"])
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"icacls.exe", "/inheritance:r", ":(F)", "Position = 0", "ValueFromRemainingArguments", "$RemoteCommand -join ' '", "& ssh.exe @SSHArguments"} {
		if !strings.Contains(string(powerShellScript), required) {
			t.Fatalf("connect.ps1 missing %q: %s", required, powerShellScript)
		}
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

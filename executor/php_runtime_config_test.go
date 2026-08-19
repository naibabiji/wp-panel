package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsurePHPRuntimeConfigFileAddsMissingKeys(t *testing.T) {
	oldPath := phpRuntimeConfigPath
	phpRuntimeConfigPath = filepath.Join(t.TempDir(), "99-wppanel.ini")
	t.Cleanup(func() { phpRuntimeConfigPath = oldPath })

	original := "memory_limit = 512M\nupload_max_filesize = 128M\npost_max_size = 128M\nmax_execution_time = 100\nmax_input_vars = 3000\n"
	if err := os.WriteFile(phpRuntimeConfigPath, []byte(original), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	changed, err := EnsurePHPRuntimeConfigFile()
	if err != nil {
		t.Fatalf("ensure config: %v", err)
	}
	if !changed {
		t.Fatal("expected missing max_input_time to be added")
	}

	data, err := os.ReadFile(phpRuntimeConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "max_input_time = 300") {
		t.Fatalf("expected max_input_time default, got:\n%s", content)
	}
	if !strings.Contains(content, "upload_max_filesize = 128M") {
		t.Fatalf("expected existing values to be preserved, got:\n%s", content)
	}
	for _, want := range []string{
		"expose_php = Off",
		"opcache.enable = 1",
		"opcache.interned_strings_buffer = 8",
		"opcache.validate_timestamps = 1",
		"opcache.revalidate_freq = 5",
		"opcache.save_comments = 1",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected new static default %q to be added, got:\n%s", want, content)
		}
	}
	if findIniValue(content, "opcache.memory_consumption") == "" {
		t.Fatalf("expected opcache.memory_consumption to be added, got:\n%s", content)
	}
	if findIniValue(content, "opcache.max_accelerated_files") == "" {
		t.Fatalf("expected opcache.max_accelerated_files to be added, got:\n%s", content)
	}
}

func TestEnsurePHPRuntimeConfigFileDoesNotOverwriteExistingOpcacheValues(t *testing.T) {
	oldPath := phpRuntimeConfigPath
	phpRuntimeConfigPath = filepath.Join(t.TempDir(), "99-wppanel.ini")
	t.Cleanup(func() { phpRuntimeConfigPath = oldPath })

	original := "opcache.memory_consumption = 999\nopcache.max_accelerated_files = 777\nexpose_php = On\n"
	if err := os.WriteFile(phpRuntimeConfigPath, []byte(original), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := EnsurePHPRuntimeConfigFile(); err != nil {
		t.Fatalf("ensure config: %v", err)
	}

	data, err := os.ReadFile(phpRuntimeConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	content := string(data)
	if findIniValue(content, "opcache.memory_consumption") != "999" {
		t.Fatalf("expected admin-set opcache.memory_consumption to be preserved, got:\n%s", content)
	}
	if findIniValue(content, "opcache.max_accelerated_files") != "777" {
		t.Fatalf("expected admin-set opcache.max_accelerated_files to be preserved, got:\n%s", content)
	}
	if findIniValue(content, "expose_php") != "On" {
		t.Fatalf("expected admin-set expose_php to be preserved, got:\n%s", content)
	}
}

func TestEnsurePHPRuntimeConfigFileFreshInstallIncludesOpcacheDefaults(t *testing.T) {
	oldPath := phpRuntimeConfigPath
	phpRuntimeConfigPath = filepath.Join(t.TempDir(), "99-wppanel.ini")
	t.Cleanup(func() { phpRuntimeConfigPath = oldPath })

	changed, err := EnsurePHPRuntimeConfigFile()
	if err != nil {
		t.Fatalf("ensure config: %v", err)
	}
	if !changed {
		t.Fatal("expected fresh install to write the config file")
	}

	data, err := os.ReadFile(phpRuntimeConfigPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	content := string(data)
	if findIniValue(content, "max_input_vars") != "10000" {
		t.Fatalf("expected fresh install to use the new max_input_vars default, got:\n%s", content)
	}
	if findIniValue(content, "opcache.memory_consumption") == "" {
		t.Fatalf("expected fresh install to include opcache.memory_consumption, got:\n%s", content)
	}
}

func TestRenderPHPFPMPoolUsesRuntimeConfig(t *testing.T) {
	oldPath := phpRuntimeConfigPath
	phpRuntimeConfigPath = filepath.Join(t.TempDir(), "99-wppanel.ini")
	t.Cleanup(func() { phpRuntimeConfigPath = oldPath })

	content := "memory_limit = 512M\nupload_max_filesize = 128M\npost_max_size = 128M\nmax_execution_time = 100\nmax_input_time = 100\nmax_input_vars = 3000\n"
	if err := os.WriteFile(phpRuntimeConfigPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	engine := NewTemplateEngine("")
	rendered, err := engine.RenderPHPFPMPool(&PHPFPMPoolData{
		Domain:     "example.com",
		PoolName:   "example_com",
		SystemUser: "wp_example",
		WebRoot:    "/www/wwwroot/example.com",
		SocketPath: "/run/php",
		SocketName: "example_com",
	})
	if err != nil {
		t.Fatalf("render pool: %v", err)
	}

	for _, want := range []string{
		"php_admin_value[upload_max_filesize] = 128M",
		"php_admin_value[post_max_size] = 128M",
		"php_admin_value[max_execution_time] = 100",
		"php_admin_value[max_input_time] = 100",
		"php_admin_value[memory_limit] = 512M",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered pool missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderPHPFPMPoolUsesPersistedMaxChildren(t *testing.T) {
	engine := NewTemplateEngine("")
	rendered, err := engine.RenderPHPFPMPool(&PHPFPMPoolData{
		Domain:      "example.com",
		PoolName:    "example_com",
		SystemUser:  "wp_example",
		WebRoot:     "/www/wwwroot/example.com",
		SocketPath:  "/run/php",
		SocketName:  "example_com",
		MaxChildren: "6",
	})
	if err != nil {
		t.Fatalf("render pool: %v", err)
	}
	if !strings.Contains(rendered, "pm.max_children = 6") {
		t.Fatalf("expected persisted MaxChildren to be rendered, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "pm.start_servers") || strings.Contains(rendered, "pm.min_spare_servers") || strings.Contains(rendered, "pm.max_spare_servers") {
		t.Fatalf("expected dead ondemand-mode directives to be removed, got:\n%s", rendered)
	}
}

func TestRenderPHPFPMPoolFallsBackToOldDefaultWhenMaxChildrenEmpty(t *testing.T) {
	engine := NewTemplateEngine("")
	rendered, err := engine.RenderPHPFPMPool(&PHPFPMPoolData{
		Domain:     "example.com",
		PoolName:   "example_com",
		SystemUser: "wp_example",
		WebRoot:    "/www/wwwroot/example.com",
		SocketPath: "/run/php",
		SocketName: "example_com",
	})
	if err != nil {
		t.Fatalf("render pool: %v", err)
	}
	if !strings.Contains(rendered, "pm.max_children = 10") {
		t.Fatalf("expected fallback default of 10 when MaxChildren is unset, got:\n%s", rendered)
	}
}

// TestRenderPHPFPMPoolFallsBackWhenMaxChildrenIsZeroOrInvalid 覆盖代码审核发现的一个
// 边界情况：如果调用方漏加载 site.PHPFPMMaxChildren（Go 零值是 0），strconv.Itoa(0)
// 得到的是非空字符串 "0"，只判断"是否为空字符串"的兜底逻辑不会触发，会渲染出
// pm.max_children = 0（php-fpm8.3 -t 会拒绝，但不该指望语法检查这层保护兜底）。
func TestRenderPHPFPMPoolFallsBackWhenMaxChildrenIsZeroOrInvalid(t *testing.T) {
	engine := NewTemplateEngine("")
	for _, bad := range []string{"0", "-5", "not-a-number"} {
		rendered, err := engine.RenderPHPFPMPool(&PHPFPMPoolData{
			Domain:      "example.com",
			PoolName:    "example_com",
			SystemUser:  "wp_example",
			WebRoot:     "/www/wwwroot/example.com",
			SocketPath:  "/run/php",
			SocketName:  "example_com",
			MaxChildren: bad,
		})
		if err != nil {
			t.Fatalf("render pool with MaxChildren=%q: %v", bad, err)
		}
		if !strings.Contains(rendered, "pm.max_children = 10") {
			t.Fatalf("expected MaxChildren=%q to fall back to 10, got:\n%s", bad, rendered)
		}
	}
}

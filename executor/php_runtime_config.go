package executor

import (
	"os"
	"strconv"
	"strings"
)

type PHPRuntimeConfig struct {
	MemoryLimit       string
	UploadMaxFilesize string
	PostMaxSize       string
	MaxExecutionTime  string
	MaxInputTime      string
	MaxInputVars      string
}

var phpRuntimeConfigPath = "/etc/php/8.3/fpm/conf.d/99-wppanel.ini"

var phpRuntimeDefaults = PHPRuntimeConfig{
	MemoryLimit:       "256M",
	UploadMaxFilesize: "64M",
	PostMaxSize:       "64M",
	MaxExecutionTime:  "300",
	MaxInputTime:      "300",
	// 2026-08：旧默认值 2000 太小，WordPress 后台大表单（Elementor/ACF 等）容易被静默截断，
	// 已改为 10000。老用户的升级路径见 executor/php_legacy_defaults_upgrade.go。
	MaxInputVars: "10000",
}

// phpOpcacheStaticDefaults 是不随硬件规格变化的 OPcache/安全基线默认值，
// 只在 key 缺失时补齐，不会覆盖管理员已手动设置的值。顺序固定，保证生成内容可预测。
var phpOpcacheStaticDefaultKeys = []string{
	"expose_php",
	"opcache.enable",
	"opcache.interned_strings_buffer",
	"opcache.validate_timestamps",
	"opcache.revalidate_freq",
	"opcache.save_comments",
}

var phpOpcacheStaticDefaults = map[string]string{
	"expose_php":                      "Off",
	"opcache.enable":                  "1",
	"opcache.interned_strings_buffer": "8",
	"opcache.validate_timestamps":     "1",
	"opcache.revalidate_freq":         "5",
	"opcache.save_comments":           "1",
}

func PHPRuntimeConfigPath() string {
	return phpRuntimeConfigPath
}

func EnsurePHPRuntimeConfigFile() (bool, error) {
	data, err := os.ReadFile(phpRuntimeConfigPath)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}

	if os.IsNotExist(err) {
		return true, os.WriteFile(phpRuntimeConfigPath, []byte(defaultPHPRuntimeConfigContent()), 0644)
	}

	content := string(data)
	next := ensureIniValue(content, "memory_limit", phpRuntimeDefaults.MemoryLimit)
	next = ensureIniValue(next, "upload_max_filesize", phpRuntimeDefaults.UploadMaxFilesize)
	next = ensureIniValue(next, "post_max_size", phpRuntimeDefaults.PostMaxSize)
	next = ensureIniValue(next, "max_execution_time", phpRuntimeDefaults.MaxExecutionTime)
	next = ensureIniValue(next, "max_input_time", phpRuntimeDefaults.MaxInputTime)
	next = ensureIniValue(next, "max_input_vars", phpRuntimeDefaults.MaxInputVars)
	for _, key := range phpOpcacheStaticDefaultKeys {
		next = ensureIniValue(next, key, phpOpcacheStaticDefaults[key])
	}

	// opcache.memory_consumption / opcache.max_accelerated_files 按当前服务器硬件规格
	// 计算推荐初始值，只在这两个 key 缺失时才会被写入；已存在的值（不论是旧安装留下的
	// 还是管理员手动改过的）不会被覆盖。
	memMB, maxFiles := recommendedOpcacheAdaptiveDefaults()
	next = ensureIniValue(next, "opcache.memory_consumption", memMB)
	next = ensureIniValue(next, "opcache.max_accelerated_files", maxFiles)

	if next == content {
		return false, nil
	}
	return true, os.WriteFile(phpRuntimeConfigPath, []byte(next), 0644)
}

// recommendedOpcacheAdaptiveDefaults 计算 opcache.memory_consumption（MB）和
// opcache.max_accelerated_files 的建议初始值，供 EnsurePHPRuntimeConfigFile 和
// defaultPHPRuntimeConfigContent 在这两个 key 缺失时写入。
func recommendedOpcacheAdaptiveDefaults() (memoryConsumptionMB string, maxAcceleratedFiles string) {
	facts := CollectSystemFacts()
	return strconv.Itoa(RecommendOPcacheMemoryConsumptionMB(facts)),
		strconv.Itoa(RecommendOPcacheMaxAcceleratedFiles(facts))
}

func LoadPHPRuntimeConfig() PHPRuntimeConfig {
	cfg := phpRuntimeDefaults
	data, err := os.ReadFile(phpRuntimeConfigPath)
	if err != nil {
		return cfg
	}
	content := string(data)
	if v := findIniValue(content, "memory_limit"); v != "" {
		cfg.MemoryLimit = v
	}
	if v := findIniValue(content, "upload_max_filesize"); v != "" {
		cfg.UploadMaxFilesize = v
	}
	if v := findIniValue(content, "post_max_size"); v != "" {
		cfg.PostMaxSize = v
	}
	if v := findIniValue(content, "max_execution_time"); v != "" {
		cfg.MaxExecutionTime = v
	}
	if v := findIniValue(content, "max_input_time"); v != "" {
		cfg.MaxInputTime = v
	}
	if v := findIniValue(content, "max_input_vars"); v != "" {
		cfg.MaxInputVars = v
	}
	return cfg
}

func defaultPHPRuntimeConfigContent() string {
	memMB, maxFiles := recommendedOpcacheAdaptiveDefaults()
	return `; WP Panel - WordPress runtime baseline
; These values are managed by Software Management.
memory_limit = ` + phpRuntimeDefaults.MemoryLimit + `
upload_max_filesize = ` + phpRuntimeDefaults.UploadMaxFilesize + `
post_max_size = ` + phpRuntimeDefaults.PostMaxSize + `
max_execution_time = ` + phpRuntimeDefaults.MaxExecutionTime + `
max_input_time = ` + phpRuntimeDefaults.MaxInputTime + `
max_input_vars = ` + phpRuntimeDefaults.MaxInputVars + `
expose_php = ` + phpOpcacheStaticDefaults["expose_php"] + `
opcache.enable = ` + phpOpcacheStaticDefaults["opcache.enable"] + `
opcache.interned_strings_buffer = ` + phpOpcacheStaticDefaults["opcache.interned_strings_buffer"] + `
opcache.memory_consumption = ` + memMB + `
opcache.max_accelerated_files = ` + maxFiles + `
opcache.validate_timestamps = ` + phpOpcacheStaticDefaults["opcache.validate_timestamps"] + `
opcache.revalidate_freq = ` + phpOpcacheStaticDefaults["opcache.revalidate_freq"] + `
opcache.save_comments = ` + phpOpcacheStaticDefaults["opcache.save_comments"] + `
`
}

func ensureIniValue(content, key, value string) string {
	if findIniValue(content, key) != "" {
		return content
	}
	return strings.TrimRight(content, "\n") + "\n" + key + " = " + value + "\n"
}

// setIniValue 替换一个已存在 key 的值；若 key 不存在则原样返回内容不做任何改动
// （调用方应先自行判断该 key 是否存在，这个函数只负责"覆盖"，不负责"补齐"）。
func setIniValue(content, key, value string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, key+" =") || strings.HasPrefix(trimmed, key+"=") {
			lines[i] = key + " = " + value
			return strings.Join(lines, "\n")
		}
	}
	return content
}

func findIniValue(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, key+" =") || strings.HasPrefix(trimmed, key+"=") {
			parts := strings.SplitN(trimmed, "=", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

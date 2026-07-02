package handlers

import (
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

type SoftwareHandler struct{}

type guardResponse struct {
	Name         string `json:"name"`
	Service      string `json:"service"`
	Version      string `json:"version"`
	Running      bool   `json:"running"`
	Paused       bool   `json:"paused"`
	Restarts     int    `json:"restarts"`
	LastIncident string `json:"last_incident"`
}

var versionCmds = map[string]string{
	"nginx":        "nginx -v 2>&1 | awk -F/ '{print $2}'",
	"php8.3-fpm":   "php -v 2>/dev/null | head -1 | awk '{print $2}'",
	"mariadb":      "mariadb --version 2>/dev/null | awk '{print $3}' | cut -d, -f1",
	"redis-server": "redis-server --version 2>/dev/null | awk '{print $3}' | cut -d= -f2",
	"nftables":     "nft --version 2>/dev/null | awk '{print $2}' | cut -dv -f2",
	"fail2ban":     "fail2ban-client --version 2>/dev/null | awk '{print $2}'",
}

type softwareItem struct {
	Name       string           `json:"name"`
	Version    string           `json:"version"`
	Status     string           `json:"status"`
	Configs    []softwareConfig `json:"configs"`
	ConfigPath string           `json:"-"`
}

type softwareConfig struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Value   string   `json:"value"`
	Hint    string   `json:"hint"`
	Options []string `json:"options,omitempty"` // kept for backward compat, no longer used in UI
}

func softwareLang(c *gin.Context) string {
	return i18n.LangFromRequest(c.Request)
}

func (h *SoftwareHandler) List(c *gin.Context) {
	lang := softwareLang(c)
	items := []softwareItem{
		getPHPInfo(lang),
		getNginxInfo(lang),
		getMariaDBInfo(lang),
		getRedisInfo(lang),
	}
	items[0].Configs = append(items[0].Configs, softwareConfig{
		Key:   "max_input_time",
		Label: i18n.T(lang, "software.max_input_time_label"),
		Hint:  i18n.T(lang, "software.max_input_time_hint"),
	})
	for i := range items {
		populateConfigValues(&items[i])
	}
	c.JSON(http.StatusOK, models.SuccessResponse(items))
}

var configDefaults = map[string]string{
	"memory_limit":            "256M",
	"upload_max_filesize":     "64M",
	"post_max_size":           "64M",
	"max_execution_time":      "300",
	"max_input_time":          "300",
	"max_input_vars":          "2000",
	"client_max_body_size":    "1m",
	"innodb_buffer_pool_size": "128M",
	"maxmemory":               "0",
}

func populateConfigValues(item *softwareItem) {
	data, err := os.ReadFile(item.ConfigPath)
	content := ""
	if err == nil {
		content = string(data)
	}
	for i := range item.Configs {
		val := findPHPIniValue(content, item.Configs[i].Key)
		if val == "" {
			val = findNginxValue(content, item.Configs[i].Key)
		}
		if val == "" {
			val = findRedisValue(content, item.Configs[i].Key)
		}
		if val != "" {
			item.Configs[i].Value = val
		} else if def, ok := configDefaults[item.Configs[i].Key]; ok {
			item.Configs[i].Value = def
		}
	}
}

var softwareLogPaths = map[string]string{
	"Nginx":   "/var/log/nginx/error.log",
	"PHP":     "/var/log/php8.3-fpm.log",
	"MariaDB": "/var/log/mysql/error.log",
	"Redis":   "/var/log/redis/redis-server.log",
}

func (h *SoftwareHandler) ViewLog(c *gin.Context) {
	lang := softwareLang(c)
	name := c.Query("name")
	path, ok := softwareLogPaths[name]
	if !ok {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.T(lang, "software.unknown_software")))
		return
	}
	lines := 200
	if n, err := strconv.Atoi(c.DefaultQuery("lines", "200")); err == nil && n > 0 && n <= 500 {
		lines = n
	}
	content := tailFile(path, lines)
	if content == "" {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"content": i18n.T(lang, "software.log_empty_or_unreadable")}))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"content": content}))
}

func (h *SoftwareHandler) ClearLog(c *gin.Context) {
	lang := softwareLang(c)
	name := c.Query("name")
	path, ok := softwareLogPaths[name]
	if !ok {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.T(lang, "software.unknown_software")))
		return
	}
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		log.Printf("清空软件日志失败 name=%s: %v", name, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.T(lang, "software.clear_failed")))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": i18n.T(lang, "software.log_cleared", i18n.P{"name": name})}))
}

func (h *SoftwareHandler) GetGuardStatus(c *gin.Context) {
	svcs := executor.GetGuardStatus()
	result := make([]guardResponse, len(svcs))
	for i, s := range svcs {
		result[i] = guardResponse{
			Name:         s.Name,
			Service:      s.ServiceName,
			Version:      strings.TrimSpace(runCmd(versionCmds[s.ServiceName])),
			Running:      s.Running,
			Paused:       s.Paused,
			Restarts:     s.Restarts,
			LastIncident: s.LastIncident,
		}
	}
	c.JSON(http.StatusOK, models.SuccessResponse(result))
}

func (h *SoftwareHandler) GuardAction(c *gin.Context) {
	lang := softwareLang(c)
	var req struct {
		Service string `json:"service"`
		Action  string `json:"action"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.T(lang, "common.invalid_params")))
		return
	}
	if req.Action != "start" && req.Action != "stop" && req.Action != "restart" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.T(lang, "software.invalid_action")))
		return
	}
	if err := executor.SetServiceState(req.Service, req.Action); err != nil {
		log.Printf("守护操作失败 service=%s action=%s: %v", req.Service, req.Action, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.T(lang, "software.operation_failed_with_error", i18n.P{"error": err.Error()})))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": i18n.T(lang, "software.action_success", i18n.P{"service": req.Service, "action": req.Action})}))
}

var softConfigAllowed = map[string]map[string]bool{
	"PHP": {
		"memory_limit": true, "upload_max_filesize": true, "post_max_size": true,
		"max_execution_time": true, "max_input_time": true, "max_input_vars": true,
	},
	"Nginx":   {"client_max_body_size": true},
	"MariaDB": {"innodb_buffer_pool_size": true},
	"Redis":   {"maxmemory": true},
}

var (
	phpSizeValueRe = regexp.MustCompile(`^[0-9]+[KMGkmg]?$`)
	phpIntValueRe  = regexp.MustCompile(`^[0-9]+$`)
)

func validateSoftwareConfigValue(lang, name, key, value string) string {
	if name != "PHP" {
		return ""
	}
	switch key {
	case "memory_limit", "upload_max_filesize", "post_max_size":
		if !phpSizeValueRe.MatchString(value) {
			return i18n.T(lang, "software.php_size_invalid")
		}
	case "max_execution_time", "max_input_time", "max_input_vars":
		if !phpIntValueRe.MatchString(value) {
			return i18n.T(lang, "software.php_int_invalid")
		}
	}
	return ""
}

func phpConfigRequiresPoolRebuild(key string) bool {
	switch key {
	case "memory_limit", "upload_max_filesize", "post_max_size", "max_execution_time", "max_input_time":
		return true
	default:
		return false
	}
}

func (h *SoftwareHandler) SaveConfig(c *gin.Context) {
	lang := softwareLang(c)
	var req struct {
		Name  string `json:"name"`
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.T(lang, "common.invalid_params")))
		return
	}

	var configPath, serviceName, checkCmd, reloadCmd string

	switch req.Name {
	case "PHP":
		configPath = executor.PHPRuntimeConfigPath()
		serviceName = "php8.3-fpm"
		checkCmd = "php-fpm8.3 -t"
		reloadCmd = "systemctl reload php8.3-fpm"
	case "Nginx":
		configPath = "/etc/nginx/conf.d/wppanel.conf"
		serviceName = "nginx"
		checkCmd = "nginx -t"
		reloadCmd = "systemctl reload nginx"
	case "MariaDB":
		configPath = "/etc/mysql/mariadb.conf.d/99-wppanel.cnf"
		serviceName = "mariadb"
		reloadCmd = "systemctl restart mariadb"
	case "Redis":
		configPath = "/etc/redis/redis.conf"
		serviceName = "redis-server"
		reloadCmd = "systemctl restart redis-server"
	default:
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.T(lang, "software.unknown_software")))
		return
	}

	// Validate key against per-service allowlist
	if allowed, ok := softConfigAllowed[req.Name]; !ok || !allowed[req.Key] {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.T(lang, "software.unsupported_config_item", i18n.P{"key": req.Key})))
		return
	}
	// Reject value containing newlines or directive-terminating characters
	if hasLineBreak(req.Value) {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.T(lang, "software.value_no_newline")))
		return
	}
	if req.Name == "Nginx" && strings.Contains(req.Value, ";") {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.T(lang, "software.nginx_value_no_semicolon")))
		return
	}

	if errMsg := validateSoftwareConfigValue(lang, req.Name, req.Key, req.Value); errMsg != "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(errMsg))
		return
	}

	// Ensure config file exists (for conf.d files created by baseline)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if req.Name == "PHP" {
			if _, err := executor.EnsurePHPRuntimeConfigFile(); err != nil {
				log.Printf("创建 PHP 配置文件失败: %v", err)
				c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.T(lang, "software.create_php_config_failed")))
				return
			}
		} else if req.Name == "Nginx" {
			os.WriteFile(configPath, []byte("# WP Panel\n"), 0644)
		} else if req.Name == "MariaDB" {
			os.WriteFile(configPath, []byte("# WP Panel\n[mysqld]\n"), 0644)
		}
	}

	// Read config file
	data, err := os.ReadFile(configPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.T(lang, "software.read_config_failed")))
		return
	}

	content := string(data)
	var oldValue string
	switch req.Name {
	case "Redis":
		oldValue = findRedisValue(content, req.Key)
	case "Nginx":
		oldValue = findNginxValue(content, req.Key)
	default:
		oldValue = findPHPIniValue(content, req.Key)
	}

	// Simple backup
	os.WriteFile(configPath+".wppanel.bak", data, 0644)

	// Replace value using appropriate function per software
	var newContent string
	switch req.Name {
	case "PHP":
		newContent = replaceIniValue(content, req.Key, req.Value)
	case "Nginx":
		newContent = replaceNginxValue(content, req.Key, req.Value)
	case "Redis":
		newContent = replaceRedisValue(content, req.Key, req.Value)
	default:
		newContent = replaceIniValue(content, req.Key, req.Value)
	}

	if newContent != content {
		if err := os.WriteFile(configPath, []byte(newContent), 0644); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.T(lang, "software.write_config_failed")))
			return
		}
	} else if oldValue == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.T(lang, "software.config_not_found", i18n.P{"key": req.Key})))
		return
	}

	// Syntax check
	if checkCmd != "" {
		out, err := exec.Command("bash", "-c", checkCmd).CombinedOutput()
		if err != nil {
			os.WriteFile(configPath, data, 0644)
			c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.T(lang, "software.syntax_check_failed_with_rollback", i18n.P{"output": strings.TrimSpace(string(out))})))
			return
		}
	}

	// Reload
	if req.Name == "PHP" && phpConfigRequiresPoolRebuild(req.Key) {
		if err := executor.RegenerateAllSitesFPM(); err != nil {
			log.Printf("PHP 配置已写入，但部分站点 PHP-FPM Pool 重建失败: %v", err)
			c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.T(lang, "software.php_pool_rebuild_failed", i18n.P{"error": err.Error()})))
			return
		}
	} else {
		exec.Command("bash", "-c", reloadCmd).Run()
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": i18n.T(lang, "software.config_updated_reloaded", i18n.P{"service": serviceName})}))
}

func getPHPInfo(lang string) softwareItem {
	ver := runCmd("php -v 2>/dev/null | head -1 | awk '{print $2}'")
	extCount := runCmd("php -m 2>/dev/null | wc -l")
	return softwareItem{
		Name:       "PHP",
		Version:    strings.TrimSpace(ver),
		Status:     i18n.T(lang, "software.php_installed_extensions", i18n.P{"count": strings.TrimSpace(extCount)}),
		ConfigPath: executor.PHPRuntimeConfigPath(),
		Configs: []softwareConfig{
			{Key: "memory_limit", Label: i18n.T(lang, "software.memory_limit_label"), Hint: i18n.T(lang, "software.memory_limit_hint")},
			{Key: "upload_max_filesize", Label: i18n.T(lang, "software.upload_max_filesize_label"), Hint: i18n.T(lang, "software.upload_max_filesize_hint")},
			{Key: "post_max_size", Label: i18n.T(lang, "software.post_max_size_label"), Hint: i18n.T(lang, "software.post_max_size_hint")},
			{Key: "max_execution_time", Label: i18n.T(lang, "software.max_execution_time_label"), Hint: i18n.T(lang, "software.max_execution_time_hint")},
			{Key: "max_input_vars", Label: i18n.T(lang, "software.max_input_vars_label"), Hint: i18n.T(lang, "software.max_input_vars_hint")},
		},
	}
}

func getNginxInfo(lang string) softwareItem {
	ver := runCmd("nginx -v 2>&1 | awk -F/ '{print $2}'")
	return softwareItem{
		Name:       "Nginx",
		Version:    strings.TrimSpace(ver),
		Status:     i18n.T(lang, "software.installed"),
		ConfigPath: "/etc/nginx/conf.d/wppanel.conf",
		Configs: []softwareConfig{
			{Key: "client_max_body_size", Label: i18n.T(lang, "software.client_max_body_size_label"), Hint: i18n.T(lang, "software.client_max_body_size_hint")},
		},
	}
}

func getMariaDBInfo(lang string) softwareItem {
	ver := runCmd("mariadb --version 2>/dev/null | awk '{print $3}' | cut -d, -f1")
	return softwareItem{
		Name:       "MariaDB",
		Version:    strings.TrimSpace(ver),
		Status:     i18n.T(lang, "software.installed"),
		ConfigPath: "/etc/mysql/mariadb.conf.d/99-wppanel.cnf",
		Configs: []softwareConfig{
			{Key: "innodb_buffer_pool_size", Label: i18n.T(lang, "software.innodb_buffer_pool_size_label"), Hint: i18n.T(lang, "software.innodb_buffer_pool_size_hint")},
		},
	}
}

func getRedisInfo(lang string) softwareItem {
	ver := runCmd("redis-server --version 2>/dev/null | awk '{print $3}' | cut -d= -f2")
	status := i18n.T(lang, "software.running")
	if runCmd("systemctl is-active redis-server 2>/dev/null") != "active" {
		status = i18n.T(lang, "software.stopped")
	}
	return softwareItem{
		Name:       "Redis",
		Version:    strings.TrimSpace(ver),
		Status:     status,
		ConfigPath: "/etc/redis/redis.conf",
		Configs: []softwareConfig{
			{Key: "maxmemory", Label: i18n.T(lang, "software.maxmemory_label"), Hint: i18n.T(lang, "software.maxmemory_hint")},
		},
	}
}

func runCmd(cmd string) string {
	out, _ := exec.Command("bash", "-c", cmd).CombinedOutput()
	return strings.TrimSpace(string(out))
}

func replaceIniValue(content, key, value string) string {
	lines := strings.Split(content, "\n")
	prefix := key + " ="
	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, prefix) || strings.HasPrefix(trimmed, key+"=") {
			lines[i] = key + " = " + value
			found = true
		}
	}
	if !found {
		lines = append(lines, "", "; WP Panel — WordPress 优化", key+" = "+value)
	}
	return strings.Join(lines, "\n")
}

func replaceNginxValue(content, key, value string) string {
	lines := strings.Split(content, "\n")
	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key) {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
				lines[i] = indent + key + " " + value + ";"
				found = true
			}
		}
	}
	if !found {
		// Add inside http block if possible, otherwise append
		for i, line := range lines {
			if strings.Contains(line, "http {") {
				lines[i] = line + "\n    " + key + " " + value + ";"
				found = true
				break
			}
		}
		if !found {
			lines = append(lines, key+" "+value+";")
		}
	}
	return strings.Join(lines, "\n")
}

func replaceRedisValue(content, key, value string) string {
	lines := strings.Split(content, "\n")
	// Strip any INI-style comments accidentally written to redis.conf
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, "maxmemory =") {
			continue
		}
		filtered = append(filtered, line)
	}
	lines = filtered

	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 && fields[0] == key {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = indent + key + " " + value
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, "", "# WP Panel", key+" "+value)
	}
	return strings.Join(lines, "\n")
}

func findPHPIniValue(content, key string) string {
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

func findRedisValue(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 && fields[0] == key {
			if fields[1] == "=" && len(fields) >= 3 {
				return fields[2]
			}
			return fields[1]
		}
	}
	return ""
}

func findNginxValue(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key) {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				return strings.TrimRight(parts[1], ";")
			}
		}
	}
	return ""
}

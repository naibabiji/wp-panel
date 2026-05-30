package handlers

import (
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/naibabiji/wp-panel/executor"
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

func (h *SoftwareHandler) List(c *gin.Context) {
	items := []softwareItem{
		getPHPInfo(),
		getNginxInfo(),
		getMariaDBInfo(),
		getRedisInfo(),
	}
	for i := range items {
		populateConfigValues(&items[i])
	}
	c.JSON(http.StatusOK, models.SuccessResponse(items))
}

var configDefaults = map[string]string{
	"memory_limit":            "128M",
	"upload_max_filesize":     "2M",
	"post_max_size":           "8M",
	"max_execution_time":      "30",
	"max_input_vars":          "1000",
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
	name := c.Query("name")
	path, ok := softwareLogPaths[name]
	if !ok {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("未知软件"))
		return
	}
	lines := 200
	if n, err := strconv.Atoi(c.DefaultQuery("lines", "200")); err == nil && n > 0 && n <= 500 {
		lines = n
	}
	content := tailFile(path, lines)
	if content == "" {
		c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"content": "（日志文件为空或不可读）"}))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"content": content}))
}

func (h *SoftwareHandler) ClearLog(c *gin.Context) {
	name := c.Query("name")
	path, ok := softwareLogPaths[name]
	if !ok {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("未知软件"))
		return
	}
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		log.Printf("清空软件日志失败 name=%s: %v", name, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("清空失败"))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": name + " 日志已清空"}))
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
	var req struct {
		Service string `json:"service"`
		Action  string `json:"action"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}
	if req.Action != "start" && req.Action != "stop" && req.Action != "restart" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("无效操作，仅支持 start/stop/restart"))
		return
	}
	if err := executor.SetServiceState(req.Service, req.Action); err != nil {
		log.Printf("守护操作失败 service=%s action=%s: %v", req.Service, req.Action, err)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("操作失败"))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": req.Service + " " + req.Action + " 成功"}))
}

var softConfigAllowed = map[string]map[string]bool{
	"PHP": {
		"memory_limit": true, "upload_max_filesize": true, "post_max_size": true,
		"max_execution_time": true, "max_input_vars": true,
	},
	"Nginx":   {"client_max_body_size": true},
	"MariaDB": {"innodb_buffer_pool_size": true},
	"Redis":   {"maxmemory": true},
}

func (h *SoftwareHandler) SaveConfig(c *gin.Context) {
	var req struct {
		Name  string `json:"name"`
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	var configPath, serviceName, checkCmd, reloadCmd string

	switch req.Name {
	case "PHP":
		configPath = "/etc/php/8.3/fpm/php.ini"
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
		c.JSON(http.StatusBadRequest, models.ErrorResponse("未知软件"))
		return
	}

	// Validate key against per-service allowlist
	if allowed, ok := softConfigAllowed[req.Name]; !ok || !allowed[req.Key] {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("不支持的配置项: " + req.Key))
		return
	}
	// Reject value containing newlines or directive-terminating characters
	if hasLineBreak(req.Value) {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("配置值不能包含换行"))
		return
	}
	if req.Name == "Nginx" && strings.Contains(req.Value, ";") {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("Nginx 配置值不能包含分号"))
		return
	}

	// Ensure config file exists (for conf.d files created by baseline)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if req.Name == "Nginx" {
			os.WriteFile(configPath, []byte("# WP Panel\n"), 0644)
		} else if req.Name == "MariaDB" {
			os.WriteFile(configPath, []byte("# WP Panel\n[mysqld]\n"), 0644)
		}
	}

	// Read config file
	data, err := os.ReadFile(configPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("读取配置文件失败"))
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
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("写入配置文件失败"))
			return
		}
	} else if oldValue == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("未找到配置项: "+req.Key))
		return
	}

	// Syntax check
	if checkCmd != "" {
		out, err := exec.Command("bash", "-c", checkCmd).CombinedOutput()
		if err != nil {
			os.WriteFile(configPath, data, 0644)
			c.JSON(http.StatusInternalServerError, models.ErrorResponse("语法检查失败，已回滚:\n"+string(out)))
			return
		}
	}

	// Reload
	exec.Command("bash", "-c", reloadCmd).Run()

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "配置已更新，" + serviceName + " 已重载"}))
}

func getPHPInfo() softwareItem {
	ver := runCmd("php -v 2>/dev/null | head -1 | awk '{print $2}'")
	extCount := runCmd("php -m 2>/dev/null | wc -l")
	return softwareItem{
		Name:       "PHP",
		Version:    strings.TrimSpace(ver),
		Status:     "已安装 " + strings.TrimSpace(extCount) + " 个扩展",
		ConfigPath: "/etc/php/8.3/fpm/php.ini",
		Configs: []softwareConfig{
			{Key: "memory_limit", Label: "memory_limit — PHP 内存限制", Hint: "单个 PHP 进程最大内存。简单博客 128M，多插件站 256M，WooCommerce/Elementor 512M"},
			{Key: "upload_max_filesize", Label: "upload_max_filesize — 上传大小上限", Hint: "主题/插件/媒体上传限制。需与 Nginx client_max_body_size 一致"},
			{Key: "post_max_size", Label: "post_max_size — POST 数据上限", Hint: "应 ≥ upload_max_filesize，否则大文件上传会被 POST 限制拦截"},
			{Key: "max_execution_time", Label: "max_execution_time — 最大执行时间(秒)", Hint: "PHP 脚本最长运行时间。导入演示数据/批量处理建议 300+"},
			{Key: "max_input_vars", Label: "max_input_vars — 最大输入变量数", Hint: "菜单数量多或使用 Elementor/Divi 建议 2000+，大型站点 5000"},
		},
	}
}

func getNginxInfo() softwareItem {
	ver := runCmd("nginx -v 2>&1 | awk -F/ '{print $2}'")
	return softwareItem{
		Name:       "Nginx",
		Version:    strings.TrimSpace(ver),
		Status:     "已安装",
		ConfigPath: "/etc/nginx/conf.d/wppanel.conf",
		Configs: []softwareConfig{
			{Key: "client_max_body_size", Label: "client_max_body_size — 请求体大小上限", Hint: "需与 PHP upload_max_filesize 一致。导入大型主题或备份时调大"},
		},
	}
}

func getMariaDBInfo() softwareItem {
	ver := runCmd("mariadb --version 2>/dev/null | awk '{print $3}' | cut -d, -f1")
	return softwareItem{
		Name:       "MariaDB",
		Version:    strings.TrimSpace(ver),
		Status:     "已安装",
		ConfigPath: "/etc/mysql/mariadb.conf.d/99-wppanel.cnf",
		Configs: []softwareConfig{
			{Key: "innodb_buffer_pool_size", Label: "innodb_buffer_pool_size — InnoDB 缓冲池", Hint: "保守建议物理内存的 10%~25%。1G 设 128M，2G 设 256M，4G 设 512M，8G+ 设 1G+"},
		},
	}
}

func getRedisInfo() softwareItem {
	ver := runCmd("redis-server --version 2>/dev/null | awk '{print $3}' | cut -d= -f2")
	status := "运行中"
	if runCmd("systemctl is-active redis-server 2>/dev/null") != "active" {
		status = "未运行"
	}
	return softwareItem{
		Name:       "Redis",
		Version:    strings.TrimSpace(ver),
		Status:     status,
		ConfigPath: "/etc/redis/redis.conf",
		Configs: []softwareConfig{
			{Key: "maxmemory", Label: "maxmemory — 最大内存", Hint: "Redis 对象缓存上限。WordPress 单站 128mb，多站或高流量 256mb+"},
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

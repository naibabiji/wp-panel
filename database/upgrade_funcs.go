package database

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

// migratePluginConfigs 将 wp-panel-config.json 从 Web 目录内迁移到 Web 目录外，
// 并轮换已暴露的 plugin_api_key。
func migratePluginConfigs() error {
	rows, err := DB.Query("SELECT domain, web_root, system_user FROM websites WHERE plugin_api_key IS NOT NULL AND plugin_api_key != ''")
	if err != nil {
		return fmt.Errorf("查询网站列表失败: %w", err)
	}
	defer rows.Close()

	baseSecretsDir := "/var/wp-panel/site-secrets"
	os.MkdirAll(baseSecretsDir, 0711)
	os.Chmod(baseSecretsDir, 0711)

	for rows.Next() {
		var domain, webRoot, systemUser string
		if err := rows.Scan(&domain, &webRoot, &systemUser); err != nil {
			log.Printf("[迁移] 读取网站数据失败: %v", err)
			continue
		}

		oldPath := filepath.Join(webRoot, "wp-content", "plugins", "wp-panel-optimizer", "wp-panel-config.json")
		oldData, err := os.ReadFile(oldPath)
		if err != nil {
			continue
		}

		var oldCfg map[string]string
		if err := json.Unmarshal(oldData, &oldCfg); err != nil || oldCfg["panel_url"] == "" {
			log.Printf("[迁移] %s: 旧配置文件无效，直接删除", domain)
			os.Remove(oldPath)
			continue
		}

		// 轮换 API Key
		b := make([]byte, 16)
		rand.Read(b)
		newKey := hex.EncodeToString(b)

		// 写入新路径
		secretsDir := filepath.Join(baseSecretsDir, domain)
		os.MkdirAll(secretsDir, 0700)
		newCfg, _ := json.Marshal(map[string]string{
			"panel_url": oldCfg["panel_url"],
			"api_key":   newKey,
		})
		cfgPath := filepath.Join(secretsDir, "wp-panel-config.json")
		if err := os.WriteFile(cfgPath, newCfg, 0600); err != nil {
			log.Printf("[迁移] %s: 写入新配置文件失败: %v", domain, err)
			continue
		}

		// 更新数据库
		if _, err := DB.Exec("UPDATE websites SET plugin_api_key = ? WHERE domain = ?", newKey, domain); err != nil {
			log.Printf("[迁移] %s: 更新 API Key 失败: %v，清理已写入配置", domain, err)
			os.Remove(cfgPath)
			continue
		}

		// 同步最新插件 PHP 到站点，确保插件从新路径读取配置
		srcPlugin := "/www/server/panel/packages/wp-panel-optimizer.php"
		dstPlugin := filepath.Join(webRoot, "wp-content", "plugins", "wp-panel-optimizer", "wp-panel-optimizer.php")
		srcData, err := os.ReadFile(srcPlugin)
		if err != nil {
			log.Printf("[迁移] %s: 读取插件包失败: %v", domain, err)
			continue
		}
		if err := os.WriteFile(dstPlugin, srcData, 0644); err != nil {
			log.Printf("[迁移] %s: 更新站点插件失败: %v", domain, err)
			continue
		}

		// 以上全部成功后才删除旧配置文件
		os.Remove(oldPath)
		exec.Command("chown", "-R", systemUser+":"+systemUser, secretsDir).Run()

		log.Printf("[迁移] %s: 配置文件已迁移到 %s", domain, secretsDir)
	}

	return nil
}

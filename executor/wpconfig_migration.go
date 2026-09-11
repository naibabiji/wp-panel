package executor

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/naibabiji/wp-panel/database"
)

func init() {
	database.RegisterUpgrade("1.0.10", BackfillWPConfigCacheKeySalts)
}

func BackfillWPConfigCacheKeySalts() error {
	if database.DB == nil {
		return fmt.Errorf("database is not initialized")
	}

	rows, err := database.DB.Query(`
		SELECT id, domain, web_root
		FROM websites
		WHERE site_type = 'wordpress'
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var siteID int
		var domain, webRoot string
		if err := rows.Scan(&siteID, &domain, &webRoot); err != nil {
			return err
		}

		configPath := filepath.Join(webRoot, "wp-config.php")
		data, err := os.ReadFile(configPath)
		if err != nil {
			log.Printf("[upgrade] skip cache prefixes for %s: read wp-config.php failed: %v", domain, err)
			continue
		}

		updated, inserted := ensureWPConfigCachePrefixes(string(data), wpCacheKeySalt(domain))
		if !inserted || updated == string(data) {
			continue
		}
		if err := os.WriteFile(configPath, []byte(updated), 0600); err != nil {
			log.Printf("[upgrade] skip cache prefixes for %s: write wp-config.php failed: %v", domain, err)
			continue
		}
		log.Printf("[upgrade] added cache prefixes for %s", domain)
		refreshWPCodeIntegrityBaselineBestEffort(siteID, "wp-config 升级迁移成功")
	}

	return rows.Err()
}

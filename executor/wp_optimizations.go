package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var managedConstants = map[string]string{
	"AUTOMATIC_UPDATER_DISABLED": "define('AUTOMATIC_UPDATER_DISABLED', true);",
	"DISALLOW_FILE_EDIT":         "define('DISALLOW_FILE_EDIT', true);",
}

func ApplyWPOptimizations(webRoot string, disableUpdates, disableFileEditing bool) error {
	configPath := filepath.Join(webRoot, "wp-config.php")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}

	wanted := map[string]bool{
		"AUTOMATIC_UPDATER_DISABLED": disableUpdates,
		"DISALLOW_FILE_EDIT":         disableFileEditing,
	}

	content := string(data)

	for name, enable := range wanted {
		re := regexp.MustCompile(`(?m)^\s*define\s*\(\s*'` + regexp.QuoteMeta(name) + `'\s*,[^)]+\)\s*;\s*\n?`)
		has := re.MatchString(content)

		if enable && !has {
			marker := "/* That's all, stop editing!"
			idx := strings.Index(content, marker)
			if idx < 0 {
				marker = "require_once ABSPATH . 'wp-settings.php';"
				idx = strings.Index(content, marker)
			}
			if idx > 0 {
				insertion := managedConstants[name] + "\n"
				content = content[:idx] + insertion + content[idx:]
			} else {
				return fmt.Errorf("无法在 wp-config.php 中找到插入位置")
			}
		} else if !enable && has {
			content = re.ReplaceAllString(content, "")
		}
	}

	return os.WriteFile(configPath, []byte(content), 0644)
}

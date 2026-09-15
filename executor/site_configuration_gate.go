package executor

import (
	"database/sql"

	"github.com/naibabiji/wp-panel/database"
)

// rejectPausedSiteConfiguration keeps the narrowly scoped configuration
// operations that can recreate an Nginx enabled link closed for paused sites.
func rejectPausedSiteConfiguration(siteID int) *TaskResult {
	var status string
	if err := database.GetDB().QueryRow(`SELECT status FROM websites WHERE id=?`, siteID).Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			return &TaskResult{Success: false, Message: "网站不存在"}
		}
		return &TaskResult{Success: false, Message: "检查网站状态失败"}
	}
	if status == "paused" {
		return &TaskResult{Success: false, Message: "网站已暂停，请先启用网站再修改此设置"}
	}
	return nil
}

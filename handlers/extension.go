package handlers

import (
	"net/http"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

type ExtensionHandler struct{}

func (h *ExtensionHandler) List(c *gin.Context) {
	db := database.GetDB()
	rows, err := db.Query("SELECT id, etype, slug, name, enabled FROM wp_extension_config ORDER BY etype, id")
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse("查询失败"))
		return
	}
	defer rows.Close()

	var extensions []models.WPExtension
	for rows.Next() {
		var e models.WPExtension
		var enabled int
		if rows.Scan(&e.ID, &e.EType, &e.Slug, &e.Name, &enabled) != nil {
			continue
		}
		e.Enabled = enabled == 1
		extensions = append(extensions, e)
	}
	if extensions == nil {
		extensions = []models.WPExtension{}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(extensions))
}

func (h *ExtensionHandler) Save(c *gin.Context) {
	var req []models.WPExtension
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse("参数错误"))
		return
	}

	db := database.GetDB()
	for _, e := range req {
		if e.EType != "theme" && e.EType != "plugin" {
			continue
		}
		enabled := 0
		if e.Enabled {
			enabled = 1
		}
		if e.ID > 0 {
			db.Exec("UPDATE wp_extension_config SET slug = ?, name = ?, enabled = ? WHERE id = ?",
				e.Slug, e.Name, enabled, e.ID)
		} else if e.Slug != "" && e.Name != "" {
			db.Exec("INSERT OR IGNORE INTO wp_extension_config (etype, slug, name, enabled) VALUES (?, ?, ?, ?)",
				e.EType, e.Slug, e.Name, enabled)
		}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "已保存"}))
}

func (h *ExtensionHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	db := database.GetDB()
	db.Exec("DELETE FROM wp_extension_config WHERE id = ?", id)
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "已删除"}))
}

func (h *ExtensionHandler) Reset(c *gin.Context) {
	db := database.GetDB()
	db.Exec("DELETE FROM wp_extension_config")
	db.Exec(`INSERT OR IGNORE INTO wp_extension_config (etype, slug, name, enabled) VALUES
		('theme',  'hello-elementor',   'Hello Elementor',  1),
		('theme',  'astra',             'Astra',            1),
		('theme',  'kadence',           'Kadence',          1),
		('theme',  'blocksy',           'Blocksy',          1),
		('plugin', 'elementor',         'Elementor',        1),
		('plugin', 'wordpress-seo',     'Yoast SEO',        1),
		('plugin', 'seo-by-rank-math',  'Rank Math SEO',    1),
		('plugin', 'woocommerce',       'WooCommerce',      1),
		('plugin', 'naibabiji-b2b-product-showcase', 'B2B Product Catalog', 1),
		('plugin', 'redis-cache',          'Redis Cache',      1)`)
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": "已恢复默认配置"}))
}

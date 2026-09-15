package handlers

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"github.com/naibabiji/wp-panel/database"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"

	"github.com/gin-gonic/gin"
)

type ExtensionHandler struct{}

func (h *ExtensionHandler) List(c *gin.Context) {
	db := database.GetDB()
	rows, err := db.Query("SELECT id, etype, slug, name, enabled FROM wp_extension_config ORDER BY etype, id")
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "extension.query_failed")))
		return
	}
	defer rows.Close()

	var extensions []models.WPExtension
	for rows.Next() {
		var e models.WPExtension
		var enabled int
		if err := rows.Scan(&e.ID, &e.EType, &e.Slug, &e.Name, &enabled); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "extension.query_failed")))
			return
		}
		e.Enabled = enabled == 1
		extensions = append(extensions, e)
	}
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "extension.query_failed")))
		return
	}
	if extensions == nil {
		extensions = []models.WPExtension{}
	}

	c.JSON(http.StatusOK, models.SuccessResponse(extensions))
}

func (h *ExtensionHandler) Save(c *gin.Context) {
	var req []models.WPExtension
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "common.invalid_params")))
		return
	}

	db := database.GetDB()
	tx, err := db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "extension.save_failed")))
		return
	}
	defer tx.Rollback()
	seen := make(map[string]bool)
	for _, e := range req {
		e.Slug = strings.TrimSpace(e.Slug)
		e.Name = strings.TrimSpace(e.Name)
		if e.EType != "theme" && e.EType != "plugin" {
			c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "extension.invalid_entry")))
			return
		}
		if e.Slug == "" || e.Name == "" || seen[e.EType+"\x00"+e.Slug] {
			c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "extension.invalid_entry")))
			return
		}
		seen[e.EType+"\x00"+e.Slug] = true
		enabled := 0
		if e.Enabled {
			enabled = 1
		}
		if e.ID > 0 {
			result, err := tx.Exec("UPDATE wp_extension_config SET etype = ?, slug = ?, name = ?, enabled = ? WHERE id = ?",
				e.EType,
				e.Slug, e.Name, enabled, e.ID)
			if err != nil || !oneRowAffected(result) {
				c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "extension.save_failed")))
				return
			}
		} else {
			if _, err := tx.Exec("INSERT INTO wp_extension_config (etype, slug, name, enabled) VALUES (?, ?, ?, ?)",
				e.EType, e.Slug, e.Name, enabled); err != nil {
				c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "extension.save_failed")))
				return
			}
		}
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "extension.save_failed")))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": i18n.TE(c.Request, "extension.saved")}))
}

func (h *ExtensionHandler) Delete(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "common.invalid_params")))
		return
	}
	db := database.GetDB()
	result, err := db.Exec("DELETE FROM wp_extension_config WHERE id = ?", id)
	if err != nil || !oneRowAffected(result) {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "extension.delete_failed")))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": i18n.TE(c.Request, "extension.deleted")}))
}

func (h *ExtensionHandler) Reset(c *gin.Context) {
	db := database.GetDB()
	tx, err := db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "extension.reset_failed")))
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM wp_extension_config"); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "extension.reset_failed")))
		return
	}
	if _, err := tx.Exec(`INSERT INTO wp_extension_config (etype, slug, name, enabled) VALUES
		('theme',  'hello-elementor',   'Hello Elementor',  1),
		('theme',  'astra',             'Astra',            1),
		('theme',  'kadence',           'Kadence',          1),
		('theme',  'blocksy',           'Blocksy',          1),
		('plugin', 'elementor',         'Elementor',        1),
		('plugin', 'wordpress-seo',     'Yoast SEO',        1),
		('plugin', 'seo-by-rank-math',  'Rank Math SEO',    1),
		('plugin', 'woocommerce',       'WooCommerce',      1),
		('plugin', 'naibabiji-b2b-product-showcase', 'B2B Product Catalog', 1),
		('plugin', 'redis-cache',          'Redis Cache',      1)`); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "extension.reset_failed")))
		return
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "extension.reset_failed")))
		return
	}
	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{"message": i18n.TE(c.Request, "extension.restored_default")}))
}

func oneRowAffected(result sql.Result) bool {
	if result == nil {
		return false
	}
	rows, err := result.RowsAffected()
	return err == nil && rows == 1
}

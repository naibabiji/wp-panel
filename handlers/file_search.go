package handlers

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/i18n"
	"github.com/naibabiji/wp-panel/models"
)

const (
	maxFileSearchQueryLength = 256
	maxFileSearchScanEntries = 100000
	maxFileSearchMatches     = 10000
	fileSearchTimeout        = 10 * time.Second
)

var fileSearchSlots = make(chan struct{}, 2)

type fileSearchEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	ParentPath string `json:"parent_path"`
	IsDir      bool   `json:"is_dir"`
	Size       int64  `json:"size"`
	Mode       string `json:"mode"`
	ModTime    string `json:"mod_time"`
}

type fileSearchResult struct {
	Files           []fileSearchEntry
	Total           int
	Page            int
	PerPage         int
	TotalPages      int
	Truncated       bool
	TruncatedReason string
}

type fileSearchLimits struct {
	MaxScanned int
	MaxMatches int
}

func (h *FileHandler) Search(c *gin.Context) {
	siteID, err := strconv.Atoi(c.Query("site_id"))
	if err != nil || siteID < 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "files.search_invalid_site")))
		return
	}
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "files.search_query_required")))
		return
	}
	if len([]rune(query)) > maxFileSearchQueryLength {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "files.search_query_too_long")))
		return
	}
	scope := c.DefaultQuery("scope", "current")
	if scope != "current" && scope != "subtree" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "files.search_scope_invalid")))
		return
	}

	basePath, err := fileBasePath(siteID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse(i18n.TE(c.Request, "website.not_found")))
		return
	}
	relPath := c.DefaultQuery("path", "/")
	fullPath := filepath.Clean(filepath.Join(basePath, relPath))
	if !isPathWithin(basePath, fullPath) {
		c.JSON(http.StatusForbidden, models.ErrorResponse(i18n.TE(c.Request, "files.path_out_of_bounds")))
		return
	}
	containsSymlink, err := pathContainsSymlinkBelowRoot(basePath, fullPath)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "files.search_directory_invalid")))
		return
	}
	if containsSymlink {
		c.JSON(http.StatusForbidden, models.ErrorResponse(i18n.TE(c.Request, "files.search_directory_symlink")))
		return
	}
	info, err := os.Stat(fullPath)
	if err != nil || !info.IsDir() {
		c.JSON(http.StatusBadRequest, models.ErrorResponse(i18n.TE(c.Request, "files.search_directory_invalid")))
		return
	}

	if scope == "subtree" {
		select {
		case fileSearchSlots <- struct{}{}:
			defer func() { <-fileSearchSlots }()
		default:
			c.JSON(http.StatusTooManyRequests, models.ErrorResponse(i18n.TE(c.Request, "files.search_busy")))
			return
		}
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", strconv.Itoa(defaultFilePageSize)))
	page, perPage = normalizeFilePage(page, perPage)
	ctx, cancel := context.WithTimeout(c.Request.Context(), fileSearchTimeout)
	defer cancel()
	result, err := searchFileTree(ctx, basePath, fullPath, query, scope == "subtree", c.DefaultQuery("sort_by", "name"), c.DefaultQuery("sort_dir", "asc"), page, perPage)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			c.JSON(http.StatusGatewayTimeout, models.ErrorResponse(i18n.TE(c.Request, "files.search_timeout")))
			return
		}
		if errors.Is(err, context.Canceled) {
			return
		}
		c.JSON(http.StatusInternalServerError, models.ErrorResponse(i18n.TE(c.Request, "files.search_failed")))
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse(gin.H{
		"path":             relPath,
		"query":            query,
		"scope":            scope,
		"files":            result.Files,
		"total":            result.Total,
		"page":             result.Page,
		"per_page":         result.PerPage,
		"total_pages":      result.TotalPages,
		"truncated":        result.Truncated,
		"truncated_reason": result.TruncatedReason,
	}))
}

func searchFileTree(ctx context.Context, basePath, searchPath, query string, recursive bool, sortBy, sortDir string, page, perPage int) (fileSearchResult, error) {
	return searchFileTreeWithLimits(ctx, basePath, searchPath, query, recursive, sortBy, sortDir, page, perPage, fileSearchLimits{
		MaxScanned: maxFileSearchScanEntries,
		MaxMatches: maxFileSearchMatches,
	})
}

func searchFileTreeWithLimits(ctx context.Context, basePath, searchPath, query string, recursive bool, sortBy, sortDir string, page, perPage int, limits fileSearchLimits) (fileSearchResult, error) {
	result := fileSearchResult{Files: []fileSearchEntry{}, Page: page, PerPage: perPage, TotalPages: 1}
	query = strings.ToLower(query)
	dirs := []string{searchPath}
	scanned := 0
	matches := make([]fileSearchEntry, 0)

	for len(dirs) > 0 {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		dir := dirs[len(dirs)-1]
		dirs = dirs[:len(dirs)-1]
		entries, err := os.ReadDir(dir)
		if err != nil {
			return result, err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			scanned++
			if scanned > limits.MaxScanned {
				result.Truncated = true
				result.TruncatedReason = "scan_limit"
				break
			}

			fullPath := filepath.Join(dir, entry.Name())
			isSymlink := entry.Type()&os.ModeSymlink != 0
			if recursive && entry.IsDir() && !isSymlink {
				dirs = append(dirs, fullPath)
			}
			if !strings.Contains(strings.ToLower(entry.Name()), query) {
				continue
			}
			if len(matches) >= limits.MaxMatches {
				result.Truncated = true
				result.TruncatedReason = "match_limit"
				break
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			rel, err := filepath.Rel(basePath, fullPath)
			if err != nil {
				return result, err
			}
			parentRel, err := filepath.Rel(basePath, dir)
			if err != nil {
				return result, err
			}
			matches = append(matches, fileSearchEntry{
				Name:       entry.Name(),
				Path:       slashRelativePath(rel),
				ParentPath: slashRelativePath(parentRel),
				IsDir:      entry.IsDir(),
				Size:       info.Size(),
				Mode:       info.Mode().String(),
				ModTime:    info.ModTime().Format("2006-01-02 15:04:05"),
			})
		}
		if result.Truncated || !recursive {
			break
		}
	}

	sortFileSearchEntries(matches, sortBy, sortDir)
	result.Total = len(matches)
	result.Page, result.PerPage = normalizeFilePage(page, perPage)
	result.TotalPages = (result.Total + result.PerPage - 1) / result.PerPage
	if result.TotalPages == 0 {
		result.TotalPages = 1
	}
	if result.Page > result.TotalPages {
		result.Page = result.TotalPages
	}
	start := (result.Page - 1) * result.PerPage
	end := start + result.PerPage
	if end > result.Total {
		end = result.Total
	}
	if start < result.Total {
		result.Files = matches[start:end]
	}
	return result, nil
}

func slashRelativePath(rel string) string {
	if rel == "." || rel == "" {
		return "/"
	}
	return "/" + strings.TrimPrefix(filepath.ToSlash(rel), "/")
}

func sortFileSearchEntries(files []fileSearchEntry, sortBy, sortDir string) {
	if sortDir != "desc" {
		sortDir = "asc"
	}
	switch sortBy {
	case "type", "size", "time":
	default:
		sortBy = "name"
	}
	direction := 1
	if sortDir == "desc" {
		direction = -1
	}
	sort.SliceStable(files, func(i, j int) bool {
		a, b := files[i], files[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		cmp := 0
		switch sortBy {
		case "type":
			cmp = strings.Compare(fileEntryType(fileEntry{Name: a.Name, IsDir: a.IsDir}), fileEntryType(fileEntry{Name: b.Name, IsDir: b.IsDir}))
		case "size":
			if a.Size < b.Size {
				cmp = -1
			} else if a.Size > b.Size {
				cmp = 1
			}
		case "time":
			cmp = strings.Compare(a.ModTime, b.ModTime)
		default:
			cmp = strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		}
		if cmp == 0 {
			cmp = strings.Compare(strings.ToLower(a.Path), strings.ToLower(b.Path))
		}
		return direction*cmp < 0
	})
}

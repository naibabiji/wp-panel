package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/naibabiji/wp-panel/executor"
	"github.com/naibabiji/wp-panel/models"
)

type PortableImportHandler struct {
	Service *executor.PortableImports
}

func (h PortableImportHandler) List(c *gin.Context) {
	tasks, err := h.Service.List()
	h.respond(c, tasks, err)
}

func (h PortableImportHandler) AliasSuggestion(c *gin.Context) {
	h.respond(c, gin.H{"alias": executor.PortableAliasSuggestion(c.Query("domain"))}, nil)
}

func (h PortableImportHandler) Detail(c *gin.Context) {
	if c.Query("log") == "php" {
		log, err := h.Service.PHPLog(c.Param("id"))
		h.respond(c, log, err)
		return
	}
	task, err := h.Service.Detail(c.Param("id"))
	h.respond(c, task, err)
}

func (h PortableImportHandler) Begin(c *gin.Context) {
	h.receiveManifest(c, false)
}

func (h PortableImportHandler) Retry(c *gin.Context) {
	h.receiveManifest(c, true)
}

func (h PortableImportHandler) receiveManifest(c *gin.Context, retry bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, executor.PortableManifestLimit)
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		h.respond(c, nil, errors.New("无法读取备份清单"))
		return
	}
	var size int64
	if value := c.Query("bundle_size"); value != "" {
		size, err = strconv.ParseInt(value, 10, 64)
		if err != nil || size <= 0 {
			h.respond(c, nil, errors.New("整套备份大小无效"))
			return
		}
	}
	if retry {
		task, err := h.Service.PrepareRetry(c.Param("id"), raw, size)
		h.respond(c, task, err)
		return
	}
	task, err := h.Service.Begin(raw, size)
	h.respond(c, task, err)
}

func (h PortableImportHandler) Upload(c *gin.Context) {
	offset, err := strconv.ParseInt(c.Query("offset"), 10, 64)
	if err != nil {
		h.respond(c, nil, errors.New("上传位置无效"))
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, executor.PortableChunkSize)
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		h.respond(c, nil, errors.New("分片无法读取，请补传"))
		return
	}
	received, err := h.Service.Upload(c.Param("id"), c.Param("name"), offset, data)
	h.respond(c, gin.H{"offset": received}, err)
}

type portableRetryRequest struct {
	Aliases      []string `json:"aliases"`
	CleanupToken string   `json:"cleanup_token"`
}

func readPortableRetryRequest(c *gin.Context) (portableRetryRequest, error) {
	var input portableRetryRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	decoder := json.NewDecoder(c.Request.Body)
	if err := decoder.Decode(&input); err != io.EOF {
		if err != nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
			return input, errors.New("无法读取恢复设置")
		}
	}
	return input, nil
}

func (h PortableImportHandler) SaveUploadAliases(c *gin.Context) {
	input, err := readPortableRetryRequest(c)
	if err != nil {
		h.respond(c, nil, err)
		return
	}
	aliases, err := h.Service.SaveUploadAliases(c.Param("id"), input.Aliases)
	h.respond(c, gin.H{"aliases": aliases}, err)
}

func (h PortableImportHandler) RetryPlan(c *gin.Context) {
	input, err := readPortableRetryRequest(c)
	if err != nil {
		h.respond(c, nil, err)
		return
	}
	plan, err := h.Service.RetryPlan(c.Param("id"), input.Aliases)
	h.respond(c, plan, err)
}

func (h PortableImportHandler) Submit(c *gin.Context) {
	input, err := readPortableRetryRequest(c)
	if err != nil {
		h.respond(c, nil, err)
		return
	}
	h.respond(c, nil, h.Service.Submit(c.Param("id"), input.Aliases, input.CleanupToken))
}

func (h PortableImportHandler) DeleteTask(c *gin.Context) {
	h.respond(c, nil, h.Service.DeleteTask(c.Param("id")))
}

func (PortableImportHandler) respond(c *gin.Context, data any, err error) {
	c.Header("Cache-Control", "no-store")
	if err == nil {
		c.JSON(http.StatusOK, models.SuccessResponse(data))
		return
	}
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, executor.ErrPortableMissing):
		status = http.StatusNotFound
	case errors.Is(err, executor.ErrPortableBusy), errors.Is(err, executor.ErrPortableState), errors.Is(err, executor.ErrPortableRetryUpload):
		status = http.StatusConflict
	}
	c.JSON(status, models.ErrorResponse(err.Error()))
}

package handlers

import (
	"fmt"
	"net/http"
	"os"
	"strconv"

	"void2md/internal/service"

	"github.com/gin-gonic/gin"
)

// getMaxUploadSize returns the maximum upload size in bytes (default 100MB)
func getMaxUploadSize() int {
	if v := os.Getenv("VOID2MD_MAX_UPLOAD_SIZE_MB"); v != "" {
		if mb, err := strconv.Atoi(v); err == nil && mb > 0 {
			return mb * 1024 * 1024
		}
	}
	// Default to 100MB like file-parse service
	return 100 * 1024 * 1024
}

// ChunkRequest chunk 请求体
type ChunkRequest struct {
	Markdown   string         `json:"markdown" binding:"required"`
	ChunkSizes map[string]int `json:"chunk_sizes"`
}

// ChunkHandler handles the /v1/chunk endpoint (matching file-parse implementation exactly)
func ChunkHandler(c *gin.Context) {
	maxUploadSize := getMaxUploadSize()

	// Large JSON request body defense: reject requests larger than upload limit
	// ContentLength of -1 (chunked transfer) skips pre-check, handled by ShouldBindJSON
	if c.Request.ContentLength > int64(maxUploadSize) {
		c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": fmt.Sprintf("request body too large (max %dMB)", maxUploadSize/1024/1024),
		})
		return
	}

	// Parse JSON request body
	var req ChunkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("invalid request body: %v", err),
		})
		return
	}

	// Call chunking service
	result, err := service.ChunkMarkdown(req.Markdown, req.ChunkSizes)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
			"error": err.Error(),
		})
		return
	}

	// Use JSONNoEscape: disable HTML escaping to avoid response bloat for large HTML tables
	// Matches file-parse service behavior exactly
	JSONNoEscape(c, http.StatusOK, gin.H{
		"chunks": result.Chunks,
		"count":  result.Count,
	})
}

package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"

	"void2md/file/pkg/service"
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

// ChunkRequest is the chunk request body.
type ChunkRequest struct {
	Markdown   string         `json:"markdown"`
	ChunkSizes map[string]int `json:"chunk_sizes"`
}

// HandleChunk handles the /v1/chunk endpoint (matching file-parse implementation exactly)
func (s *Service) HandleChunk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	maxUploadSize := s.maxUploadSize

	// Large JSON request body defense: reject requests larger than upload limit
	// ContentLength of -1 (chunked transfer) skips pre-check, handled by JSON decode
	if r.ContentLength > int64(maxUploadSize) {
		JSONNoEscape(w, http.StatusRequestEntityTooLarge, map[string]interface{}{
			"error": fmt.Sprintf("request body too large (max %dMB)", maxUploadSize/1024/1024),
		})
		return
	}

	// Parse JSON request body
	var req ChunkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		JSONNoEscape(w, http.StatusBadRequest, map[string]interface{}{
			"error": fmt.Sprintf("invalid request body: %v", err),
		})
		return
	}

	if req.Markdown == "" {
		JSONNoEscape(w, http.StatusBadRequest, map[string]interface{}{
			"error": "markdown is required",
		})
		return
	}

	// Call chunking service
	result, err := service.ChunkMarkdown(req.Markdown, req.ChunkSizes)
	if err != nil {
		JSONNoEscape(w, http.StatusInternalServerError, map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	// Use JSONNoEscape: disable HTML escaping to avoid response bloat for large HTML tables
	// Matches file-parse service behavior exactly
	JSONNoEscape(w, http.StatusOK, map[string]interface{}{
		"chunks": result.Chunks,
		"count":  result.Count,
	})
}

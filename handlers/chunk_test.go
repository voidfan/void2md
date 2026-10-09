package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestChunkHandler(t *testing.T) {
	// Create a new router
	router := gin.New()
	router.POST("/v1/chunk", ChunkHandler)

	// Test valid request
	markdown := "# Test\n\nThis is a test paragraph."
	reqBody := map[string]interface{}{
		"markdown": markdown,
	}
	jsonBody, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/v1/chunk", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	if err != nil {
		t.Fatalf("Failed to parse response JSON: %v", err)
	}

	chunks, ok := response["chunks"].([]interface{})
	if !ok {
		t.Error("Expected 'chunks' field to be an array")
	}

	count, ok := response["count"].(float64)
	if !ok {
		t.Error("Expected 'count' field to be a number")
	}

	if len(chunks) == 0 {
		t.Error("Expected at least one chunk")
	}

	if int(count) != len(chunks) {
		t.Errorf("Expected count %d to match chunks length %d", int(count), len(chunks))
	}
}

func TestChunkHandlerInvalidJSON(t *testing.T) {
	router := gin.New()
	router.POST("/v1/chunk", ChunkHandler)

	req := httptest.NewRequest("POST", "/v1/chunk", bytes.NewBufferString("invalid json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}
}

func TestChunkHandlerMissingMarkdown(t *testing.T) {
	router := gin.New()
	router.POST("/v1/chunk", ChunkHandler)

	reqBody := map[string]interface{}{
		"chunk_sizes": map[string]int{"text": 100},
	}
	jsonBody, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/v1/chunk", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}
}
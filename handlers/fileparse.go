package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"void2md/rust"

	"github.com/gin-gonic/gin"
)

// getMaxUploadSize returns the maximum upload size in bytes (default 32MB for fileparse)
func getFileParseMaxUploadSize() int {
	if v := os.Getenv("VOID2MD_MAX_UPLOAD_SIZE_MB"); v != "" {
		if mb, err := strconv.Atoi(v); err == nil && mb > 0 {
			return mb * 1024 * 1024
		}
	}
	// Default to 32MB like original void2md implementation
	return 32 * 1024 * 1024
}

// FileParseResponse represents the API response structure (compatible with file-parse v1/pdfparse)
type FileParseResponse struct {
	Pages    int    `json:"pages"`
	Markdown string `json:"markdown"`
	Error    string `json:"error"`
}

// FileParseHandler handles the /v1/fileparse endpoint (updated to match v1/pdfparse behavior exactly)
func FileParseHandler(c *gin.Context) {
	maxUploadSize := getFileParseMaxUploadSize()

	// Limit multipart form total size
	if err := c.Request.ParseMultipartForm(int64(maxUploadSize)); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, FileParseResponse{
			Pages: 0,
			Error: fmt.Sprintf("file too large (max %dMB) or invalid format: %s", maxUploadSize/1024/1024, err.Error()),
		})
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, FileParseResponse{
			Pages: 0,
			Error: "missing file",
		})
		return
	}

	// Open the uploaded file
	src, err := fileHeader.Open()
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, FileParseResponse{
			Pages: 0,
			Error: "failed to open uploaded file",
		})
		return
	}
	defer src.Close()

	// Read file content
	fileBytes, err := io.ReadAll(src)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, FileParseResponse{
			Pages: 0,
			Error: "failed to read file",
		})
		return
	}

	// Check actual file size after reading
	if len(fileBytes) > maxUploadSize {
		c.AbortWithStatusJSON(http.StatusBadRequest, FileParseResponse{
			Pages: 0,
			Error: fmt.Sprintf("file too large (max %dMB)", maxUploadSize/1024/1024),
		})
		return
	}

	filename := fileHeader.Filename

	// Build configuration JSON matching file-parse logic
	configMap := make(map[string]interface{})

	// Form parameters (highest priority)
	formFields := map[string]string{
		"image_analysis":     c.PostForm("image_analysis"),
		"chart_analysis":     c.PostForm("chart_analysis"),
		"seal_rec":           c.PostForm("seal_rec"),
		"keep_header_footer": c.PostForm("keep_header_footer"),
		"keep_appendix":      c.PostForm("keep_appendix"),
		"relevel_titles":     c.PostForm("relevel_titles"),
	}

	// Convert form fields to config map with proper types
	for key, value := range formFields {
		if value != "" {
			// Convert to boolean for boolean parameters
			if boolVal, err := parseBool(value, getDefaultValue(key)); err == nil {
				configMap[key] = boolVal
			}
		}
	}

	// ParseConfig JSON (second priority)
	parseConfigJSON := c.PostForm("parseConfig")
	if parseConfigJSON != "" {
		var parseConfigMap map[string]interface{}
		if err := json.Unmarshal([]byte(parseConfigJSON), &parseConfigMap); err == nil {
			// Merge parseConfig into configMap (form fields take precedence)
			for key, value := range parseConfigMap {
				if _, exists := configMap[key]; !exists {
					configMap[key] = value
				}
			}
		}
	}

	// Marshal final config to JSON
	configJSON, _ := json.Marshal(configMap)
	configJSONStr := string(configJSON)

	// Use image_analysis from config for embedImages
	embedImages := false
	if val, ok := configMap["image_analysis"]; ok {
		if boolVal, ok := val.(bool); ok {
			embedImages = boolVal
		}
	}

	// Extract metadata from the form file
	mimetype := fileHeader.Header.Get("Content-Type")
	extension := filepath.Ext(filename)

	// Client disconnection detection
	ctx := c.Request.Context()

	type resultWrapper struct {
		result *rust.ConversionResult
		err    error
	}
	ch := make(chan resultWrapper, 1)

	go func() {
		result, err := rust.ConvertBufferToMarkdown(fileBytes, mimetype, extension, filename, embedImages, configJSONStr)
		ch <- resultWrapper{result: result, err: err}
	}()

	select {
	case <-ctx.Done():
		if strings.Contains(ctx.Err().Error(), "canceled") || strings.Contains(ctx.Err().Error(), "deadline") {
			c.AbortWithStatusJSON(http.StatusBadRequest, FileParseResponse{
				Pages: 0,
				Error: "client disconnected",
			})
			return
		}
		c.AbortWithStatusJSON(http.StatusInternalServerError, FileParseResponse{
			Pages: 0,
			Error: "request cancelled",
		})
		return
	case rw := <-ch:
		if rw.err != nil {
			log.Printf("Conversion error: %v", rw.err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, FileParseResponse{
				Pages: 0,
				Error: rw.err.Error(),
			})
			return
		}

		// Return successful response (matching v1/pdfparse structure exactly)
		response := FileParseResponse{
			Pages:    1, // Default to 1 page since Rust backend doesn't provide detailed page info
			Markdown: rw.result.Markdown,
			Error:    "", // Error is separate field, success has empty error
		}

		JSONNoEscape(c, http.StatusOK, response)
	}
}

// getBoolParam extracts a boolean parameter from form data
func getBoolParam(c *gin.Context, paramName string, defaultValue bool) bool {
	paramValue := strings.ToLower(strings.TrimSpace(c.PostForm(paramName)))
	if paramValue == "" {
		return defaultValue
	}

	// Handle various truthy values
	switch paramValue {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		// Try to parse as integer
		if intValue, err := strconv.Atoi(paramValue); err == nil {
			return intValue != 0
		}
		return defaultValue
	}
} // getDefaultValue returns the default value for a given parameter (matching file-parse)
func getDefaultValue(paramName string) bool {
	switch paramName {
	case "keep_header_footer":
		return false
	case "keep_appendix":
		return true
	case "relevel_titles":
		return true
	case "seal_rec":
		return false
	case "chart_analysis":
		return false
	case "image_analysis":
		return true
	default:
		return false
	}
}

// parseBool parses a string to boolean with default fallback
func parseBool(value string, defaultValue bool) (bool, error) {
	paramValue := strings.ToLower(strings.TrimSpace(value))
	if paramValue == "" {
		return defaultValue, nil
	}

	switch paramValue {
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off":
		return false, nil
	default:
		if intValue, err := strconv.Atoi(paramValue); err == nil {
			return intValue != 0, nil
		}
		return defaultValue, fmt.Errorf("invalid boolean value: %s", value)
	}
}

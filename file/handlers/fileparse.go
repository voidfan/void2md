package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"void2md/file/rust"
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

// writeFileParseError writes a FileParseResponse error with the given status code
func writeFileParseError(w http.ResponseWriter, code int, msg string) {
	JSONNoEscape(w, code, FileParseResponse{Pages: 0, Error: msg})
}

// HandleFileParse handles the /v1/fileparse endpoint (updated to match v1/pdfparse behavior exactly)
func (s *Service) HandleFileParse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	maxUploadSize := s.fileParseMaxUploadSize

	// Limit multipart form total size
	if err := r.ParseMultipartForm(int64(maxUploadSize)); err != nil {
		writeFileParseError(w, http.StatusBadRequest,
			fmt.Sprintf("file too large (max %dMB) or invalid format: %s", maxUploadSize/1024/1024, err.Error()))
		return
	}

	// Get the uploaded file (r.FormFile returns the opened file and its header)
	src, fileHeader, err := r.FormFile("file")
	if err != nil {
		writeFileParseError(w, http.StatusBadRequest, "missing file")
		return
	}
	defer src.Close()

	// Read file content
	fileBytes, err := io.ReadAll(src)
	if err != nil {
		writeFileParseError(w, http.StatusInternalServerError, "failed to read file")
		return
	}

	// Check actual file size after reading
	if len(fileBytes) > maxUploadSize {
		writeFileParseError(w, http.StatusBadRequest,
			fmt.Sprintf("file too large (max %dMB)", maxUploadSize/1024/1024))
		return
	}

	filename := fileHeader.Filename

	// Build configuration JSON matching file-parse logic
	configMap := make(map[string]interface{})

	// Form parameters (highest priority)
	formFields := map[string]string{
		"image_analysis":     r.PostFormValue("image_analysis"),
		"chart_analysis":     r.PostFormValue("chart_analysis"),
		"seal_rec":           r.PostFormValue("seal_rec"),
		"keep_header_footer": r.PostFormValue("keep_header_footer"),
		"keep_appendix":      r.PostFormValue("keep_appendix"),
		"relevel_titles":     r.PostFormValue("relevel_titles"),
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
	parseConfigJSON := r.PostFormValue("parseConfig")
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
	ctx := r.Context()

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
			writeFileParseError(w, http.StatusBadRequest, "client disconnected")
			return
		}
		writeFileParseError(w, http.StatusInternalServerError, "request cancelled")
		return
	case rw := <-ch:
		if rw.err != nil {
			s.logger.Error("Conversion error", "error", rw.err)
			writeFileParseError(w, http.StatusInternalServerError, rw.err.Error())
			return
		}

		// Return successful response (matching v1/pdfparse structure exactly)
		response := FileParseResponse{
			Pages:    1, // Default to 1 page since Rust backend doesn't provide detailed page info
			Markdown: rw.result.Markdown,
			Error:    "", // Error is separate field, success has empty error
		}

		JSONNoEscape(w, http.StatusOK, response)
	}
}

// getBoolParam extracts a boolean parameter from form data
func getBoolParam(r *http.Request, paramName string, defaultValue bool) bool {
	paramValue := strings.ToLower(strings.TrimSpace(r.PostFormValue(paramName)))
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
}

// getDefaultValue returns the default value for a given parameter (matching file-parse)
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

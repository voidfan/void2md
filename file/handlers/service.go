package handlers

import (
	"log/slog"
	"net/http"

	"void2md/common/logger"
)

// Service represents the file-parse API service.
// Mirrors the dbc/handlers Service pattern: NewService constructor,
// net/http handler methods, and SetupRoutes returning an http.ServeMux.
type Service struct {
	maxUploadSize          int // limit for /chunk endpoint
	fileParseMaxUploadSize int // limit for /fileparse endpoint
	logger                 *slog.Logger
}

// NewService creates a new file-parse API service
func NewService() *Service {
	return &Service{
		maxUploadSize:          getMaxUploadSize(),
		fileParseMaxUploadSize: getFileParseMaxUploadSize(),
		logger:                 logger.GetLogger("fileparse"),
	}
}

// SetupRoutes sets up the HTTP routes
func (s *Service) SetupRoutes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/fileparse", s.HandleFileParse)
	mux.HandleFunc("/pdfparse", s.HandleFileParse) // Alias for file-parse compatibility
	mux.HandleFunc("/chunk", s.HandleChunk)
	return mux
}

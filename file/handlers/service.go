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
}

// log returns the module logger lazily so it picks up the handler configured
// by logger.InitLogger (NewService runs at package init, before main).
func (s *Service) log() *slog.Logger { return logger.GetLogger("fileparse") }

// NewService creates a new file-parse API service
func NewService() *Service {
	return &Service{
		maxUploadSize:          getMaxUploadSize(),
		fileParseMaxUploadSize: getFileParseMaxUploadSize(),
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

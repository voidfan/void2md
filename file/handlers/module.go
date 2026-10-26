package handlers

import (
	"net/http"

	"void2md/file/pkg/handler"

	"github.com/gin-gonic/gin"
)

// svc is created at package init: importing this package loads the module
// configuration into memory.
var svc *Service

func init() {
	svc = NewService()
}

// Mount registers the file module routes (/v1/fileparse, /v1/pdfparse,
// /v1/chunk, /v1/htmlparse*) onto the shared gin engine.
func Mount(r *gin.Engine) {
	mux := svc.SetupRoutes()

	v1 := r.Group("/v1")
	v1.Any("/fileparse", gin.WrapH(http.StripPrefix("/v1", mux)))
	v1.Any("/pdfparse", gin.WrapH(http.StripPrefix("/v1", mux))) // Alias for file-parse compatibility
	v1.Any("/chunk", gin.WrapH(http.StripPrefix("/v1", mux)))

	handler.RegisterHTMLParseRoutes(v1)
}

package api

import (
	"void2md/common/logger"
	sconfig "void2md/structured/pkg/config"

	"github.com/gin-gonic/gin"
)

// settings loads the structured module configuration (DATIVE_* env vars / .env) at package init.
var settings *sconfig.Settings

// server is the shared API server backing the mounted handler.
var server *Server

func init() {
	settings = sconfig.Load()
	server = &Server{}
}

// Mount registers the structured module routes onto the shared gin engine
// (URLs defined by Server.SetupRoutes).
// The /v1 prefix is applied inside NewHandler: the starlette-compatible dispatch
// must see the original full path so trailing-slash 307s keep the absolute Location.
// The gin route is the narrow /v1/data_source subtree (not a /v1/* wildcard) so it
// coexists with the file and dbc modules' /v1 routes.
// /healthz is a project-level route registered in main.go, not part of this module.
func Mount(r *gin.Engine) {
	if server == nil {
		return
	}

	// Assigned here (not in init) so it picks up the handler configured by
	// logger.InitLogger in main.
	server.Logger = logger.GetLogger("structured")

	h := NewHandler(server) // CORS + starlette-compatible dispatch over SetupRoutes

	r.Any("/v1/data_source/*path", gin.WrapH(h))
}

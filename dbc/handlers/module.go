package handlers

import (
	"log/slog"
	"net/http"
	"os"

	"void2md/dbc/pkg/config"

	"github.com/gin-gonic/gin"
)

// svc is created at package init: importing this package loads the dbc
// configuration into memory (DBC_CONFIG env var, default
// "dbc/config/config.json").
var svc *Service

func init() {
	configPath := os.Getenv("DBC_CONFIG")
	if configPath == "" {
		configPath = "dbc/config/config.json"
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		// Do not kill the host service: warn and leave svc nil so Mount
		// skips route registration and the other modules keep working.
		slog.Warn("dbc config load failed; skipping /v1/dbc route mount", "path", configPath, "error", err)
		return
	}

	svc = NewService(cfg)
}

// Mount registers the dbc module routes (/dbc/query, /dbc/connections,
// /dbc/schema) onto the shared gin engine. It is a no-op when the dbc
// configuration failed to load at init.
func Mount(r *gin.Engine) {
	if svc == nil {
		return
	}

	group := r.Group("/v1/dbc")
	group.Any("/*path", gin.WrapH(http.StripPrefix("/v1/dbc", svc.SetupRoutes())))
}

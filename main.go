package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"void2md/common/config"
	"void2md/common/logger"
	dbchandlers "void2md/dbc/handlers"
	filehandlers "void2md/file/handlers"
	structuredapi "void2md/structured/handler"

	"github.com/gin-gonic/gin"
)

func main() {
	// 1. Load service config, init logger
	cfg := config.LoadConfig()
	logger.InitLogger(cfg.LogLevel, cfg.LogFormat)
	log := logger.GetLogger("main")
	log.Info("config loaded", "host", cfg.Host, "port", cfg.Port, "logLevel", cfg.LogLevel)

	// 2. Gin engine and middleware
	if cfg.LogLevel == "debug" || cfg.LogLevel == "DEBUG" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()

	// Service-level health check, registered before middleware: no auth, for probes/LB.
	r.Any("/healthz", healthzHandler())

	r.Use(gin.Logger())
	r.Use(authMiddleware())

	// 3. Mount module routes (each module loads its config in package init at import)
	filehandlers.Mount(r)  // file module (/v1/*)
	dbchandlers.Mount(r)   // dbc module (/v1/dbc/*)
	structuredapi.Mount(r) // structured module (/v1/data_source/*)
	log.Info("module routes mounted", "modules", []string{"file", "dbc", "structured"})

	// 4. Start server (graceful shutdown)
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: r,
	}

	go func() {
		log.Info("http server listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http server failed", "error", err)
			panic(err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	log.Info("shutdown signal received", "signal", sig.String())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error("shutdown error", "error", err)
	}
	log.Info("server stopped gracefully")
}

// healthzHandler is the service-level health check: GET returns the JSON
// string "ok" (same body the structured module used to serve); other methods
// get 405 with an Allow header.
func healthzHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodGet {
			c.Header("Allow", http.MethodGet)
			c.JSON(http.StatusMethodNotAllowed, gin.H{"detail": "Method Not Allowed"})
			return
		}
		c.JSON(http.StatusOK, "ok")
	}
}

// authMiddleware validates the Authorization header
func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header required"})
			c.Abort()
			return
		}

		// Simple Bearer token validation - in production, you might want to validate against a list of valid tokens
		// For now, we just check that it starts with "Bearer "
		if len(authHeader) < 7 || authHeader[:7] != "Bearer " {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid authorization format. Expected 'Bearer <token>'"})
			c.Abort()
			return
		}

		// Extract token (for future use if needed)
		token := authHeader[7:]
		c.Set("token", token)

		c.Next()
	}
}

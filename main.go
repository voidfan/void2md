package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"void2md/handlers"
	"void2md/internal/config"
	"void2md/internal/handler"
	"void2md/internal/pkg/logger"

	"github.com/gin-gonic/gin"
)

func main() {
	// 1. 加载配置
	cfg := config.LoadConfig()

	// 2. 初始化日志
	logger.InitLogger(cfg.LogLevel, cfg.LogFormat)

	logger.GetLogger("main").Info("配置加载完成",
		"host", cfg.Host,
		"port", cfg.Port,
		"logLevel", cfg.LogLevel)

	// 3. 创建 Gin Engine
	if cfg.LogLevel == "debug" || cfg.LogLevel == "DEBUG" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	// Create router
	r := gin.New()

	// Add logger middleware for request logging
	r.Use(gin.Logger())

	// Add middleware for authentication
	r.Use(authMiddleware())

	// Register routes
	r.POST("/v1/fileparse", handlers.FileParseHandler)
	r.POST("/v1/pdfparse", handlers.FileParseHandler) // Alias for file-parse compatibility
	r.POST("/v1/chunk", handlers.ChunkHandler)

	v1 := r.Group("/v1") // Group routes under /v1
	handler.RegisterHTMLParseRoutes(v1)

	logger.GetLogger("main").Info("路由注册完成",
		"routes", []string{
			"POST /v1/fileparse",
			"POST /v1/pdfparse",
			"POST /v1/chunk",
			"POST /v1/htmlparse",
			"GET /v1/htmlparse/:taskId",
			"DELETE /v1/htmlparse/:taskId",
		})

	// 6. 启动服务器（优雅关闭）
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: r,
	}

	go func() {
		logger.GetLogger("main").Info("HTTP 服务启动", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.GetLogger("main").Error("HTTP 服务启动失败", "error", err)
			panic(err)
		}
	}()

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	logger.GetLogger("main").Info("收到退出信号，正在关闭服务", "signal", sig.String())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.GetLogger("main").Error("服务关闭异常", "error", err)
	}
	logger.GetLogger("main").Info("服务已安全关闭")
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

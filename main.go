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

	"github.com/gin-gonic/gin"
)

func main() {
	// 1. 加载服务配置并初始化日志
	cfg := config.LoadConfig()
	logger.InitLogger(cfg.LogLevel, cfg.LogFormat)
	log := logger.GetLogger("main")
	log.Info("配置加载完成", "host", cfg.Host, "port", cfg.Port, "logLevel", cfg.LogLevel)

	// 2. 创建 Gin Engine 与中间件
	if cfg.LogLevel == "debug" || cfg.LogLevel == "DEBUG" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Logger())
	r.Use(authMiddleware())

	// 3. 挂载模块路由（模块配置已在 import 时由各自包的 init 加载入内存）
	filehandlers.Mount(r) // file 模块（/v1/*）
	dbchandlers.Mount(r)  // dbc 模块（/dbc/*）
	log.Info("模块路由挂载完成", "modules", []string{"file", "dbc"})

	// 4. 启动服务器（优雅关闭）
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: r,
	}

	go func() {
		log.Info("HTTP 服务启动", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("HTTP 服务启动失败", "error", err)
			panic(err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	log.Info("收到退出信号，正在关闭服务", "signal", sig.String())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error("服务关闭异常", "error", err)
	}
	log.Info("服务已安全关闭")
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

package main

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"void2md-api/handlers"
)

func main() {
	// Set Gin to release mode for production
	gin.SetMode(gin.ReleaseMode)

	// Create router
	r := gin.Default()

	// Add middleware for authentication
	r.Use(authMiddleware())

	// Register routes
	r.POST("/v1/fileparse", handlers.FileParseHandler)
	r.POST("/v1/pdfparse", handlers.FileParseHandler) // Alias for file-parse compatibility
	r.POST("/v1/chunk", handlers.ChunkHandler)

	// Start server on port 3457
	log.Println("Starting server on :3457")
	if err := r.Run(":3457"); err != nil {
		log.Fatal("Failed to start server:", err)
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
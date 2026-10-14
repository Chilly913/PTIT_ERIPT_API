package server

import (
	"net/http"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/gin-gonic/gin"
)

func Run(cfg config.Config) error {
	gin.SetMode(gin.ReleaseMode)
	if cfg.AppEnv == "development" || cfg.AppEnv == "local" {
		gin.SetMode(gin.DebugMode)
	}

	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.Use(corsMiddleware())

	r.GET("/health", health)
	r.GET("/api/health", health)
	r.GET("/api", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"service": cfg.AppName, "status": "ok"})
	})

	// Placeholder groups to be implemented during migration.
	// See docs/MIGRATION_PLAN.md for the full Laravel route inventory.
	v1 := r.Group("/api")
	{
		v1.GET("/ping", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"pong": true}) })
	}

	addr := ":" + cfg.Port
	return r.Run(addr)
}

func health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Requested-With")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}



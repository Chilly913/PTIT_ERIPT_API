package httpx

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// OK renders 200 with v as the JSON body (Laravel: response()->json($v)).
func OK(c *gin.Context, v any) { c.JSON(http.StatusOK, v) }

// Created renders 201 with v.
func Created(c *gin.Context, v any) { c.JSON(http.StatusCreated, v) }

// NoContent renders 204.
func NoContent(c *gin.Context) { c.Status(http.StatusNoContent) }

// Message renders {"message": msg} with the given status.
func Message(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"message": msg})
}

// Data renders {"data": v} (Laravel API resources / explicit envelopes).
func Data(c *gin.Context, status int, v any) {
	c.JSON(status, gin.H{"data": v})
}

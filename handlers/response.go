package handlers

import (
	"encoding/json"

	"github.com/gin-gonic/gin"
)

// JSONNoEscape returns JSON response without HTML escaping.
// This prevents response bloat for large HTML tables by not escaping <, >, & characters.
func JSONNoEscape(c *gin.Context, code int, obj interface{}) {
	c.Status(code)
	c.Header("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(c.Writer)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		c.Error(err)
	}
}
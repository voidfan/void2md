package response

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"void2md/common/diagnosis"
)

// SuccessResponse is the standard success response.
type SuccessResponse struct {
	Code    string      `json:"code"`
	Message string      `json:"msg"`
	Data    interface{} `json:"data"`
}

// OK returns a success response.
func OK(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, SuccessResponse{
		Code:    "0",
		Message: "success",
		Data:    data,
	})
}

// Created returns a 201 success response.
func Created(c *gin.Context, data interface{}) {
	c.JSON(http.StatusCreated, SuccessResponse{
		Code:    "0",
		Message: "success",
		Data:    data,
	})
}

// NoContent returns an empty success response.
func NoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

// JSONNoEscape writes JSON with HTML escaping disabled (<, >, & stay verbatim):
// encoding/json escapes < and > to 6-byte \u sequences, bloating large
// markdown/HTML-table responses several times (OOM risk on large CSV parses).
func JSONNoEscape(c *gin.Context, code int, obj interface{}) {
	c.Status(code)
	c.Header("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(c.Writer)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		c.Error(err)
	}
}

// GinRecovery is a gin recovery middleware: panic -> 500 JSON (DiagnosisError shape).
func GinRecovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		c.AbortWithStatusJSON(http.StatusInternalServerError, diagnosis.DiagnosisError{
			Code:          "500",
			DiagnosisCode: diagnosis.ErrorUnknownDiagnosisCode,
			Message:       "Internal server error",
		})
	})
}

// GinErrorHandler converts DiagnosisException into the standard JSON response.
func GinErrorHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		if len(c.Errors) == 0 {
			return
		}

		for _, e := range c.Errors {
			if de, ok := e.Err.(*diagnosis.DiagnosisException); ok {
				c.AbortWithStatusJSON(de.StatusCode, de.ToDiagnosisError())
				return
			}
		}

		// Unknown error type: 500.
		c.AbortWithStatusJSON(http.StatusInternalServerError, diagnosis.DiagnosisError{
			Code:          "500",
			DiagnosisCode: diagnosis.ErrorUnknownDiagnosisCode,
			Message:       "Internal server error",
		})
	}
}

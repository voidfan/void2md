package response

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"void2md/common/diagnosis"
)

// SuccessResponse 标准成功响应
type SuccessResponse struct {
	Code    string      `json:"code"`
	Message string      `json:"msg"`
	Data    interface{} `json:"data"`
}

// OK 返回成功响应
func OK(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, SuccessResponse{
		Code:    "0",
		Message: "success",
		Data:    data,
	})
}

// Created 返回创建成功响应
func Created(c *gin.Context, data interface{}) {
	c.JSON(http.StatusCreated, SuccessResponse{
		Code:    "0",
		Message: "success",
		Data:    data,
	})
}

// NoContent 返回空成功响应
func NoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

// JSONNoEscape 返回 JSON 响应，但关闭 HTML 转义（<、>、& 原样输出）。
// 用于 markdown / HTML 表格等大体积、含大量 < 标签的响应：encoding/json 默认
// HTML 转义会把每个 < 膨胀为 <（6 字节）、> 膨胀为 >，导致响应体数倍
// 放大而 OOM（大 CSV 解析为 HTML 表格时尤其明显）。
func JSONNoEscape(c *gin.Context, code int, obj interface{}) {
	c.Status(code)
	c.Header("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(c.Writer)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		c.Error(err)
	}
}

// GinRecovery Gin recovery 中间件：panic → 500 JSON（DiagnosisError 格式）
func GinRecovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		c.AbortWithStatusJSON(http.StatusInternalServerError, diagnosis.DiagnosisError{
			Code:          "500",
			DiagnosisCode: diagnosis.ErrorUnknownDiagnosisCode,
			Message:       "Internal server error",
		})
	})
}

// GinErrorHandler Gin 错误处理中间件：将 DiagnosisException 转换为标准 JSON 响应
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

		// 未识别的错误类型，返回 500
		c.AbortWithStatusJSON(http.StatusInternalServerError, diagnosis.DiagnosisError{
			Code:          "500",
			DiagnosisCode: diagnosis.ErrorUnknownDiagnosisCode,
			Message:       "Internal server error",
		})
	}
}

package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"void2md/common/diagnosis"
	"void2md/common/response"
	htmld "void2md/file/pkg/service/crawl"
)

// getHTMLCrawlService 提供服务的取值点，测试可替换。
var getHTMLCrawlService = htmld.GetHTMLCrawlService

// htmlParseRequest POST /v1/htmlparse 请求体。
// MaxPage 用指针区分「未传」（nil，用服务端默认）与「显式传 0」（非法，400）。
type htmlParseRequest struct {
	URL      string `json:"url"`
	Selector string `json:"selector"`
	MaxPage  *int   `json:"maxPage"`
}

// RegisterHTMLParseRoutes 注册站点知识获取路由。
func RegisterHTMLParseRoutes(router *gin.RouterGroup) {
	router.POST("/htmlparse", handleHTMLParseSubmit)
	router.GET("/htmlparse/:taskId", handleHTMLParseGet)
	router.DELETE("/htmlparse/:taskId", handleHTMLParseCancel)
}

// handleHTMLParseSubmit 提交爬取任务。同一 URL 已有存活任务时返回该 taskId。
func handleHTMLParseSubmit(c *gin.Context) {
	var req htmlParseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, diagnosis.NewClientException(
			diagnosis.ClientInvalidArgument,
			diagnosis.ErrorParamCheckDiagnosisCode,
			"invalid request body: "+err.Error()).ToDiagnosisError())
		return
	}

	taskID, _, err := getHTMLCrawlService().Submit(req.URL, req.Selector, req.MaxPage)
	if err != nil {
		abortWithDiagnosis(c, err)
		return
	}

	response.JSONNoEscape(c, http.StatusOK, gin.H{
		"taskId": taskID,
		"status": string(htmld.CrawlPending),
	})
}

// handleHTMLParseGet 查询任务状态与结果。
func handleHTMLParseGet(c *gin.Context) {
	task, ok := getHTMLCrawlService().Get(c.Param("taskId"))
	if !ok {
		c.AbortWithStatusJSON(http.StatusNotFound,
			diagnosis.NewClientException(
				diagnosis.ClientNotFound,
				diagnosis.ErrorNotFoundDiagnosisCode,
				"task not found").ToDiagnosisError())
		return
	}
	response.JSONNoEscape(c, http.StatusOK, gin.H{
		"status":    string(task.Status),
		"pages":     task.Pages,
		"markdown":  task.Markdown,
		"items":     task.Items,
		"truncated": task.Truncated,
		"error":     task.Error,
	})
}

// handleHTMLParseCancel 取消任务；已终态为幂等 no-op。
func handleHTMLParseCancel(c *gin.Context) {
	if !getHTMLCrawlService().Cancel(c.Param("taskId")) {
		c.AbortWithStatusJSON(http.StatusNotFound,
			diagnosis.NewClientException(
				diagnosis.ClientNotFound,
				diagnosis.ErrorNotFoundDiagnosisCode,
				"task not found").ToDiagnosisError())
		return
	}
	response.NoContent(c)
}

// abortWithDiagnosis 把 DiagnosisException 按其状态码写回，其余按 500 处理。
func abortWithDiagnosis(c *gin.Context, err error) {
	var de *diagnosis.DiagnosisException
	if errors.As(err, &de) {
		c.AbortWithStatusJSON(de.StatusCode, de.ToDiagnosisError())
		return
	}
	c.AbortWithStatusJSON(http.StatusInternalServerError, diagnosis.NewServerException(
		diagnosis.ServerInternal,
		diagnosis.ErrorUnknownDiagnosisCode,
		err.Error(),
	).ToDiagnosisError())
}

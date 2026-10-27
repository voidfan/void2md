package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"void2md/common/diagnosis"
	"void2md/common/response"
	htmld "void2md/file/pkg/service/crawl"
)

// getHTMLCrawlService is the service accessor; replaceable in tests.
var getHTMLCrawlService = htmld.GetHTMLCrawlService

// htmlParseRequest is the POST /v1/htmlparse request body.
// MaxPage is a pointer to distinguish "not sent" (nil → server default) from
// "explicitly 0" (invalid → 400).
type htmlParseRequest struct {
	URL      string `json:"url"`
	Selector string `json:"selector"`
	MaxPage  *int   `json:"maxPage"`
}

// RegisterHTMLParseRoutes registers the site-knowledge routes.
func RegisterHTMLParseRoutes(router *gin.RouterGroup) {
	router.POST("/htmlparse", handleHTMLParseSubmit)
	router.GET("/htmlparse/:taskId", handleHTMLParseGet)
	router.DELETE("/htmlparse/:taskId", handleHTMLParseCancel)
}

// handleHTMLParseSubmit submits a crawl task. If a live task exists for the same URL, its taskId is returned.
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

// handleHTMLParseGet queries task status and result.
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

// handleHTMLParseCancel cancels a task; idempotent no-op when already terminal.
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

// abortWithDiagnosis writes a DiagnosisException back with its status code; everything else becomes 500.
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

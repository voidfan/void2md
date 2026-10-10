package response

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGinRecovery_Returns500JSONOnPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GinRecovery())
	r.GET("/boom", func(c *gin.Context) {
		panic("boom")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"diagnosisCode"`) {
		t.Errorf("expected DiagnosisError JSON, got: %s", w.Body.String())
	}
}

func TestJSONNoEscape_KeepsHTMLUnescaped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	JSONNoEscape(c, http.StatusOK, gin.H{"html": "<b>&</b>"})

	body := w.Body.String()
	if strings.Contains(body, `\u003c`) || strings.Contains(body, `\u0026`) {
		t.Errorf("HTML escaped, got: %q", body)
	}
	if !strings.Contains(body, "<b>&</b>") {
		t.Errorf("expected literal html, got: %q", body)
	}
}

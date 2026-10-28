package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func corsRouter(origins []string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CORS(origins))
	router.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	return router
}

func TestCORSAllowsOnlyListedOrigins(t *testing.T) {
	router := corsRouter([]string{"https://panel.example.com"})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Origin", "https://panel.example.com")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://panel.example.com" {
		t.Errorf("listed origin should be echoed, got %q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("an unlisted origin must get no CORS headers")
	}
}

func TestCORSAnswersPreflight(t *testing.T) {
	router := corsRouter([]string{"https://panel.example.com"})
	req := httptest.NewRequest(http.MethodOptions, "/x", nil)
	req.Header.Set("Origin", "https://panel.example.com")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Headers") == "" {
		t.Errorf("preflight should answer 204 with allowed headers, got %d / %v", rec.Code, rec.Header())
	}
}

func TestCORSWithoutAllowedOriginsAddsNothing(t *testing.T) {
	router := corsRouter(nil)
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Origin", "https://panel.example.com")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("with no allowed origins no CORS header may be added")
	}
}

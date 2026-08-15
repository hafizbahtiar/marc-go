package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func newCORSRouter(t *testing.T, allowed []string, methods string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	mw := CORS(allowed, methods)
	r.POST("/x", mw, func(c *gin.Context) { c.Status(http.StatusOK) })
	r.OPTIONS("/x", mw)
	return r
}

func TestCORSAllowedOriginGetsHeaders(t *testing.T) {
	r := newCORSRouter(t, []string{"https://marc.hafizbahtiar.com"}, "POST, OPTIONS")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Origin", "https://marc.hafizbahtiar.com")
	r.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://marc.hafizbahtiar.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q, mahu origin dibenarkan", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q, mahu %q", got, "Origin")
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "POST, OPTIONS" {
		t.Fatalf("Access-Control-Allow-Methods = %q, mahu %q", got, "POST, OPTIONS")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, mahu %d (handler sebenar patut jalan)", rec.Code, http.StatusOK)
	}
}

func TestCORSDisallowedOriginGetsNoHeaders(t *testing.T) {
	r := newCORSRouter(t, []string{"https://marc.hafizbahtiar.com"}, "POST, OPTIONS")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	r.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, mahu kosong bagi origin tak dibenarkan", got)
	}
	// Laluan tetap jalan (bukan CORS ni yang blok permintaan same-origin/
	// non-browser — browser sendiri yang tolak baca respons tanpa header).
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, mahu %d", rec.Code, http.StatusOK)
	}
}

func TestCORSNoOriginHeaderPassesThrough(t *testing.T) {
	r := newCORSRouter(t, []string{"https://marc.hafizbahtiar.com"}, "POST, OPTIONS")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	r.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, mahu kosong tanpa header Origin", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, mahu %d", rec.Code, http.StatusOK)
	}
}

func TestCORSPreflightRespondsNoContent(t *testing.T) {
	r := newCORSRouter(t, []string{"https://marc.hafizbahtiar.com"}, "POST, OPTIONS")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/x", nil)
	req.Header.Set("Origin", "https://marc.hafizbahtiar.com")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status preflight = %d, mahu %d", rec.Code, http.StatusNoContent)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body preflight = %q, mahu kosong", rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://marc.hafizbahtiar.com" {
		t.Fatalf("Access-Control-Allow-Origin preflight = %q, mahu origin dibenarkan", got)
	}
}

func TestCORSEmptyAllowlistBlocksEveryOrigin(t *testing.T) {
	r := newCORSRouter(t, nil, "GET, OPTIONS")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Origin", "https://marc.hafizbahtiar.com")
	r.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, mahu kosong bila CORS_ALLOWED_ORIGINS tak diisi", got)
	}
}

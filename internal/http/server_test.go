package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hirasolutions/cloudmesh/internal/auth"
	"github.com/hirasolutions/cloudmesh/internal/db"
)

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	sqlDB, err := db.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("abrir db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return New(Deps{DB: sqlDB, Auth: auth.NewService(sqlDB, make([]byte, 32)), Log: slog.New(slog.DiscardHandler)})
}

func TestHealthz(t *testing.T) {
	h := newTestHandler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Fatalf("status = %q, want ok", body["status"])
	}
}

func TestAPIUnauthenticated(t *testing.T) {
	h := newTestHandler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/nope", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	var body struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "unauthenticated" {
		t.Fatalf("code = %q, want unauthenticated", body.Error.Code)
	}
}

func TestSPAFallback(t *testing.T) {
	h := newTestHandler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/cuentas/123", nil))

	// Con web/dist compilado devuelve index.html (200); sin compilar, 503.
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 200 o 503", rec.Code)
	}
	if rec.Code == http.StatusOK && rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", rec.Header().Get("Cache-Control"))
	}
}

func TestSPAWebAppFiles(t *testing.T) {
	h := newTestHandler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil))
	if rec.Code == http.StatusServiceUnavailable {
		t.Skip("web/dist sin compilar")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/manifest+json" {
		t.Fatalf("Content-Type = %q, want application/manifest+json", ct)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sw.js", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("sw.js: status %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
}

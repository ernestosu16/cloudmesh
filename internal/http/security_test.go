package http

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hirasolutions/cloudmesh/internal/storage"
)

// Un HTML subido por un editor no puede ejecutar scripts en el origen de la
// app aunque se abra en una pestaña o en la vista previa.
func TestUploadedContentIsSandboxed(t *testing.T) {
	h, p, c := newFullHandler(t)
	p.Quota = storage.Quota{Total: 1 << 30}
	if rec := do(h, "POST", "/api/accounts", `{"provider":"fake","email":"a@x.com","password":"ok"}`, c); rec.Code != 201 {
		t.Fatalf("alta = %d %s", rec.Code, rec.Body)
	}
	for _, name := range []string{"evil.html", "evil.svg", "doc.pdf"} {
		if rec := upload(h, c, "0", name, []byte("<script>alert(1)</script>"), true); rec.Code != 201 {
			t.Fatalf("upload %s = %d %s", name, rec.Code, rec.Body)
		}
	}
	for id, sandboxed := range map[string]bool{"1": true, "2": true, "3": false} {
		rec := do(h, "GET", "/api/files/"+id+"/content", "", c)
		csp := rec.Header().Get("Content-Security-Policy")
		if rec.Code != 200 || strings.HasPrefix(csp, "sandbox") != sandboxed {
			t.Fatalf("archivo %s: %d csp=%q", id, rec.Code, csp)
		}
	}
	if !activeContent("application/atom+xml") || activeContent("text/plain") {
		t.Fatal("activeContent")
	}
}

// Una página de otro origen del mismo sitio (un bucket público en un
// subdominio) no puede cambiar nada con la cookie del usuario.
func TestCrossOriginWritesRejected(t *testing.T) {
	h, _, c := newFullHandler(t)
	req := httptest.NewRequest("POST", "/api/folders", strings.NewReader(`{"parent_id":0,"name":"x"}`))
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("same-site POST = %d", rec.Code)
	}
	req = httptest.NewRequest("POST", "/api/folders", strings.NewReader(`{"parent_id":0,"name":"x"}`))
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(c)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("same-origin POST = %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache-control = %q", rec.Header().Get("Cache-Control"))
	}
}

func TestRedactPath(t *testing.T) {
	for in, want := range map[string]string{
		"/api/public/shares/abc/files/3/content": "/api/public/shares/***/files/3/content",
		"/api/public/invitations/tok":            "/api/public/invitations/***",
		"/s/tok":                                 "/s/***",
		"/invite/tok":                            "/invite/***",
		"/api/files/3":                           "/api/files/3",
	} {
		if got := redactPath(in); got != want {
			t.Errorf("redactPath(%q) = %q, quiero %q", in, got, want)
		}
	}
}

// Diez intentos fallidos contra un email lo frenan aunque lleguen de IP distintas.
func TestLoginRateLimitPerEmail(t *testing.T) {
	h := newAuthHandler(t)
	var code int
	for i := range 11 {
		req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"email":"Admin@x.com","password":"mal"}`))
		req.RemoteAddr = "10.0.0." + itoa(i+1) + ":1234"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		code = rec.Code
	}
	if code != 429 {
		t.Fatalf("intento 11 = %d", code)
	}
}

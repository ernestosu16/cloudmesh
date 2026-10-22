package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hirasolutions/cloudmesh/internal/auth"
	"github.com/hirasolutions/cloudmesh/internal/db"
)

// newAuthHandler devuelve el handler con un usuario a@b.com / contraseña-larga.
func newAuthHandler(t *testing.T) http.Handler {
	t.Helper()
	sqlDB, err := db.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	svc := auth.NewService(sqlDB, make([]byte, 32))
	if _, err := svc.CreateUser(context.Background(), "a@b.com", "contraseña-larga"); err != nil {
		t.Fatal(err)
	}
	return New(Deps{DB: sqlDB, Auth: svc, Log: slog.New(slog.DiscardHandler)})
}

func do(h http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sessionCookieOf(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("sin cookie de sesión")
	return nil
}

func TestAuthFlow(t *testing.T) {
	h := newAuthHandler(t)

	// Sin sesión
	if rec := do(h, "GET", "/api/auth/me", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me sin sesión = %d, want 401", rec.Code)
	}

	// Credenciales malas
	if rec := do(h, "POST", "/api/auth/login", `{"email":"a@b.com","password":"mal"}`, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("login malo = %d, want 401", rec.Code)
	}
	if rec := do(h, "POST", "/api/auth/login", `no json`, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("login sin json = %d, want 400", rec.Code)
	}

	// Login correcto
	rec := do(h, "POST", "/api/auth/login", `{"email":"a@b.com","password":"contraseña-larga"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", rec.Code, rec.Body)
	}
	c := sessionCookieOf(t, rec)
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge <= 0 {
		t.Fatalf("cookie insegura: %+v", c)
	}
	if c.Secure {
		t.Fatal("Secure no debe ponerse en una petición http sin proxy")
	}

	// Me
	rec = do(h, "GET", "/api/auth/me", "", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("me = %d", rec.Code)
	}
	var me auth.User
	_ = json.NewDecoder(rec.Body).Decode(&me)
	if me.Email != "a@b.com" {
		t.Fatalf("me = %+v", me)
	}

	// Ruta privada desconocida con sesión: 404
	if rec := do(h, "GET", "/api/nope", "", c); rec.Code != http.StatusNotFound {
		t.Fatalf("404 con sesión = %d", rec.Code)
	}

	// Logout
	rec = do(h, "POST", "/api/auth/logout", "", c)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", rec.Code)
	}
	if cleared := sessionCookieOf(t, rec); cleared.MaxAge >= 0 {
		t.Fatalf("logout debe borrar la cookie, MaxAge = %d", cleared.MaxAge)
	}
	if rec := do(h, "GET", "/api/auth/me", "", c); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me tras logout = %d, want 401", rec.Code)
	}
}

func TestSecureCookieBehindProxy(t *testing.T) {
	login := func(h http.Handler) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"email":"a@b.com","password":"contraseña-larga"}`))
		req.Header.Set("X-Forwarded-Proto", "https")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	// Sin confiar en el proxy, la cabecera se ignora.
	if sessionCookieOf(t, login(newAuthHandler(t))).Secure {
		t.Fatal("sin TrustProxy no debe fiarse de X-Forwarded-Proto")
	}
	// Confiando en el proxy, la cookie es Secure.
	sqlDB, err := db.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	svc := auth.NewService(sqlDB, make([]byte, 32))
	_, _ = svc.CreateUser(context.Background(), "a@b.com", "contraseña-larga")
	h := New(Deps{DB: sqlDB, Auth: svc, TrustProxy: true, Log: slog.New(slog.DiscardHandler)})
	if !sessionCookieOf(t, login(h)).Secure {
		t.Fatal("detrás de https la cookie debe ser Secure")
	}
}

func TestLoginRateLimit(t *testing.T) {
	h := newAuthHandler(t)
	var last int
	for range 11 {
		last = do(h, "POST", "/api/auth/login", `{"email":"a@b.com","password":"mal"}`, nil).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("intento 11 = %d, want 429", last)
	}
}

func TestSetupAPI(t *testing.T) {
	sqlDB, err := db.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	svc := auth.NewService(sqlDB, make([]byte, 32))
	code, _ := svc.InitSetup(context.Background())
	h := New(Deps{DB: sqlDB, Auth: svc, Log: slog.New(slog.DiscardHandler)})

	if rec := do(h, "GET", "/api/setup", "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"needed":true`) {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/setup", `{"code":"XXXX-XXXX","email":"a@b.com","password":"contraseña-larga"}`, nil); rec.Code != 403 {
		t.Fatalf("código malo = %d %s", rec.Code, rec.Body)
	}
	rec := do(h, "POST", "/api/setup", `{"code":"`+code+`","email":"a@b.com","password":"contraseña-larga"}`, nil)
	if rec.Code != 201 {
		t.Fatalf("setup = %d %s", rec.Code, rec.Body)
	}
	c := sessionCookieOf(t, rec)
	if rec := do(h, "GET", "/api/auth/me", "", c); rec.Code != 200 {
		t.Fatalf("me tras setup = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/setup", "", nil); !strings.Contains(rec.Body.String(), `"needed":false`) {
		t.Fatalf("status = %s", rec.Body)
	}
	if rec := do(h, "POST", "/api/setup", `{"code":"`+code+`","email":"b@b.com","password":"contraseña-larga"}`, nil); rec.Code != 409 {
		t.Fatalf("repetido = %d", rec.Code)
	}
}

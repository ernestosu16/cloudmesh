package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hirasolutions/cloudmesh/internal/mail"
)

func TestInvitationsAPI(t *testing.T) {
	h, _, admin := newFullHandler(t)
	invite := func(body string) (int, map[string]any) {
		req := httptest.NewRequest("POST", "/api/invitations", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://drive.ejemplo.test")
		req.Host = "drive.ejemplo.test" // el proxy conserva el Host: mismo origen
		req.AddCookie(admin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, _ := invite(`{"email":"a@b.com","role":"editor"}`); code != 409 {
		t.Fatalf("invitar a quien ya tiene cuenta = %d", code)
	}
	if code, _ := invite(`{"email":"nuevo@b.com","role":"root"}`); code != 400 {
		t.Fatalf("rol inválido = %d", code)
	}
	code, out := invite(`{"email":"Nuevo@B.com","role":"reader","quota_bytes":5000}`)
	link, _ := out["url"].(string)
	if code != 201 || !strings.HasPrefix(link, "https://drive.ejemplo.test/invite/") || out["emailed"] != false {
		t.Fatalf("invitar = %d %v", code, out)
	}
	token := strings.TrimPrefix(link, "https://drive.ejemplo.test/invite/")

	// Reinvitar sustituye: el primer enlace deja de valer.
	_, again := invite(`{"email":"nuevo@b.com","role":"reader","quota_bytes":5000}`)
	token2 := strings.TrimPrefix(again["url"].(string), "https://drive.ejemplo.test/invite/")
	if rec := do(h, "GET", "/api/public/invitations/"+token, "", nil); rec.Code != 410 {
		t.Fatalf("enlace sustituido = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/invitations", "", admin); rec.Code != 200 || strings.Count(rec.Body.String(), "nuevo@b.com") != 1 {
		t.Fatalf("pendientes = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/public/invitations/"+token2, "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"email":"nuevo@b.com"`) {
		t.Fatalf("ver invitación = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/public/invitations/"+token2, `{"name":"Nueva","password":"corta"}`, nil); rec.Code != 400 {
		t.Fatalf("contraseña corta = %d", rec.Code)
	}
	rec := do(h, "POST", "/api/public/invitations/"+token2, `{"name":"Nueva","password":"contraseña-nueva"}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"role":"reader"`) || !strings.Contains(rec.Body.String(), `"quota_bytes":5000`) {
		t.Fatalf("aceptar = %d %s", rec.Code, rec.Body)
	}
	// Entra directamente y el enlace ya no sirve.
	if me := do(h, "GET", "/api/auth/me", "", sessionCookieOf(t, rec)); me.Code != 200 || !strings.Contains(me.Body.String(), `"name":"Nueva"`) {
		t.Fatalf("me = %d %s", me.Code, me.Body)
	}
	if rec := do(h, "POST", "/api/public/invitations/"+token2, `{"password":"contraseña-nueva"}`, nil); rec.Code != 410 {
		t.Fatalf("reutilizar = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/invitations", "", admin); strings.Contains(rec.Body.String(), "nuevo@b.com") {
		t.Fatalf("aceptada sigue pendiente: %s", rec.Body)
	}

	// Anular.
	_, out = invite(`{"email":"otra@b.com"}`)
	inv := out["invitation"].(map[string]any)
	id := int(inv["id"].(float64))
	if inv["role"] != "editor" {
		t.Fatalf("rol por defecto = %v", inv["role"])
	}
	if rec := do(h, "DELETE", "/api/invitations/"+itoa(id), "", admin); rec.Code != 204 {
		t.Fatalf("anular = %d", rec.Code)
	}
	tok := strings.TrimPrefix(out["url"].(string), "https://drive.ejemplo.test/invite/")
	if rec := do(h, "GET", "/api/public/invitations/"+tok, "", nil); rec.Code != 410 {
		t.Fatalf("anulada = %d", rec.Code)
	}
}

func TestInvitationMailFailureStillGivesLink(t *testing.T) {
	sender, err := mail.New("smtp://127.0.0.1:1", "CloudMesh <cm@ejemplo.test>")
	if err != nil {
		t.Fatal(err)
	}
	h, _, admin := newFullHandlerWith(t, func(d *Deps) { d.Mail = sender })
	rec := do(h, "POST", "/api/invitations", `{"email":"x@b.com"}`, admin)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"emailed":false`) || !strings.Contains(rec.Body.String(), "email_error") ||
		!strings.Contains(rec.Body.String(), "/invite/") {
		t.Fatalf("sin servidor SMTP = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/invitations", "", admin); !strings.Contains(rec.Body.String(), `"mail_enabled":true`) {
		t.Fatalf("mail_enabled = %s", rec.Body)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

var _ http.Handler

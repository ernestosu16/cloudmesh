package http

import (
	"archive/zip"
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/shares"
	"github.com/hirasolutions/cloudmesh/internal/storage"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

func TestSharesAPI(t *testing.T) {
	h, p, c := newFullHandler(t)
	p.Quota = storage.Quota{Total: 1 << 30}
	if rec := do(h, "POST", "/api/accounts", `{"provider":"fake","email":"a@x.com","password":"ok"}`, c); rec.Code != 201 {
		t.Fatalf("alta = %d %s", rec.Code, rec.Body)
	}
	do(h, "POST", "/api/folders", `{"parent_id":0,"name":"Boda"}`, c)    // 1
	do(h, "POST", "/api/folders", `{"parent_id":1,"name":"Fiesta"}`, c)  // 2
	do(h, "POST", "/api/folders", `{"parent_id":0,"name":"Otra"}`, c)    // 3
	upload(h, c, "1", "a.txt", []byte("hola"), true)                     // 1
	upload(h, c, "2", "b.txt", []byte("adiós"), true)                    // 2
	upload(h, c, "3", "secreto.txt", []byte("no"), true)                 // 3
	upload(h, c, "0", "pagina.html", []byte("<script>x</script>"), true) // 4

	if rec := do(h, "POST", "/api/shares", `{"kind":"folder","target_id":0}`, c); rec.Code != 400 {
		t.Fatalf("compartir raíz = %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/shares", `{"kind":"file","target_id":99}`, c); rec.Code != 404 {
		t.Fatalf("archivo inexistente = %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/shares", `{"kind":"folder","target_id":1,"password":"ab"}`, c); rec.Code != 400 {
		t.Fatalf("contraseña corta = %d %s", rec.Code, rec.Body)
	}

	// Carpeta con contraseña
	rec := do(h, "POST", "/api/shares", `{"kind":"folder","target_id":1,"password":"clave1"}`, c)
	if rec.Code != 201 {
		t.Fatalf("crear = %d %s", rec.Code, rec.Body)
	}
	var sh shares.Share
	_ = json.NewDecoder(rec.Body).Decode(&sh)
	if sh.Name != "Boda" || !sh.HasPassword || len(sh.Token) < 20 || bytes.Contains(rec.Body.Bytes(), []byte("argon")) {
		t.Fatalf("share = %s", rec.Body)
	}
	base := "/api/public/shares/" + sh.Token

	rec = do(h, "GET", base, "", nil)
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"locked":true`)) {
		t.Fatalf("público bloqueado = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", base+"/folders/0", "", nil); rec.Code != 403 {
		t.Fatalf("listar sin contraseña = %d", rec.Code)
	}
	if rec := do(h, "POST", base+"/unlock", `{"password":"mal"}`, nil); rec.Code != 403 {
		t.Fatalf("contraseña mala = %d", rec.Code)
	}
	rec = do(h, "POST", base+"/unlock", `{"password":"clave1"}`, nil)
	if rec.Code != 204 {
		t.Fatalf("unlock = %d %s", rec.Code, rec.Body)
	}
	var grant *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == shareCookie {
			grant = ck
		}
	}
	if grant == nil || grant.Path != base || !grant.HttpOnly {
		t.Fatalf("cookie = %+v", grant)
	}

	// Navegación dentro del enlace, nunca fuera
	rec = do(h, "GET", base+"/folders/0", "", grant)
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte("a.txt")) || !bytes.Contains(rec.Body.Bytes(), []byte("Fiesta")) {
		t.Fatalf("raíz del enlace = %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "GET", base+"/folders/2", "", grant)
	var l publicListing
	_ = json.NewDecoder(rec.Body).Decode(&l)
	if rec.Code != 200 || len(l.Path) != 2 || l.Path[0].Name != "Boda" || len(l.Files) != 1 {
		t.Fatalf("subcarpeta = %d %+v", rec.Code, l)
	}
	for _, path := range []string{"/folders/3", "/files/3/content", "/files/4/content"} {
		if rec := do(h, "GET", base+path, "", grant); rec.Code != 404 {
			t.Fatalf("%s fuera del enlace = %d", path, rec.Code)
		}
	}
	rec = do(h, "GET", base+"/files/2/content", "", grant)
	if rec.Code != 200 || rec.Body.String() != "adiós" {
		t.Fatalf("contenido = %d %q", rec.Code, rec.Body)
	}

	// ZIP de la carpeta entera
	rec = do(h, "GET", base+"/zip", "", grant)
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if rec.Code != 200 || err != nil {
		t.Fatalf("zip = %d %v", rec.Code, err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["a.txt"] || !names["Fiesta/b.txt"] || len(names) != 3 {
		t.Fatalf("zip contiene %v", names)
	}

	// Cambiar la contraseña invalida el permiso
	if rec := do(h, "PATCH", fmt.Sprintf("/api/shares/%d", sh.ID), `{"password":"otra-clave"}`, c); rec.Code != 200 {
		t.Fatalf("patch = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", base+"/folders/0", "", grant); rec.Code != 403 {
		t.Fatalf("permiso viejo = %d", rec.Code)
	}

	// Las opciones que no existen se ignoran: el enlace es como en Mega.
	if rec := do(h, "GET", "/api/shares", "", c); bytes.Contains(rec.Body.Bytes(), []byte("expires_at")) || bytes.Contains(rec.Body.Bytes(), []byte("max_downloads")) {
		t.Fatalf("quedan opciones de caducidad o límite: %s", rec.Body)
	}

	// Archivo sin contraseña; HTML se fuerza como descarga
	rec = do(h, "POST", "/api/shares", `{"kind":"file","target_id":4}`, c)
	_ = json.NewDecoder(rec.Body).Decode(&sh)
	base = "/api/public/shares/" + sh.Token
	rec = do(h, "GET", base, "", nil)
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"locked":false`)) || !bytes.Contains(rec.Body.Bytes(), []byte(`pagina.html`)) {
		t.Fatalf("público archivo = %d %s", rec.Code, rec.Body)
	}
	// Un salto con Range no cuenta como descarga
	req := httptest.NewRequest("GET", base+"/files/4/content", nil)
	req.Header.Set("Range", "bytes=2-")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 206 {
		t.Fatalf("range = %d", rr.Code)
	}
	rec = do(h, "GET", base+"/files/4/content", "", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Disposition") != "attachment; filename=pagina.html" {
		t.Fatalf("descarga = %d %q", rec.Code, rec.Header().Get("Content-Disposition"))
	}
	// Sin límite: se descarga las veces que haga falta y el contador suma.
	if rec := do(h, "GET", base+"/files/4/content", "", nil); rec.Code != 200 {
		t.Fatalf("segunda descarga = %d", rec.Code)
	}
	rec = do(h, "GET", "/api/shares", "", c)
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"downloads":2`)) {
		t.Fatalf("contador = %s", rec.Body)
	}
	rec = do(h, "POST", "/api/shares", `{"kind":"file","target_id":1}`, c)
	_ = json.NewDecoder(rec.Body).Decode(&sh)

	// Listado y borrado
	rec = do(h, "GET", "/api/shares", "", c)
	var list []shares.Share
	_ = json.NewDecoder(rec.Body).Decode(&list)
	if len(list) != 3 || list[0].Name != "a.txt" || list[0].CreatedByEmail != "a@b.com" {
		t.Fatalf("lista = %s", rec.Body)
	}
	if rec := do(h, "DELETE", fmt.Sprintf("/api/shares/%d", sh.ID), "", c); rec.Code != 204 {
		t.Fatalf("delete = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/public/shares/"+sh.Token, "", nil); rec.Code != 404 {
		t.Fatalf("tras borrar = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/public/shares/nope", "", nil); rec.Code != 404 {
		t.Fatalf("token desconocido = %d", rec.Code)
	}
}

func TestUsersAPI(t *testing.T) {
	h, _, c := newFullHandler(t)

	if rec := do(h, "POST", "/api/users", `{"email":"b@b.com","password":"corta"}`, c); rec.Code != 400 {
		t.Fatalf("contraseña corta = %d", rec.Code)
	}
	rec := do(h, "POST", "/api/users", `{"email":"B@b.com","password":"otra-larga-1"}`, c)
	if rec.Code != 201 || !bytes.Contains(rec.Body.Bytes(), []byte(`"email":"b@b.com"`)) {
		t.Fatalf("alta = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/users", `{"email":"b@b.com","password":"otra-larga-1"}`, c); rec.Code != 409 {
		t.Fatalf("duplicado = %d", rec.Code)
	}
	rec = do(h, "GET", "/api/users", "", c)
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte("b@b.com")) || bytes.Contains(rec.Body.Bytes(), []byte("argon")) {
		t.Fatalf("lista = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "DELETE", "/api/users/1", "", c); rec.Code != 409 {
		t.Fatalf("borrarse a sí mismo = %d", rec.Code)
	}

	// Contraseña: otra sesión del mismo usuario se cierra, la actual sigue
	other := sessionCookieOf(t, do(h, "POST", "/api/auth/login", `{"email":"a@b.com","password":"contraseña-larga"}`, nil))
	rec = do(h, "GET", "/api/auth/sessions", "", c)
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"active":2`)) {
		t.Fatalf("sesiones = %s", rec.Body)
	}
	if rec := do(h, "POST", "/api/auth/password", `{"current_password":"mal","new_password":"nueva-contraseña"}`, c); rec.Code != 400 {
		t.Fatalf("actual mala = %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/auth/password", `{"current_password":"contraseña-larga","new_password":"nueva-contraseña"}`, c); rec.Code != 204 {
		t.Fatalf("cambiar = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/auth/me", "", other); rec.Code != 401 {
		t.Fatalf("otra sesión = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/auth/me", "", c); rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte("created_at")) {
		t.Fatalf("sesión actual = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/auth/login", `{"email":"a@b.com","password":"nueva-contraseña"}`, nil); rec.Code != 200 {
		t.Fatalf("login nueva = %d", rec.Code)
	}
	if rec := do(h, "DELETE", "/api/auth/sessions", "", c); rec.Code != 204 {
		t.Fatalf("cerrar otras = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/auth/sessions", "", c); !bytes.Contains(rec.Body.Bytes(), []byte(`"active":1`)) {
		t.Fatalf("tras cerrar = %s", rec.Body)
	}

	// Nombre visible
	if rec := do(h, "PATCH", "/api/auth/me", `{"name":"`+strings.Repeat("x", 81)+`"}`, c); rec.Code != 400 {
		t.Fatalf("nombre largo = %d", rec.Code)
	}
	if rec := do(h, "PATCH", "/api/auth/me", `{"name":"  Ana Pérez "}`, c); rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"name":"Ana Pérez"`)) {
		t.Fatalf("nombre = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/auth/me", "", c); !bytes.Contains(rec.Body.Bytes(), []byte(`"name":"Ana Pérez"`)) {
		t.Fatalf("me = %s", rec.Body)
	}

	// Índice exportado: binario cifrado, nunca JSON en claro
	rec = do(h, "GET", "/api/health/manifest/export", "", c)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") ||
		rec.Body.Len() == 0 || bytes.Contains(rec.Body.Bytes(), []byte("a@b.com")) {
		t.Fatalf("export = %d %q len=%d", rec.Code, rec.Header().Get("Content-Disposition"), rec.Body.Len())
	}

	if rec := do(h, "DELETE", "/api/users/2", "", c); rec.Code != 204 {
		t.Fatalf("borrar = %d", rec.Code)
	}
	if rec := do(h, "DELETE", "/api/users/2", "", c); rec.Code != 404 {
		t.Fatalf("borrar de nuevo = %d", rec.Code)
	}
}

// totp calcula el código actual de un secreto base32 (RFC 6238, como la app del móvil).
func totp(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[off:off+4])&0x7fffffff)%1_000_000)
}

func TestTOTPAPI(t *testing.T) {
	h, _, c := newFullHandler(t)
	login := `{"email":"a@b.com","password":"contraseña-larga"}`

	rec := do(h, "POST", "/api/auth/2fa/setup", "", c)
	var setup struct{ Secret, URL string }
	_ = json.NewDecoder(rec.Body).Decode(&setup)
	if rec.Code != 200 || setup.Secret == "" || !strings.HasPrefix(setup.URL, "otpauth://totp/") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("setup = %d %+v", rec.Code, setup)
	}
	other := sessionCookieOf(t, do(h, "POST", "/api/auth/login", login, nil))
	if rec := do(h, "POST", "/api/auth/2fa/enable", `{"code":"000000"}`, c); rec.Code != 400 {
		t.Fatalf("código malo = %d", rec.Code)
	}
	// El código de hace un paso también vale: no se pisa con el que use el login.
	if rec := do(h, "POST", "/api/auth/2fa/enable", `{"code":"`+totp(t, setup.Secret, time.Now().Add(-30*time.Second))+`"}`, c); rec.Code != 200 || !strings.Contains(rec.Body.String(), "recovery_codes") {
		t.Fatalf("activar = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/auth/me", "", other); rec.Code != 401 {
		t.Fatalf("al activar se cierran las demás sesiones: %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/auth/me", "", c); !bytes.Contains(rec.Body.Bytes(), []byte(`"totp_enabled":true`)) {
		t.Fatalf("me = %s", rec.Body)
	}

	// Login en dos pasos.
	if rec := do(h, "POST", "/api/auth/login", login, nil); rec.Code != 428 || !bytes.Contains(rec.Body.Bytes(), []byte("totp_required")) || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("sin código = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/auth/login", `{"email":"a@b.com","password":"contraseña-larga","code":"111111"}`, nil); rec.Code != 401 || !bytes.Contains(rec.Body.Bytes(), []byte("invalid_totp")) {
		t.Fatalf("código malo = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/auth/login", `{"email":"a@b.com","password":"contraseña-larga","code":"`+totp(t, setup.Secret, time.Now())+`"}`, nil); rec.Code != 200 {
		t.Fatalf("con código = %d %s", rec.Code, rec.Body)
	}

	// Otro usuario quita la 2FA de quien perdió el móvil; la propia no.
	rec = do(h, "POST", "/api/users", `{"email":"b@b.com","password":"otra-larga-1","role":"admin"}`, c)
	var b struct{ ID int64 }
	_ = json.NewDecoder(rec.Body).Decode(&b)
	bc := sessionCookieOf(t, do(h, "POST", "/api/auth/login", `{"email":"b@b.com","password":"otra-larga-1"}`, nil))
	if rec := do(h, "DELETE", "/api/users/"+strconv.FormatInt(b.ID, 10)+"/2fa", "", bc); rec.Code != 409 {
		t.Fatalf("quitarse la propia = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/users", "", bc); !bytes.Contains(rec.Body.Bytes(), []byte(`"totp_enabled":true`)) {
		t.Fatalf("lista = %s", rec.Body)
	}
	if rec := do(h, "DELETE", "/api/users/1/2fa", "", bc); rec.Code != 204 {
		t.Fatalf("reset = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/auth/login", login, nil); rec.Code != 200 {
		t.Fatalf("login tras reset = %d", rec.Code)
	}
}

func TestRolesAndQuota(t *testing.T) {
	h, p, admin := newFullHandler(t)
	p.Quota = storage.Quota{Total: 1 << 30}
	for _, e := range []string{"a@x.com", "b@x.com", "c@x.com"} {
		do(h, "POST", "/api/accounts", `{"provider":"fake","email":"`+e+`","password":"ok"}`, admin)
	}
	if rec := do(h, "POST", "/api/users", `{"email":"x@b.com","password":"otra-larga-1","role":"root"}`, admin); rec.Code != 400 {
		t.Fatalf("rol inválido = %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/users", `{"email":"e@b.com","password":"otra-larga-1","role":"editor","quota_bytes":1000}`, admin); rec.Code != 201 ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`"role":"editor"`)) {
		t.Fatalf("alta editor = %d %s", rec.Code, rec.Body)
	}
	do(h, "POST", "/api/users", `{"email":"l@b.com","password":"otra-larga-1","role":"reader"}`, admin)
	editor := sessionCookieOf(t, do(h, "POST", "/api/auth/login", `{"email":"e@b.com","password":"otra-larga-1"}`, nil))
	reader := sessionCookieOf(t, do(h, "POST", "/api/auth/login", `{"email":"l@b.com","password":"otra-larga-1"}`, nil))

	// Editor: sube dentro de su cuota; pasarse se rechaza antes de subir nada.
	if rec := upload(h, editor, "0", "a.txt", bytes.Repeat([]byte("a"), 600), true); rec.Code != 201 {
		t.Fatalf("subida editor = %d %s", rec.Code, rec.Body)
	}
	if rec := upload(h, editor, "0", "b.txt", bytes.Repeat([]byte("b"), 500), true); rec.Code != 403 || !bytes.Contains(rec.Body.Bytes(), []byte("quota_exceeded")) {
		t.Fatalf("pasarse de cuota = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/auth/me", "", editor); !bytes.Contains(rec.Body.Bytes(), []byte(`"used_bytes":600`)) || !bytes.Contains(rec.Body.Bytes(), []byte(`"quota_bytes":1000`)) {
		t.Fatalf("me editor = %s", rec.Body)
	}
	if rec := do(h, "POST", "/api/folders", `{"parent_id":0,"name":"Ed"}`, editor); rec.Code != 201 {
		t.Fatalf("editor crea carpeta = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/users", "", editor); rec.Code != 403 {
		t.Fatalf("editor no gestiona usuarios = %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/accounts/test", `{"provider":"fake","email":"z@x.com","password":"ok"}`, editor); rec.Code != 403 {
		t.Fatalf("editor no toca cuentas = %d", rec.Code)
	}
	// La papelera sigue contando; purgada deja de contar.
	do(h, "DELETE", "/api/files/1", "", editor)
	if rec := upload(h, editor, "0", "b.txt", bytes.Repeat([]byte("b"), 500), true); rec.Code != 403 {
		t.Fatalf("la papelera cuenta = %d", rec.Code)
	}
	do(h, "DELETE", "/api/trash/1", "", editor)
	if rec := upload(h, editor, "0", "b.txt", bytes.Repeat([]byte("b"), 500), true); rec.Code != 201 {
		t.Fatalf("tras purgar = %d %s", rec.Code, rec.Body)
	}

	// Lector: ve y descarga; no cambia nada.
	if rec := do(h, "GET", "/api/folders/0", "", reader); rec.Code != 200 {
		t.Fatalf("lector lista = %d", rec.Code)
	}
	var root vfs.Listing
	_ = json.NewDecoder(do(h, "GET", "/api/folders/0", "", reader).Body).Decode(&root)
	if len(root.Files) != 1 {
		t.Fatalf("archivos = %+v", root.Files)
	}
	if rec := do(h, "GET", fmt.Sprintf("/api/files/%d/content", root.Files[0].ID), "", reader); rec.Code != 200 {
		t.Fatalf("lector descarga = %d", rec.Code)
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/folders", `{"parent_id":0,"name":"L"}`},
		{"PATCH", "/api/files/2", `{"name":"x.txt"}`},
		{"DELETE", "/api/files/2", ""},
		{"POST", "/api/shares", `{"kind":"file","target_id":2}`},
		{"GET", "/api/trash", ""},
	} {
		if rec := do(h, c.method, c.path, c.body, reader); rec.Code != 403 {
			t.Fatalf("lector %s %s = %d", c.method, c.path, rec.Code)
		}
	}
	if rec := upload(h, reader, "0", "c.txt", []byte("c"), true); rec.Code != 403 {
		t.Fatalf("lector sube = %d", rec.Code)
	}

	// Uso por usuario en la lista; cambiar rol y cuota.
	rec := do(h, "GET", "/api/users", "", admin)
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"used_bytes":500`)) {
		t.Fatalf("uso en la lista = %s", rec.Body)
	}
	if rec := do(h, "PATCH", "/api/users/3", `{"role":"editor","quota_bytes":0}`, admin); rec.Code != 200 {
		t.Fatalf("ascender lector = %d %s", rec.Code, rec.Body)
	}
	if rec := upload(h, reader, "0", "c.txt", []byte("c"), true); rec.Code != 201 {
		t.Fatalf("el cambio de rol vale al momento = %d", rec.Code)
	}

	// Nunca sin administradores.
	if rec := do(h, "PATCH", "/api/users/1", `{"role":"editor","quota_bytes":0}`, admin); rec.Code != 409 {
		t.Fatalf("degradar al único admin = %d", rec.Code)
	}
	do(h, "PATCH", "/api/users/2", `{"role":"admin","quota_bytes":0}`, admin)
	if rec := do(h, "PATCH", "/api/users/1", `{"role":"reader","quota_bytes":0}`, admin); rec.Code != 200 {
		t.Fatalf("degradarse con otro admin = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/users", "", admin); rec.Code != 403 {
		t.Fatalf("tras degradarse = %d", rec.Code)
	}
}

package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hirasolutions/cloudmesh/internal/storage"
)

func TestRestrictedFolders(t *testing.T) {
	h, p, admin := newFullHandler(t)
	p.Quota = storage.Quota{Total: 1 << 30}
	for _, e := range []string{"a@x.com", "b@x.com", "c@x.com"} {
		do(h, "POST", "/api/accounts", `{"provider":"fake","email":"`+e+`","password":"ok"}`, admin)
	}
	userID := map[string]float64{}
	login := map[string]*http.Cookie{}
	for _, u := range []struct{ email, role string }{{"w@b.com", "editor"}, {"r@b.com", "editor"}, {"n@b.com", "editor"}} {
		rec := do(h, "POST", "/api/users", `{"email":"`+u.email+`","password":"otra-larga-1","role":"`+u.role+`"}`, admin)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		userID[u.email] = out["id"].(float64)
		login[u.email] = sessionCookieOf(t, do(h, "POST", "/api/auth/login", `{"email":"`+u.email+`","password":"otra-larga-1"}`, nil))
	}
	w, rd, no := login["w@b.com"], login["r@b.com"], login["n@b.com"]

	// Árbol: /Equipo (abierta) con /Equipo/RRHH (restringida) y /Equipo/RRHH/Nóminas.
	equipo := folderID(t, do(h, "POST", "/api/folders", `{"parent_id":0,"name":"Equipo"}`, admin))
	rrhh := folderID(t, do(h, "POST", "/api/folders", `{"parent_id":`+itoa(equipo)+`,"name":"RRHH"}`, admin))
	nominas := folderID(t, do(h, "POST", "/api/folders", `{"parent_id":`+itoa(rrhh)+`,"name":"Nóminas"}`, admin))
	if rec := upload(h, admin, itoa(nominas), "enero.pdf", []byte("secreto"), true); rec.Code != 201 {
		t.Fatalf("subida admin = %d %s", rec.Code, rec.Body)
	}
	var file struct{ ID int }
	_ = json.Unmarshal(upload(h, admin, itoa(nominas), "febrero.pdf", []byte("secreto 2"), true).Body.Bytes(), &file)

	if rec := do(h, "PUT", "/api/folders/0/access", `{"restricted":true}`, admin); rec.Code != 404 && rec.Code != 400 {
		t.Fatalf("restringir la raíz = %d", rec.Code)
	}
	if rec := do(h, "PUT", "/api/folders/"+itoa(rrhh)+"/access", `{"restricted":true,"grants":[{"user_id":`+itoa(int(userID["w@b.com"]))+`,"level":"write"},{"user_id":`+itoa(int(userID["r@b.com"]))+`,"level":"read"}]}`, w); rec.Code != 403 {
		t.Fatalf("un editor no configura accesos = %d", rec.Code)
	}
	rec := do(h, "PUT", "/api/folders/"+itoa(rrhh)+"/access", `{"restricted":true,"grants":[{"user_id":`+itoa(int(userID["w@b.com"]))+`,"level":"write"},{"user_id":`+itoa(int(userID["r@b.com"]))+`,"level":"read"}]}`, admin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"restricted":true`) || strings.Count(rec.Body.String(), `"user_id"`) != 2 {
		t.Fatalf("restringir = %d %s", rec.Code, rec.Body)
	}

	// Ver: sin acceso, RRHH desaparece del listado y por id es 404.
	if rec := do(h, "GET", "/api/folders/"+itoa(equipo), "", no); strings.Contains(rec.Body.String(), "RRHH") {
		t.Fatalf("sin acceso ve RRHH: %s", rec.Body)
	}
	for _, path := range []string{"/api/folders/" + itoa(rrhh), "/api/folders/" + itoa(nominas), "/api/files/" + itoa(file.ID), "/api/files/" + itoa(file.ID) + "/content"} {
		if rec := do(h, "GET", path, "", no); rec.Code != 404 {
			t.Fatalf("%s sin acceso = %d", path, rec.Code)
		}
	}
	if rec := do(h, "GET", "/api/search?q=enero", "", no); strings.Contains(rec.Body.String(), "enero.pdf") {
		t.Fatalf("la búsqueda revela: %s", rec.Body)
	}
	if rec := do(h, "GET", "/api/search?q=enero", "", rd); !strings.Contains(rec.Body.String(), "enero.pdf") {
		t.Fatalf("con acceso de lectura no lo encuentra: %s", rec.Body)
	}
	if rec := do(h, "GET", "/api/folders/"+itoa(nominas), "", rd); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"writable":false`) {
		t.Fatalf("lectura = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/files/"+itoa(file.ID)+"/content", "", rd); rec.Code != 200 || rec.Body.String() != "secreto 2" {
		t.Fatalf("descargar con lectura = %d", rec.Code)
	}

	// Cambiar: solo quien tiene write; lectura da 403 y sin acceso 404.
	if rec := upload(h, rd, itoa(nominas), "x.txt", []byte("x"), true); rec.Code != 403 {
		t.Fatalf("subir con lectura = %d", rec.Code)
	}
	if rec := upload(h, no, itoa(nominas), "x.txt", []byte("x"), true); rec.Code != 404 {
		t.Fatalf("subir sin acceso = %d", rec.Code)
	}
	if rec := upload(h, w, itoa(nominas), "marzo.pdf", []byte("m"), true); rec.Code != 201 {
		t.Fatalf("subir con write = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "PATCH", "/api/files/"+itoa(file.ID), `{"name":"otro.pdf"}`, rd); rec.Code != 403 {
		t.Fatalf("renombrar con lectura = %d", rec.Code)
	}
	if rec := do(h, "DELETE", "/api/files/"+itoa(file.ID), "", no); rec.Code != 404 {
		t.Fatalf("borrar sin acceso = %d", rec.Code)
	}
	// Sacarlo de la carpeta restringida tampoco: hace falta poder cambiar el origen.
	if rec := do(h, "PATCH", "/api/files/"+itoa(file.ID), `{"folder_id":`+itoa(equipo)+`}`, rd); rec.Code != 403 {
		t.Fatalf("mover fuera con lectura = %d", rec.Code)
	}
	// Ni meter cosas desde fuera sin acceso.
	var mine struct{ ID int }
	_ = json.Unmarshal(upload(h, no, itoa(equipo), "mio.txt", []byte("m"), true).Body.Bytes(), &mine)
	if rec := do(h, "PATCH", "/api/files/"+itoa(mine.ID), `{"folder_id":`+itoa(nominas)+`}`, no); rec.Code != 404 {
		t.Fatalf("mover dentro sin acceso = %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/shares", `{"kind":"file","target_id":`+itoa(file.ID)+`}`, rd); rec.Code != 403 {
		t.Fatalf("compartir con lectura = %d", rec.Code)
	}

	// Papelera: lo borrado dentro no lo ve quien no tiene acceso.
	if rec := do(h, "DELETE", "/api/files/"+itoa(file.ID), "", w); rec.Code != 204 {
		t.Fatalf("borrar con write = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/trash", "", no); strings.Contains(rec.Body.String(), "febrero.pdf") {
		t.Fatalf("papelera revela: %s", rec.Body)
	}
	if rec := do(h, "POST", "/api/trash/"+itoa(file.ID)+"/restore", "", rd); rec.Code != 403 {
		t.Fatalf("restaurar con lectura = %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/trash/"+itoa(file.ID)+"/restore", "", w); rec.Code != 200 {
		t.Fatalf("restaurar con write = %d", rec.Code)
	}

	// Un enlace público de /Equipo no deja ver ni bajar lo restringido de dentro.
	var sh struct{ Token string }
	_ = json.Unmarshal(do(h, "POST", "/api/shares", `{"kind":"folder","target_id":`+itoa(equipo)+`}`, admin).Body.Bytes(), &sh)
	if rec := do(h, "GET", "/api/public/shares/"+sh.Token+"/folders/0", "", nil); rec.Code != 200 || strings.Contains(rec.Body.String(), "RRHH") {
		t.Fatalf("enlace público = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/public/shares/"+sh.Token+"/folders/"+itoa(nominas), "", nil); rec.Code == 200 {
		t.Fatalf("el enlace deja entrar a una restringida: %s", rec.Body)
	}
	if rec := do(h, "GET", "/api/public/shares/"+sh.Token+"/files/"+itoa(file.ID)+"/content", "", nil); rec.Code == 200 {
		t.Fatal("el enlace deja bajar un archivo restringido")
	}

	// Borrar /Equipo se lleva lo de dentro: sin permiso en RRHH no se puede.
	for _, c := range []*http.Cookie{no, rd} {
		if rec := do(h, "DELETE", "/api/folders/"+itoa(equipo), "", c); rec.Code != 403 {
			t.Fatalf("borrar árbol con restringida = %d %s", rec.Code, rec.Body)
		}
	}
	if rec := do(h, "GET", "/api/folders/"+itoa(nominas), "", admin); rec.Code != 200 {
		t.Fatalf("el árbol se borró = %d", rec.Code)
	}

	// El administrador lo ve todo; quitar la restricción lo abre otra vez.
	if rec := do(h, "GET", "/api/folders/"+itoa(equipo), "", admin); !strings.Contains(rec.Body.String(), `"restricted":true`) {
		t.Fatalf("admin = %s", rec.Body)
	}
	do(h, "PUT", "/api/folders/"+itoa(rrhh)+"/access", `{"restricted":false}`, admin)
	if rec := do(h, "GET", "/api/folders/"+itoa(nominas), "", no); rec.Code != 200 {
		t.Fatalf("abierta otra vez = %d", rec.Code)
	}
}

// folderID saca el id de la respuesta de crear carpeta.
func folderID(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	var f struct{ ID int }
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil || f.ID == 0 {
		t.Fatalf("crear carpeta = %d %s", rec.Code, rec.Body)
	}
	return f.ID
}

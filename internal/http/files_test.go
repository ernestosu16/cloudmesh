package http

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/storage"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

func upload(h http.Handler, c *http.Cookie, folder, name string, body []byte, withLength bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/files?folder="+folder+"&name="+name, bytes.NewReader(body))
	if !withLength {
		req.ContentLength = -1
	}
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestFilesAPI(t *testing.T) {
	h, p, c := newFullHandler(t)
	p.Quota = storage.Quota{Total: 1 << 30}
	for _, e := range []string{"a@x.com", "b@x.com", "c@x.com"} {
		if rec := do(h, "POST", "/api/accounts", `{"provider":"fake","email":"`+e+`","password":"ok"}`, c); rec.Code != 201 {
			t.Fatalf("alta %s = %d %s", e, rec.Code, rec.Body)
		}
	}
	// Ajustes: 2+1 con bloque de 1 MiB
	if rec := do(h, "PATCH", "/api/settings", `{"routing_policy":"most_free","data_shards":2,"parity_shards":1,"chunk_size":1048576}`, c); rec.Code != 200 {
		t.Fatalf("settings = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "PATCH", "/api/settings", `{"routing_policy":"x","data_shards":2,"parity_shards":1,"chunk_size":1048576}`, c); rec.Code != 400 {
		t.Fatalf("settings inválidos = %d", rec.Code)
	}

	data := make([]byte, 300_000)
	rand.Read(data)
	if rec := upload(h, c, "0", "x.bin", data, false); rec.Code != 411 {
		t.Fatalf("sin length = %d", rec.Code)
	}
	if rec := upload(h, c, "0", "x.bin", make([]byte, 2<<20), true); rec.Code != 413 {
		t.Fatalf("demasiado grande = %d", rec.Code)
	}
	rec := upload(h, c, "0", "foto.jpg", data, true)
	if rec.Code != 201 {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body)
	}
	var f vfs.File
	_ = json.NewDecoder(rec.Body).Decode(&f)
	if f.Layout != "rs" || f.Mime != "image/jpeg" {
		t.Fatalf("file = %+v", f)
	}

	// Contenido completo
	rec = do(h, "GET", "/api/files/1/content", "", c)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" || !bytes.Equal(rec.Body.Bytes(), data) {
		t.Fatalf("content = %d ct=%s len=%d", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	if rec.Header().Get("Content-Disposition") != `inline; filename=foto.jpg` {
		t.Fatalf("disposition = %q", rec.Header().Get("Content-Disposition"))
	}
	// Descarga forzada
	rec = do(h, "GET", "/api/files/1/content?download=1", "", c)
	if rec.Header().Get("Content-Disposition") != `attachment; filename=foto.jpg` {
		t.Fatalf("disposition = %q", rec.Header().Get("Content-Disposition"))
	}
	// Range
	req := httptest.NewRequest("GET", "/api/files/1/content", nil)
	req.Header.Set("Range", "bytes=100-199")
	req.AddCookie(c)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 206 || rec.Header().Get("Content-Range") != "bytes 100-199/300000" || !bytes.Equal(rec.Body.Bytes(), data[100:200]) {
		t.Fatalf("range = %d %s len=%d", rec.Code, rec.Header().Get("Content-Range"), rec.Body.Len())
	}
	req.Header.Set("Range", "bytes=999999-")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 416 {
		t.Fatalf("range fuera = %d", rec.Code)
	}

	// ETag: revalidar da 304 sin cuerpo; If-Range con otra copia da el archivo entero.
	etag := rec.Header().Get("ETag")
	if len(etag) != 66 {
		t.Fatalf("etag = %q", etag)
	}
	cond := func(h1, v1, h2, v2 string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/files/1/content", nil)
		req.Header.Set(h1, v1)
		if h2 != "" {
			req.Header.Set(h2, v2)
		}
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := cond("If-None-Match", `"otro", W/`+etag, "", ""); rec.Code != 304 || rec.Body.Len() != 0 || rec.Header().Get("ETag") != etag {
		t.Fatalf("if-none-match = %d len=%d", rec.Code, rec.Body.Len())
	}
	if rec := cond("If-None-Match", `"otro"`, "", ""); rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), data) {
		t.Fatalf("if-none-match distinto = %d", rec.Code)
	}
	if rec := cond("Range", "bytes=100-199", "If-Range", etag); rec.Code != 206 || !bytes.Equal(rec.Body.Bytes(), data[100:200]) {
		t.Fatalf("if-range igual = %d", rec.Code)
	}
	if rec := cond("Range", "bytes=100-199", "If-Range", `"otro"`); rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), data) {
		t.Fatalf("if-range distinto = %d", rec.Code)
	}

	// Detalle con bloques
	rec = do(h, "GET", "/api/files/1", "", c)
	var det vfs.FileDetail
	_ = json.NewDecoder(rec.Body).Decode(&det)
	if rec.Code != 200 || len(det.Chunks) != 2 || det.Chunks[1].Kind != "parity" {
		t.Fatalf("detalle = %d %+v", rec.Code, det)
	}

	// Renombrar y mover
	do(h, "POST", "/api/folders", `{"parent_id":0,"name":"Fotos"}`, c)
	rec = do(h, "PATCH", "/api/files/1", `{"name":"playa.jpg","folder_id":1}`, c)
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"folder_id":1`)) {
		t.Fatalf("patch = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "PATCH", "/api/files/1", `{"folder_id":99}`, c); rec.Code != 404 {
		t.Fatalf("mover a inexistente = %d", rec.Code)
	}
	rec = do(h, "GET", "/api/folders/1", "", c)
	if !bytes.Contains(rec.Body.Bytes(), []byte(`playa.jpg`)) {
		t.Fatalf("listado = %s", rec.Body)
	}

	// Borrar manda a la papelera: desaparece del disco y de los enlaces, los bloques siguen
	share := do(h, "POST", "/api/shares", `{"kind":"file","target_id":1}`, c)
	var sh struct{ Token string }
	_ = json.NewDecoder(share.Body).Decode(&sh)
	if rec := do(h, "DELETE", "/api/files/1", "", c); rec.Code != 204 {
		t.Fatalf("delete = %d", rec.Code)
	}
	for _, path := range []string{"/api/files/1/content", "/api/files/1", "/api/public/shares/" + sh.Token} {
		if rec := do(h, "GET", path, "", c); rec.Code != 404 {
			t.Fatalf("%s en papelera = %d", path, rec.Code)
		}
	}
	if rec := do(h, "PATCH", "/api/files/1", `{"name":"otro.jpg"}`, c); rec.Code != 404 {
		t.Fatalf("renombrar en papelera = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/search?q=playa", "", c); bytes.Contains(rec.Body.Bytes(), []byte("playa.jpg")) {
		t.Fatalf("búsqueda ve la papelera: %s", rec.Body)
	}
	if rec := do(h, "DELETE", "/api/files/1", "", c); rec.Code != 404 {
		t.Fatalf("borrar dos veces = %d", rec.Code)
	}
	if len(p.Blobs) != 2 {
		t.Fatalf("blobs tras papelera = %d, want 2", len(p.Blobs))
	}
	rec = do(h, "GET", "/api/trash", "", c)
	var trash []vfs.TrashItem
	_ = json.NewDecoder(rec.Body).Decode(&trash)
	if len(trash) != 1 || trash[0].File.Name != "playa.jpg" || trash[0].FolderName != "Fotos" || !trash[0].FolderExists ||
		trash[0].PurgeAt-trash[0].File.DeletedAt != int64(vfs.TrashRetention/time.Second) {
		t.Fatalf("papelera = %s", rec.Body)
	}

	// Restaurar vuelve a su carpeta y el enlace revive
	if rec := do(h, "POST", "/api/trash/1/restore", "", c); rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"folder_id":1`)) {
		t.Fatalf("restore = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/public/shares/"+sh.Token, "", nil); rec.Code != 200 {
		t.Fatalf("enlace tras restaurar = %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/trash/1/restore", "", c); rec.Code != 404 {
		t.Fatalf("restaurar fuera de la papelera = %d", rec.Code)
	}
	if rec := do(h, "DELETE", "/api/trash/1", "", c); rec.Code != 404 {
		t.Fatalf("purgar fuera de la papelera = %d", rec.Code)
	}

	// Una carpeta con solo archivos en la papelera se puede borrar; restaurar cae en la raíz
	do(h, "DELETE", "/api/files/1", "", c)
	if rec := do(h, "DELETE", "/api/folders/1", "", c); rec.Code != 204 {
		t.Fatalf("borrar carpeta = %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "GET", "/api/trash", "", c)
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"folder_exists":false`)) {
		t.Fatalf("papelera sin carpeta = %s", rec.Body)
	}
	if rec := do(h, "POST", "/api/trash/1/restore", "", c); !bytes.Contains(rec.Body.Bytes(), []byte(`"folder_id":0`)) {
		t.Fatalf("restore a raíz = %s", rec.Body)
	}

	// Purgar uno destruye sus bloques; vaciar se lleva el resto
	do(h, "DELETE", "/api/files/1", "", c)
	if rec := do(h, "DELETE", "/api/trash/1", "", c); rec.Code != 204 {
		t.Fatalf("purgar = %d", rec.Code)
	}
	if len(p.Blobs) != 0 {
		t.Fatalf("blobs huérfanos: %d", len(p.Blobs))
	}
	for _, name := range []string{"b.bin", "c.bin"} {
		var nf vfs.File
		_ = json.NewDecoder(upload(h, c, "0", name, data, true).Body).Decode(&nf)
		if rec := do(h, "DELETE", fmt.Sprintf("/api/files/%d", nf.ID), "", c); rec.Code != 204 {
			t.Fatalf("delete %s = %d", name, rec.Code)
		}
	}
	if rec := do(h, "DELETE", "/api/trash", "", c); rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"purged":2`)) {
		t.Fatalf("vaciar = %d %s", rec.Code, rec.Body)
	}
	if len(p.Blobs) != 0 {
		t.Fatalf("blobs tras vaciar: %d", len(p.Blobs))
	}
	_ = io.Discard
}

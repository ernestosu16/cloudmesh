package http

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

func TestFoldersAPI(t *testing.T) {
	h, _, c := newFullHandler(t)

	rec := do(h, "POST", "/api/folders", `{"parent_id":0,"name":"Docs"}`, c)
	if rec.Code != 201 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var docs vfs.Folder
	_ = json.NewDecoder(rec.Body).Decode(&docs)

	if rec := do(h, "POST", "/api/folders", `{"parent_id":0,"name":"Docs"}`, c); rec.Code != 409 {
		t.Fatalf("duplicada = %d", rec.Code)
	}
	rec = do(h, "POST", "/api/folders", `{"parent_id":0,"name":" Docs ","existing":true}`, c)
	var again vfs.Folder
	_ = json.NewDecoder(rec.Body).Decode(&again)
	if rec.Code != 200 || again.ID != docs.ID {
		t.Fatalf("existente = %d %+v", rec.Code, again)
	}
	if rec := do(h, "POST", "/api/folders", `{"parent_id":0,"name":"a/b"}`, c); rec.Code != 400 {
		t.Fatalf("inválida = %d", rec.Code)
	}

	rec = do(h, "GET", "/api/folders/0", "", c)
	var root vfs.Listing
	_ = json.NewDecoder(rec.Body).Decode(&root)
	if rec.Code != 200 || len(root.Folders) != 1 || root.Folder.ID != 0 {
		t.Fatalf("root = %d %+v", rec.Code, root)
	}
	if rec := do(h, "GET", "/api/folders/999", "", c); rec.Code != 404 {
		t.Fatalf("inexistente = %d", rec.Code)
	}

	rec = do(h, "PATCH", "/api/folders/1", `{"name":"Documentos"}`, c)
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"name":"Documentos"`)) {
		t.Fatalf("patch = %d %s", rec.Code, rec.Body)
	}

	rec = do(h, "GET", "/api/search?q=doc", "", c)
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`Documentos`)) {
		t.Fatalf("search = %d %s", rec.Code, rec.Body)
	}

	if rec := do(h, "DELETE", "/api/folders/1", "", c); rec.Code != 204 {
		t.Fatalf("delete = %d", rec.Code)
	}
	if rec := do(h, "DELETE", "/api/folders/0", "", c); rec.Code != 404 {
		t.Fatalf("delete raíz = %d", rec.Code)
	}
}

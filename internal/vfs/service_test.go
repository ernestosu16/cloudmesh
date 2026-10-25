package vfs

import (
	"context"
	"errors"
	"testing"

	"github.com/hirasolutions/cloudmesh/internal/db"
	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
)

func newService(t *testing.T) *Service {
	t.Helper()
	sqlDB, err := db.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return NewService(sqlDB)
}

func TestFoldersTree(t *testing.T) {
	s := newService(t)
	ctx := context.Background()

	docs, err := s.CreateFolder(ctx, RootID, " Documentos ")
	if err != nil || docs.Name != "Documentos" {
		t.Fatalf("create = %+v, %v", docs, err)
	}
	if _, err := s.CreateFolder(ctx, RootID, "Documentos"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicada: %v", err)
	}
	if _, err := s.CreateFolder(ctx, RootID, "a/b"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("barra: %v", err)
	}
	if _, err := s.CreateFolder(ctx, 999, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("padre inexistente: %v", err)
	}
	fotos, _ := s.CreateFolder(ctx, docs.ID, "Fotos")
	verano, _ := s.CreateFolder(ctx, fotos.ID, "Verano")

	l, err := s.List(ctx, verano.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Breadcrumbs) != 2 || l.Breadcrumbs[0].ID != docs.ID || l.Breadcrumbs[1].ID != fotos.ID {
		t.Fatalf("breadcrumbs = %+v", l.Breadcrumbs)
	}
	root, _ := s.List(ctx, RootID)
	if len(root.Folders) != 1 || root.Breadcrumbs == nil || len(root.Breadcrumbs) != 0 || root.Files == nil {
		t.Fatalf("root = %+v", root)
	}

	// Mover Documentos dentro de Verano crea un ciclo.
	if _, err := s.UpdateFolder(ctx, docs.ID, nil, &verano.ID); !errors.Is(err, ErrCycle) {
		t.Fatalf("ciclo: %v", err)
	}
	if _, err := s.UpdateFolder(ctx, docs.ID, nil, &docs.ID); !errors.Is(err, ErrCycle) {
		t.Fatalf("en sí misma: %v", err)
	}
	// Mover Verano a la raíz y renombrar.
	name := "Verano 2026"
	rootID := RootID
	got, err := s.UpdateFolder(ctx, verano.ID, &name, &rootID)
	if err != nil || got.ParentID != RootID || got.Name != name {
		t.Fatalf("update = %+v, %v", got, err)
	}

	if err := s.DeleteFolder(ctx, docs.ID); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("no vacía: %v", err)
	}
	if err := s.DeleteFolder(ctx, fotos.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFolder(ctx, fotos.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repetido: %v", err)
	}
	if _, err := s.List(ctx, fotos.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("listar borrada: %v", err)
	}

	res, _ := s.Search(ctx, "ver")
	if len(res.Folders) != 1 || res.Folders[0].ID != verano.ID {
		t.Fatalf("search = %+v", res)
	}
	empty, _ := s.Search(ctx, "  ")
	if len(empty.Folders) != 0 || empty.Files == nil {
		t.Fatalf("search vacío = %+v", empty)
	}
}

func TestDeleteFolderTree(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	docs, _ := s.CreateFolder(ctx, RootID, "Documentos")
	fotos, _ := s.CreateFolder(ctx, docs.ID, "Fotos")
	otra, _ := s.CreateFolder(ctx, RootID, "Otra")
	file := func(folder int64, name string, internal int64) int64 {
		t.Helper()
		f, err := s.q.InsertFile(ctx, sqlcgen.InsertFileParams{FolderID: folder, Name: name, Layout: "rs", Status: "ready", Internal: internal})
		if err != nil {
			t.Fatal(err)
		}
		return f.ID
	}
	a := file(docs.ID, "a.txt", 0)
	b := file(fotos.ID, "b.jpg", 0)
	part := file(docs.ID, "u.00001.part", 1)
	fuera := file(otra.ID, "c.txt", 0)

	if err := s.DeleteFolderTree(ctx, RootID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("raíz: %v", err)
	}
	if ids, _ := s.FolderTree(ctx, docs.ID); len(ids) != 2 {
		t.Fatalf("árbol = %v", ids)
	}
	if err := s.DeleteFolderTree(ctx, docs.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{docs.ID, fotos.ID} {
		if err := s.FolderExists(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("carpeta %d sigue: %v", id, err)
		}
	}
	for _, id := range []int64{a, b} {
		if _, err := s.GetTrashed(ctx, id); err != nil {
			t.Fatalf("archivo %d fuera de la papelera: %v", id, err)
		}
	}
	if _, err := s.GetTrashed(ctx, part); !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("parte interna en la papelera: %v", err)
	}
	if _, err := s.GetFile(ctx, fuera); err != nil {
		t.Fatalf("archivo de otra carpeta: %v", err)
	}
	if err := s.DeleteFolderTree(ctx, docs.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repetido: %v", err)
	}
	if f, err := s.Restore(ctx, b); err != nil || f.FolderID != RootID {
		t.Fatalf("restore = %+v, %v", f, err)
	}
}

func TestEnsureFolder(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	a, _ := s.EnsureFolder(ctx, RootID, "Importados")
	b, _ := s.EnsureFolder(ctx, RootID, "Importados")
	if a.ID != b.ID {
		t.Fatal("EnsureFolder creó dos veces")
	}
}

func TestWithSizes(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	file := func(folder int64, size int64, status string, internal int64) int64 {
		t.Helper()
		f, err := s.q.InsertFile(ctx, sqlcgen.InsertFileParams{FolderID: folder, Name: "f", Size: size, Layout: "rs", Status: status, Internal: internal})
		if err != nil {
			t.Fatal(err)
		}
		return f.ID
	}
	docs, _ := s.CreateFolder(ctx, RootID, "Docs")
	fotos, _ := s.CreateFolder(ctx, docs.ID, "Fotos")
	secret, _ := s.CreateFolder(ctx, docs.ID, "Secreto")
	inner, _ := s.CreateFolder(ctx, secret.ID, "Dentro")
	s.CreateFolder(ctx, RootID, "Vacía")
	file(RootID, 1, "ready", 0)
	file(docs.ID, 10, "ready", 0)
	file(fotos.ID, 100, "degraded", 0)
	file(fotos.ID, 5000, "uploading", 0)
	file(fotos.ID, 6000, "ready", 1)
	if err := s.Trash(ctx, file(fotos.ID, 7000, "ready", 0)); err != nil {
		t.Fatal(err)
	}
	file(secret.ID, 1000, "ready", 0)
	file(inner.ID, 2000, "ready", 0)
	if _, err := s.SetFolderAccess(ctx, secret.ID, true, nil); err != nil {
		t.Fatal(err)
	}

	sizes := func(id int64, visible func(int64, bool) bool) map[string]int64 {
		t.Helper()
		l, err := s.List(ctx, id)
		if err == nil {
			l, err = s.WithSizes(ctx, l, visible)
		}
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int64{}
		for _, f := range l.Folders {
			out[f.Name] = *f.Size
		}
		return out
	}
	all := FullAccess().Sees
	if got := sizes(RootID, all); got["Docs"] != 3110 || got["Vacía"] != 0 {
		t.Fatalf("raíz = %v", got)
	}
	if got := sizes(docs.ID, all); got["Fotos"] != 100 || got["Secreto"] != 3000 {
		t.Fatalf("docs = %v", got)
	}
	if got := sizes(RootID, Open); got["Docs"] != 110 {
		t.Fatalf("enlace = %v", got)
	}
	hidden := Access{hidden: map[int64]bool{secret.ID: true, inner.ID: true}, readOnly: map[int64]bool{}}
	if got := sizes(RootID, hidden.Sees); got["Docs"] != 110 {
		t.Fatalf("sin acceso = %v", got)
	}
}

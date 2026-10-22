package sync

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/accounts"
	"github.com/hirasolutions/cloudmesh/internal/crypto"
	"github.com/hirasolutions/cloudmesh/internal/db"
	"github.com/hirasolutions/cloudmesh/internal/storage"
	"github.com/hirasolutions/cloudmesh/internal/storage/fake"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

func setup(t *testing.T) (*Syncer, *vfs.Service, *fake.Provider, accounts.Account) {
	t.Helper()
	ctx := context.Background()
	sqlDB, err := db.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	box, _ := crypto.NewBox(bytes.Repeat([]byte{1}, 32))
	p := fake.New()
	log := slog.New(slog.DiscardHandler)
	acc := accounts.NewService(sqlDB, box, log, p)
	v := vfs.NewService(sqlDB)
	a, err := acc.Connect(ctx, "fake", "Mega A", "a@x.com", "ok", "")
	if err != nil {
		t.Fatal(err)
	}
	return New(acc, v, log), v, p, a
}

func entry(id, name string, size int64) storage.Entry {
	return storage.Entry{ID: id, Name: name, Size: size, ModTime: time.Unix(1000, 0)}
}

func TestReconcile(t *testing.T) {
	s, v, p, a := setup(t)
	ctx := context.Background()
	root := "root-a@x.com"

	// Dos archivos, una subcarpeta con otro y la carpeta de bloques (ignorada).
	p.SetRemote("a@x.com", root, entry("f1", "uno.txt", 1), entry("f2", "dos.jpg", 2),
		storage.Entry{ID: "d1", Name: "sub", IsDir: true}, storage.Entry{ID: "blk", Name: storage.BlocksFolder, IsDir: true})
	p.SetRemote("a@x.com", "d1", entry("f3", "tres.pdf", 3))
	p.SetRemote("a@x.com", "blk", entry("b1", "1-0-0.cmb", 99))

	res, err := s.SyncAccount(ctx, a.ID)
	if err != nil || res.Added != 3 {
		t.Fatalf("res = %+v, %v", res, err)
	}
	// Caen en Importados/<label>
	rootL, _ := v.List(ctx, vfs.RootID)
	if len(rootL.Folders) != 1 || rootL.Folders[0].Name != ImportsFolder {
		t.Fatalf("root = %+v", rootL.Folders)
	}
	impL, _ := v.List(ctx, rootL.Folders[0].ID)
	if len(impL.Folders) != 1 || impL.Folders[0].Name != "Mega A" {
		t.Fatalf("importados = %+v", impL.Folders)
	}
	accL, _ := v.List(ctx, impL.Folders[0].ID)
	if len(accL.Files) != 3 || accL.Files[0].Mime != "image/jpeg" || accL.Files[0].Layout != "whole" {
		t.Fatalf("files = %+v", accL.Files)
	}

	// Sin cambios: nada que hacer.
	if res, _ = s.SyncAccount(ctx, a.ID); res != (Result{}) {
		t.Fatalf("segunda pasada = %+v", res)
	}

	// Renombrado + borrado remoto.
	p.SetRemote("a@x.com", root, entry("f1", "uno-renombrado.txt", 11), storage.Entry{ID: "d1", Name: "sub", IsDir: true}, storage.Entry{ID: "blk", Name: storage.BlocksFolder, IsDir: true})
	res, err = s.SyncAccount(ctx, a.ID)
	if err != nil || res.Updated != 1 || res.Removed != 1 || res.Added != 0 {
		t.Fatalf("res = %+v, %v", res, err)
	}
	accL, _ = v.List(ctx, impL.Folders[0].ID)
	if len(accL.Files) != 2 || accL.Files[1].Name != "uno-renombrado.txt" || accL.Files[1].Size != 11 {
		t.Fatalf("files = %+v", accL.Files)
	}

	// Búsqueda encuentra archivos.
	sr, _ := v.Search(ctx, "tres")
	if len(sr.Files) != 1 {
		t.Fatalf("search = %+v", sr)
	}
}

func TestExpiredSessionMarksAccount(t *testing.T) {
	s, _, p, a := setup(t)
	ctx := context.Background()
	p.Expire("a@x.com")
	if _, err := s.SyncAccount(ctx, a.ID); err == nil {
		t.Fatal("esperaba error de sesión")
	}
	got, _ := s.accounts.Get(ctx, a.ID)
	if got.Status != accounts.StatusReconnectRequired {
		t.Fatalf("status = %s", got.Status)
	}
}

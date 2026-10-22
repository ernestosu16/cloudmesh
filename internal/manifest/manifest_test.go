package manifest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/hirasolutions/cloudmesh/internal/accounts"
	"github.com/hirasolutions/cloudmesh/internal/auth"
	"github.com/hirasolutions/cloudmesh/internal/crypto"
	"github.com/hirasolutions/cloudmesh/internal/db"
	"github.com/hirasolutions/cloudmesh/internal/settings"
	"github.com/hirasolutions/cloudmesh/internal/storage"
	"github.com/hirasolutions/cloudmesh/internal/storage/fake"
	"github.com/hirasolutions/cloudmesh/internal/transfer"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

type stack struct {
	acc  *accounts.Service
	v    *vfs.Service
	tr   *transfer.Service
	man  *Service
	auth *auth.Service
}

func newStack(t *testing.T, p *fake.Provider) stack {
	t.Helper()
	ctx := context.Background()
	sqlDB, err := db.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	box, _ := crypto.NewBox(bytes.Repeat([]byte{9}, 32))
	log := slog.New(slog.DiscardHandler)
	acc := accounts.NewService(sqlDB, box, log, p)
	v := vfs.NewService(sqlDB)
	st := settings.NewService(sqlDB)
	_, _ = st.Update(ctx, settings.Settings{RoutingPolicy: settings.PolicyMostFree, DataShards: 2, ParityShards: 1, ChunkSize: settings.MinChunk})
	return stack{
		acc: acc, v: v, tr: transfer.New(sqlDB, acc, v, st, box, log, 2, 4),
		man: NewService(sqlDB, box, acc, log), auth: auth.NewService(sqlDB, make([]byte, 32)),
	}
}

func TestWriteAndRestore(t *testing.T) {
	ctx := context.Background()
	p := fake.New()
	p.Quota = storage.Quota{Total: 1 << 30}
	a := newStack(t, p)

	admin, err := a.auth.CreateUser(ctx, "admin@x.com", "contraseña-larga")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.auth.SetName(ctx, admin.ID, "Admin"); err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{"a@x.com", "b@x.com", "c@x.com"} {
		if _, err := a.acc.Connect(ctx, "fake", "", e, "ok", ""); err != nil {
			t.Fatal(err)
		}
	}
	folder, _ := a.v.CreateFolder(ctx, vfs.RootID, "Docs")
	data := bytes.Repeat([]byte("cloudmesh"), 200_000)
	f, err := a.tr.Upload(ctx, folder.ID, "x.txt", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	n, err := a.man.WriteIfChanged(ctx, false)
	if err != nil || n != 3 {
		t.Fatalf("write = %d, %v", n, err)
	}
	// Sin cambios no se reescribe; forzado sí. El sync refresca la cuota, que
	// ahora incluye el propio manifiesto: eso no es un cambio del índice.
	list, _ := a.acc.List(ctx)
	for _, acc := range list {
		if _, err := a.acc.RefreshQuota(ctx, acc.ID); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := a.man.WriteIfChanged(ctx, false); n != 0 {
		t.Fatalf("reescritura innecesaria: %d", n)
	}
	if n, _ := a.man.WriteIfChanged(ctx, true); n != 3 {
		t.Fatalf("forzado = %d", n)
	}
	// Solo queda un manifiesto por cuenta (el anterior se borra).
	count := 0
	for _, entries := range p.Tree["a@x.com"] {
		for _, e := range entries {
			if e.Name == Name {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("manifiestos en la cuenta = %d, want 1", count)
	}
	if a.man.WrittenAt(ctx) == 0 {
		t.Fatal("written_at = 0")
	}

	// Instancia nueva, misma clave maestra, mismo proveedor (los bloques siguen ahí).
	b := newStack(t, p)
	sess, err := p.Login(ctx, "b@x.com", "ok", "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Fetch(ctx, sess)
	if err != nil {
		t.Fatal(err)
	}
	m, err := b.man.Open(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.man.Restore(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := b.man.Restore(ctx, m); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("segundo restore: %v", err)
	}

	// Usuario, carpeta, archivo y bloques están; las cuentas piden reconexión.
	if u, _, err := b.auth.Login(ctx, "admin@x.com", "contraseña-larga", ""); err != nil || u.Name != "Admin" {
		t.Fatalf("login tras restore: %+v %v", u, err)
	}
	l, _ := b.v.List(ctx, folder.ID)
	if len(l.Files) != 1 || l.Files[0].ID != f.ID || l.Files[0].Name != "x.txt" {
		t.Fatalf("listado = %+v", l.Files)
	}
	accs, _ := b.acc.List(ctx)
	if len(accs) != 3 || accs[0].Status != accounts.StatusReconnectRequired {
		t.Fatalf("cuentas = %+v", accs)
	}
	// Tras reconectar, el archivo se lee.
	for _, acc := range accs {
		if _, err := b.acc.Reconnect(ctx, acc.ID, "ok", ""); err != nil {
			t.Fatal(err)
		}
	}
	st, err := b.tr.Open(ctx, f.ID, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(st.Body)
	if !bytes.Equal(got, data) {
		t.Fatal("contenido distinto tras restaurar")
	}

	// Clave maestra distinta: no abre.
	other, _ := crypto.NewBox(bytes.Repeat([]byte{1}, 32))
	if _, err := other.Open(raw); err == nil {
		t.Fatal("abrió con otra clave")
	}
}

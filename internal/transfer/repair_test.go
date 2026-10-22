package transfer

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/settings"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

// loseAccountBlobs borra en el proveedor fake todos los bloques de una cuenta.
func loseAccountBlobs(t *testing.T, e env, accountID int64, fileID int64) int {
	t.Helper()
	rows, err := e.s.q.ListChunkRowsByFile(context.Background(), fileID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range rows {
		if c.AccountID.Valid && c.AccountID.Int64 == accountID {
			delete(e.p.Blobs, c.RemoteID)
			for parent, entries := range e.p.Tree[e.accs[accountIndex(e, accountID)].Email] {
				for i, en := range entries {
					if en.ID == c.RemoteID {
						e.p.Tree[e.accs[accountIndex(e, accountID)].Email][parent] = append(entries[:i:i], entries[i+1:]...)
					}
				}
			}
			n++
		}
	}
	return n
}

func accountIndex(e env, id int64) int {
	for i, a := range e.accs {
		if a.ID == id {
			return i
		}
	}
	return -1
}

func TestScrubAndRebuild(t *testing.T) {
	e := setup(t, 4) // 2+1 sobre 4 cuentas: hay una libre para recolocar
	ctx := context.Background()
	data := randomBytes(2*2*settings.MinChunk + 100) // 3 franjas

	f, err := e.s.Upload(ctx, vfs.RootID, "a.bin", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	// Scrub limpio: nada falta.
	for _, a := range e.accs {
		r, err := e.s.Scrub(ctx, a.ID)
		if err != nil || r.Missing != 0 || r.Orphans != 0 {
			t.Fatalf("scrub limpio %d = %+v, %v", a.ID, r, err)
		}
	}

	// Un huérfano en blocks/ de un archivo inexistente se borra.
	sess, _ := e.s.accounts.Session(ctx, e.accs[0].ID)
	root, _ := e.s.accounts.RootRemoteID(ctx, e.accs[0].ID)
	dir, _ := sess.EnsureDir(ctx, root, "blocks")
	if _, err := sess.Upload(ctx, dir, "999-0-0.cmb", 3, bytes.NewReader([]byte{1, 2, 3})); err != nil {
		t.Fatal(err)
	}
	// Recién subido se respeta (puede ser una reparación a medio apuntar); pasada la gracia, se borra.
	if r, _ := e.s.Scrub(ctx, e.accs[0].ID); r.Orphans != 0 {
		t.Fatalf("huérfano reciente borrado: %d", r.Orphans)
	}
	real := e.s.now
	e.s.now = func() time.Time { return real().Add(orphanGrace + time.Minute) }
	r, _ := e.s.Scrub(ctx, e.accs[0].ID)
	e.s.now = real
	if r.Orphans != 1 {
		t.Fatalf("orphans = %d", r.Orphans)
	}

	// Se pierden los bloques de una cuenta: scrub los marca y el archivo queda degraded.
	lostAcc := e.accs[1].ID
	n := loseAccountBlobs(t, e, lostAcc, f.ID)
	if n == 0 {
		t.Fatal("la cuenta no tenía bloques")
	}
	r, err = e.s.Scrub(ctx, lostAcc)
	if err != nil || r.Missing != n || r.Files != 1 {
		t.Fatalf("scrub = %+v, %v", r, err)
	}
	got, _ := e.v.GetFile(ctx, f.ID)
	if got.Status != StatusDegraded {
		t.Fatalf("status = %s, want degraded", got.Status)
	}
	h, _ := e.s.Health(ctx)
	if h.Files[StatusDegraded] != 1 || h.MissingChunks != int64(n) || len(h.Degraded) != 1 {
		t.Fatalf("health = %+v", h)
	}

	// Sigue legible mientras tanto.
	st, _ := e.s.Open(ctx, f.ID, 0, -1)
	if b := readAll(t, st); !bytes.Equal(b, data) {
		t.Fatal("degradado ilegible")
	}

	// Rebuild recoloca los bloques en la cuenta libre.
	rr, err := e.s.Rebuild(ctx, f.ID)
	if err != nil || rr.Rebuilt != n || rr.Pending != 0 || rr.Status != StatusReady {
		t.Fatalf("rebuild = %+v, %v", rr, err)
	}
	det, _ := e.v.FileDetail(ctx, f.ID)
	for _, c := range det.Chunks {
		if c.Missing || c.AccountID == lostAcc {
			t.Fatalf("bloque sin reparar: %+v", c)
		}
	}
	st, _ = e.s.Open(ctx, f.ID, 0, -1)
	if b := readAll(t, st); !bytes.Equal(b, data) {
		t.Fatal("reparado ilegible")
	}

	// Perder dos cuentas de una franja: lost, rebuild no puede.
	loseAccountBlobs(t, e, e.accs[0].ID, f.ID)
	loseAccountBlobs(t, e, e.accs[2].ID, f.ID)
	e.s.ScrubAll(ctx)
	got, _ = e.v.GetFile(ctx, f.ID)
	if got.Status != StatusLost {
		t.Fatalf("status = %s, want lost", got.Status)
	}
	rr, _ = e.s.Rebuild(ctx, f.ID)
	if rr.Status != StatusLost || rr.Pending == 0 {
		t.Fatalf("rebuild de perdido = %+v", rr)
	}
}

func TestDeleteAccountMarksFiles(t *testing.T) {
	e := setup(t, 3)
	ctx := context.Background()
	data := randomBytes(settings.MinChunk + 5)
	f, err := e.s.Upload(ctx, vfs.RootID, "b.bin", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.accounts.Delete(ctx, e.accs[2].ID); err != nil {
		t.Fatal(err)
	}
	if err := e.s.RecomputeAll(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := e.v.GetFile(ctx, f.ID)
	if got.Status != StatusDegraded {
		t.Fatalf("status = %s, want degraded", got.Status)
	}
	det, _ := e.v.FileDetail(ctx, f.ID)
	deleted := 0
	for _, c := range det.Chunks {
		if c.Missing && c.AccountLabel == "(cuenta eliminada)" {
			deleted++
		}
	}
	if deleted == 0 {
		t.Fatalf("ningún bloque marcado como de cuenta eliminada: %+v", det.Chunks)
	}
	// Sin cuenta libre (solo quedan 2 y ambas tienen bloque de la franja): pendiente.
	rr, _ := e.s.Rebuild(ctx, f.ID)
	if rr.Pending == 0 || rr.Status != StatusDegraded {
		t.Fatalf("rebuild sin cuenta libre = %+v", rr)
	}
	// Llega una cuenta nueva: se repara.
	if _, err := e.s.accounts.Connect(ctx, "fake", "", "d@x.com", "ok", ""); err != nil {
		t.Fatal(err)
	}
	rr, _ = e.s.Rebuild(ctx, f.ID)
	if rr.Pending != 0 || rr.Status != StatusReady {
		t.Fatalf("rebuild con cuenta nueva = %+v", rr)
	}
	st, _ := e.s.Open(ctx, f.ID, 0, -1)
	if b, _ := io.ReadAll(st.Body); !bytes.Equal(b, data) {
		t.Fatal("contenido distinto tras reparar")
	}
}

// downloads suma las descargas servidas por el proveedor fake.
func downloads(e env) int {
	n := 0
	for _, d := range e.p.Downloads {
		n += d
	}
	return n
}

func TestRebuildSkipsUselessDownloads(t *testing.T) {
	e := setup(t, 3) // 2+1 sobre 3 cuentas: perdida una, no queda cuenta libre
	ctx := context.Background()
	data := randomBytes(2*settings.MinChunk + 100)
	f, err := e.s.Upload(ctx, vfs.RootID, "c.bin", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	loseAccountBlobs(t, e, e.accs[0].ID, f.ID)
	e.s.ScrubAll(ctx)

	// Sin cuenta libre no se descarga nada para luego tirarlo.
	before := downloads(e)
	rr, _ := e.s.Rebuild(ctx, f.ID)
	if rr.Pending == 0 || rr.Status != StatusDegraded {
		t.Fatalf("rebuild sin cuenta libre = %+v", rr)
	}
	if d := downloads(e) - before; d != 0 {
		t.Fatalf("descargas sin cuenta libre = %d", d)
	}

	// RebuildAll lo intenta una vez y, sin cambios, no vuelve a llamar a Rebuild.
	if out := e.s.RebuildAll(ctx); len(out) != 1 {
		t.Fatalf("primera pasada = %+v", out)
	}
	if out := e.s.RebuildAll(ctx); len(out) != 0 {
		t.Fatalf("pasada sin cambios reintentó: %+v", out)
	}

	// Llega una cuenta: la huella cambia y se repara.
	if _, err := e.s.accounts.Connect(ctx, "fake", "", "d@x.com", "ok", ""); err != nil {
		t.Fatal(err)
	}
	out := e.s.RebuildAll(ctx)
	if len(out) != 1 || out[0].Pending != 0 || out[0].Status != StatusReady {
		t.Fatalf("pasada con cuenta nueva = %+v", out)
	}
	if len(e.s.rebuildSkip) != 0 {
		t.Fatalf("archivo reparado sigue apartado: %v", e.s.rebuildSkip)
	}
}

func TestRebuildRetriesAfterNetworkOutage(t *testing.T) {
	e := setup(t, 4) // 2+1 sobre 4 cuentas: hay una libre para recolocar
	ctx := context.Background()
	data := randomBytes(2*settings.MinChunk + 100)
	f, err := e.s.Upload(ctx, vfs.RootID, "e.bin", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	loseAccountBlobs(t, e, e.accs[0].ID, f.ID)
	e.s.ScrubAll(ctx)

	// Sin red no se puede leer nada: queda pendiente pero no apartado.
	for _, a := range e.accs {
		e.p.Offline[a.Email] = true
	}
	out := e.s.RebuildAll(ctx)
	if len(out) != 1 || out[0].Pending == 0 || !out[0].Transient {
		t.Fatalf("pasada sin red = %+v", out)
	}
	if len(e.s.rebuildSkip) != 0 {
		t.Fatalf("archivo apartado por un corte de red: %v", e.s.rebuildSkip)
	}

	// Vuelve la red: la siguiente pasada repara sin que cambie nada más.
	clear(e.p.Offline)
	out = e.s.RebuildAll(ctx)
	if len(out) != 1 || out[0].Pending != 0 || out[0].Status != StatusReady {
		t.Fatalf("pasada con red = %+v", out)
	}
}

func TestRebuildLostStripeDoesNotDownload(t *testing.T) {
	e := setup(t, 4)
	ctx := context.Background()
	data := randomBytes(settings.MinChunk + 5) // una franja
	f, err := e.s.Upload(ctx, vfs.RootID, "d.bin", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	// Se pierden dos de los tres bloques: queda uno, menos que k = 2.
	rows, _ := e.s.q.ListChunkRowsByFile(ctx, f.ID)
	loseAccountBlobs(t, e, rows[0].AccountID.Int64, f.ID)
	loseAccountBlobs(t, e, rows[1].AccountID.Int64, f.ID)
	e.s.ScrubAll(ctx)
	before := downloads(e)
	rr, _ := e.s.Rebuild(ctx, f.ID)
	if rr.Status != StatusLost || rr.Pending == 0 {
		t.Fatalf("rebuild de perdido = %+v", rr)
	}
	if d := downloads(e) - before; d != 0 {
		t.Fatalf("descargas de franja perdida = %d", d)
	}
}

package transfer

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/accounts"
	"github.com/hirasolutions/cloudmesh/internal/crypto"
	"github.com/hirasolutions/cloudmesh/internal/db"
	"github.com/hirasolutions/cloudmesh/internal/settings"
	"github.com/hirasolutions/cloudmesh/internal/storage"
	"github.com/hirasolutions/cloudmesh/internal/storage/fake"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

type env struct {
	s    *Service
	v    *vfs.Service
	st   *settings.Service
	p    *fake.Provider
	accs []accounts.Account
}

func setup(t *testing.T, nAccounts int) env {
	t.Helper()
	ctx := context.Background()
	sqlDB, err := db.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	box, _ := crypto.NewBox(bytes.Repeat([]byte{1}, 32))
	p := fake.New()
	p.Quota = storage.Quota{Used: 0, Total: 1 << 30}
	log := slog.New(slog.DiscardHandler)
	acc := accounts.NewService(sqlDB, box, log, p)
	v := vfs.NewService(sqlDB)
	st := settings.NewService(sqlDB)
	if _, err := st.Update(ctx, settings.Settings{RoutingPolicy: settings.PolicyMostFree, DataShards: 2, ParityShards: 1, ChunkSize: settings.MinChunk}); err != nil {
		t.Fatal(err)
	}
	e := env{s: New(sqlDB, acc, v, st, box, log, 2, 4), v: v, st: st, p: p}
	for i := range nAccounts {
		a, err := acc.Connect(ctx, "fake", "", string(rune('a'+i))+"@x.com", "ok", "")
		if err != nil {
			t.Fatal(err)
		}
		e.accs = append(e.accs, a)
	}
	return e
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func readAll(t *testing.T, st *Stream) []byte {
	t.Helper()
	defer st.Body.Close()
	b, err := io.ReadAll(st.Body)
	if err != nil {
		t.Fatalf("leer stream: %v", err)
	}
	return b
}

func TestStripedRoundTrip(t *testing.T) {
	e := setup(t, 3)
	ctx := context.Background()
	// 2 franjas completas (2 MiB cada una) + una parcial de 7 bytes.
	data := randomBytes(2*2*settings.MinChunk + 7)

	f, err := e.s.Upload(ctx, vfs.RootID, "video.mp4", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if f.Layout != LayoutRS || f.Status != StatusReady || f.Size != int64(len(data)) || f.Mime != "video/mp4" {
		t.Fatalf("file = %+v", f)
	}
	det, _ := e.v.FileDetail(ctx, f.ID)
	if len(det.Chunks) != 3*3 {
		t.Fatalf("chunks = %d, want 9", len(det.Chunks))
	}
	// Cada franja reparte sus 3 bloques en 3 cuentas distintas.
	seen := map[int64]map[int64]bool{}
	for _, c := range det.Chunks {
		if seen[c.Stripe] == nil {
			seen[c.Stripe] = map[int64]bool{}
		}
		if seen[c.Stripe][c.AccountID] {
			t.Fatalf("franja %d repite cuenta %d", c.Stripe, c.AccountID)
		}
		seen[c.Stripe][c.AccountID] = true
	}

	st, err := e.s.Open(ctx, f.ID, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, st); !bytes.Equal(got, data) {
		t.Fatal("contenido distinto")
	}

	// Range que cruza franjas.
	off, ln := int64(settings.MinChunk*2-10), int64(25)
	st, _ = e.s.Open(ctx, f.ID, off, ln)
	if got := readAll(t, st); !bytes.Equal(got, data[off:off+ln]) {
		t.Fatal("range distinto")
	}
	// Range hasta el final.
	st, _ = e.s.Open(ctx, f.ID, int64(len(data))-3, -1)
	if got := readAll(t, st); !bytes.Equal(got, data[len(data)-3:]) {
		t.Fatal("cola distinta")
	}

	// Pierde todos los bloques de una cuenta: sigue legible por paridad. Sin
	// vaciar la caché las lecturas no llegarían a los bloques.
	e.s.cache.drop(f.ID)
	lost := e.accs[0].ID
	rows, _ := e.s.q.ListChunks(ctx, f.ID)
	for _, c := range rows {
		if c.AccountID.Int64 == lost {
			e.p.FailDownloads[c.RemoteID] = true
		}
	}
	st, err = e.s.Open(ctx, f.ID, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, st); !bytes.Equal(got, data) {
		t.Fatal("reconstrucción distinta")
	}
	// Pierde una segunda cuenta: ilegible.
	e.s.cache.drop(f.ID)
	for _, c := range rows {
		if c.AccountID.Int64 == e.accs[1].ID {
			e.p.FailDownloads[c.RemoteID] = true
		}
	}
	st, err = e.s.Open(ctx, f.ID, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(st.Body); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("con 2 cuentas caídas: err = %v", err)
	}
	st.Body.Close()
	e.p.FailDownloads = map[string]bool{}

	// Borrar destruye los bloques remotos.
	before := len(e.p.Blobs)
	if err := e.s.Delete(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.p.Blobs) != before-9 {
		t.Fatalf("blobs tras borrar = %d, want %d", len(e.p.Blobs), before-9)
	}
	if _, err := e.v.GetFile(ctx, f.ID); !errors.Is(err, vfs.ErrFileNotFound) {
		t.Fatalf("archivo sigue en el índice: %v", err)
	}
}

func TestSmallFileCopies(t *testing.T) {
	e := setup(t, 3)
	ctx := context.Background()
	data := randomBytes(3000)

	// Con 2+1 un archivo pequeño va en 1+1: dos copias en cuentas distintas.
	f, err := e.s.Upload(ctx, vfs.RootID, "foto.jpg", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if f.DataShards != 1 || f.ParityShards != 1 {
		t.Fatalf("file = %+v", f)
	}
	det, _ := e.v.FileDetail(ctx, f.ID)
	if len(det.Chunks) != 2 || det.Chunks[0].AccountID == det.Chunks[1].AccountID {
		t.Fatalf("chunks = %+v", det.Chunks)
	}

	// Sin una de las copias sigue legible.
	e.s.cache.drop(f.ID)
	rows, _ := e.s.q.ListChunks(ctx, f.ID)
	e.p.FailDownloads[rows[0].RemoteID] = true
	st, err := e.s.Open(ctx, f.ID, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, st); !bytes.Equal(got, data) {
		t.Fatal("contenido distinto")
	}

	// Lo que no cabe en un bloque se trocea como siempre.
	big, err := e.s.Upload(ctx, vfs.RootID, "big.bin", settings.MinChunk+1, bytes.NewReader(randomBytes(settings.MinChunk+1)))
	if err != nil || big.DataShards != 2 || big.ParityShards != 1 {
		t.Fatalf("big = %+v, %v", big, err)
	}
}

func TestBlocksInSubfolders(t *testing.T) {
	e := setup(t, 1)
	ctx := context.Background()
	f, err := e.s.Upload(ctx, vfs.RootID, "a.txt", 10, bytes.NewReader(randomBytes(10)))
	if err != nil {
		t.Fatal(err)
	}
	acc := e.accs[0]
	sess, _ := e.s.accounts.Session(ctx, acc.ID)
	root, _ := e.s.accounts.RootRemoteID(ctx, acc.ID)
	blocks, _ := sess.EnsureDir(ctx, root, "blocks")
	bucket, _ := sess.EnsureDir(ctx, blocks, blockBucket(f.ID))
	rows, _ := e.s.q.ListChunks(ctx, f.ID)
	inBucket := func() bool {
		list, _ := sess.List(ctx, bucket)
		for _, en := range list {
			if en.ID == rows[0].RemoteID {
				return true
			}
		}
		return false
	}
	if !inBucket() {
		t.Fatal("el bloque no está en su subcarpeta")
	}

	// Un bloque suelto en blocks/ (subido antes de las subcarpetas) se recoloca
	// sin cambiar de id y sigue legible.
	if err := sess.Move(ctx, rows[0].RemoteID, blocks); err != nil {
		t.Fatal(err)
	}
	r, err := e.s.Scrub(ctx, acc.ID)
	if err != nil || r.Moved != 1 || r.Missing != 0 || r.Orphans != 0 {
		t.Fatalf("scrub = %+v, %v", r, err)
	}
	if !inBucket() {
		t.Fatal("el bloque suelto no se recolocó")
	}
	e.s.cache.drop(f.ID)
	st, err := e.s.Open(ctx, f.ID, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, st); len(got) != 10 {
		t.Fatalf("leído %d bytes", len(got))
	}
}

func TestSingleAccountEncryptedAndEmpty(t *testing.T) {
	e := setup(t, 1) // menos cuentas que k+m: bloques 1+0, cifrados igual
	ctx := context.Background()
	data := randomBytes(3000)

	f, err := e.s.Upload(ctx, vfs.RootID, "doc.pdf", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if f.Layout != LayoutRS || f.DataShards != 1 || f.ParityShards != 0 {
		t.Fatalf("file = %+v", f)
	}
	det, _ := e.v.FileDetail(ctx, f.ID)
	if len(det.Chunks) != 1 || det.Chunks[0].Size != 3000+sealOverhead {
		t.Fatalf("chunks = %+v", det.Chunks)
	}
	for _, blob := range e.p.Blobs {
		if bytes.Contains(blob, data[:64]) {
			t.Fatal("el proveedor ve el contenido en claro")
		}
	}
	st, _ := e.s.Open(ctx, f.ID, 100, 50)
	if got := readAll(t, st); !bytes.Equal(got, data[100:150]) {
		t.Fatal("rango distinto")
	}

	empty, err := e.s.Upload(ctx, vfs.RootID, "vacio.txt", 0, bytes.NewReader(nil))
	if err != nil || empty.Size != 0 || empty.Status != StatusReady {
		t.Fatalf("vacío = %+v, %v", empty, err)
	}
	st, _ = e.s.Open(ctx, empty.ID, 0, -1)
	if got := readAll(t, st); len(got) != 0 {
		t.Fatal("vacío con contenido")
	}
}

func TestUploadErrors(t *testing.T) {
	e := setup(t, 2)
	ctx := context.Background()
	if _, err := e.s.Upload(ctx, 999, "x.txt", 1, bytes.NewReader([]byte{1})); !errors.Is(err, vfs.ErrNotFound) {
		t.Fatalf("carpeta inexistente: %v", err)
	}
	if _, err := e.s.Upload(ctx, vfs.RootID, "a/b", 1, bytes.NewReader([]byte{1})); !errors.Is(err, vfs.ErrInvalidName) {
		t.Fatalf("nombre inválido: %v", err)
	}
	// Sin espacio: dos cuentas de 1 GiB, archivo mayor que las dos juntas.
	if _, err := e.s.Upload(ctx, vfs.RootID, "grande.bin", 3<<30, bytes.NewReader(nil)); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("sin espacio: %v", err)
	}
	// Cuerpo más corto que el tamaño declarado: falla y no deja rastro.
	if _, err := e.s.Upload(ctx, vfs.RootID, "corto.bin", 100, bytes.NewReader([]byte{1, 2, 3})); err == nil {
		t.Fatal("cuerpo corto aceptado")
	}
	l, _ := e.v.List(ctx, vfs.RootID)
	if len(l.Files) != 0 || len(e.p.Blobs) != 0 {
		t.Fatalf("quedaron restos: files=%d blobs=%d", len(l.Files), len(e.p.Blobs))
	}
}

// Sin cuentas activas no se acepta ningún archivo, ni siquiera de 0 bytes:
// no debe quedar fila en el índice.
func TestUploadWithoutAccounts(t *testing.T) {
	e := setup(t, 0)
	ctx := context.Background()
	for _, size := range []int64{0, 1} {
		if _, err := e.s.Upload(ctx, vfs.RootID, "x.txt", size, bytes.NewReader(make([]byte, size))); !errors.Is(err, ErrNoAccounts) {
			t.Fatalf("tamaño %d sin cuentas: %v", size, err)
		}
	}
	l, _ := e.v.List(ctx, vfs.RootID)
	if len(l.Files) != 0 {
		t.Fatalf("quedaron archivos: %d", len(l.Files))
	}
}

func TestRoutingPolicies(t *testing.T) {
	e := setup(t, 3)
	ctx := context.Background()
	// 1+0 para que cada archivo caiga en una sola cuenta.
	if _, err := e.st.Update(ctx, settings.Settings{RoutingPolicy: settings.PolicyRoundRobin, DataShards: 1, ParityShards: 0, ChunkSize: settings.MinChunk}); err != nil {
		t.Fatal(err)
	}
	used := map[int64]int{}
	for i := range 6 {
		f, err := e.s.Upload(ctx, vfs.RootID, "f"+string(rune('0'+i))+".txt", 10, bytes.NewReader(randomBytes(10)))
		if err != nil {
			t.Fatal(err)
		}
		det, _ := e.v.FileDetail(ctx, f.ID)
		used[det.Chunks[0].AccountID]++
	}
	for _, a := range e.accs {
		if used[a.ID] != 2 {
			t.Fatalf("round robin desigual: %v", used)
		}
	}
}

func TestPurgeTrashByAge(t *testing.T) {
	e := setup(t, 3)
	ctx := context.Background()
	data := randomBytes(3 << 20)
	f, err := e.s.Upload(ctx, vfs.RootID, "viejo.bin", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.v.Trash(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	blobs := len(e.p.Blobs)

	// Recién borrado: la purga del mantenimiento no lo toca.
	if n, err := e.s.PurgeTrash(ctx, time.Now().Add(-vfs.TrashRetention)); err != nil || n != 0 || len(e.p.Blobs) != blobs {
		t.Fatalf("purga temprana = %d, %v, blobs %d", n, err, len(e.p.Blobs))
	}
	// Pasado el plazo se destruye con sus bloques.
	if n, err := e.s.PurgeTrash(ctx, time.Now().Add(time.Second)); err != nil || n != 1 || len(e.p.Blobs) != 0 {
		t.Fatalf("purga = %d, %v, blobs %d", n, err, len(e.p.Blobs))
	}
	if _, err := e.v.GetTrashed(ctx, f.ID); !errors.Is(err, vfs.ErrFileNotFound) {
		t.Fatalf("sigue en la papelera: %v", err)
	}
}

func TestPickRespectsContribution(t *testing.T) {
	e := setup(t, 3)
	ctx := context.Background()
	accs, err := e.s.accounts.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// La cuenta está llena en el proveedor (lo ajeno ocupa todo): aunque aporte 1 GiB, no cabe nada.
	e.p.Quota = storage.Quota{Used: 20 << 30, Total: 20 << 30}
	if _, err := e.s.accounts.RefreshQuota(ctx, accs[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.accounts.UpdateSettings(ctx, accs[0].ID, accounts.Settings{
		Label: "lleno", Priority: accs[0].Priority, Enabled: true, ContribMode: accounts.ContribLimit, ContribValue: 1 << 30,
	}); err != nil {
		t.Fatal(err)
	}
	// 2+1 necesita tres cuentas con hueco y solo quedan dos (el archivo no
	// cabe en un bloque, así que no va en copias).
	data := randomBytes(settings.MinChunk + 1)
	if _, err := e.s.Upload(ctx, vfs.RootID, "x.bin", int64(len(data)), bytes.NewReader(data)); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("upload = %v, want ErrNoSpace", err)
	}
	// Con sitio en la cuenta real y sin límite de aporte vuelve a entrar.
	e.p.Quota = storage.Quota{Used: 2 << 30, Total: 20 << 30}
	if _, err := e.s.accounts.RefreshQuota(ctx, accs[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.accounts.UpdateSettings(ctx, accs[0].ID, accounts.Settings{
		Label: "lleno", Priority: accs[0].Priority, Enabled: true, ContribMode: accounts.ContribAll,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Upload(ctx, vfs.RootID, "x.bin", int64(len(data)), bytes.NewReader(data)); err != nil {
		t.Fatalf("upload con todo el espacio = %v", err)
	}
}

// Lo ajeno sale del último sondeo; lo propio crece al subir. El uso de la cuenta
// real debe seguir a las subidas sin esperar al siguiente sondeo.
func TestProviderUsedFollowsOwnUploads(t *testing.T) {
	e := setup(t, 3)
	ctx := context.Background()
	before, err := e.s.accounts.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data := randomBytes(1 << 20)
	if _, err := e.s.Upload(ctx, vfs.RootID, "x.bin", int64(len(data)), bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	after, err := e.s.accounts.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	grew := false
	for i := range after {
		own := after[i].QuotaUsed - before[i].QuotaUsed
		if after[i].ProviderUsed-before[i].ProviderUsed != own {
			t.Fatalf("cuenta %d: crece %d en la cuenta real y %d propios", after[i].ID, after[i].ProviderUsed-before[i].ProviderUsed, own)
		}
		grew = grew || own > 0
	}
	if !grew {
		t.Fatal("ninguna cuenta registra bloques propios")
	}
}

func TestComposeParts(t *testing.T) {
	e := setup(t, 3)
	ctx := context.Background()
	whole := randomBytes(5<<20 + 12345)
	cuts := []int{0, 2 << 20, 4<<20 + 7, len(whole)}
	var parts []Part
	for i := 0; i+1 < len(cuts); i++ {
		chunk := whole[cuts[i]:cuts[i+1]]
		f, err := e.s.UploadPart(ctx, Owner{}, vfs.RootID, fmt.Sprintf("x.part%d", i+1), int64(len(chunk)), bytes.NewReader(chunk))
		if err != nil {
			t.Fatal(err)
		}
		sum := md5.Sum(chunk)
		if f.ETag != hex.EncodeToString(sum[:]) || !f.Internal {
			t.Fatalf("parte = %+v", f)
		}
		if _, err := e.v.GetFile(ctx, f.ID); !errors.Is(err, vfs.ErrFileNotFound) {
			t.Fatalf("la parte no debe verse: %v", err)
		}
		parts = append(parts, Part{Number: int64(i + 1), FileID: f.ID, Size: int64(len(chunk))})
	}
	if l, _ := e.v.List(ctx, vfs.RootID); len(l.Files) != 0 {
		t.Fatalf("partes en el listado: %+v", l.Files)
	}
	obj, err := e.s.Compose(ctx, 0, vfs.RootID, "x.bin", parts, "abc-3")
	if err != nil {
		t.Fatal(err)
	}
	if obj.Size != int64(len(whole)) || obj.Layout != LayoutParts || obj.ETag != "abc-3" || obj.Status != StatusReady {
		t.Fatalf("objeto = %+v", obj)
	}
	read := func(off, n int64) []byte {
		st, err := e.s.Open(ctx, obj.ID, off, n)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Body.Close()
		b, err := io.ReadAll(st.Body)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if !bytes.Equal(read(0, -1), whole) {
		t.Fatal("contenido completo distinto")
	}
	// Rango que cruza las tres partes y otro dentro de la última.
	if got := read(2<<20-10, 2<<20+30); !bytes.Equal(got, whole[2<<20-10:4<<20+20]) {
		t.Fatalf("rango cruzado: %d bytes", len(got))
	}
	if got := read(int64(len(whole))-5, 5); !bytes.Equal(got, whole[len(whole)-5:]) {
		t.Fatal("rango final distinto")
	}
	if err := e.s.Delete(ctx, obj.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.p.Blobs) != 0 {
		t.Fatalf("blobs tras borrar el compuesto: %d", len(e.p.Blobs))
	}
}

// Cada esquema de la página de almacenamiento sube k+m bloques por franja en
// cuentas distintas y aguanta exactamente m cuentas caídas.
func TestSchemes(t *testing.T) {
	for _, sc := range []struct {
		name string
		k, m int
	}{
		{"copia doble", 1, 1},
		{"copia triple", 1, 2},
		{"paridad simple", 2, 1},
		{"paridad doble", 3, 2},
	} {
		t.Run(sc.name, func(t *testing.T) {
			e := setup(t, sc.k+sc.m)
			ctx := context.Background()
			if _, err := e.st.Update(ctx, settings.Settings{RoutingPolicy: settings.PolicyMostFree, DataShards: sc.k, ParityShards: sc.m, ChunkSize: settings.MinChunk}); err != nil {
				t.Fatal(err)
			}
			stripe := sc.k * settings.MinChunk
			data := randomBytes(2*stripe + 7)
			f, err := e.s.Upload(ctx, vfs.RootID, "a.bin", int64(len(data)), bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if f.DataShards != int64(sc.k) || f.ParityShards != int64(sc.m) {
				t.Fatalf("archivo %d+%d, want %d+%d", f.DataShards, f.ParityShards, sc.k, sc.m)
			}
			rows, _ := e.s.q.ListChunks(ctx, f.ID)
			if len(rows) != 3*(sc.k+sc.m) {
				t.Fatalf("bloques = %d, want %d", len(rows), 3*(sc.k+sc.m))
			}
			accs := map[int64]map[int64]bool{}
			parity := map[int64]int{}
			for _, c := range rows {
				if accs[c.Stripe] == nil {
					accs[c.Stripe] = map[int64]bool{}
				}
				if accs[c.Stripe][c.AccountID.Int64] {
					t.Fatalf("franja %d repite cuenta %d", c.Stripe, c.AccountID.Int64)
				}
				accs[c.Stripe][c.AccountID.Int64] = true
				if c.Kind == "parity" {
					parity[c.Stripe]++
				}
			}
			for s, n := range parity {
				if n != sc.m {
					t.Fatalf("franja %d: %d de paridad, want %d", s, n, sc.m)
				}
			}

			fail := func(n int) {
				e.s.cache.drop(f.ID)
				for _, a := range e.accs[:n] {
					for _, c := range rows {
						if c.AccountID.Int64 == a.ID {
							e.p.FailDownloads[c.RemoteID] = true
						}
					}
				}
			}
			fail(sc.m)
			st, err := e.s.Open(ctx, f.ID, 0, -1)
			if err != nil {
				t.Fatal(err)
			}
			if got := readAll(t, st); !bytes.Equal(got, data) {
				t.Fatalf("con %d cuentas caídas: contenido distinto", sc.m)
			}
			fail(sc.m + 1)
			st, err = e.s.Open(ctx, f.ID, 0, -1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadAll(st.Body); !errors.Is(err, ErrUnreadable) {
				t.Fatalf("con %d cuentas caídas: err = %v", sc.m+1, err)
			}
			st.Body.Close()
		})
	}
}

func TestFormat(t *testing.T) {
	e := setup(t, 3)
	ctx := context.Background()
	dir, err := e.v.CreateFolder(ctx, vfs.RootID, "fotos")
	if err != nil {
		t.Fatal(err)
	}
	big := randomBytes(2*settings.MinChunk + 7)
	if _, err := e.s.Upload(ctx, dir.ID, "big.bin", int64(len(big)), bytes.NewReader(big)); err != nil {
		t.Fatal(err)
	}
	small, err := e.s.Upload(ctx, vfs.RootID, "a.txt", 3, bytes.NewReader([]byte("abc")))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.v.Trash(ctx, small.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.p.Blobs) == 0 {
		t.Fatal("sin bloques antes de formatear")
	}

	n, err := e.s.Format(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("borrados = %d, want 2", n)
	}
	if len(e.p.Blobs) != 0 {
		t.Fatalf("quedan %d bloques en el proveedor", len(e.p.Blobs))
	}
	if c, _ := e.s.q.CountFiles(ctx); c != 0 {
		t.Fatalf("quedan %d archivos en el índice", c)
	}
	l, err := e.v.List(ctx, vfs.RootID)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Folders) != 0 || len(l.Files) != 0 {
		t.Fatalf("raíz no vacía: %+v", l)
	}
	// El índice vuelve a empezar: los ids arrancan de nuevo en 1.
	f, err := e.s.Upload(ctx, vfs.RootID, "b.txt", 3, bytes.NewReader([]byte("xyz")))
	if err != nil {
		t.Fatal(err)
	}
	if f.ID != 1 {
		t.Fatalf("id tras formatear = %d, want 1", f.ID)
	}
}

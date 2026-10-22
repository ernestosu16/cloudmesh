package transfer

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

func (e env) downloads() int {
	n := 0
	for _, d := range e.p.Downloads {
		n += d
	}
	return n
}

func TestStripeCacheServesRepeatedReads(t *testing.T) {
	e := setup(t, 3)
	ctx := context.Background()
	data := randomBytes(700_000)
	f, err := e.s.Upload(ctx, vfs.RootID, "doc.pdf", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	// La subida deja el archivo en la caché: leerlo enseguida no baja nada.
	before := e.downloads()
	st, err := e.s.Open(ctx, f.ID, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, st); !bytes.Equal(got, data) || e.downloads() != before {
		t.Fatalf("lectura tras subir: %d descargas", e.downloads()-before)
	}
	e.s.cache.drop(f.ID)

	// Un visor pide a la vez el archivo y varios rangos: una sola descarga
	// (archivo pequeño en copias, k = 1).
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			off := int64(i * 1000)
			st, err := e.s.Open(ctx, f.ID, off, 500)
			if err != nil {
				t.Error(err)
				return
			}
			if got := readAll(t, st); !bytes.Equal(got, data[off:off+500]) {
				t.Errorf("rango %d distinto", i)
			}
		}()
	}
	wg.Wait()
	if got := e.downloads() - before; got != 1 {
		t.Fatalf("descargas = %d, want 1 (una copia una vez)", got)
	}

	// Lo siguiente sale de memoria aunque los bloques ya no se puedan leer.
	rows, _ := e.s.q.ListChunks(ctx, f.ID)
	for _, c := range rows {
		e.p.FailDownloads[c.RemoteID] = true
	}
	st, err = e.s.Open(ctx, f.ID, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, st); !bytes.Equal(got, data) {
		t.Fatal("contenido desde la caché distinto")
	}

	// Borrar el archivo lo saca de la caché.
	if err := e.s.Delete(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.s.cache.get(stripeKey{file: f.ID, sha: f.SHA256, stripe: 0}); ok {
		t.Fatal("la franja sigue en la caché tras borrar")
	}
}

func TestStripeCacheEvictsLeastRecent(t *testing.T) {
	c := newStripeCache(10)
	k := func(i int64) stripeKey { return stripeKey{file: i, sha: "x"} }
	c.put(k(1), make([]byte, 4))
	c.put(k(2), make([]byte, 4))
	c.get(k(1)) // la 2 pasa a ser la menos reciente
	c.put(k(3), make([]byte, 4))
	if _, ok := c.get(k(2)); ok {
		t.Fatal("no se expulsó la menos reciente")
	}
	if _, ok := c.get(k(1)); !ok {
		t.Fatal("se expulsó una reciente")
	}
	if c.used != 8 {
		t.Fatalf("used = %d, want 8", c.used)
	}
	c.put(k(4), make([]byte, 11)) // no cabe nunca: ni entra ni expulsa
	if _, ok := c.get(k(4)); ok || c.used != 8 {
		t.Fatalf("entrada mayor que el tope: used = %d", c.used)
	}
}

func TestStripeCacheReportsSource(t *testing.T) {
	c := newStripeCache(10)
	k := stripeKey{file: 1, sha: "x"}
	fetch := func(context.Context) ([]byte, error) { return []byte("abc"), nil }
	if _, src, _ := c.load(context.Background(), k, fetch); src != sourceFetched {
		t.Fatalf("primera lectura: source = %q", src)
	}
	if b, src, _ := c.load(context.Background(), k, fetch); src != sourceCache || string(b) != "abc" {
		t.Fatalf("segunda lectura: source = %q", src)
	}
}

// Un archivo de varias franjas entra franja a franja y cada una coincide con
// lo que daría reconstruirla.
func TestUploadWarmsEveryStripe(t *testing.T) {
	e := setup(t, 3)
	ctx := context.Background()
	data := randomBytes(2*2<<20 + 7) // 2 franjas de 2 MiB y una de 7 bytes
	f, err := e.s.Upload(ctx, vfs.RootID, "x.bin", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	for stripe, want := range [][]byte{data[:2<<20], data[2<<20 : 4<<20], data[4<<20:]} {
		got, ok := e.s.cache.get(stripeKey{file: f.ID, sha: f.SHA256, stripe: int64(stripe)})
		if !ok || !bytes.Equal(got, want) {
			t.Fatalf("franja %d: en caché = %v", stripe, ok)
		}
	}
	// Lo que hay en caché es lo mismo que sale de los bloques.
	e.s.cache.drop(f.ID)
	st, _ := e.s.Open(ctx, f.ID, 0, -1)
	if !bytes.Equal(readAll(t, st), data) {
		t.Fatal("contenido reconstruido distinto")
	}
}

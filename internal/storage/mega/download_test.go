package mega

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDownload trocea data como go-mega y cuenta cuántos trozos se piden a la vez.
type fakeDownload struct {
	data     []byte
	sizes    []int
	delay    time.Duration
	fail     int // id del trozo que falla; -1 ninguno
	inFlight atomic.Int32
	peak     atomic.Int32
	finishes atomic.Int32

	mu    sync.Mutex
	asked []int
}

func newFakeDownload(data []byte, sizes ...int) *fakeDownload {
	return &fakeDownload{data: data, sizes: sizes, fail: -1}
}

func (f *fakeDownload) Chunks() int { return len(f.sizes) }

func (f *fakeDownload) ChunkLocation(id int) (int64, int, error) {
	pos := 0
	for _, s := range f.sizes[:id] {
		pos += s
	}
	return int64(pos), f.sizes[id], nil
}

func (f *fakeDownload) DownloadChunk(id int) ([]byte, error) {
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	f.mu.Lock()
	f.asked = append(f.asked, id)
	f.mu.Unlock()
	// Los primeros trozos tardan más: llegan desordenados.
	time.Sleep(f.delay * time.Duration(len(f.sizes)-id))
	if id == f.fail {
		return nil, errors.New("Http Status: 500")
	}
	pos, size, _ := f.ChunkLocation(id)
	return bytes.Clone(f.data[pos : pos+int64(size)]), nil
}

func (f *fakeDownload) Finish() error {
	f.finishes.Add(1)
	return nil
}

func TestPumpParallelInOrder(t *testing.T) {
	data := make([]byte, 100)
	for i := range data {
		data[i] = byte(i)
	}
	sizes := []int{10, 10, 10, 10, 10, 10, 10, 10, 20}
	for _, tc := range []struct{ offset, length int64 }{{0, 100}, {15, 50}, {95, 5}, {0, 1}} {
		d := newFakeDownload(data, sizes...)
		d.delay = 2 * time.Millisecond
		l := &link{d: d}
		var out bytes.Buffer
		if err := pump(context.Background(), l, 100, tc.offset, tc.length, &out); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out.Bytes(), data[tc.offset:tc.offset+tc.length]) {
			t.Fatalf("[%d,+%d) = %v", tc.offset, tc.length, out.Bytes())
		}
		if p := d.peak.Load(); p > downloadWorkers {
			t.Fatalf("%d trozos a la vez, máximo %d", p, downloadWorkers)
		}
		// Solo se piden los trozos del rango y el MAC solo con el archivo entero.
		if tc.offset == 15 && len(d.asked) != 6 {
			t.Fatalf("trozos pedidos = %v", d.asked)
		}
		want := int32(0)
		if tc.length == 100 {
			want = 1
		}
		if got := d.finishes.Load(); got != want {
			t.Fatalf("[%d,+%d): Finish = %d, want %d", tc.offset, tc.length, got, want)
		}
	}

	// Con varios trozos ya hay paralelismo.
	d := newFakeDownload(data, sizes...)
	d.delay = 2 * time.Millisecond
	if err := pump(context.Background(), &link{d: d}, 100, 0, 100, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if d.peak.Load() < 2 {
		t.Fatal("los trozos se pidieron en serie")
	}
}

func TestPumpChunkError(t *testing.T) {
	d := newFakeDownload(make([]byte, 40), 10, 10, 10, 10)
	d.fail = 1
	var out bytes.Buffer
	if err := pump(context.Background(), &link{d: d}, 40, 0, 40, &out); err == nil {
		t.Fatal("se esperaba error")
	}
	if out.Len() > 10 {
		t.Fatalf("se escribió más allá del trozo roto: %d bytes", out.Len())
	}
}

func TestLinkReuseAndExpiry(t *testing.T) {
	now := time.Unix(0, 0)
	c := links{now: func() time.Time { return now }}
	opens := 0
	open := func() (chunkSource, error) {
		opens++
		return newFakeDownload(make([]byte, 10), 10), nil
	}
	a, _ := c.get("n1", open)
	b, _ := c.get("n1", open)
	if a != b || opens != 1 {
		t.Fatalf("enlace no reutilizado: opens = %d", opens)
	}
	now = now.Add(linkTTL)
	if c2, _ := c.get("n1", open); c2 == a || opens != 2 {
		t.Fatal("enlace caducado reutilizado")
	}
	c.forget("n1", a) // ya no es el vigente: no se toca
	if _, ok := c.m["n1"]; !ok {
		t.Fatal("forget borró otro enlace")
	}
	c.forget("n1", nil)
	if _, ok := c.m["n1"]; ok {
		t.Fatal("forget no borró")
	}

	// El MAC se comprueba una sola vez por enlace.
	d := newFakeDownload(make([]byte, 10), 10)
	l := &link{d: d}
	for range 2 {
		if err := pump(context.Background(), l, 10, 0, 10, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	if d.finishes.Load() != 1 {
		t.Fatalf("Finish = %d, want 1", d.finishes.Load())
	}
}

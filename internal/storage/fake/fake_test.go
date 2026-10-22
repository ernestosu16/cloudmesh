package fake

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func TestPersistentRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	p, err := NewPersistent(dir)
	if err != nil {
		t.Fatal(err)
	}
	baseUsed := p.Quota.Used
	sess, _ := p.Login(ctx, "a@x.com", "ok", "")
	root, _ := sess.EnsureRoot(ctx)
	data := bytes.Repeat([]byte{7}, 5000)
	e, err := sess.Upload(ctx, root, "f.bin", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Blobs) != 0 {
		t.Fatal("el modo persistente no debe retener blobs en memoria")
	}

	// Nuevo proceso: mismo directorio.
	p2, err := NewPersistent(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess2, _ := p2.Login(ctx, "a@x.com", "ok", "")
	entries, _ := sess2.List(ctx, root)
	if len(entries) != 1 || entries[0].ID != e.ID {
		t.Fatalf("árbol no persistido: %+v", entries)
	}
	rc, err := sess2.Download(ctx, e.ID, 100, 50)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, data[100:150]) {
		t.Fatal("rango distinto")
	}
	if err := sess2.Delete(ctx, e.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := sess2.Download(ctx, e.ID, 0, -1); err == nil {
		t.Fatal("blob borrado sigue legible")
	}
	if p2.Quota.Used != baseUsed {
		t.Fatalf("quota usada = %d, want %d", p2.Quota.Used, baseUsed)
	}
}

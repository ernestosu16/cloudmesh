package crypto

import (
	"bytes"
	"errors"
	"testing"
)

func TestSealOpen(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	b, err := NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := b.Seal([]byte("secreto"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := b.Open(ct)
	if err != nil || string(pt) != "secreto" {
		t.Fatalf("open = %q, %v", pt, err)
	}

	ct[len(ct)-1] ^= 1
	if _, err := b.Open(ct); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("alterado: err = %v", err)
	}

	other, _ := NewBox(bytes.Repeat([]byte{8}, 32))
	ct, _ = b.Seal([]byte("x"))
	if _, err := other.Open(ct); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("otra clave: err = %v", err)
	}

	if _, err := NewBox([]byte("corta")); err == nil {
		t.Fatal("clave corta aceptada")
	}
}

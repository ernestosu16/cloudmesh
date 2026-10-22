package erasure

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"testing"
)

func TestRoundTripAndReconstruct(t *testing.T) {
	for _, tc := range []struct{ k, m, n int }{{1, 0, 1000}, {2, 1, 1001}, {4, 2, 5}, {4, 2, 1 << 20}, {3, 0, 10}} {
		c, err := New(tc.k, tc.m)
		if err != nil {
			t.Fatal(err)
		}
		data := make([]byte, tc.n)
		rand.Read(data)
		shards, err := c.Encode(data)
		if err != nil {
			t.Fatal(err)
		}
		if len(shards) != tc.k+tc.m {
			t.Fatalf("%d+%d: %d bloques", tc.k, tc.m, len(shards))
		}
		// Sin pérdidas
		got, err := c.Reconstruct(clone(shards), tc.n)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("%d+%d sin pérdidas: %v", tc.k, tc.m, err)
		}
		// Perder m bloques cualesquiera (los primeros, de datos)
		lost := clone(shards)
		for i := range tc.m {
			lost[i] = nil
		}
		got, err = c.Reconstruct(lost, tc.n)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("%d+%d con %d perdidos: %v", tc.k, tc.m, tc.m, err)
		}
		// Perder m+1 falla
		lost = clone(shards)
		for i := range tc.m + 1 {
			if i < len(lost) {
				lost[i] = nil
			}
		}
		if _, err := c.Reconstruct(lost, tc.n); !errors.Is(err, ErrTooFewShards) && tc.k+tc.m > tc.m+1 {
			t.Fatalf("%d+%d con %d perdidos: err = %v", tc.k, tc.m, tc.m+1, err)
		}
	}
}

func TestInvalid(t *testing.T) {
	if _, err := New(0, 1); err == nil {
		t.Fatal("k=0 aceptado")
	}
	if _, err := New(30, 10); err == nil {
		t.Fatal("k+m > MaxShards aceptado")
	}
}

func clone(s [][]byte) [][]byte {
	out := make([][]byte, len(s))
	for i, b := range s {
		if b != nil {
			out[i] = append([]byte(nil), b...)
		}
	}
	return out
}

// Benchmarks: go test ./internal/erasure -bench . -benchmem
// Miden codificación y hash por franja de 64 MiB (2+1 y 4+2) y SHA-256.

func benchEncode(b *testing.B, k, m int) {
	c, _ := New(k, m)
	stripe := make([]byte, 64<<20)
	rand.Read(stripe)
	b.SetBytes(int64(len(stripe)))
	b.ResetTimer()
	for range b.N {
		if _, err := c.Encode(stripe); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncode2x1(b *testing.B) { benchEncode(b, 2, 1) }
func BenchmarkEncode4x2(b *testing.B) { benchEncode(b, 4, 2) }

func BenchmarkReconstruct4x2(b *testing.B) {
	c, _ := New(4, 2)
	stripe := make([]byte, 64<<20)
	rand.Read(stripe)
	shards, _ := c.Encode(stripe)
	b.SetBytes(int64(len(stripe)))
	b.ResetTimer()
	for range b.N {
		lost := clone(shards)
		lost[0], lost[3] = nil, nil
		if _, err := c.Reconstruct(lost, len(stripe)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSHA256(b *testing.B) {
	buf := make([]byte, 64<<20)
	rand.Read(buf)
	b.SetBytes(int64(len(buf)))
	b.ResetTimer()
	for range b.N {
		sha256.Sum256(buf)
	}
}

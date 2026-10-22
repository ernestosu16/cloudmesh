// Package erasure parte franjas en bloques de datos y paridad Reed–Solomon
// y las reconstruye cuando faltan bloques. Con m = 0 es un simple troceo.
package erasure

import (
	"errors"
	"fmt"

	"github.com/klauspost/reedsolomon"
)

// MaxShards acota k+m; Mega y el enrutado no tienen sentido con más cuentas.
const MaxShards = 32

var ErrTooFewShards = errors.New("no hay bloques suficientes para reconstruir")

// Codec codifica franjas en k bloques de datos y m de paridad.
type Codec struct {
	K, M int
	enc  reedsolomon.Encoder // nil cuando M == 0
}

func New(k, m int) (*Codec, error) {
	if k < 1 || m < 0 || k+m > MaxShards {
		return nil, fmt.Errorf("k+m inválido: %d+%d", k, m)
	}
	c := &Codec{K: k, M: m}
	if m > 0 {
		enc, err := reedsolomon.New(k, m)
		if err != nil {
			return nil, err
		}
		c.enc = enc
	}
	return c, nil
}

// ShardSize es el tamaño de cada bloque para una franja de n bytes.
func (c *Codec) ShardSize(n int) int {
	return (n + c.K - 1) / c.K
}

// Encode parte stripe en k bloques iguales (rellenando el último con ceros)
// y añade m de paridad. Devuelve k+m bloques del mismo tamaño.
func (c *Codec) Encode(stripe []byte) ([][]byte, error) {
	if len(stripe) == 0 {
		return nil, errors.New("franja vacía")
	}
	size := c.ShardSize(len(stripe))
	shards := make([][]byte, c.K+c.M)
	// Franja completa: los bloques de datos son vistas del propio buffer, sin copia.
	// Solo la última franja (más corta) necesita relleno a cero.
	data := stripe
	if len(stripe) != size*c.K {
		data = make([]byte, size*c.K)
		copy(data, stripe)
	}
	for i := range c.K {
		shards[i] = data[i*size : (i+1)*size]
	}
	for i := c.K; i < c.K+c.M; i++ {
		shards[i] = make([]byte, size)
	}
	if c.enc != nil {
		if err := c.enc.Encode(shards); err != nil {
			return nil, err
		}
	}
	return shards, nil
}

// Reconstruct rellena los bloques nil a partir de los presentes. Necesita al
// menos k. Devuelve los datos originales concatenados, recortados a n bytes.
func (c *Codec) Reconstruct(shards [][]byte, n int) ([]byte, error) {
	if len(shards) != c.K+c.M {
		return nil, fmt.Errorf("se esperaban %d bloques, hay %d", c.K+c.M, len(shards))
	}
	present := 0
	for _, s := range shards {
		if s != nil {
			present++
		}
	}
	if present < c.K {
		return nil, ErrTooFewShards
	}
	if c.enc != nil {
		missingData := false
		for i := range c.K {
			if shards[i] == nil {
				missingData = true
				break
			}
		}
		if missingData {
			if err := c.enc.ReconstructData(shards); err != nil {
				return nil, err
			}
		}
	}
	out := make([]byte, 0, n)
	for i := range c.K {
		out = append(out, shards[i]...)
	}
	if len(out) < n {
		return nil, errors.New("datos reconstruidos más cortos que la franja")
	}
	return out[:n], nil
}

// Rebuild rellena en sitio todos los bloques nil (datos y paridad) a partir
// de al menos k presentes. Con m = 0 no hay nada que reconstruir.
func (c *Codec) Rebuild(shards [][]byte) error {
	if len(shards) != c.K+c.M {
		return fmt.Errorf("se esperaban %d bloques, hay %d", c.K+c.M, len(shards))
	}
	present := 0
	for _, s := range shards {
		if s != nil {
			present++
		}
	}
	if present < c.K {
		return ErrTooFewShards
	}
	if present == len(shards) {
		return nil
	}
	if c.enc == nil {
		return ErrTooFewShards
	}
	return c.enc.Reconstruct(shards)
}

// Verify comprueba que la paridad es coherente con los datos (todos presentes).
func (c *Codec) Verify(shards [][]byte) (bool, error) {
	if c.enc == nil {
		return true, nil
	}
	return c.enc.Verify(shards)
}

package mega

import (
	"bytes"
	"context"
	"io"
	"sync"
	"time"
)

const (
	// downloadWorkers son los trozos de un archivo que se piden a la vez. go-mega
	// parte cada archivo en trozos de 128 KiB a 1 MiB, uno por GET: en serie,
	// la latencia de cada petición se suma.
	downloadWorkers = 4
	// linkTTL es cuánto se reutiliza el enlace de descarga de un archivo.
	// Pedirlo (NewDownload) es una llamada a la API, en cola con las demás de
	// la cuenta; los enlaces de Mega duran bastante más.
	linkTTL  = 5 * time.Minute
	maxLinks = 1024
)

// chunkSource es la parte de *gomega.Download que usa pump.
type chunkSource interface {
	Chunks() int
	ChunkLocation(id int) (position int64, size int, err error)
	DownloadChunk(id int) ([]byte, error)
	Finish() error
}

// link es un enlace de descarga reutilizable.
type link struct {
	d       chunkSource
	expires time.Time

	mu       sync.Mutex
	verified bool
}

// finish comprueba el MAC del archivo la primera vez que se baja entero. El
// cifrador del MAC de go-mega guarda estado y no da lo mismo dos veces, así
// que no se repite: el contenido de un nodo no cambia.
func (l *link) finish() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.verified {
		return nil
	}
	if err := l.d.Finish(); err != nil {
		return err
	}
	l.verified = true
	return nil
}

// links guarda los enlaces de descarga por nodo.
type links struct {
	mu  sync.Mutex
	m   map[string]*link
	now func() time.Time // nil: time.Now
}

func (c *links) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// get devuelve el enlace vigente del nodo id o pide uno con open.
func (c *links) get(id string, open func() (chunkSource, error)) (*link, error) {
	c.mu.Lock()
	l, ok := c.m[id]
	c.mu.Unlock()
	if ok && c.clock().Before(l.expires) {
		return l, nil
	}
	d, err := open()
	if err != nil {
		return nil, err
	}
	l = &link{d: d, expires: c.clock().Add(linkTTL)}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]*link{}
	}
	if len(c.m) >= maxLinks {
		now := c.clock()
		for k, v := range c.m {
			if !now.Before(v.expires) {
				delete(c.m, k)
			}
		}
		if len(c.m) >= maxLinks {
			clear(c.m)
		}
	}
	c.m[id] = l
	return l, nil
}

// forget descarta el enlace del nodo (si sigue siendo l, cuando l no es nil):
// tras un error puede haber caducado o no valer.
func (c *links) forget(id string, l *link) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur, ok := c.m[id]; ok && (l == nil || cur == l) {
		delete(c.m, id)
	}
}

// pump escribe en w los trozos que cubren [offset, offset+length), pidiendo
// hasta downloadWorkers a la vez y escribiéndolos en orden. Solo comprueba el
// MAC cuando se descarga el archivo entero.
func pump(ctx context.Context, l *link, total, offset, length int64, w io.Writer) error {
	end := offset + length
	var ids []int
	for id := 0; id < l.d.Chunks(); id++ {
		pos, size, err := l.d.ChunkLocation(id)
		if err != nil {
			return mapError(err)
		}
		if pos+int64(size) > offset && pos < end {
			ids = append(ids, id)
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		chunk []byte
		err   error
	}
	results := make([]chan result, len(ids))
	for i := range results {
		results[i] = make(chan result, 1)
	}
	// Un hueco por trozo pedido y aún sin escribir: como mucho downloadWorkers
	// trozos en memoria.
	slots := make(chan struct{}, downloadWorkers)
	go func() {
		for i, id := range ids {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			go func() {
				chunk, err := l.d.DownloadChunk(id)
				results[i] <- result{chunk, err}
			}()
		}
	}()

	for i, id := range ids {
		var r result
		select {
		case r = <-results[i]:
		case <-ctx.Done():
			return ctx.Err()
		}
		<-slots
		if r.err != nil {
			return downloadError(r.err)
		}
		pos, size, _ := l.d.ChunkLocation(id)
		from := max(offset-pos, 0)
		to := min(int64(size), end-pos)
		if _, err := io.Copy(w, bytes.NewReader(r.chunk[from:to])); err != nil {
			return err
		}
	}
	if offset == 0 && end == total {
		if err := l.finish(); err != nil {
			return mapError(err)
		}
	}
	return nil
}

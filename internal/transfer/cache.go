package transfer

import (
	"container/list"
	"context"
	"fmt"
	"sync"

	"golang.org/x/sync/singleflight"
)

// Un visor (PDF, vídeo) pide el mismo archivo varias veces con Range y cada
// petición bajaría y descifraría otra vez la franja entera. stripeCache guarda
// en claro las últimas franjas reconstruidas, con un tope de memoria; solo
// entran franjas pequeñas para que un vídeo largo no la vacíe.
const (
	stripeCacheBytes = 64 << 20
	stripeCacheEntry = 16 << 20
)

// stripeKey lleva el SHA-256 del archivo: el contenido de un id no cambia
// (reparar o cifrar bloques antiguos no altera el claro), y si SQLite reutiliza
// el id de un archivo borrado la clave ya no coincide.
type stripeKey struct {
	file   int64
	sha    string
	stripe int64
}

type stripeEntry struct {
	key  stripeKey
	data []byte
}

type stripeCache struct {
	mu    sync.Mutex
	max   int64
	used  int64
	lru   *list.List // delante, la más reciente
	items map[stripeKey]*list.Element
	group singleflight.Group
}

func newStripeCache(max int64) *stripeCache {
	return &stripeCache{max: max, lru: list.New(), items: map[stripeKey]*list.Element{}}
}

// De dónde sale una franja, para los logs de tiempos.
const (
	sourceCache    = "cache"    // ya estaba en memoria
	sourceFetched  = "fetched"  // la bajó esta petición
	sourceShared   = "shared"   // esperó a la descarga de otra petición
	sourceUncached = "uncached" // no entra en la caché
	sourceCanceled = "canceled" // la petición se fue antes de tenerla
)

// load devuelve la franja de la caché o la pide con fetch, y de dónde salió.
// Las peticiones simultáneas de la misma franja esperan a una sola descarga,
// que no se corta si se va quien la empezó: los demás siguen esperándola.
func (c *stripeCache) load(ctx context.Context, k stripeKey, fetch func(context.Context) ([]byte, error)) ([]byte, string, error) {
	if b, ok := c.get(k); ok {
		return b, sourceCache, nil
	}
	src := sourceShared
	ch := c.group.DoChan(fmt.Sprintf("%d/%s/%d", k.file, k.sha, k.stripe), func() (any, error) {
		src = sourceFetched
		b, err := fetch(context.WithoutCancel(ctx))
		if err == nil {
			c.put(k, b)
		}
		return b, err
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			return nil, src, r.Err
		}
		return r.Val.([]byte), src, nil
	case <-ctx.Done():
		// src no se lee aquí: la descarga puede seguir escribiéndolo.
		return nil, sourceCanceled, ctx.Err()
	}
}

func (c *stripeCache) get(k stripeKey) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[k]
	if !ok {
		return nil, false
	}
	c.lru.MoveToFront(el)
	return el.Value.(*stripeEntry).data, true
}

func (c *stripeCache) put(k stripeKey, data []byte) {
	if int64(len(data)) > c.max {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[k]; ok {
		c.remove(el)
	}
	c.items[k] = c.lru.PushFront(&stripeEntry{key: k, data: data})
	c.used += int64(len(data))
	for c.used > c.max {
		c.remove(c.lru.Back())
	}
}

// drop saca las franjas de un archivo (al borrarlo, para no retener su claro).
func (c *stripeCache) drop(file int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, el := range c.items {
		if k.file == file {
			c.remove(el)
		}
	}
}

// clear vacía la caché (al formatear el disco).
func (c *stripeCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lru.Init()
	c.items = map[stripeKey]*list.Element{}
	c.used = 0
}

func (c *stripeCache) remove(el *list.Element) {
	e := el.Value.(*stripeEntry)
	c.lru.Remove(el)
	delete(c.items, e.key)
	c.used -= int64(len(e.data))
}

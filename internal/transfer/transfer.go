// Package transfer sube, descarga y borra archivos del drive repartiéndolos
// en bloques entre las cuentas, con paridad Reed–Solomon opcional.
package transfer

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/md5"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"path"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/hirasolutions/cloudmesh/internal/accounts"
	"github.com/hirasolutions/cloudmesh/internal/crypto"
	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
	"github.com/hirasolutions/cloudmesh/internal/erasure"
	"github.com/hirasolutions/cloudmesh/internal/settings"
	"github.com/hirasolutions/cloudmesh/internal/storage"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

const (
	LayoutWhole = "whole"
	LayoutRS    = "rs"
	LayoutParts = "parts" // concatenación de archivos internos (multipart de S3)

	StatusUploading = "uploading"
	StatusReady     = "ready"
	StatusDegraded  = "degraded"
	StatusLost      = "lost"

	// smallFile es el tamaño hasta el que un archivo se guarda en copias (1+m)
	// en vez de trocearse en k+m; nunca más que un bloque.
	smallFile = 4 << 20
)

var (
	ErrNoSpace     = errors.New("ninguna cuenta tiene espacio suficiente")
	ErrNoAccounts  = errors.New("no hay cuentas activas para guardar el archivo")
	ErrUnreadable  = errors.New("faltan bloques para leer el archivo")
	ErrUnavailable = errors.New("archivo aún subiéndose")
	ErrQuota       = errors.New("la subida supera tu cuota de espacio")
)

type Service struct {
	db       *sql.DB
	q        *sqlcgen.Queries
	accounts *accounts.Service
	vfs      *vfs.Service
	settings *settings.Service
	box      *crypto.Box // envuelve la clave de cada archivo
	log      *slog.Logger
	now      func() time.Time
	sem      chan struct{} // subidas simultáneas
	dlSem    chan struct{} // descargas simultáneas (cada una retiene una franja en memoria)
	cache    *stripeCache  // franjas pequeñas ya reconstruidas

	mu        sync.Mutex
	reserved  map[int64]int64 // bytes comprometidos por cuenta en subidas en curso
	rr        int             // cursor de round robin
	encCursor int64           // último bloque visto por EncryptPending
	blocksDir map[blockDirKey]string
	// rebuildSkip guarda, por archivo que RebuildAll dejó a medias, la huella
	// del estado en que falló: no se reintenta hasta que cambie.
	rebuildSkip map[int64]string
}

// New limita las subidas y descargas simultáneas por proceso. box es la clave
// maestra con la que se envuelven las claves de los archivos.
func New(sqlDB *sql.DB, acc *accounts.Service, v *vfs.Service, st *settings.Service, box *crypto.Box, log *slog.Logger, maxUploads, maxDownloads int) *Service {
	if maxUploads < 1 {
		maxUploads = 1
	}
	if maxDownloads < 1 {
		maxDownloads = 1
	}
	return &Service{
		db: sqlDB, q: sqlcgen.New(sqlDB), accounts: acc, vfs: v, settings: st, box: box, log: log, now: time.Now,
		sem: make(chan struct{}, maxUploads), dlSem: make(chan struct{}, maxDownloads),
		cache:    newStripeCache(stripeCacheBytes),
		reserved: map[int64]int64{}, blocksDir: map[blockDirKey]string{}, rebuildSkip: map[int64]string{},
	}
}

// Upload guarda size bytes de r como archivo name en la carpeta folderID.
func (s *Service) Upload(ctx context.Context, folderID int64, name string, size int64, r io.Reader) (vfs.File, error) {
	return s.upload(ctx, Owner{}, folderID, name, size, r, false)
}

// Owner es quien sube: el archivo cuenta para su cuota. Sin ID no hay dueño ni
// límite (importaciones, pruebas); Quota 0 es sin límite.
type Owner struct {
	ID    int64
	Quota int64
}

// UploadFor es Upload a nombre de un usuario, comprobando su cuota.
func (s *Service) UploadFor(ctx context.Context, owner Owner, folderID int64, name string, size int64, r io.Reader) (vfs.File, error) {
	return s.upload(ctx, owner, folderID, name, size, r, false)
}

// UploadPart sube una parte de una subida multipart: un archivo interno, que no
// aparece en el disco hasta que Compose lo une a su objeto. Cuenta para la cuota.
func (s *Service) UploadPart(ctx context.Context, owner Owner, folderID int64, name string, size int64, r io.Reader) (vfs.File, error) {
	return s.upload(ctx, owner, folderID, name, size, r, true)
}

// insertChecked reserva la fila del archivo comprobando antes la cuota. Lo que
// se está subiendo ya cuenta, así dos subidas a la vez no se saltan el límite.
func (s *Service) insertChecked(ctx context.Context, owner Owner, size int64, p sqlcgen.InsertFileParams) (sqlcgen.File, error) {
	if owner.ID == 0 {
		return s.q.InsertFile(ctx, p)
	}
	p.OwnerID = sql.NullInt64{Int64: owner.ID, Valid: true}
	if owner.Quota <= 0 {
		return s.q.InsertFile(ctx, p)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	used, err := s.q.OwnerUsage(ctx, p.OwnerID)
	if err != nil {
		return sqlcgen.File{}, err
	}
	if used+size > owner.Quota {
		return sqlcgen.File{}, ErrQuota
	}
	return s.q.InsertFile(ctx, p)
}

func (s *Service) upload(ctx context.Context, owner Owner, folderID int64, name string, size int64, r io.Reader, internal bool) (vfs.File, error) {
	name, err := vfs.CleanName(name)
	if err != nil {
		return vfs.File{}, err
	}
	if err := s.vfs.FolderExists(ctx, folderID); err != nil {
		return vfs.File{}, err
	}
	if size < 0 {
		return vfs.File{}, errors.New("tamaño desconocido")
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return vfs.File{}, ctx.Err()
	}

	cfg, err := s.settings.Get(ctx)
	if err != nil {
		return vfs.File{}, err
	}
	cands, err := s.candidates(ctx)
	if err != nil {
		return vfs.File{}, err
	}
	if len(cands) == 0 {
		return vfs.File{}, ErrNoAccounts
	}

	// Siempre en bloques cifrados. Sin cuentas para k+m, bloques sueltos (1+0):
	// sin redundancia, pero el proveedor sigue sin ver el contenido. «whole»
	// queda solo para lo que el sync encuentra ya en el proveedor.
	layout, k, m := LayoutRS, 1, 0
	switch {
	case size <= min(smallFile, cfg.ChunkSize) && cfg.ParityShards > 0 && len(cands) >= 1+cfg.ParityShards:
		// Archivo pequeño: 1+m es guardar m+1 copias. Menos objetos en el
		// proveedor y una sola petición para leerlo, con la misma tolerancia
		// a perder m cuentas; el espacio extra es despreciable a este tamaño.
		m = cfg.ParityShards
	case cfg.DataShards+cfg.ParityShards > 1 && len(cands) >= cfg.DataShards+cfg.ParityShards:
		k, m = cfg.DataShards, cfg.ParityShards
	}
	chunkSize := cfg.ChunkSize
	// Antes de leer el cuerpo: si ni sumando todo el pool cabe con su paridad,
	// se rechaza ya. Es condición necesaria; el reparto real lo decide pick.
	if !s.fits(cands, size+size/int64(k)*int64(m)) {
		return vfs.File{}, ErrNoSpace
	}

	row, err := s.insertChecked(ctx, owner, size, sqlcgen.InsertFileParams{
		FolderID: folderID, Name: name, Size: size, Mime: mimeOf(name), Mtime: s.now().Unix(),
		Layout: layout, DataShards: int64(k), ParityShards: int64(m), ChunkSize: chunkSize,
		Status: StatusUploading, CreatedAt: s.now().Unix(), Internal: boolInt(internal),
	})
	if err != nil {
		return vfs.File{}, err
	}

	// SHA-256 para la integridad propia; MD5 es el ETag que esperan los clientes S3.
	hasher, md := sha256.New(), md5.New()
	sinks := []io.Writer{hasher, md}
	// Lo recién subido se suele leer enseguida (S3: PUT y luego GET). Si cabe
	// en la caché de franjas, se guarda una copia en claro para no bajarlo.
	var plain *bytes.Buffer
	if size > 0 && size <= stripeCacheEntry {
		plain = bytes.NewBuffer(make([]byte, 0, size))
		sinks = append(sinks, plain)
	}
	tee := io.TeeReader(io.LimitReader(r, size), io.MultiWriter(sinks...))
	var uploaded []sqlcgen.Chunk
	if size > 0 {
		uploaded, err = s.uploadStriped(ctx, row.ID, size, tee, cands, cfg, k, m)
	}
	if err != nil {
		s.cleanup(context.WithoutCancel(ctx), row, uploaded)
		return vfs.File{}, err
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	if err := s.q.FinishFile(ctx, sqlcgen.FinishFileParams{
		Size: size, Sha256: sum, Etag: hex.EncodeToString(md.Sum(nil)), ID: row.ID,
	}); err != nil {
		s.cleanup(context.WithoutCancel(ctx), row, uploaded)
		return vfs.File{}, err
	}
	if plain != nil {
		stripeBytes := chunkSize * int64(k)
		b := plain.Bytes()
		for stripe := int64(0); stripe*stripeBytes < size; stripe++ {
			start := stripe * stripeBytes
			s.cache.put(stripeKey{file: row.ID, sha: sum, stripe: stripe}, b[start:min(start+stripeBytes, size)])
		}
	}
	s.refreshQuotas(uploaded)
	done, err := s.q.GetFile(ctx, row.ID)
	if err != nil {
		return vfs.File{}, err
	}
	return vfs.FromRow(done), nil
}

// uploadStriped lee franjas de k×chunk bytes, codifica k+m bloques y los sube en paralelo.
func (s *Service) uploadStriped(ctx context.Context, fileID, size int64, r io.Reader, cands []accounts.Account, cfg settings.Settings, k, m int) ([]sqlcgen.Chunk, error) {
	codec, err := erasure.New(k, m)
	if err != nil {
		return nil, err
	}
	aead, err := s.fileKey(ctx, fileID, true)
	if err != nil {
		return nil, err
	}
	var uploaded []sqlcgen.Chunk
	var umu sync.Mutex
	stripeBytes := cfg.ChunkSize * int64(k)
	buf := make([]byte, min(stripeBytes, size))
	var remaining = size
	for stripe := int64(0); remaining > 0; stripe++ {
		n := int(min(remaining, stripeBytes))
		if _, err := io.ReadFull(r, buf[:n]); err != nil {
			return uploaded, fmt.Errorf("leer cuerpo: %w", err)
		}
		remaining -= int64(n)
		shards, err := codec.Encode(buf[:n])
		if err != nil {
			return uploaded, err
		}
		shardSize := int64(len(shards[0])) + sealOverhead
		picked, err := s.pick(cands, k+m, shardSize, cfg.RoutingPolicy)
		if err != nil {
			return uploaded, err
		}
		// Rota la asignación por franja para que la paridad no caiga siempre en
		// las mismas cuentas (como RAID 5 frente a RAID 4): reparte lecturas y escrituras.
		if r := int(stripe % int64(len(picked))); r > 0 {
			picked = append(picked[r:], picked[:r]...)
		}
		g, gctx := errgroup.WithContext(ctx)
		for idx := range shards {
			acc, data := picked[idx], sealBlock(aead, fileID, stripe, int64(idx), shards[idx])
			g.Go(func() error {
				defer s.release(acc.ID, shardSize)
				sess, root, err := s.session(gctx, acc.ID)
				if err != nil {
					return err
				}
				dir, err := s.blocks(gctx, acc.ID, sess, root, fileID)
				if err != nil {
					return err
				}
				name := fmt.Sprintf("%d-%d-%d.cmb", fileID, stripe, idx)
				e, err := sess.Upload(gctx, dir, name, shardSize, bytes.NewReader(data))
				if err != nil {
					return s.accounts.HandleSessionError(gctx, acc.ID, err)
				}
				kind := "data"
				if idx >= k {
					kind = "parity"
				}
				sum := sha256.Sum256(data)
				c, err := s.q.InsertChunk(gctx, sqlcgen.InsertChunkParams{
					FileID: fileID, Stripe: stripe, Idx: int64(idx), Kind: kind, AccountID: nullID(acc.ID),
					RemoteID: e.ID, Size: shardSize, Sha256: hex.EncodeToString(sum[:]), Encrypted: 1,
				})
				if err != nil {
					_ = sess.Delete(gctx, e.ID, true)
					return err
				}
				umu.Lock()
				uploaded = append(uploaded, c)
				umu.Unlock()
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return uploaded, err
		}
	}
	return uploaded, nil
}

// Stream es un archivo abierto para lectura parcial.
type Stream struct {
	File   vfs.File
	Offset int64
	Length int64
	Body   io.ReadCloser
}

// Open devuelve [offset, offset+length) del archivo. length < 0 es hasta el final.
func (s *Service) Open(ctx context.Context, id, offset, length int64) (*Stream, error) {
	f, err := s.vfs.GetFile(ctx, id)
	if err != nil {
		return nil, err
	}
	if f.Status == StatusUploading {
		return nil, ErrUnavailable
	}
	if offset < 0 || offset > f.Size {
		return nil, fmt.Errorf("rango fuera del archivo")
	}
	if length < 0 || offset+length > f.Size {
		length = f.Size - offset
	}
	st := &Stream{File: f, Offset: offset, Length: length}
	if length == 0 {
		st.Body = io.NopCloser(bytes.NewReader(nil))
		return st, nil
	}
	body, err := s.openBody(ctx, f, offset, length)
	if err != nil {
		return nil, err
	}
	st.Body = body
	return st, nil
}

// openBody lee [offset, offset+length) de cualquier archivo, visible o interno.
func (s *Service) openBody(ctx context.Context, f vfs.File, offset, length int64) (io.ReadCloser, error) {
	if f.Layout == LayoutParts {
		return s.openParts(ctx, f, offset, length)
	}
	chunks, err := s.q.ListChunks(ctx, f.ID)
	if err != nil {
		return nil, err
	}
	if f.Layout == LayoutWhole {
		if len(chunks) != 1 {
			return nil, ErrUnreadable
		}
		c := chunks[0]
		if !c.AccountID.Valid || c.Missing != 0 {
			return nil, ErrUnreadable
		}
		t0 := time.Now()
		sess, err := s.accounts.Session(ctx, c.AccountID.Int64)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnreadable, err)
		}
		t1 := time.Now()
		body, err := sess.Download(ctx, c.RemoteID, offset, length)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnreadable, s.accounts.HandleSessionError(ctx, c.AccountID.Int64, err))
		}
		s.log.Debug("archivo abierto", "file", f.ID, "account", c.AccountID.Int64, "session", ms(t1.Sub(t0)), "open", ms(time.Since(t1)))
		return body, nil
	}

	k, m := int(f.DataShards), int(f.ParityShards)
	codec, err := erasure.New(k, m)
	if err != nil {
		return nil, err
	}
	row, err := s.q.GetFile(ctx, f.ID)
	if err != nil {
		return nil, err
	}
	aead, err := s.fileKey(ctx, f.ID, false)
	if err != nil {
		return nil, err
	}
	stripeBytes := row.ChunkSize * int64(k)
	byStripe := map[int64][]sqlcgen.ListChunksRow{}
	for _, c := range chunks {
		byStripe[c.Stripe] = append(byStripe[c.Stripe], c)
	}
	select {
	case s.dlSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(s.pumpStriped(ctx, codec, aead, f, byStripe, stripeBytes, offset, length, pw))
	}()
	return &releaseOnClose{ReadCloser: pr, release: func() { <-s.dlSem }}, nil
}

// openParts encadena las partes que cubren el rango, una detrás de otra.
func (s *Service) openParts(ctx context.Context, f vfs.File, offset, length int64) (io.ReadCloser, error) {
	parts, err := s.q.ListFileParts(ctx, f.ID)
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(func() error {
			start, end := offset, offset+length
			pos := int64(0)
			for _, p := range parts {
				pStart, pEnd := pos, pos+p.Size
				pos = pEnd
				if pEnd <= start || pStart >= end {
					continue
				}
				row, err := s.q.GetFile(ctx, p.PartFileID)
				if err != nil {
					return fmt.Errorf("%w: parte %d: %v", ErrUnreadable, p.PartNumber, err)
				}
				from := max(start, pStart) - pStart
				n := min(end, pEnd) - pStart - from
				body, err := s.openBody(ctx, vfs.FromRow(row), from, n)
				if err != nil {
					return err
				}
				_, err = io.Copy(pw, body)
				body.Close()
				if err != nil {
					return err
				}
			}
			return nil
		}())
	}()
	return pr, nil
}

// Part es una parte ya subida que Compose une a un objeto.
type Part struct {
	Number int64
	FileID int64
	Size   int64
}

// Compose crea en folderID el archivo name como concatenación de parts
// (archivos internos subidos con UploadPart). No copia datos.
// El objeto queda a nombre de ownerID (0: sin dueño); sus partes ya contaron
// en la cuota al subirse, así que el compuesto no suma otra vez.
func (s *Service) Compose(ctx context.Context, ownerID int64, folderID int64, name string, parts []Part, etag string) (vfs.File, error) {
	name, err := vfs.CleanName(name)
	if err != nil {
		return vfs.File{}, err
	}
	if err := s.vfs.FolderExists(ctx, folderID); err != nil {
		return vfs.File{}, err
	}
	size := int64(0)
	for _, p := range parts {
		size += p.Size
	}
	now := s.now().Unix()
	row, err := s.q.InsertFile(ctx, sqlcgen.InsertFileParams{
		FolderID: folderID, Name: name, Size: size, Mime: mimeOf(name), Mtime: now, Layout: LayoutParts,
		DataShards: 1, Status: StatusReady, CreatedAt: now, Etag: etag,
		OwnerID: sql.NullInt64{Int64: ownerID, Valid: ownerID != 0},
	})
	if err != nil {
		return vfs.File{}, err
	}
	for _, p := range parts {
		if err := s.q.InsertFilePart(ctx, sqlcgen.InsertFilePartParams{
			FileID: row.ID, PartNumber: p.Number, PartFileID: p.FileID, Size: p.Size,
		}); err != nil {
			_, _ = s.q.DeleteFile(ctx, row.ID)
			return vfs.File{}, err
		}
	}
	if _, err := s.RecomputeStatus(ctx, row.ID); err != nil {
		return vfs.File{}, err
	}
	done, err := s.q.GetFile(ctx, row.ID)
	if err != nil {
		return vfs.File{}, err
	}
	return vfs.FromRow(done), nil
}

// releaseOnClose libera el semáforo de descargas una sola vez al cerrar.
type releaseOnClose struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (r *releaseOnClose) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(r.release)
	return err
}

// pumpStriped reconstruye franja a franja y escribe el rango pedido.
func (s *Service) pumpStriped(ctx context.Context, codec *erasure.Codec, aead cipher.AEAD, f vfs.File, byStripe map[int64][]sqlcgen.ListChunksRow, stripeBytes, offset, length int64, w io.Writer) error {
	end := offset + length
	for stripe := offset / stripeBytes; stripe*stripeBytes < end; stripe++ {
		start := stripe * stripeBytes
		n := int(min(stripeBytes, f.Size-start))
		data, err := s.readStripe(ctx, codec, aead, stripeKey{file: f.ID, sha: f.SHA256, stripe: stripe}, byStripe[stripe], n)
		if err != nil {
			return err
		}
		from := max(offset-start, 0)
		to := min(int64(n), end-start)
		if _, err := w.Write(data[from:to]); err != nil {
			return err
		}
	}
	return nil
}

// readStripe devuelve la franja de la caché o la reconstruye. Sin SHA-256
// (archivo sin terminar de indexar) no hay clave segura y no se cachea.
func (s *Service) readStripe(ctx context.Context, codec *erasure.Codec, aead cipher.AEAD, key stripeKey, rows []sqlcgen.ListChunksRow, n int) ([]byte, error) {
	start := time.Now()
	fetch := func(ctx context.Context) ([]byte, error) { return s.reconstruct(ctx, codec, aead, key, rows, n) }
	var (
		data []byte
		src  = sourceUncached
		err  error
	)
	if key.sha == "" || n > stripeCacheEntry {
		data, err = fetch(ctx)
	} else {
		data, src, err = s.cache.load(ctx, key, fetch)
	}
	s.log.Debug("franja leída", "file", key.file, "stripe", key.stripe, "bytes", n, "source", src, "dur", ms(time.Since(start)), "err", err)
	return data, err
}

// reconstruct reúne k bloques de la franja y la reconstruye.
func (s *Service) reconstruct(ctx context.Context, codec *erasure.Codec, aead cipher.AEAD, key stripeKey, rows []sqlcgen.ListChunksRow, n int) ([]byte, error) {
	t0 := time.Now()
	shards, _ := s.gather(ctx, codec, aead, rows)
	t1 := time.Now()
	out, err := codec.Reconstruct(shards, n)
	s.log.Debug("franja reconstruida", "file", key.file, "stripe", key.stripe, "gather", ms(t1.Sub(t0)), "decode", ms(time.Since(t1)))
	if errors.Is(err, erasure.ErrTooFewShards) {
		return nil, ErrUnreadable
	}
	return out, err
}

// gather descarga bloques de una franja hasta tener k, en este orden: datos
// de cuentas sin límite de transferencia, paridad de esas cuentas y, como
// último recurso, las cuentas limitadas (puede que su cuota ya se haya
// renovado). Los datos primero evitan decodificar; esquivar las limitadas
// evita gastar su cuota y esperar a que Mega rechace la descarga.
// transient dice si algún bloque no se pudo leer por algo pasajero (red,
// sesión, límite de transferencia) y no porque falte o esté dañado.
func (s *Service) gather(ctx context.Context, codec *erasure.Codec, aead cipher.AEAD, rows []sqlcgen.ListChunksRow) (shards [][]byte, transient bool) {
	shards = make([][]byte, codec.K+codec.M)
	var order []sqlcgen.ListChunksRow
	for _, throttled := range []bool{false, true} {
		for _, parity := range []bool{false, true} {
			for _, r := range rows {
				if !r.AccountID.Valid || r.Missing != 0 || int(r.Idx) >= len(shards) {
					continue
				}
				if (int(r.Idx) >= codec.K) == parity && s.accounts.Throttled(r.AccountID.Int64) == throttled {
					order = append(order, r)
				}
			}
		}
	}
	have := 0
	for pos := 0; have < codec.K && pos < len(order); {
		batch := order[pos:min(pos+codec.K-have, len(order))]
		pos += len(batch)
		var wg sync.WaitGroup
		var mu sync.Mutex
		for _, r := range batch {
			wg.Add(1)
			go func() {
				defer wg.Done()
				b, err := s.fetchShard(ctx, r, aead)
				if err != nil {
					s.log.Warn("bloque ilegible", "file", r.FileID, "stripe", r.Stripe, "idx", r.Idx, "account", r.AccountID.Int64, "err", err)
					if !badBlock(err) {
						mu.Lock()
						transient = true
						mu.Unlock()
					}
					return
				}
				mu.Lock()
				shards[r.Idx] = b
				have++
				mu.Unlock()
			}()
		}
		wg.Wait()
	}
	return shards, transient
}

var errBadHash = errors.New("hash no coincide")

// badBlock dice si el error de lectura es del bloque en sí (no está o está
// dañado): reintentar no cambia nada.
func badBlock(err error) bool {
	return errors.Is(err, storage.ErrNotFound) || errors.Is(err, errBadHash) || errors.Is(err, ErrBlockAuth)
}

// fetchShard descarga un bloque completo, verifica su hash y, si está
// cifrado, lo descifra. Devuelve el bloque en claro.
func (s *Service) fetchShard(ctx context.Context, r sqlcgen.ListChunksRow, aead cipher.AEAD) ([]byte, error) {
	t0 := time.Now()
	sess, err := s.accounts.Session(ctx, r.AccountID.Int64)
	if err != nil {
		return nil, err
	}
	t1 := time.Now()
	body, err := sess.Download(ctx, r.RemoteID, 0, r.Size)
	if err != nil {
		return nil, s.accounts.HandleSessionError(ctx, r.AccountID.Int64, err)
	}
	defer body.Close()
	t2 := time.Now()
	buf := make([]byte, r.Size)
	if _, err := io.ReadFull(body, buf); err != nil {
		return nil, err
	}
	// open es lo que tarda el proveedor en dar el enlace (en Mega, NewDownload);
	// read, las peticiones que bajan el contenido.
	s.log.Debug("bloque descargado", "file", r.FileID, "stripe", r.Stripe, "idx", r.Idx, "account", r.AccountID.Int64, "bytes", r.Size,
		"session", ms(t1.Sub(t0)), "open", ms(t2.Sub(t1)), "read", ms(time.Since(t2)))
	if r.Sha256 != "" {
		sum := sha256.Sum256(buf)
		if hex.EncodeToString(sum[:]) != r.Sha256 {
			return nil, errBadHash
		}
	}
	if r.Encrypted == 0 {
		return buf, nil
	}
	if aead == nil {
		return nil, fmt.Errorf("%w: el archivo no tiene clave", ErrBlockAuth)
	}
	return openBlock(aead, r.FileID, r.Stripe, r.Idx, buf)
}

// Delete borra el índice y destruye los bloques en cada cuenta (mejor esfuerzo).
// Vale para archivos del disco, de la papelera e internos. Un compuesto se
// lleva sus partes.
func (s *Service) Delete(ctx context.Context, id int64) error {
	f, err := s.q.GetFile(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return vfs.ErrFileNotFound
	}
	if err != nil {
		return err
	}
	if f.Layout == LayoutParts {
		parts, err := s.q.ListFileParts(ctx, id)
		if err != nil {
			return err
		}
		// Primero el compuesto (arrastra file_parts) y luego cada parte.
		if _, err := s.q.DeleteFile(ctx, id); err != nil {
			return err
		}
		for _, p := range parts {
			if err := s.Delete(ctx, p.PartFileID); err != nil && !errors.Is(err, vfs.ErrFileNotFound) {
				s.log.Warn("borrar parte", "file", id, "part", p.PartFileID, "err", err)
			}
		}
		return nil
	}
	chunks, err := s.q.ListChunks(ctx, id)
	if err != nil {
		return err
	}
	rows := make([]sqlcgen.Chunk, 0, len(chunks))
	for _, c := range chunks {
		if c.AccountID.Valid {
			rows = append(rows, sqlcgen.Chunk{AccountID: c.AccountID, RemoteID: c.RemoteID})
		}
	}
	s.deleteRemote(ctx, rows, f.Layout == LayoutRS)
	if _, err := s.q.DeleteFile(ctx, id); err != nil {
		return err
	}
	s.cache.drop(id)
	s.refreshQuotas(rows)
	return nil
}

// PurgeTrash destruye los archivos que entraron en la papelera antes de t.
// Devuelve cuántos se borraron; un fallo con uno no detiene a los demás.
func (s *Service) PurgeTrash(ctx context.Context, before time.Time) (int, error) {
	ids, err := s.vfs.TrashedBefore(ctx, before)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		if err := s.Delete(ctx, id); err != nil {
			s.log.Warn("papelera: no se pudo purgar", "file", id, "err", err)
			continue
		}
		n++
	}
	return n, nil
}

// Format vacía el disco y empieza de cero: borra del índice todos los
// archivos (papelera e internos incluidos) y carpetas, con lo que cuelga de
// ellas (enlaces, buckets S3 y sus subidas multipart, permisos), y después
// destruye los bloques en cada cuenta (mejor esfuerzo: lo que quede lo borra el
// scrub como huérfano). Cuentas, usuarios, ajustes y llaves S3 no se tocan.
// Devuelve cuántos archivos se borraron.
func (s *Service) Format(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)
	remote, err := q.ListAllRemoteChunks(ctx)
	if err != nil {
		return 0, err
	}
	// Primero las carpetas: sus buckets se llevan las subidas multipart, que
	// apuntan a archivos sin borrado en cascada.
	if err := q.DeleteAllFolders(ctx); err != nil {
		return 0, err
	}
	n, err := q.DeleteAllFiles(ctx)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.cache.clear()
	s.mu.Lock()
	s.rebuildSkip = map[int64]string{}
	s.mu.Unlock()

	var blocks, whole []sqlcgen.Chunk
	for _, c := range remote {
		row := sqlcgen.Chunk{AccountID: c.AccountID, RemoteID: c.RemoteID}
		if c.Layout == LayoutRS {
			blocks = append(blocks, row)
		} else {
			whole = append(whole, row)
		}
	}
	s.deleteRemote(ctx, blocks, true)
	s.deleteRemote(ctx, whole, false)
	s.refreshQuotas(append(blocks, whole...))
	s.log.Info("disco formateado", "files", n, "blocks", len(remote))
	return n, nil
}

func (s *Service) cleanup(ctx context.Context, row sqlcgen.File, uploaded []sqlcgen.Chunk) {
	s.deleteRemote(ctx, uploaded, row.Layout == LayoutRS)
	if _, err := s.q.DeleteFile(ctx, row.ID); err != nil {
		s.log.Error("limpiar subida fallida", "file", row.ID, "err", err)
	}
}

func (s *Service) deleteRemote(ctx context.Context, rows []sqlcgen.Chunk, destroy bool) {
	for _, c := range rows {
		if !c.AccountID.Valid {
			continue
		}
		sess, err := s.accounts.Session(ctx, c.AccountID.Int64)
		if err != nil {
			s.log.Warn("bloque huérfano: sesión no disponible", "account", c.AccountID.Int64, "remote", c.RemoteID, "err", err)
			continue
		}
		if err := sess.Delete(ctx, c.RemoteID, destroy); err != nil && !errors.Is(err, storage.ErrNotFound) {
			s.log.Warn("bloque huérfano: no se pudo borrar", "account", c.AccountID.Int64, "remote", c.RemoteID, "err", err)
		}
	}
}

// ms redondea una duración para los logs de tiempos.
func ms(d time.Duration) time.Duration { return d.Round(time.Millisecond) }

func nullID(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: true} }

// candidates devuelve las cuentas activas y habilitadas para recibir subidas.
func (s *Service) candidates(ctx context.Context) ([]accounts.Account, error) {
	all, err := s.accounts.List(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0:0]
	for _, a := range all {
		if a.Enabled && a.Status == accounts.StatusActive {
			out = append(out, a)
		}
	}
	return out, nil
}

// pick elige n cuentas distintas con al menos need bytes libres según la política
// y reserva esos bytes hasta que release los devuelva.
func (s *Service) pick(cands []accounts.Account, n int, need int64, policy string) ([]accounts.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	free := s.freeOf
	ok := make([]accounts.Account, 0, len(cands))
	for _, a := range cands {
		if free(a) >= need {
			ok = append(ok, a)
		}
	}
	if len(ok) < n {
		if len(cands) == 0 {
			return nil, ErrNoAccounts
		}
		return nil, ErrNoSpace
	}
	switch policy {
	case settings.PolicyPriority:
		sort.SliceStable(ok, func(i, j int) bool {
			if ok[i].Priority != ok[j].Priority {
				return ok[i].Priority < ok[j].Priority
			}
			return free(ok[i]) > free(ok[j])
		})
	case settings.PolicyRoundRobin:
		sort.SliceStable(ok, func(i, j int) bool { return ok[i].ID < ok[j].ID })
		start := s.rr % len(ok)
		s.rr++
		ok = append(ok[start:], ok[:start]...)
	default:
		sort.SliceStable(ok, func(i, j int) bool { return free(ok[i]) > free(ok[j]) })
	}
	// Las cuentas con límite de transferencia van al final: lo que se suba a
	// ellas no se podría leer de ahí hasta que se renueve su cuota.
	sort.SliceStable(ok, func(i, j int) bool { return !ok[i].Throttled() && ok[j].Throttled() })
	picked := ok[:n]
	for _, a := range picked {
		s.reserved[a.ID] += need
	}
	return append([]accounts.Account(nil), picked...), nil
}

// fits dice si el espacio libre de todas las cuentas suma al menos need.
func (s *Service) fits(cands []accounts.Account, need int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	free := int64(0)
	for _, a := range cands {
		free += max(s.freeOf(a), 0)
	}
	return free >= need
}

// freeOf devuelve cuánto puede recibir una cuenta: lo que queda de su aporte y
// lo que cabe en la cuenta real del proveedor (que también aloja lo ajeno a
// CloudMesh). Hay que llamarlo con s.mu tomado.
func (s *Service) freeOf(a accounts.Account) int64 {
	return a.Free() - s.reserved[a.ID]
}

func (s *Service) release(accountID, n int64) {
	s.mu.Lock()
	s.reserved[accountID] -= n
	if s.reserved[accountID] <= 0 {
		delete(s.reserved, accountID)
	}
	s.mu.Unlock()
}

// session devuelve la sesión y el id de la raíz remota de una cuenta.
func (s *Service) session(ctx context.Context, accountID int64) (storage.Session, string, error) {
	sess, err := s.accounts.Session(ctx, accountID)
	if err != nil {
		return nil, "", err
	}
	root, err := s.accounts.RootRemoteID(ctx, accountID)
	if err != nil {
		return nil, "", err
	}
	return sess, root, nil
}

// blocks devuelve (cacheado) el id de cloudmesh/blocks en una cuenta.
// blocks devuelve la carpeta de los bloques de un archivo,
// cloudmesh/blocks/<xx> con xx = id % 256 en hexadecimal: ninguna carpeta
// acumula todos los bloques y los de un archivo quedan juntos.
func (s *Service) blocks(ctx context.Context, accountID int64, sess storage.Session, root string, fileID int64) (string, error) {
	return s.blockDir(ctx, accountID, sess, root, blockBucket(fileID))
}

func blockBucket(fileID int64) string { return fmt.Sprintf("%02x", uint64(fileID)%256) }

type blockDirKey struct {
	account int64
	bucket  string
}

// blockDir resuelve, creándola si falta, cloudmesh/blocks/<bucket>; con
// bucket vacío, cloudmesh/blocks.
func (s *Service) blockDir(ctx context.Context, accountID int64, sess storage.Session, root, bucket string) (string, error) {
	key := blockDirKey{accountID, bucket}
	s.mu.Lock()
	id, ok := s.blocksDir[key]
	s.mu.Unlock()
	if ok {
		return id, nil
	}
	parent, name := root, storage.BlocksFolder
	if bucket != "" {
		var err error
		if parent, err = s.blockDir(ctx, accountID, sess, root, ""); err != nil {
			return "", err
		}
		name = bucket
	}
	id, err := sess.EnsureDir(ctx, parent, name)
	if err != nil {
		return "", s.accounts.HandleSessionError(ctx, accountID, err)
	}
	s.mu.Lock()
	s.blocksDir[key] = id
	s.mu.Unlock()
	return id, nil
}

// refreshQuotas actualiza en segundo plano la cuota de las cuentas tocadas.
func (s *Service) refreshQuotas(rows []sqlcgen.Chunk) {
	seen := map[int64]bool{}
	for _, c := range rows {
		if !c.AccountID.Valid || seen[c.AccountID.Int64] {
			continue
		}
		seen[c.AccountID.Int64] = true
		go func(id int64) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if _, err := s.accounts.RefreshQuota(ctx, id); err != nil {
				s.log.Debug("refrescar cuota", "account", id, "err", err)
			}
		}(c.AccountID.Int64)
	}
}

func mimeOf(name string) string {
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}

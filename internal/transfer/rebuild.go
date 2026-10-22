package transfer

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/hirasolutions/cloudmesh/internal/accounts"
	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
	"github.com/hirasolutions/cloudmesh/internal/erasure"
)

// RebuildResult resume la reparación de un archivo.
type RebuildResult struct {
	FileID  int64  `json:"file_id"`
	Rebuilt int    `json:"rebuilt"` // bloques regenerados y subidos
	Pending int    `json:"pending"` // bloques que siguen faltando (sin cuenta libre o franja perdida)
	Status  string `json:"status"`  // estado final del archivo
	Reason  string `json:"reason,omitempty"`
	// Transient: algún bloque no se pudo leer o subir por algo pasajero (red
	// caída, sesión): RebuildAll lo reintenta aunque la huella no cambie.
	Transient bool `json:"-"`
}

var ErrNoSpareAccount = errors.New("no hay ninguna cuenta libre donde recolocar el bloque")

// Rebuild regenera los bloques perdidos de un archivo a partir de los que
// quedan y los sube a cuentas que no tengan ya un bloque de esa franja.
func (s *Service) Rebuild(ctx context.Context, fileID int64) (RebuildResult, error) {
	res := RebuildResult{FileID: fileID}
	f, err := s.q.GetFile(ctx, fileID)
	if err != nil {
		return res, err
	}
	if f.Status == StatusUploading {
		res.Status, res.Reason = f.Status, "subida en curso"
		return res, nil
	}
	if f.Layout == LayoutParts {
		parts, err := s.q.ListFileParts(ctx, fileID)
		if err != nil {
			return res, err
		}
		for _, p := range parts {
			r, err := s.Rebuild(ctx, p.PartFileID)
			if err != nil {
				return res, err
			}
			res.Rebuilt += r.Rebuilt
			res.Pending += r.Pending
			res.Transient = res.Transient || r.Transient
			if r.Reason != "" {
				res.Reason = r.Reason
			}
		}
		res.Status, err = s.RecomputeStatus(ctx, fileID)
		return res, err
	}
	rows, err := s.q.ListChunkRowsByFile(ctx, fileID)
	if err != nil {
		return res, err
	}
	if f.Layout == LayoutWhole {
		// Sin redundancia no hay nada que regenerar.
		res.Status, err = s.RecomputeStatus(ctx, fileID)
		if res.Status != StatusReady {
			res.Reason = "archivo entero sin paridad: no se puede reconstruir"
			res.Pending = 1
		}
		return res, err
	}

	k, m := int(f.DataShards), int(f.ParityShards)
	codec, err := erasure.New(k, m)
	if err != nil {
		return res, err
	}
	// Los bloques regenerados salen siempre cifrados, aunque el archivo sea anterior al cifrado.
	aead, err := s.fileKey(ctx, fileID, true)
	if err != nil {
		return res, err
	}
	cands, err := s.candidates(ctx)
	if err != nil {
		return res, err
	}
	byStripe := map[int64][]sqlcgen.Chunk{}
	for _, c := range rows {
		byStripe[c.Stripe] = append(byStripe[c.Stripe], c)
	}
	for stripe, chunks := range byStripe {
		var lost []sqlcgen.Chunk
		holders := map[int64]bool{}
		// Una cuenta que acaba de perder un bloque no recibe el regenerado.
		lostOn := map[int64]bool{}
		for _, c := range chunks {
			if !c.AccountID.Valid || c.Missing != 0 {
				lost = append(lost, c)
				if c.AccountID.Valid {
					lostOn[c.AccountID.Int64] = true
				}
			} else {
				holders[c.AccountID.Int64] = true
			}
		}
		if len(lost) == 0 {
			continue
		}
		// Antes de descargar nada se comprueba que la reparación puede terminar:
		// sin k bloques o sin cuenta donde dejar el regenerado, la descarga se tira.
		if len(chunks)-len(lost) < k {
			res.Pending += len(lost)
			s.log.Warn("rebuild: franja irrecuperable", "file", fileID, "stripe", stripe, "present", len(chunks)-len(lost), "k", k)
			continue
		}
		// Cuentas libres: activas, habilitadas, sin bloque de esta franja ni bloque
		// perdido y con sitio para un bloque (pick lo vuelve a comprobar al subir).
		free := s.spare(cands, holders, lostOn, stripeBlockSize(chunks))
		if len(free) == 0 {
			res.Pending += len(lost)
			continue
		}
		shards, transient := s.fetchStripeShards(ctx, codec, aead, chunks)
		if err := codec.Rebuild(shards); err != nil {
			res.Pending += len(lost)
			if transient {
				res.Transient = true
				res.Reason = "no se pudieron descargar los bloques; se reintentará"
				s.log.Warn("rebuild: franja sin leer, se reintentará", "file", fileID, "stripe", stripe, "err", err)
				continue
			}
			s.log.Warn("rebuild: franja irrecuperable", "file", fileID, "stripe", stripe, "err", err)
			continue
		}
		for _, c := range lost {
			data := shards[c.Idx]
			if len(free) == 0 {
				res.Pending++
				continue
			}
			acc, err := s.placeShard(ctx, free, c, sealBlock(aead, fileID, stripe, c.Idx, data), fileID, stripe)
			if err != nil {
				if errors.Is(err, ErrNoSpace) || errors.Is(err, ErrNoAccounts) {
					res.Pending++
					continue
				}
				return res, err
			}
			holders[acc.ID] = true
			for i, a := range free {
				if a.ID == acc.ID {
					free = append(free[:i:i], free[i+1:]...)
					break
				}
			}
			res.Rebuilt++
		}
	}
	res.Status, err = s.RecomputeStatus(ctx, fileID)
	if res.Pending > 0 && res.Reason == "" {
		res.Reason = ErrNoSpareAccount.Error()
	}
	s.log.Info("rebuild", "file", fileID, "rebuilt", res.Rebuilt, "pending", res.Pending, "status", res.Status)
	return res, err
}

// spare filtra las cuentas que pueden recibir un bloque regenerado de la franja.
func (s *Service) spare(cands []accounts.Account, holders, lostOn map[int64]bool, size int64) []accounts.Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	free := cands[:0:0]
	for _, a := range cands {
		if !holders[a.ID] && !lostOn[a.ID] && s.freeOf(a) >= size {
			free = append(free, a)
		}
	}
	return free
}

// stripeBlockSize estima el tamaño de un bloque de la franja: todos los bloques
// de una franja miden lo mismo salvo los anteriores al cifrado, algo menores.
func stripeBlockSize(chunks []sqlcgen.Chunk) int64 {
	var size int64
	for _, c := range chunks {
		size = max(size, c.Size)
	}
	return size
}

// fetchStripeShards reúne k bloques presentes de una franja, en claro; los
// demás quedan nil para que el códec los regenere.
func (s *Service) fetchStripeShards(ctx context.Context, codec *erasure.Codec, aead cipher.AEAD, chunks []sqlcgen.Chunk) ([][]byte, bool) {
	rows := make([]sqlcgen.ListChunksRow, 0, len(chunks))
	for _, c := range chunks {
		rows = append(rows, chunkRow(c))
	}
	return s.gather(ctx, codec, aead, rows)
}

// chunkRow adapta una fila de chunks a la forma que usan las lecturas.
func chunkRow(c sqlcgen.Chunk) sqlcgen.ListChunksRow {
	return sqlcgen.ListChunksRow{
		ID: c.ID, FileID: c.FileID, Stripe: c.Stripe, Idx: c.Idx, AccountID: c.AccountID, RemoteID: c.RemoteID,
		Size: c.Size, Sha256: c.Sha256, Missing: c.Missing, Encrypted: c.Encrypted,
	}
}

// placeShard sube un bloque regenerado (ya cifrado) a una de las cuentas libres y actualiza la fila.
func (s *Service) placeShard(ctx context.Context, free []accounts.Account, c sqlcgen.Chunk, data []byte, fileID, stripe int64) (accounts.Account, error) {
	size := int64(len(data))
	picked, err := s.pick(free, 1, size, "most_free")
	if err != nil {
		return accounts.Account{}, err
	}
	acc := picked[0]
	defer s.release(acc.ID, size)
	sess, root, err := s.session(ctx, acc.ID)
	if err != nil {
		return accounts.Account{}, err
	}
	dir, err := s.blocks(ctx, acc.ID, sess, root, fileID)
	if err != nil {
		return accounts.Account{}, err
	}
	name := fmt.Sprintf("%d-%d-%d.cmb", fileID, stripe, c.Idx)
	e, err := sess.Upload(ctx, dir, name, size, bytes.NewReader(data))
	if err != nil {
		return accounts.Account{}, s.accounts.HandleSessionError(ctx, acc.ID, err)
	}
	// Si el bloque viejo sigue existiendo en otra cuenta (corrupto), se borra.
	if c.AccountID.Valid {
		s.deleteRemote(ctx, []sqlcgen.Chunk{c}, true)
	}
	sum := sha256.Sum256(data)
	if err := s.q.ReplaceChunk(ctx, sqlcgen.ReplaceChunkParams{
		AccountID: nullID(acc.ID), RemoteID: e.ID, Size: size, Sha256: hex.EncodeToString(sum[:]), Encrypted: 1, CheckedAt: s.now().Unix(), ID: c.ID,
	}); err != nil {
		_ = sess.Delete(ctx, e.ID, true)
		return accounts.Account{}, err
	}
	s.refreshQuotas([]sqlcgen.Chunk{{AccountID: nullID(acc.ID)}})
	return acc, nil
}

// RebuildAll repara, uno a uno, todos los archivos con bloques perdidos. Un
// archivo que se quedó a medias no se reintenta hasta que cambien sus bloques
// o las cuentas candidatas: cada intento descarga k bloques por franja.
func (s *Service) RebuildAll(ctx context.Context) []RebuildResult {
	ids, err := s.q.ListFileIDsNeedingRepair(ctx)
	if err != nil {
		s.log.Error("rebuild: listar", "err", err)
		return nil
	}
	cands, err := s.candidates(ctx)
	if err != nil {
		s.log.Error("rebuild: cuentas", "err", err)
		return nil
	}
	pending := map[int64]string{}
	var out []RebuildResult
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		fp, err := s.rebuildPrint(ctx, id, cands)
		if err != nil {
			s.log.Warn("rebuild: archivo", "file", id, "err", err)
			continue
		}
		s.mu.Lock()
		last := s.rebuildSkip[id]
		s.mu.Unlock()
		if last == fp {
			s.log.Debug("rebuild: sin cambios desde el último intento", "file", id)
			pending[id] = fp
			continue
		}
		r, err := s.Rebuild(ctx, id)
		if err != nil {
			s.log.Warn("rebuild: archivo", "file", id, "err", err)
			continue
		}
		if r.Pending > 0 && !r.Transient {
			// La huella se toma después: Rebuild pudo recolocar parte de los bloques.
			if cands, err = s.candidates(ctx); err == nil {
				if fp, err = s.rebuildPrint(ctx, id, cands); err == nil {
					pending[id] = fp
				}
			}
		}
		out = append(out, r)
	}
	if ctx.Err() == nil {
		s.mu.Lock()
		s.rebuildSkip = pending // los archivos ya reparados o borrados salen del mapa
		s.mu.Unlock()
	}
	return out
}

// rebuildPrint resume, con datos locales, todo lo que decide si una reparación
// puede salir distinta: los bloques del archivo y las cuentas donde colocarlos.
func (s *Service) rebuildPrint(ctx context.Context, fileID int64, cands []accounts.Account) (string, error) {
	parts, err := s.q.ListFileParts(ctx, fileID)
	if err != nil {
		return "", err
	}
	ids := []int64{fileID}
	for _, p := range parts {
		ids = append(ids, p.PartFileID)
	}
	h := sha256.New()
	var block int64
	for _, id := range ids {
		rows, err := s.q.ListChunkRowsByFile(ctx, id)
		if err != nil {
			return "", err
		}
		for _, c := range rows {
			fmt.Fprintf(h, "c%d:%d:%t:%d;", c.ID, c.AccountID.Int64, c.AccountID.Valid, c.Missing)
		}
		block = max(block, stripeBlockSize(rows))
	}
	// De cada cuenta solo importa si cabe un bloque, no cuánto espacio le queda.
	s.mu.Lock()
	for _, a := range cands {
		fmt.Fprintf(h, "a%d:%t:%t;", a.ID, s.freeOf(a) >= block, s.accounts.Throttled(a.ID))
	}
	s.mu.Unlock()
	return hex.EncodeToString(h.Sum(nil)), nil
}

package transfer

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
)

// Cifrado de bloques. Cada archivo tiene una clave AES-256 propia, guardada en
// files.enc_key envuelta con la clave maestra; cada bloque se sella con
// AES-256-GCM antes de salir hacia el proveedor, que solo ve datos cifrados.
//
// El nonce se deriva de (franja, índice): cada posición tiene un solo bloque
// por clave y regenerarlo da exactamente el mismo contenido, así que repetir
// nonce solo repite el mismo texto cifrado. El AAD ata el bloque a su
// archivo y posición: no se puede cambiar un bloque por otro sin que falle.

// sealOverhead es lo que crece un bloque al cifrarlo (la etiqueta GCM).
const sealOverhead = 16

var ErrBlockAuth = errors.New("bloque cifrado alterado o con otra clave")

// fileKey devuelve el cifrador de un archivo. Con create genera y guarda la
// clave si aún no tiene; sin create, nil significa que no hay clave.
func (s *Service) fileKey(ctx context.Context, fileID int64, create bool) (cipher.AEAD, error) {
	f, err := s.q.GetFile(ctx, fileID)
	if err != nil {
		return nil, err
	}
	if len(f.EncKey) == 0 {
		if !create {
			return nil, nil
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		wrapped, err := s.box.Seal(key)
		if err != nil {
			return nil, err
		}
		// Solo la primera escritura gana: dos goroutines no pueden dejar claves distintas.
		if _, err := s.q.SetFileKey(ctx, sqlcgen.SetFileKeyParams{EncKey: wrapped, ID: fileID}); err != nil {
			return nil, err
		}
		if f, err = s.q.GetFile(ctx, fileID); err != nil {
			return nil, err
		}
	}
	key, err := s.box.Open(f.EncKey)
	if err != nil {
		return nil, fmt.Errorf("clave del archivo %d: %w", fileID, err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func blockNonce(stripe, idx int64) []byte {
	n := make([]byte, 12)
	binary.BigEndian.PutUint64(n[:8], uint64(stripe))
	binary.BigEndian.PutUint32(n[8:], uint32(idx))
	return n
}

func blockAAD(fileID, stripe, idx int64) []byte {
	return fmt.Appendf(nil, "cloudmesh/block/v1/%d/%d/%d", fileID, stripe, idx)
}

func sealBlock(aead cipher.AEAD, fileID, stripe, idx int64, plain []byte) []byte {
	return aead.Seal(nil, blockNonce(stripe, idx), plain, blockAAD(fileID, stripe, idx))
}

func openBlock(aead cipher.AEAD, fileID, stripe, idx int64, sealed []byte) ([]byte, error) {
	plain, err := aead.Open(nil, blockNonce(stripe, idx), sealed, blockAAD(fileID, stripe, idx))
	if err != nil {
		return nil, ErrBlockAuth
	}
	return plain, nil
}

// EncryptPending cifra hasta limit bloques antiguos (subidos antes del
// cifrado): descarga cada uno, lo sella con la clave de su archivo, lo sube a
// la misma cuenta y destruye el original. Cada bloque se convierte por
// separado, así una pasada cortada deja el archivo legible. Salta las cuentas
// con límite de transferencia. Devuelve cuántos convirtió; lo llama el
// mantenimiento, poco a poco para no agotar la transferencia de Mega.
func (s *Service) EncryptPending(ctx context.Context, limit int) (int, error) {
	s.mu.Lock()
	after := s.encCursor
	s.mu.Unlock()
	rows, err := s.q.ListPlainChunks(ctx, sqlcgen.ListPlainChunksParams{ID: after, Limit: int64(limit)})
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 && after > 0 {
		// Vuelta completa: se empieza otra vez por los que fallaron.
		s.mu.Lock()
		s.encCursor = 0
		s.mu.Unlock()
		return 0, nil
	}
	n := 0
	for _, c := range rows {
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		s.mu.Lock()
		s.encCursor = c.ID
		s.mu.Unlock()
		if s.accounts.Throttled(c.AccountID.Int64) {
			continue
		}
		if err := s.encryptChunk(ctx, c); err != nil {
			s.log.Warn("cifrar bloque antiguo", "file", c.FileID, "chunk", c.ID, "account", c.AccountID.Int64, "err", err)
			continue
		}
		n++
	}
	return n, nil
}

func (s *Service) encryptChunk(ctx context.Context, c sqlcgen.Chunk) error {
	aead, err := s.fileKey(ctx, c.FileID, true)
	if err != nil {
		return err
	}
	plain, err := s.fetchShard(ctx, chunkRow(c), nil)
	if err != nil {
		return err
	}
	sealed := sealBlock(aead, c.FileID, c.Stripe, c.Idx, plain)
	sess, root, err := s.session(ctx, c.AccountID.Int64)
	if err != nil {
		return err
	}
	dir, err := s.blocks(ctx, c.AccountID.Int64, sess, root, c.FileID)
	if err != nil {
		return err
	}
	e, err := sess.Upload(ctx, dir, fmt.Sprintf("%d-%d-%d.cmb", c.FileID, c.Stripe, c.Idx), int64(len(sealed)), bytes.NewReader(sealed))
	if err != nil {
		return s.accounts.HandleSessionError(ctx, c.AccountID.Int64, err)
	}
	sum := sha256.Sum256(sealed)
	// Solo si nadie tocó el bloque mientras tanto (rebuild, borrado).
	changed, err := s.q.EncryptChunk(ctx, sqlcgen.EncryptChunkParams{
		RemoteID: e.ID, Size: int64(len(sealed)), Sha256: hex.EncodeToString(sum[:]), CheckedAt: s.now().Unix(),
		ID: c.ID, RemoteID_2: c.RemoteID,
	})
	if err != nil || changed == 0 {
		_ = sess.Delete(context.WithoutCancel(ctx), e.ID, true)
		if err == nil {
			err = errors.New("el bloque cambió mientras se cifraba")
		}
		return err
	}
	s.deleteRemote(ctx, []sqlcgen.Chunk{c}, true)
	s.refreshQuotas([]sqlcgen.Chunk{c})
	return nil
}

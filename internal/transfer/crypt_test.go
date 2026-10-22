package transfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/hirasolutions/cloudmesh/internal/accounts"
	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
	"github.com/hirasolutions/cloudmesh/internal/settings"
	"github.com/hirasolutions/cloudmesh/internal/storage"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

func TestBlocksEncryptedAtRest(t *testing.T) {
	e := setup(t, 3)
	ctx := context.Background()
	data := bytes.Repeat([]byte("CloudMesh texto reconocible "), 100_000) // ~2,8 MB, varias franjas
	f, err := e.s.Upload(ctx, vfs.RootID, "claro.txt", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := e.s.q.ListChunkRowsByFile(ctx, f.ID)
	for _, c := range rows {
		if c.Encrypted != 1 {
			t.Fatalf("bloque sin cifrar: %+v", c)
		}
		if bytes.Contains(e.p.Blobs[c.RemoteID], []byte("texto reconocible")) {
			t.Fatal("el proveedor ve el contenido en claro")
		}
	}
	if st, err := e.s.Open(ctx, f.ID, 0, -1); err != nil || !bytes.Equal(readAll(t, st), data) {
		t.Fatalf("lectura descifrada distinta: %v", err)
	}

	// Un bloque de datos alterado (con su hash recalculado, como haría alguien
	// con acceso a la cuenta) no pasa la autenticación: se lee por paridad.
	victim := rows[0]
	blob := append([]byte(nil), e.p.Blobs[victim.RemoteID]...)
	blob[10] ^= 0xff
	e.p.Blobs[victim.RemoteID] = blob
	sum := sha256.Sum256(blob)
	if err := e.s.q.ReplaceChunk(ctx, sqlcgen.ReplaceChunkParams{
		AccountID: victim.AccountID, RemoteID: victim.RemoteID, Size: victim.Size, Sha256: hex.EncodeToString(sum[:]), Encrypted: 1, ID: victim.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if st, err := e.s.Open(ctx, f.ID, 0, -1); err != nil || !bytes.Equal(readAll(t, st), data) {
		t.Fatalf("con un bloque alterado la paridad debía cubrirlo: %v", err)
	}

	// Un bloque movido a otra posición tampoco descifra (el AAD ata archivo y posición).
	aead, _ := e.s.fileKey(ctx, f.ID, false)
	if _, err := openBlock(aead, f.ID, rows[1].Stripe, rows[1].Idx+1, e.p.Blobs[rows[1].RemoteID]); err == nil {
		t.Fatal("un bloque descifró en otra posición")
	}
}

// legacy deja los bloques de un archivo como antes del cifrado: en claro en el proveedor.
func legacy(t *testing.T, e env, fileID int64) {
	t.Helper()
	ctx := context.Background()
	aead, err := e.s.fileKey(ctx, fileID, false)
	if err != nil || aead == nil {
		t.Fatalf("clave: %v", err)
	}
	rows, _ := e.s.q.ListChunkRowsByFile(ctx, fileID)
	for _, c := range rows {
		plain, err := openBlock(aead, fileID, c.Stripe, c.Idx, e.p.Blobs[c.RemoteID])
		if err != nil {
			t.Fatal(err)
		}
		e.p.Blobs[c.RemoteID] = plain
		sum := sha256.Sum256(plain)
		if err := e.s.q.ReplaceChunk(ctx, sqlcgen.ReplaceChunkParams{
			AccountID: c.AccountID, RemoteID: c.RemoteID, Size: int64(len(plain)), Sha256: hex.EncodeToString(sum[:]), Encrypted: 0, ID: c.ID,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEncryptPending(t *testing.T) {
	e := setup(t, 3)
	ctx := context.Background()
	data := randomBytes(3<<20 + 77)
	f, err := e.s.Upload(ctx, vfs.RootID, "viejo.bin", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	legacy(t, e, f.ID)
	plain, _ := e.s.q.CountPlainChunks(ctx)
	if plain == 0 {
		t.Fatal("no quedaron bloques en claro")
	}
	// Mezcla de bloques en claro y cifrados: se sigue leyendo.
	if n, err := e.s.EncryptPending(ctx, 2); err != nil || n != 2 {
		t.Fatalf("primera tanda: n=%d err=%v", n, err)
	}
	if st, err := e.s.Open(ctx, f.ID, 0, -1); err != nil || !bytes.Equal(readAll(t, st), data) {
		t.Fatalf("lectura a medio cifrar: %v", err)
	}
	for range 10 {
		if _, err := e.s.EncryptPending(ctx, 4); err != nil {
			t.Fatal(err)
		}
	}
	if left, _ := e.s.q.CountPlainChunks(ctx); left != 0 {
		t.Fatalf("quedan %d bloques en claro", left)
	}
	if st, err := e.s.Open(ctx, f.ID, 0, -1); err != nil || !bytes.Equal(readAll(t, st), data) {
		t.Fatalf("lectura tras cifrar: %v", err)
	}
	// Los bloques en claro se destruyeron: solo quedan los cifrados.
	rows, _ := e.s.q.ListChunkRowsByFile(ctx, f.ID)
	if len(e.p.Blobs) != len(rows) {
		t.Fatalf("blobs = %d, bloques = %d", len(e.p.Blobs), len(rows))
	}
	// Y el scrub los da por buenos (tamaño nuevo apuntado).
	for _, a := range e.accs {
		if r, err := e.s.Scrub(ctx, a.ID); err != nil || r.Missing != 0 {
			t.Fatalf("scrub tras cifrar = %+v %v", r, err)
		}
	}
}

func TestTransferLimitedAccount(t *testing.T) {
	e := setup(t, 3)
	ctx := context.Background()
	data := randomBytes(2<<20 + 5)
	f, err := e.s.Upload(ctx, vfs.RootID, "x.bin", int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	e.s.cache.drop(f.ID) // la subida la dejó en la caché
	// Se agota la transferencia de la cuenta con el bloque de datos 0 de la primera franja.
	rows, _ := e.s.q.ListChunkRowsByFile(ctx, f.ID)
	var limited accounts.Account
	for _, a := range e.accs {
		if a.ID == rows[0].AccountID.Int64 {
			limited = a
		}
	}
	e.p.TransferLimited[limited.Email] = true
	if st, err := e.s.Open(ctx, f.ID, 0, -1); err != nil || !bytes.Equal(readAll(t, st), data) {
		t.Fatalf("lectura con una cuenta limitada: %v", err)
	}
	if !e.s.accounts.Throttled(limited.ID) {
		t.Fatal("la cuenta no quedó apartada")
	}
	acc, _ := e.s.accounts.Get(ctx, limited.ID)
	if acc.ThrottledUntil == 0 || acc.Status != accounts.StatusActive {
		t.Fatalf("cuenta = %+v", acc)
	}
	// La siguiente lectura ni lo intenta en la cuenta limitada.
	e.p.TransferLimited[limited.Email] = false
	e.s.cache.drop(f.ID) // que la lectura vuelva a los bloques
	before := e.p.Downloads[limited.Email]
	if st, err := e.s.Open(ctx, f.ID, 0, -1); err != nil || !bytes.Equal(readAll(t, st), data) {
		t.Fatalf("segunda lectura: %v", err)
	}
	if e.p.Downloads[limited.Email] != before {
		t.Fatal("se leyó de la cuenta apartada teniendo paridad")
	}
	// Con dos cuentas perdidas no basta la paridad: se recurre a la limitada.
	other := e.accs[0]
	if other.ID == limited.ID {
		other = e.accs[1]
	}
	for _, c := range rows {
		if c.AccountID.Int64 == other.ID {
			e.p.FailDownloads[c.RemoteID] = true
		}
	}
	e.s.cache.drop(f.ID)
	if st, err := e.s.Open(ctx, f.ID, 0, -1); err != nil || !bytes.Equal(readAll(t, st), data) {
		t.Fatalf("lectura de último recurso: %v", err)
	}
}

func TestUploadAvoidsLimitedAccount(t *testing.T) {
	e := setup(t, 4)
	ctx := context.Background()
	// 1+0 para que cada archivo vaya a una sola cuenta.
	if _, err := e.st.Update(ctx, settings.Settings{RoutingPolicy: settings.PolicyMostFree, DataShards: 1, ParityShards: 0, ChunkSize: settings.MinChunk}); err != nil {
		t.Fatal(err)
	}
	limited := e.accs[2]
	e.s.accounts.HandleSessionError(ctx, limited.ID, storage.ErrTransferQuota)
	for i := range 8 {
		f, err := e.s.Upload(ctx, vfs.RootID, fmt.Sprintf("f%d", i), 10, bytes.NewReader(randomBytes(10)))
		if err != nil {
			t.Fatal(err)
		}
		rows, _ := e.s.q.ListChunkRowsByFile(ctx, f.ID)
		if rows[0].AccountID.Int64 == limited.ID {
			t.Fatal("se subió a la cuenta limitada habiendo otras")
		}
	}
}

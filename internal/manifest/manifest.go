// Package manifest exporta el índice completo cifrado a cada cuenta para
// poder reconstruir la instancia con la clave maestra y una sola nube viva.
package manifest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/accounts"
	"github.com/hirasolutions/cloudmesh/internal/crypto"
	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
	"github.com/hirasolutions/cloudmesh/internal/storage"
)

// Name es el archivo que se deja en la raíz cloudmesh/ de cada cuenta.
const Name = "manifest.json.enc"

const (
	keyHash      = "manifest_sha256"
	keyWrittenAt = "manifest_written_at"
)

var ErrNotEmpty = errors.New("la base ya tiene usuarios o cuentas: restaurar solo vale en una instancia vacía")

// Manifest es el volcado íntegro del índice. Las credenciales van ya cifradas
// con la clave maestra (tal cual están en la base) y el conjunto se cifra otra vez.
type Manifest struct {
	Version   int               `json:"version"`
	CreatedAt int64             `json:"created_at"`
	Settings  []sqlcgen.Setting `json:"settings"`
	Users     []sqlcgen.User    `json:"users"`
	Accounts  []sqlcgen.Account `json:"accounts"`
	Folders   []sqlcgen.Folder  `json:"folders"`
	Files     []sqlcgen.File    `json:"files"`
	Chunks    []Chunk           `json:"chunks"`
	// Objetos multipart de S3 y la configuración de la API S3.
	FileParts  []sqlcgen.FilePart               `json:"file_parts"`
	Buckets    []sqlcgen.S3Bucket               `json:"s3_buckets"`
	Keys       []sqlcgen.ListKeysForManifestRow `json:"s3_keys"`
	KeyBuckets []sqlcgen.S3KeyBucket            `json:"s3_key_buckets"`
	// FolderAccess va detrás de users y folders al restaurar (claves ajenas).
	FolderAccess []sqlcgen.FolderAccess `json:"folder_access"`
}

// Chunk evita sql.NullInt64 en el JSON.
type Chunk struct {
	ID        int64  `json:"id"`
	FileID    int64  `json:"file_id"`
	Stripe    int64  `json:"stripe"`
	Idx       int64  `json:"idx"`
	Kind      string `json:"kind"`
	AccountID *int64 `json:"account_id"`
	RemoteID  string `json:"remote_id"`
	Size      int64  `json:"size"`
	Sha256    string `json:"sha256"`
	Missing   int64  `json:"missing"`
	Encrypted int64  `json:"encrypted,omitempty"`
}

type Service struct {
	db       *sql.DB
	q        *sqlcgen.Queries
	box      *crypto.Box
	accounts *accounts.Service
	log      *slog.Logger
	now      func() time.Time
}

func NewService(sqlDB *sql.DB, box *crypto.Box, acc *accounts.Service, log *slog.Logger) *Service {
	return &Service{db: sqlDB, q: sqlcgen.New(sqlDB), box: box, accounts: acc, log: log, now: time.Now}
}

// Build serializa el índice. El hash devuelto ignora created_at.
func (s *Service) Build(ctx context.Context) (Manifest, string, error) {
	var m Manifest
	var err error
	m.Version = 1
	all, err := s.q.ListSettings(ctx)
	if err != nil {
		return m, "", err
	}
	// Las claves del propio manifiesto cambian al escribirlo: fuera del volcado y del hash.
	m.Settings = make([]sqlcgen.Setting, 0, len(all))
	for _, st := range all {
		if st.Key != keyHash && st.Key != keyWrittenAt {
			m.Settings = append(m.Settings, st)
		}
	}
	if m.Users, err = s.q.ListUsers(ctx); err != nil {
		return m, "", err
	}
	if m.Accounts, err = s.q.ListAccounts(ctx); err != nil {
		return m, "", err
	}
	if m.Folders, err = s.q.ListAllFolders(ctx); err != nil {
		return m, "", err
	}
	if m.Files, err = s.q.ListAllFiles(ctx); err != nil {
		return m, "", err
	}
	if m.FileParts, err = s.q.ListAllFileParts(ctx); err != nil {
		return m, "", err
	}
	if m.Buckets, err = s.q.ListBuckets(ctx); err != nil {
		return m, "", err
	}
	if m.Keys, err = s.q.ListKeysForManifest(ctx); err != nil {
		return m, "", err
	}
	if m.FolderAccess, err = s.q.ListAllFolderAccess(ctx); err != nil {
		return m, "", err
	}
	if m.KeyBuckets, err = s.q.ListAllKeyBuckets(ctx); err != nil {
		return m, "", err
	}
	rows, err := s.q.ListAllChunks(ctx)
	if err != nil {
		return m, "", err
	}
	m.Chunks = make([]Chunk, 0, len(rows))
	for _, r := range rows {
		c := Chunk{ID: r.ID, FileID: r.FileID, Stripe: r.Stripe, Idx: r.Idx, Kind: r.Kind, RemoteID: r.RemoteID, Size: r.Size, Sha256: r.Sha256, Missing: r.Missing, Encrypted: r.Encrypted}
		if r.AccountID.Valid {
			id := r.AccountID.Int64
			c.AccountID = &id
		}
		m.Chunks = append(m.Chunks, c)
	}
	// Volátiles fuera del hash: el manifiesto no debe reescribirse por ellos.
	for i := range m.Users {
		m.Users[i].TotpLastStep = 0 // cambia en cada entrada con 2FA
	}
	for i := range m.Accounts {
		m.Accounts[i].ManifestRemoteID = ""
		m.Accounts[i].LastSyncAt = 0
		m.Accounts[i].QuotaUsed = 0
		m.Accounts[i].ForeignUsed = 0 // cambia en cada refresco de cuota, incluso por subir el propio manifiesto
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return m, "", err
	}
	sum := sha256.Sum256(raw)
	m.CreatedAt = s.now().Unix()
	return m, hex.EncodeToString(sum[:]), nil
}

// Seal cifra el manifiesto con la clave maestra.
func (s *Service) Seal(m Manifest) ([]byte, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return s.box.Seal(raw)
}

// Open descifra y decodifica un manifiesto.
func (s *Service) Open(data []byte) (Manifest, error) {
	raw, err := s.box.Open(data)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("manifiesto corrupto: %w", err)
	}
	return m, nil
}

// WriteIfChanged sube el manifiesto a todas las cuentas activas si el índice
// cambió desde la última escritura. Devuelve cuántas cuentas lo recibieron.
func (s *Service) WriteIfChanged(ctx context.Context, force bool) (int, error) {
	m, hash, err := s.Build(ctx)
	if err != nil {
		return 0, err
	}
	last, _ := s.q.GetSetting(ctx, keyHash)
	if !force && last == hash {
		return 0, nil
	}
	data, err := s.Seal(m)
	if err != nil {
		return 0, err
	}
	written, failed := 0, 0
	for _, a := range m.Accounts {
		if a.Status != accounts.StatusActive {
			continue
		}
		if err := s.writeTo(ctx, a, data); err != nil {
			s.log.Warn("manifiesto: cuenta", "account", a.ID, "err", err)
			failed++
			continue
		}
		written++
	}
	if written == 0 && len(m.Accounts) > 0 {
		return 0, errors.New("ninguna cuenta aceptó el manifiesto")
	}
	// Si alguna cuenta activa no lo recibió (p. ej. un corte de red), el hash
	// no se guarda: la siguiente pasada lo vuelve a escribir en todas.
	if failed == 0 {
		if err := s.q.UpsertSetting(ctx, sqlcgen.UpsertSettingParams{Key: keyHash, Value: hash}); err != nil {
			return written, err
		}
	}
	if err := s.q.UpsertSetting(ctx, sqlcgen.UpsertSettingParams{Key: keyWrittenAt, Value: strconv.FormatInt(s.now().Unix(), 10)}); err != nil {
		return written, err
	}
	s.log.Info("manifiesto escrito", "accounts", written, "failed", failed)
	return written, nil
}

// WrittenAt devuelve el unix de la última escritura (0 si nunca).
func (s *Service) WrittenAt(ctx context.Context) int64 {
	v, err := s.q.GetSetting(ctx, keyWrittenAt)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

// writeTo sube el nuevo manifiesto y borra el anterior de esa cuenta.
func (s *Service) writeTo(ctx context.Context, a sqlcgen.Account, data []byte) error {
	sess, err := s.accounts.Session(ctx, a.ID)
	if err != nil {
		return err
	}
	root, err := s.accounts.RootRemoteID(ctx, a.ID)
	if err != nil {
		return err
	}
	e, err := sess.Upload(ctx, root, Name, int64(len(data)), bytesReader(data))
	if err != nil {
		return s.accounts.HandleSessionError(ctx, a.ID, err)
	}
	row, err := s.q.GetAccount(ctx, a.ID)
	if err == nil && row.ManifestRemoteID != "" && row.ManifestRemoteID != e.ID {
		if err := sess.Delete(ctx, row.ManifestRemoteID, true); err != nil && !errors.Is(err, storage.ErrNotFound) {
			s.log.Debug("manifiesto: borrar anterior", "account", a.ID, "err", err)
		}
	}
	return s.q.SetAccountManifest(ctx, sqlcgen.SetAccountManifestParams{ManifestRemoteID: e.ID, ID: a.ID})
}

// Fetch busca y descarga el manifiesto en la raíz de una sesión.
func Fetch(ctx context.Context, sess storage.Session) ([]byte, error) {
	root, err := sess.EnsureRoot(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := sess.List(ctx, root)
	if err != nil {
		return nil, err
	}
	var found *storage.Entry
	for i := range entries {
		if !entries[i].IsDir && entries[i].Name == Name {
			if found == nil || entries[i].ModTime.After(found.ModTime) {
				found = &entries[i]
			}
		}
	}
	if found == nil {
		return nil, fmt.Errorf("no hay %s en esta cuenta", Name)
	}
	body, err := sess.Download(ctx, found.ID, 0, -1)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(body)
}

// Restore vuelca un manifiesto en una base vacía. Las cuentas quedan en
// reconnect_required: sus sesiones guardadas pueden haber caducado.
func (s *Service) Restore(ctx context.Context, m Manifest) error {
	users, err := s.q.CountUsers(ctx)
	if err != nil {
		return err
	}
	accs, err := s.q.ListAccounts(ctx)
	if err != nil {
		return err
	}
	if users > 0 || len(accs) > 0 {
		return ErrNotEmpty
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)
	for _, u := range m.Users {
		if u.TotpSecret == nil {
			u.TotpSecret = []byte{} // manifiestos sin 2FA o anteriores a ella
		}
		if u.Role == "" {
			u.Role = "admin" // anteriores a los roles: todos eran administradores
		}
		if u.RecoveryCodes == "" {
			u.RecoveryCodes = "[]" // anteriores a los códigos de recuperación
		}
		if err := q.InsertUserWithID(ctx, sqlcgen.InsertUserWithIDParams{
			ID: u.ID, Email: u.Email, PasswordHash: u.PasswordHash, Name: u.Name, CreatedAt: u.CreatedAt,
			TotpSecret: u.TotpSecret, TotpEnabled: u.TotpEnabled, TotpLastStep: u.TotpLastStep, RecoveryCodes: u.RecoveryCodes,
			Role: u.Role, QuotaBytes: u.QuotaBytes,
		}); err != nil {
			return fmt.Errorf("usuarios: %w", err)
		}
	}
	for _, a := range m.Accounts {
		if err := q.InsertAccountWithID(ctx, sqlcgen.InsertAccountWithIDParams{
			ID: a.ID, Provider: a.Provider, Label: a.Label, Email: a.Email, EncCredentials: a.EncCredentials,
			Status: accounts.StatusReconnectRequired, Enabled: a.Enabled, Priority: a.Priority,
			QuotaUsed: 0, QuotaTotal: a.QuotaTotal, RootRemoteID: a.RootRemoteID,
			LastError: "restaurada desde manifiesto: reconecta para reanudar", LastSyncAt: 0, CreatedAt: a.CreatedAt,
			ContribMode: a.ContribMode, ContribValue: a.ContribValue,
		}); err != nil {
			return fmt.Errorf("cuentas: %w", err)
		}
	}
	for _, st := range m.Settings {
		if st.Key == keyHash || st.Key == keyWrittenAt {
			continue
		}
		if err := q.UpsertSetting(ctx, sqlcgen.UpsertSettingParams{Key: st.Key, Value: st.Value}); err != nil {
			return fmt.Errorf("ajustes: %w", err)
		}
	}
	for _, f := range m.Folders {
		if err := q.InsertFolderWithID(ctx, sqlcgen.InsertFolderWithIDParams{ID: f.ID, ParentID: f.ParentID, Name: f.Name, CreatedAt: f.CreatedAt, Restricted: f.Restricted}); err != nil {
			return fmt.Errorf("carpetas: %w", err)
		}
	}
	for _, f := range m.Files {
		if err := q.InsertFileWithID(ctx, sqlcgen.InsertFileWithIDParams{
			ID: f.ID, FolderID: f.FolderID, Name: f.Name, Size: f.Size, Mime: f.Mime, Mtime: f.Mtime, Layout: f.Layout,
			DataShards: f.DataShards, ParityShards: f.ParityShards, ChunkSize: f.ChunkSize, Sha256: f.Sha256, Status: f.Status, CreatedAt: f.CreatedAt, DeletedAt: f.DeletedAt,
			Etag: f.Etag, Internal: f.Internal, Meta: f.Meta, OwnerID: f.OwnerID, EncKey: f.EncKey,
		}); err != nil {
			return fmt.Errorf("archivos: %w", err)
		}
	}
	for _, p := range m.FileParts {
		if err := q.InsertFilePart(ctx, sqlcgen.InsertFilePartParams{FileID: p.FileID, PartNumber: p.PartNumber, PartFileID: p.PartFileID, Size: p.Size}); err != nil {
			return fmt.Errorf("partes: %w", err)
		}
	}
	for _, b := range m.Buckets {
		if err := q.InsertBucketWithID(ctx, sqlcgen.InsertBucketWithIDParams{
			ID: b.ID, Name: b.Name, FolderID: b.FolderID, CreatedAt: b.CreatedAt, Public: b.Public, Cors: b.Cors, Lifecycle: b.Lifecycle,
		}); err != nil {
			return fmt.Errorf("buckets: %w", err)
		}
	}
	for _, k := range m.Keys {
		if err := q.InsertKeyWithID(ctx, sqlcgen.InsertKeyWithIDParams{
			ID: k.ID, AccessKeyID: k.AccessKeyID, EncSecret: k.EncSecret, Name: k.Name, Permission: k.Permission,
			CreatedBy: k.CreatedBy, CreatedAt: k.CreatedAt,
		}); err != nil {
			return fmt.Errorf("llaves S3: %w", err)
		}
	}
	for _, kb := range m.KeyBuckets {
		if err := q.AddKeyBucket(ctx, sqlcgen.AddKeyBucketParams{KeyID: kb.KeyID, BucketID: kb.BucketID}); err != nil {
			return fmt.Errorf("llaves S3: %w", err)
		}
	}
	for _, a := range m.FolderAccess {
		if err := q.AddFolderAccess(ctx, sqlcgen.AddFolderAccessParams{FolderID: a.FolderID, UserID: a.UserID, Level: a.Level}); err != nil {
			return fmt.Errorf("acceso a carpetas: %w", err)
		}
	}
	for _, c := range m.Chunks {
		acc := sql.NullInt64{}
		if c.AccountID != nil {
			acc = sql.NullInt64{Int64: *c.AccountID, Valid: true}
		}
		if err := q.InsertChunkWithID(ctx, sqlcgen.InsertChunkWithIDParams{
			ID: c.ID, FileID: c.FileID, Stripe: c.Stripe, Idx: c.Idx, Kind: c.Kind, AccountID: acc,
			RemoteID: c.RemoteID, Size: c.Size, Sha256: c.Sha256, Missing: c.Missing, CheckedAt: 0, Encrypted: c.Encrypted,
		}); err != nil {
			return fmt.Errorf("bloques: %w", err)
		}
	}
	return tx.Commit()
}

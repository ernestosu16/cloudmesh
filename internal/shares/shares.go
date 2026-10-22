// Package shares gestiona los enlaces públicos a un archivo o a una carpeta.
//
// Como en Mega, un enlace es de solo lectura y puede llevar contraseña. Quien
// abre un enlace con contraseña recibe un permiso firmado con HMAC(secret,
// token + hash de la contraseña): cambiar la contraseña invalida los permisos
// ya emitidos sin guardar estado.
package shares

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/auth"
	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

const (
	KindFile   = "file"
	KindFolder = "folder"
)

var (
	ErrNotFound      = errors.New("enlace no encontrado")
	ErrWrongPassword = errors.New("contraseña incorrecta")
	ErrInvalidTarget = errors.New("solo se puede compartir un archivo o una carpeta que no sea la raíz")
	ErrOutside       = errors.New("el elemento no pertenece al enlace")
	ErrWeakPassword  = errors.New("la contraseña del enlace debe tener al menos 4 caracteres")
)

// Share es la vista de un enlace. Nunca expone el hash de la contraseña.
type Share struct {
	ID             int64  `json:"id"`
	Token          string `json:"token"`
	Kind           string `json:"kind"`
	TargetID       int64  `json:"target_id"`
	Name           string `json:"name"`
	ParentID       int64  `json:"parent_id"`
	Size           int64  `json:"size"`
	HasPassword    bool   `json:"has_password"`
	Downloads      int64  `json:"downloads"` // informativo, sin límite
	CreatedAt      int64  `json:"created_at"`
	CreatedBy      int64  `json:"created_by"`
	CreatedByEmail string `json:"created_by_email,omitempty"`

	passwordHash string
}

// Options son los ajustes editables. Password nil conserva la actual y "" la quita.
type Options struct {
	Password *string `json:"password"`
}

type Service struct {
	q      *sqlcgen.Queries
	vfs    *vfs.Service
	secret []byte
	now    func() time.Time
}

// NewService recibe el secreto de sesión para firmar los permisos de acceso.
func NewService(sqlDB *sql.DB, v *vfs.Service, secret []byte) *Service {
	return &Service{q: sqlcgen.New(sqlDB), vfs: v, secret: secret, now: time.Now}
}

// Create comparte un archivo (kind file) o una carpeta (kind folder).
func (s *Service) Create(ctx context.Context, userID int64, kind string, targetID int64, opt Options) (Share, error) {
	p := sqlcgen.CreateShareParams{CreatedBy: userID, CreatedAt: s.now().Unix()}
	switch kind {
	case KindFile:
		f, err := s.vfs.GetFile(ctx, targetID)
		if err != nil {
			return Share{}, err
		}
		p.FileID = sql.NullInt64{Int64: f.ID, Valid: true}
	case KindFolder:
		if targetID == vfs.RootID {
			return Share{}, ErrInvalidTarget
		}
		if err := s.vfs.FolderExists(ctx, targetID); err != nil {
			return Share{}, err
		}
		p.FolderID = sql.NullInt64{Int64: targetID, Valid: true}
	default:
		return Share{}, ErrInvalidTarget
	}
	var err error
	if p.PasswordHash, err = s.passwordHash(opt, ""); err != nil {
		return Share{}, err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return Share{}, err
	}
	p.Token = base64.RawURLEncoding.EncodeToString(raw)
	row, err := s.q.CreateShare(ctx, p)
	if err != nil {
		return Share{}, err
	}
	return s.byID(ctx, row.ID)
}

// List devuelve todos los enlaces, el más reciente primero.
func (s *Service) List(ctx context.Context) ([]Share, error) {
	rows, err := s.q.ListShares(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Share, 0, len(rows))
	for _, r := range rows {
		sh := fromRow(sqlcgen.Share{
			ID: r.ID, Token: r.Token, FileID: r.FileID, FolderID: r.FolderID, CreatedBy: r.CreatedBy,
			PasswordHash: r.PasswordHash, Downloads: r.Downloads, CreatedAt: r.CreatedAt,
		})
		sh.Name, sh.ParentID, sh.Size, sh.CreatedByEmail = r.ItemName, r.ItemParentID, r.ItemSize, r.CreatedByEmail
		out = append(out, sh)
	}
	return out, nil
}

// Update cambia la contraseña.
func (s *Service) Update(ctx context.Context, id int64, opt Options) (Share, error) {
	cur, err := s.q.GetShare(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Share{}, ErrNotFound
	}
	if err != nil {
		return Share{}, err
	}
	hash, err := s.passwordHash(opt, cur.PasswordHash)
	if err != nil {
		return Share{}, err
	}
	if _, err := s.q.UpdateSharePassword(ctx, sqlcgen.UpdateSharePasswordParams{PasswordHash: hash, ID: id}); err != nil {
		return Share{}, err
	}
	return s.byID(ctx, id)
}

// Delete desactiva un enlace.
func (s *Service) Delete(ctx context.Context, id int64) error {
	n, err := s.q.DeleteShare(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Resolve abre un enlace por su token.
func (s *Service) Resolve(ctx context.Context, token string) (Share, error) {
	row, err := s.q.GetShareByToken(ctx, token)
	if errors.Is(err, sql.ErrNoRows) {
		return Share{}, ErrNotFound
	}
	if err != nil {
		return Share{}, err
	}
	sh := fromRow(row)
	if err := s.fillTarget(ctx, &sh); err != nil {
		return Share{}, err
	}
	return sh, nil
}

// Grant es el permiso que demuestra haber dado la contraseña del enlace.
func (s *Service) Grant(sh Share) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("share:" + sh.Token + ":" + sh.passwordHash))
	return hex.EncodeToString(mac.Sum(nil))
}

// Locked dice si hace falta la contraseña para ver el contenido.
func (s *Service) Locked(sh Share, grant string) bool {
	return sh.passwordHash != "" && !hmac.Equal([]byte(grant), []byte(s.Grant(sh)))
}

// Unlock valida la contraseña y devuelve el permiso para la cookie.
func (s *Service) Unlock(sh Share, password string) (string, error) {
	if sh.passwordHash != "" && !auth.VerifyPassword(sh.passwordHash, password) {
		return "", ErrWrongPassword
	}
	return s.Grant(sh), nil
}

// Folder valida que la carpeta id esté dentro de un enlace de carpeta. 0 es su raíz.
func (s *Service) Folder(ctx context.Context, sh Share, id int64) (int64, error) {
	if sh.Kind != KindFolder {
		return 0, ErrOutside
	}
	if id == 0 {
		return sh.TargetID, nil
	}
	return id, s.contains(ctx, sh, id)
}

// contains exige que la carpeta id (la raíz real incluida) cuelgue del enlace
// sin pasar por una subcarpeta restringida: esas no se ven por enlace.
func (s *Service) contains(ctx context.Context, sh Share, id int64) error {
	ok, err := s.vfs.IsWithinOpen(ctx, id, sh.TargetID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrOutside
	}
	return nil
}

// File valida que el archivo id pertenezca al enlace.
func (s *Service) File(ctx context.Context, sh Share, id int64) (vfs.File, error) {
	if sh.Kind == KindFile && id != sh.TargetID {
		return vfs.File{}, ErrOutside
	}
	f, err := s.vfs.GetFile(ctx, id)
	if err != nil {
		return vfs.File{}, err
	}
	if sh.Kind == KindFolder {
		if err := s.contains(ctx, sh, f.FolderID); err != nil {
			return vfs.File{}, err
		}
	}
	return f, nil
}

// CountDownload suma una descarga al contador del enlace.
func (s *Service) CountDownload(ctx context.Context, sh Share) error {
	return s.q.CountShareDownload(ctx, sh.ID)
}

func (s *Service) byID(ctx context.Context, id int64) (Share, error) {
	row, err := s.q.GetShare(ctx, id)
	if err != nil {
		return Share{}, err
	}
	sh := fromRow(row)
	return sh, s.fillTarget(ctx, &sh)
}

// fillTarget completa nombre, carpeta padre y tamaño del elemento compartido.
func (s *Service) fillTarget(ctx context.Context, sh *Share) error {
	if sh.Kind == KindFile {
		f, err := s.vfs.GetFile(ctx, sh.TargetID)
		if errors.Is(err, vfs.ErrFileNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		sh.Name, sh.ParentID, sh.Size = f.Name, f.FolderID, f.Size
		return nil
	}
	l, err := s.vfs.List(ctx, sh.TargetID)
	if errors.Is(err, vfs.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	sh.Name, sh.ParentID = l.Folder.Name, l.Folder.ParentID
	return nil
}

// passwordHash aplica Options.Password sobre el hash actual.
func (s *Service) passwordHash(opt Options, cur string) (string, error) {
	switch {
	case opt.Password == nil:
		return cur, nil
	case *opt.Password == "":
		return "", nil
	case len(*opt.Password) < 4:
		return "", ErrWeakPassword
	default:
		return auth.HashPassword(*opt.Password)
	}
}

func fromRow(r sqlcgen.Share) Share {
	sh := Share{
		ID: r.ID, Token: r.Token, HasPassword: r.PasswordHash != "",
		Downloads: r.Downloads, CreatedAt: r.CreatedAt, CreatedBy: r.CreatedBy,
		passwordHash: r.PasswordHash,
	}
	if r.FileID.Valid {
		sh.Kind, sh.TargetID = KindFile, r.FileID.Int64
	} else {
		sh.Kind, sh.TargetID = KindFolder, r.FolderID.Int64
	}
	return sh
}

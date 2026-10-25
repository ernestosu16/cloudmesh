package vfs

import (
	"context"
	"database/sql"
	"errors"

	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
)

// Niveles de acceso a una carpeta restringida.
const (
	AccessRead  = "read"
	AccessWrite = "write"
)

var (
	ErrRestrictRoot  = errors.New("la raíz no se puede restringir")
	ErrAccessLevel   = errors.New("nivel de acceso inválido: read o write")
	ErrNoFolderWrite = errors.New("en esta carpeta solo tienes permiso de lectura")
)

// Access es lo que un usuario puede ver y cambiar del disco. Una carpeta
// restringida, y todo lo que cuelga de ella, solo la ve quien tiene acceso a
// ella (a cada restringida del camino) y solo la cambia quien tiene «write».
// El valor cero no deja ver nada: se construye con AccessFor o FullAccess.
type Access struct {
	all      bool
	hidden   map[int64]bool
	readOnly map[int64]bool
}

// FullAccess ve y cambia todo: administradores y procesos internos.
func FullAccess() Access { return Access{all: true} }

// AccessFor calcula el acceso de un usuario. Los administradores lo ven todo;
// sin ninguna carpeta restringida no hace falta mirar más.
func (s *Service) AccessFor(ctx context.Context, userID int64, admin bool) (Access, error) {
	if admin {
		return FullAccess(), nil
	}
	n, err := s.q.CountRestrictedFolders(ctx)
	if err != nil {
		return Access{}, err
	}
	if n == 0 {
		return FullAccess(), nil
	}
	hidden, err := s.q.HiddenFolders(ctx, userID)
	if err != nil {
		return Access{}, err
	}
	ro, err := s.q.ReadOnlyFolders(ctx, userID)
	if err != nil {
		return Access{}, err
	}
	a := Access{hidden: map[int64]bool{}, readOnly: map[int64]bool{}}
	for _, id := range hidden {
		a.hidden[id] = true
	}
	for _, id := range ro {
		a.readOnly[id] = true
	}
	return a, nil
}

// CanRead dice si la carpeta (y lo que contiene) es visible. La raíz siempre.
func (a Access) CanRead(folderID int64) bool {
	return a.all || (a.hidden != nil && !a.hidden[folderID])
}

// CanWrite dice si se puede crear, cambiar o borrar dentro de la carpeta.
func (a Access) CanWrite(folderID int64) bool {
	return a.all || (a.readOnly != nil && !a.readOnly[folderID])
}

// Sees es CanRead con la firma de WithSizes.
func (a Access) Sees(folderID int64, _ bool) bool { return a.CanRead(folderID) }

// Listing quita de un listado las subcarpetas que no se ven.
func (a Access) Listing(l Listing) Listing {
	if a.all {
		return l
	}
	folders := l.Folders[:0:0]
	for _, f := range l.Folders {
		if a.CanRead(f.ID) {
			folders = append(folders, f)
		}
	}
	l.Folders = folders
	return l
}

// Search quita de unos resultados lo que no se ve.
func (a Access) Search(r SearchResult) SearchResult {
	if a.all {
		return r
	}
	out := SearchResult{Folders: []Folder{}, Files: []File{}}
	for _, f := range r.Folders {
		if a.CanRead(f.ID) {
			out.Folders = append(out.Folders, f)
		}
	}
	for _, f := range r.Files {
		if a.CanRead(f.FolderID) {
			out.Files = append(out.Files, f)
		}
	}
	return out
}

// Grant es el acceso de un usuario a una carpeta restringida.
type Grant struct {
	UserID int64  `json:"user_id"`
	Level  string `json:"level"`
	Email  string `json:"email,omitempty"`
	Name   string `json:"name,omitempty"`
	Role   string `json:"role,omitempty"`
}

// FolderAccess es la configuración de acceso de una carpeta.
type FolderAccess struct {
	Restricted bool    `json:"restricted"`
	Grants     []Grant `json:"grants"`
}

// GetFolderAccess devuelve si la carpeta está restringida y a quién deja entrar.
func (s *Service) GetFolderAccess(ctx context.Context, id int64) (FolderAccess, error) {
	f, err := s.GetFolder(ctx, id)
	if err != nil {
		return FolderAccess{}, err
	}
	rows, err := s.q.ListFolderAccess(ctx, id)
	if err != nil {
		return FolderAccess{}, err
	}
	out := FolderAccess{Restricted: f.Restricted, Grants: make([]Grant, 0, len(rows))}
	for _, r := range rows {
		out.Grants = append(out.Grants, Grant{UserID: r.UserID, Level: r.Level, Email: r.Email, Name: r.Name, Role: r.Role})
	}
	return out, nil
}

// SetFolderAccess sustituye la configuración. Sin restringir, la lista se vacía.
func (s *Service) SetFolderAccess(ctx context.Context, id int64, restricted bool, grants []Grant) (FolderAccess, error) {
	if id == RootID {
		return FolderAccess{}, ErrRestrictRoot
	}
	for _, g := range grants {
		if g.Level != AccessRead && g.Level != AccessWrite {
			return FolderAccess{}, ErrAccessLevel
		}
	}
	if err := s.FolderExists(ctx, id); err != nil {
		return FolderAccess{}, err
	}
	if !restricted {
		grants = nil
	}
	if err := s.q.ClearFolderAccess(ctx, id); err != nil {
		return FolderAccess{}, err
	}
	seen := map[int64]bool{}
	for _, g := range grants {
		if seen[g.UserID] {
			continue
		}
		seen[g.UserID] = true
		if err := s.q.AddFolderAccess(ctx, sqlcgen.AddFolderAccessParams{FolderID: id, UserID: g.UserID, Level: g.Level}); err != nil {
			return FolderAccess{}, err
		}
	}
	flag := int64(0)
	if restricted {
		flag = 1
	}
	if _, err := s.q.SetFolderRestricted(ctx, sqlcgen.SetFolderRestrictedParams{Restricted: flag, ID: id}); err != nil {
		return FolderAccess{}, err
	}
	return s.GetFolderAccess(ctx, id)
}

// GetFolder devuelve una carpeta; la raíz es un valor fijo.
func (s *Service) GetFolder(ctx context.Context, id int64) (Folder, error) {
	if id == RootID {
		return Folder{ID: RootID}, nil
	}
	r, err := s.q.GetFolder(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Folder{}, ErrNotFound
	}
	if err != nil {
		return Folder{}, err
	}
	return toFolder(r), nil
}

// WithoutRestricted quita las subcarpetas restringidas de un listado: lo que
// ve un enlace público, que no tiene usuario.
func WithoutRestricted(l Listing) Listing {
	folders := l.Folders[:0:0]
	for _, f := range l.Folders {
		if !f.Restricted {
			folders = append(folders, f)
		}
	}
	l.Folders = folders
	return l
}

// Open es lo que suma WithSizes para un enlace público: nada bajo una restringida.
func Open(_ int64, restricted bool) bool { return !restricted }

// IsWithinOpen es IsWithin sin atravesar carpetas restringidas: id cuelga de
// ancestor y ninguna carpeta entre las dos (ancestor excluida) está restringida.
func (s *Service) IsWithinOpen(ctx context.Context, id, ancestor int64) (bool, error) {
	if id == ancestor {
		return true, nil
	}
	chain, err := s.ancestors(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	for i, f := range chain {
		if f.ID != ancestor {
			continue
		}
		for _, below := range chain[i+1:] {
			if below.Restricted {
				return false, nil
			}
		}
		return true, nil
	}
	return false, nil
}

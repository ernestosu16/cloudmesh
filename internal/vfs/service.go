// Package vfs es el drive virtual: carpetas propias de CloudMesh y el índice
// de archivos que apuntan a nodos remotos de las cuentas.
package vfs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
)

// RootID identifica la raíz virtual, que no existe como fila.
const RootID int64 = 0

// TrashRetention es lo que pasa un archivo en la papelera antes de purgarse.
const TrashRetention = 30 * 24 * time.Hour

var (
	ErrNotFound     = errors.New("carpeta no encontrada")
	ErrNotEmpty     = errors.New("la carpeta no está vacía")
	ErrNameTaken    = errors.New("ya existe una carpeta con ese nombre aquí")
	ErrInvalidName  = errors.New("nombre inválido")
	ErrCycle        = errors.New("no se puede mover una carpeta dentro de sí misma")
	ErrFileNotFound = errors.New("archivo no encontrado")
)

type Folder struct {
	ID        int64  `json:"id"`
	ParentID  int64  `json:"parent_id"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"created_at"`
	// Restricted: solo la ven los administradores y los usuarios con acceso.
	Restricted bool `json:"restricted,omitempty"`
}

type File struct {
	ID           int64  `json:"id"`
	FolderID     int64  `json:"folder_id"`
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	Mime         string `json:"mime"`
	ModTime      int64  `json:"mtime"`
	Layout       string `json:"layout"` // whole | rs
	DataShards   int64  `json:"data_shards"`
	ParityShards int64  `json:"parity_shards"`
	Status       string `json:"status"` // uploading | ready | degraded | lost
	CreatedAt    int64  `json:"created_at"`
	DeletedAt    int64  `json:"deleted_at,omitempty"` // > 0: en la papelera
	ETag         string `json:"etag,omitempty"`       // MD5 (o «md5-N» si es multipart), para S3
	SHA256       string `json:"-"`
	Meta         string `json:"-"` // metadatos de usuario de S3, en JSON
	Internal     bool   `json:"-"` // parte de un multipart, fuera del disco
}

// Chunk es un bloque de un archivo con la cuenta que lo guarda.
// AccountID 0 y Missing true indican un bloque perdido (cuenta borrada o bloque ausente).
type Chunk struct {
	Stripe        int64  `json:"stripe"`
	Index         int64  `json:"idx"`
	Kind          string `json:"kind"`
	AccountID     int64  `json:"account_id"`
	AccountLabel  string `json:"account_label"`
	AccountStatus string `json:"account_status"`
	Size          int64  `json:"size"`
	Missing       bool   `json:"missing"`
}

// FileDetail es un archivo con sus bloques.
type FileDetail struct {
	File   File    `json:"file"`
	Chunks []Chunk `json:"chunks"`
}

// FromRow convierte la fila sqlc en la vista pública.
func FromRow(f sqlcgen.File) File {
	return File{
		ID: f.ID, FolderID: f.FolderID, Name: f.Name, Size: f.Size, Mime: f.Mime, ModTime: f.Mtime,
		Layout: f.Layout, DataShards: f.DataShards, ParityShards: f.ParityShards, Status: f.Status, CreatedAt: f.CreatedAt,
		DeletedAt: f.DeletedAt, ETag: f.Etag, SHA256: f.Sha256, Meta: f.Meta, Internal: f.Internal != 0,
	}
}

// Listing es el contenido de una carpeta con su ruta.
type Listing struct {
	Folder      Folder   `json:"folder"`
	Breadcrumbs []Folder `json:"breadcrumbs"` // de la raíz (excluida) hasta el padre
	Folders     []Folder `json:"folders"`
	Files       []File   `json:"files"`
}

type SearchResult struct {
	Folders []Folder `json:"folders"`
	Files   []File   `json:"files"`
}

type Service struct {
	db  *sql.DB
	q   *sqlcgen.Queries
	now func() time.Time
}

func NewService(sqlDB *sql.DB) *Service {
	return &Service{db: sqlDB, q: sqlcgen.New(sqlDB), now: time.Now}
}

// Queries expone las consultas para paquetes que comparten tabla (sync).
func (s *Service) Queries() *sqlcgen.Queries { return s.q }

func (s *Service) List(ctx context.Context, id int64) (Listing, error) {
	var out Listing
	out.Breadcrumbs = []Folder{}
	if id == RootID {
		out.Folder = Folder{ID: RootID, Name: ""}
	} else {
		row, err := s.q.GetFolder(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return Listing{}, ErrNotFound
		}
		if err != nil {
			return Listing{}, err
		}
		out.Folder = toFolder(row)
		if out.Breadcrumbs, err = s.ancestors(ctx, row.ParentID); err != nil {
			return Listing{}, err
		}
	}
	subs, err := s.q.ListFolders(ctx, id)
	if err != nil {
		return Listing{}, err
	}
	out.Folders = make([]Folder, 0, len(subs))
	for _, f := range subs {
		out.Folders = append(out.Folders, toFolder(f))
	}
	files, err := s.q.ListFilesInFolder(ctx, id)
	if err != nil {
		return Listing{}, err
	}
	out.Files = make([]File, 0, len(files))
	for _, f := range files {
		out.Files = append(out.Files, FromRow(f))
	}
	return out, nil
}

// GetFile devuelve un archivo del disco por id. Los de la papelera no existen
// para el resto de la app: ni se leen, ni se comparten.
func (s *Service) GetFile(ctx context.Context, id int64) (File, error) {
	row, err := s.q.GetFile(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (row.DeletedAt > 0 || row.Internal != 0) {
		return File{}, ErrFileNotFound
	}
	if err != nil {
		return File{}, err
	}
	return FromRow(row), nil
}

// TrashItem es un archivo de la papelera con su carpeta de origen.
type TrashItem struct {
	File File `json:"file"`
	// FolderName vacío con FolderExists es la raíz; sin FolderExists la carpeta
	// ya no está y al restaurar el archivo vuelve a la raíz.
	FolderName   string `json:"folder_name"`
	FolderExists bool   `json:"folder_exists"`
	PurgeAt      int64  `json:"purge_at"`
}

// Trash manda un archivo a la papelera. Sus bloques se quedan donde están.
func (s *Service) Trash(ctx context.Context, id int64) error {
	n, err := s.q.TrashFile(ctx, sqlcgen.TrashFileParams{DeletedAt: s.now().Unix(), ID: id})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrFileNotFound
	}
	return nil
}

// GetTrashed devuelve un archivo solo si está en la papelera.
func (s *Service) GetTrashed(ctx context.Context, id int64) (File, error) {
	row, err := s.q.GetFile(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && row.DeletedAt == 0 {
		return File{}, ErrFileNotFound
	}
	if err != nil {
		return File{}, err
	}
	return FromRow(row), nil
}

// Restore saca un archivo de la papelera a su carpeta, o a la raíz si ya no existe.
func (s *Service) Restore(ctx context.Context, id int64) (File, error) {
	f, err := s.GetTrashed(ctx, id)
	if err != nil {
		return File{}, err
	}
	folder := f.FolderID
	if err := s.FolderExists(ctx, folder); errors.Is(err, ErrNotFound) {
		folder = RootID
	} else if err != nil {
		return File{}, err
	}
	row, err := s.q.RestoreFile(ctx, sqlcgen.RestoreFileParams{FolderID: folder, ID: id})
	if err != nil {
		return File{}, err
	}
	return FromRow(row), nil
}

// ListTrash devuelve la papelera, lo último borrado primero.
func (s *Service) ListTrash(ctx context.Context) ([]TrashItem, error) {
	rows, err := s.q.ListTrash(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]TrashItem, 0, len(rows))
	for _, r := range rows {
		f := FromRow(sqlcgen.File{
			ID: r.ID, FolderID: r.FolderID, Name: r.Name, Size: r.Size, Mime: r.Mime, Mtime: r.Mtime, Layout: r.Layout,
			DataShards: r.DataShards, ParityShards: r.ParityShards, ChunkSize: r.ChunkSize, Sha256: r.Sha256,
			Status: r.Status, CreatedAt: r.CreatedAt, DeletedAt: r.DeletedAt, Etag: r.Etag,
		})
		out = append(out, TrashItem{
			File: f, FolderName: r.FolderName, FolderExists: r.FolderExists != 0,
			PurgeAt: r.DeletedAt + int64(TrashRetention/time.Second),
		})
	}
	return out, nil
}

// TrashedBefore lista los archivos que entraron en la papelera antes de t.
func (s *Service) TrashedBefore(ctx context.Context, t time.Time) ([]int64, error) {
	return s.q.ListTrashedBefore(ctx, t.Unix())
}

// FileDetail devuelve el archivo con sus bloques y cuentas.
func (s *Service) FileDetail(ctx context.Context, id int64) (FileDetail, error) {
	f, err := s.GetFile(ctx, id)
	if err != nil {
		return FileDetail{}, err
	}
	rows, err := s.q.ListChunks(ctx, id)
	if err != nil {
		return FileDetail{}, err
	}
	out := FileDetail{File: f, Chunks: make([]Chunk, 0, len(rows))}
	for _, c := range rows {
		label := c.AccountLabel
		if !c.AccountID.Valid {
			label = "(cuenta eliminada)"
		}
		out.Chunks = append(out.Chunks, Chunk{
			Stripe: c.Stripe, Index: c.Idx, Kind: c.Kind, AccountID: c.AccountID.Int64,
			AccountLabel: label, AccountStatus: c.AccountStatus, Size: c.Size,
			Missing: c.Missing != 0 || !c.AccountID.Valid,
		})
	}
	return out, nil
}

// UpdateFile renombra y/o mueve un archivo. Solo toca el índice.
func (s *Service) UpdateFile(ctx context.Context, id int64, name *string, folderID *int64) (File, error) {
	cur, err := s.q.GetFile(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (cur.DeletedAt > 0 || cur.Internal != 0) {
		return File{}, ErrFileNotFound
	}
	if err != nil {
		return File{}, err
	}
	newName, newFolder := cur.Name, cur.FolderID
	if name != nil {
		if newName, err = CleanName(*name); err != nil {
			return File{}, err
		}
	}
	if folderID != nil {
		newFolder = *folderID
		if err := s.FolderExists(ctx, newFolder); err != nil {
			return File{}, err
		}
	}
	row, err := s.q.UpdateFileLocation(ctx, sqlcgen.UpdateFileLocationParams{Name: newName, FolderID: newFolder, ID: id})
	if err != nil {
		return File{}, err
	}
	return FromRow(row), nil
}

// IsWithin dice si la carpeta id es ancestor o cuelga de ella.
func (s *Service) IsWithin(ctx context.Context, id, ancestor int64) (bool, error) {
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
	for _, f := range chain {
		if f.ID == ancestor {
			return true, nil
		}
	}
	return false, nil
}

// ancestors devuelve la cadena desde la raíz (excluida) hasta la carpeta id (incluida).
func (s *Service) ancestors(ctx context.Context, id int64) ([]Folder, error) {
	var chain []Folder
	for depth := 0; id != RootID && depth < 64; depth++ {
		row, err := s.q.GetFolder(ctx, id)
		if err != nil {
			return nil, err
		}
		chain = append([]Folder{toFolder(row)}, chain...)
		id = row.ParentID
	}
	if chain == nil {
		chain = []Folder{}
	}
	return chain, nil
}

func (s *Service) CreateFolder(ctx context.Context, parentID int64, name string) (Folder, error) {
	name, err := CleanName(name)
	if err != nil {
		return Folder{}, err
	}
	if err := s.FolderExists(ctx, parentID); err != nil {
		return Folder{}, err
	}
	row, err := s.q.CreateFolder(ctx, sqlcgen.CreateFolderParams{ParentID: parentID, Name: name, CreatedAt: s.now().Unix()})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Folder{}, ErrNameTaken
		}
		return Folder{}, err
	}
	return toFolder(row), nil
}

// ChildFolder busca la subcarpeta name de parent.
func (s *Service) ChildFolder(ctx context.Context, parentID int64, name string) (Folder, error) {
	row, err := s.q.GetFolderByName(ctx, sqlcgen.GetFolderByNameParams{ParentID: parentID, Name: name})
	if errors.Is(err, sql.ErrNoRows) {
		return Folder{}, ErrNotFound
	}
	if err != nil {
		return Folder{}, err
	}
	return toFolder(row), nil
}

// SetClientMeta fija la fecha de modificación y los metadatos que manda un
// cliente S3 (JSON opaco para el resto de la app).
func (s *Service) SetClientMeta(ctx context.Context, id, mtime int64, meta string) error {
	return s.q.SetClientMeta(ctx, sqlcgen.SetClientMetaParams{Mtime: mtime, Meta: meta, ID: id})
}

// FindFiles devuelve los archivos visibles llamados name en la carpeta, el más
// reciente primero.
func (s *Service) FindFiles(ctx context.Context, folderID int64, name string) ([]File, error) {
	rows, err := s.q.ListObjectFiles(ctx, sqlcgen.ListObjectFilesParams{FolderID: folderID, Name: name})
	if err != nil {
		return nil, err
	}
	out := make([]File, 0, len(rows))
	for _, r := range rows {
		out = append(out, FromRow(r))
	}
	return out, nil
}

// EnsureFolder devuelve la carpeta (parent, name), creándola si falta.
func (s *Service) EnsureFolder(ctx context.Context, parentID int64, name string) (Folder, error) {
	row, err := s.q.GetFolderByName(ctx, sqlcgen.GetFolderByNameParams{ParentID: parentID, Name: name})
	if err == nil {
		return toFolder(row), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Folder{}, err
	}
	return s.CreateFolder(ctx, parentID, name)
}

// UpdateFolder renombra y/o mueve. Un puntero nil deja el campo como está.
func (s *Service) UpdateFolder(ctx context.Context, id int64, name *string, parentID *int64) (Folder, error) {
	cur, err := s.q.GetFolder(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Folder{}, ErrNotFound
	}
	if err != nil {
		return Folder{}, err
	}
	newName, newParent := cur.Name, cur.ParentID
	if name != nil {
		if newName, err = CleanName(*name); err != nil {
			return Folder{}, err
		}
	}
	if parentID != nil && *parentID != cur.ParentID {
		newParent = *parentID
		if newParent == id {
			return Folder{}, ErrCycle
		}
		if err := s.FolderExists(ctx, newParent); err != nil {
			return Folder{}, err
		}
		chain, err := s.ancestors(ctx, newParent)
		if err != nil {
			return Folder{}, err
		}
		for _, a := range chain {
			if a.ID == id {
				return Folder{}, ErrCycle
			}
		}
	}
	row, err := s.q.UpdateFolder(ctx, sqlcgen.UpdateFolderParams{Name: newName, ParentID: newParent, ID: id})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Folder{}, ErrNameTaken
		}
		return Folder{}, err
	}
	return toFolder(row), nil
}

// DeleteFolder solo borra carpetas vacías: los archivos viven en los proveedores.
func (s *Service) DeleteFolder(ctx context.Context, id int64) error {
	if id == RootID {
		return ErrNotFound
	}
	n, err := s.q.CountFolderContents(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrNotEmpty
	}
	rows, err := s.q.DeleteFolder(ctx, id)
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// FolderTree devuelve la carpeta y todas las que cuelgan de ella.
func (s *Service) FolderTree(ctx context.Context, id int64) ([]int64, error) {
	if id == RootID {
		return nil, ErrNotFound
	}
	ids, err := s.q.FolderTree(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, ErrNotFound
	}
	return ids, nil
}

// DeleteFolderTree borra la carpeta con todo su contenido. Los archivos van a
// la papelera y, al restaurarlos, a la raíz. Los internos no cuentan: las
// partes siguen a su compuesto y las huérfanas (subidas multipart de un bucket
// que se va con la carpeta) las limpia la pasada de S3.
func (s *Service) DeleteFolderTree(ctx context.Context, id int64) error {
	ids, err := s.FolderTree(ctx, id)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)
	now := s.now().Unix()
	for _, folder := range ids {
		if err := q.TrashFolderFiles(ctx, sqlcgen.TrashFolderFilesParams{DeletedAt: now, FolderID: folder}); err != nil {
			return err
		}
		if _, err := q.DeleteFolder(ctx, folder); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) Search(ctx context.Context, q string) (SearchResult, error) {
	q = strings.TrimSpace(q)
	out := SearchResult{Folders: []Folder{}, Files: []File{}}
	if q == "" {
		return out, nil
	}
	arg := sql.NullString{String: q, Valid: true}
	folders, err := s.q.SearchFolders(ctx, arg)
	if err != nil {
		return out, err
	}
	for _, f := range folders {
		out.Folders = append(out.Folders, toFolder(f))
	}
	files, err := s.q.SearchFiles(ctx, arg)
	if err != nil {
		return out, err
	}
	for _, f := range files {
		out.Files = append(out.Files, FromRow(f))
	}
	return out, nil
}

// FolderExists valida que una carpeta destino exista (la raíz siempre).
func (s *Service) FolderExists(ctx context.Context, id int64) error {
	if id == RootID {
		return nil
	}
	if _, err := s.q.GetFolder(ctx, id); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	return nil
}

// CleanName valida un nombre de carpeta o archivo.
func CleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 255 || strings.ContainsAny(name, "/\\\x00") || name == "." || name == ".." {
		return "", fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	return name, nil
}

func toFolder(r sqlcgen.Folder) Folder {
	return Folder{ID: r.ID, ParentID: r.ParentID, Name: r.Name, CreatedAt: r.CreatedAt, Restricted: r.Restricted != 0}
}

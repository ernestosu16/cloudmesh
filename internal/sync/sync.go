// Package sync reconcilia el índice de archivos con lo que hay en cada
// proveedor bajo la carpeta raíz de CloudMesh.
package sync

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"mime"
	"path"
	"sync"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/accounts"
	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
	"github.com/hirasolutions/cloudmesh/internal/manifest"
	"github.com/hirasolutions/cloudmesh/internal/storage"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

// ImportsFolder es la carpeta virtual donde caen los archivos que aparecen
// en el proveedor sin haber pasado por CloudMesh.
const ImportsFolder = "Importados"

// Result resume una pasada de sincronización.
type Result struct {
	Added   int `json:"added"`
	Updated int `json:"updated"`
	Removed int `json:"removed"`
}

type Syncer struct {
	accounts *accounts.Service
	vfs      *vfs.Service
	q        *sqlcgen.Queries
	log      *slog.Logger
	now      func() time.Time

	mu    sync.Mutex
	locks map[int64]*sync.Mutex // una pasada a la vez por cuenta
}

func New(acc *accounts.Service, v *vfs.Service, log *slog.Logger) *Syncer {
	return &Syncer{accounts: acc, vfs: v, q: v.Queries(), log: log, now: time.Now, locks: map[int64]*sync.Mutex{}}
}

// SyncAccount reconcilia una cuenta. Las pasadas concurrentes sobre la misma
// cuenta se serializan: la segunda espera y normalmente no encuentra cambios.
func (s *Syncer) SyncAccount(ctx context.Context, accountID int64) (Result, error) {
	lock := s.lockFor(accountID)
	lock.Lock()
	defer lock.Unlock()

	acc, err := s.accounts.Get(ctx, accountID)
	if err != nil {
		return Result{}, err
	}
	sess, err := s.accounts.Session(ctx, accountID)
	if err != nil {
		return Result{}, err
	}
	row, err := s.q.GetAccount(ctx, accountID)
	if err != nil {
		return Result{}, err
	}

	remote, err := s.walk(ctx, sess, row.RootRemoteID)
	if errors.Is(err, storage.ErrNotFound) {
		// La raíz desapareció en el proveedor: se recrea y el índice queda vacío.
		rootID, rerr := sess.EnsureRoot(ctx)
		if rerr != nil {
			return Result{}, s.accounts.HandleSessionError(ctx, accountID, rerr)
		}
		if uerr := s.q.UpdateAccountRoot(ctx, sqlcgen.UpdateAccountRootParams{RootRemoteID: rootID, ID: accountID}); uerr != nil {
			return Result{}, uerr
		}
		remote, err = s.walk(ctx, sess, rootID)
	}
	if err != nil {
		return Result{}, s.accounts.HandleSessionError(ctx, accountID, err)
	}

	local, err := s.q.ListWholeFilesByAccount(ctx, sql.NullInt64{Int64: accountID, Valid: true})
	if err != nil {
		return Result{}, err
	}
	byRemote := make(map[string]sqlcgen.ListWholeFilesByAccountRow, len(local))
	for _, f := range local {
		byRemote[f.RemoteID] = f
	}

	var res Result
	var imports *vfs.Folder
	for _, e := range remote {
		cur, ok := byRemote[e.ID]
		if !ok {
			if imports == nil {
				f, err := s.importsFolder(ctx, acc.Label)
				if err != nil {
					return res, err
				}
				imports = &f
			}
			f, err := s.q.InsertFile(ctx, sqlcgen.InsertFileParams{
				FolderID: imports.ID, Name: e.Name, Size: e.Size, Mime: mimeOf(e.Name), Mtime: e.ModTime.Unix(),
				Layout: "whole", DataShards: 1, ParityShards: 0, ChunkSize: 0, Sha256: "", Status: "ready",
				CreatedAt: s.now().Unix(),
			})
			if err != nil {
				return res, err
			}
			if _, err := s.q.InsertChunk(ctx, sqlcgen.InsertChunkParams{
				FileID: f.ID, Stripe: 0, Idx: 0, Kind: "data", AccountID: sql.NullInt64{Int64: accountID, Valid: true}, RemoteID: e.ID, Size: e.Size,
			}); err != nil {
				return res, err
			}
			res.Added++
			continue
		}
		delete(byRemote, e.ID)
		if cur.Name != e.Name || cur.Size != e.Size || cur.Mtime != e.ModTime.Unix() {
			if err := s.q.UpdateFileRemote(ctx, sqlcgen.UpdateFileRemoteParams{
				Name: e.Name, Size: e.Size, Mime: mimeOf(e.Name), Mtime: e.ModTime.Unix(), ID: cur.ID,
			}); err != nil {
				return res, err
			}
			res.Updated++
		}
	}
	for _, gone := range byRemote {
		if _, err := s.q.DeleteFile(ctx, gone.ID); err != nil {
			return res, err
		}
		res.Removed++
	}

	if _, err := s.accounts.RefreshQuota(ctx, accountID); err != nil {
		s.log.Warn("sync: cuota", "account", accountID, "err", err)
	}
	if err := s.q.TouchAccountSync(ctx, sqlcgen.TouchAccountSyncParams{LastSyncAt: s.now().Unix(), ID: accountID}); err != nil {
		return res, err
	}
	s.log.Info("sync", "account", accountID, "added", res.Added, "updated", res.Updated, "removed", res.Removed)
	return res, nil
}

func (s *Syncer) lockFor(accountID int64) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[accountID]
	if !ok {
		l = &sync.Mutex{}
		s.locks[accountID] = l
	}
	return l
}

// walk lista recursivamente los archivos bajo rootID (las carpetas remotas se
// aplanan). Salta la carpeta de bloques: sus archivos no son archivos del drive.
func (s *Syncer) walk(ctx context.Context, sess storage.Session, rootID string) ([]storage.Entry, error) {
	var files []storage.Entry
	queue := []string{rootID}
	seen := map[string]bool{}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		entries, err := sess.List(ctx, id)
		if err != nil {
			if id != rootID && errors.Is(err, storage.ErrNotFound) {
				continue // subcarpeta borrada entre medias
			}
			return nil, err
		}
		for _, e := range entries {
			switch {
			case e.IsDir && id == rootID && e.Name == storage.BlocksFolder:
				continue
			case !e.IsDir && id == rootID && e.Name == manifest.Name:
				continue
			case e.IsDir:
				queue = append(queue, e.ID)
			default:
				files = append(files, e)
			}
		}
	}
	return files, nil
}

// importsFolder devuelve Importados/<label>, creándola si falta.
func (s *Syncer) importsFolder(ctx context.Context, label string) (vfs.Folder, error) {
	top, err := s.vfs.EnsureFolder(ctx, vfs.RootID, ImportsFolder)
	if err != nil {
		return vfs.Folder{}, err
	}
	return s.vfs.EnsureFolder(ctx, top.ID, label)
}

func mimeOf(name string) string {
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}

// SyncAll recorre las cuentas activas; los errores se registran y no detienen el resto.
func (s *Syncer) SyncAll(ctx context.Context) {
	list, err := s.accounts.List(ctx)
	if err != nil {
		s.log.Error("sync: listar cuentas", "err", err)
		return
	}
	for _, a := range list {
		if a.Status != accounts.StatusActive {
			continue
		}
		if _, err := s.SyncAccount(ctx, a.ID); err != nil {
			s.log.Warn("sync: cuenta", "account", a.ID, "err", err)
		}
	}
}

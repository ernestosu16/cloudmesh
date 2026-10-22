package http

import (
	"net/http"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

// handleListTrash: GET /api/trash
func (s *Server) handleListTrash(w http.ResponseWriter, r *http.Request) {
	a, ok := s.access(w, r)
	if !ok {
		return
	}
	items, err := s.vfs.ListTrash(r.Context())
	if err != nil {
		s.internal(w, "listar papelera", err)
		return
	}
	visible := items[:0:0]
	for _, it := range items {
		if a.CanRead(it.File.FolderID) {
			visible = append(visible, it)
		}
	}
	writeJSON(w, http.StatusOK, visible)
}

// handleRestoreTrash: POST /api/trash/{id}/restore
func (s *Server) handleRestoreTrash(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	cur, err := s.vfs.GetTrashed(r.Context(), id)
	if err != nil {
		s.fileError(w, "restaurar archivo", err)
		return
	}
	if !s.canWrite(w, r, vfs.ErrFileNotFound, cur.FolderID) {
		return
	}
	f, err := s.vfs.Restore(r.Context(), id)
	if err != nil {
		s.fileError(w, "restaurar archivo", err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// handlePurgeTrash: DELETE /api/trash/{id} — destruye el archivo y sus bloques.
func (s *Server) handlePurgeTrash(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	cur, err := s.vfs.GetTrashed(r.Context(), id)
	if err != nil {
		s.fileError(w, "vaciar archivo", err)
		return
	}
	if !s.canWrite(w, r, vfs.ErrFileNotFound, cur.FolderID) {
		return
	}
	if err := s.transfer.Delete(r.Context(), id); err != nil {
		s.fileError(w, "vaciar archivo", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleEmptyTrash: DELETE /api/trash — vacía la papelera entera.
func (s *Server) handleEmptyTrash(w http.ResponseWriter, r *http.Request) {
	n, err := s.transfer.PurgeTrash(r.Context(), time.Now().Add(time.Second))
	if err != nil {
		s.internal(w, "vaciar papelera", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"purged": n})
}

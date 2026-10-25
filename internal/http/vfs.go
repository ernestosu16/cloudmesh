package http

import (
	"errors"
	"net/http"
	"strings"

	"github.com/hirasolutions/cloudmesh/internal/auth"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

// access calcula qué carpetas ve y cambia el usuario de la petición.
func (s *Server) access(w http.ResponseWriter, r *http.Request) (vfs.Access, bool) {
	u := userFrom(r.Context())
	a, err := s.vfs.AccessFor(r.Context(), u.ID, u.Can(auth.RoleAdmin))
	if err != nil {
		s.internal(w, "permisos de carpeta", err)
		return a, false
	}
	return a, true
}

// canRead responde 404 si la carpeta no se ve: una restringida no revela que existe.
func (s *Server) canRead(w http.ResponseWriter, r *http.Request, folderID int64, notFound error) bool {
	a, ok := s.access(w, r)
	if !ok {
		return false
	}
	if !a.CanRead(folderID) {
		writeError(w, http.StatusNotFound, "not_found", notFound.Error())
		return false
	}
	return true
}

// canWrite exige poder cambiar todas las carpetas dadas: 404 si alguna no se
// ve, 403 si solo se puede leer.
func (s *Server) canWrite(w http.ResponseWriter, r *http.Request, notFound error, folderIDs ...int64) bool {
	a, ok := s.access(w, r)
	if !ok {
		return false
	}
	for _, id := range folderIDs {
		if !a.CanRead(id) {
			writeError(w, http.StatusNotFound, "not_found", notFound.Error())
			return false
		}
		if !a.CanWrite(id) {
			writeError(w, http.StatusForbidden, "forbidden", vfs.ErrNoFolderWrite.Error())
			return false
		}
	}
	return true
}

// handleListFolder: GET /api/folders/{id} (0 = raíz)
func (s *Server) handleListFolder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathIDAllowZero(w, r)
	if !ok {
		return
	}
	a, ok := s.access(w, r)
	if !ok {
		return
	}
	if !a.CanRead(id) {
		s.vfsError(w, "listar carpeta", vfs.ErrNotFound)
		return
	}
	listing, err := s.vfs.List(r.Context(), id)
	if err == nil {
		listing, err = s.vfs.WithSizes(r.Context(), a.Listing(listing), a.Sees)
	}
	if err != nil {
		s.vfsError(w, "listar carpeta", err)
		return
	}
	out := struct {
		vfs.Listing
		// Writable: se puede subir y cambiar cosas aquí (además del rol).
		Writable bool `json:"writable"`
	}{listing, a.CanWrite(id)}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateFolder: POST /api/folders
func (s *Server) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ParentID int64  `json:"parent_id"`
		Name     string `json:"name"`
		// Existing devuelve la carpeta si ya hay una con ese nombre (subir un
		// directorio a una carpeta que ya lo tiene) en vez de responder 409.
		Existing bool `json:"existing"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.canWrite(w, r, vfs.ErrNotFound, req.ParentID) {
		return
	}
	f, err := s.vfs.CreateFolder(r.Context(), req.ParentID, req.Name)
	if errors.Is(err, vfs.ErrNameTaken) && req.Existing {
		name, _ := vfs.CleanName(req.Name)
		if f, err = s.vfs.ChildFolder(r.Context(), req.ParentID, name); err == nil {
			if !s.canWrite(w, r, vfs.ErrNotFound, f.ID) {
				return
			}
			writeJSON(w, http.StatusOK, f)
			return
		}
	}
	if err != nil {
		s.vfsError(w, "crear carpeta", err)
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

// handleUpdateFolder: PATCH /api/folders/{id}
func (s *Server) handleUpdateFolder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name     *string `json:"name"`
		ParentID *int64  `json:"parent_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	cur, err := s.vfs.GetFolder(r.Context(), id)
	if err != nil {
		s.vfsError(w, "actualizar carpeta", err)
		return
	}
	// Renombrar o mover cambia la carpeta donde está; mover, también la de destino.
	targets := []int64{id, cur.ParentID}
	if req.ParentID != nil {
		targets = append(targets, *req.ParentID)
	}
	if !s.canWrite(w, r, vfs.ErrNotFound, targets...) {
		return
	}
	f, err := s.vfs.UpdateFolder(r.Context(), id, req.Name, req.ParentID)
	if err != nil {
		s.vfsError(w, "actualizar carpeta", err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// handleDeleteFolder: DELETE /api/folders/{id} — con todo su contenido.
func (s *Server) handleDeleteFolder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	cur, err := s.vfs.GetFolder(r.Context(), id)
	if err != nil {
		s.vfsError(w, "borrar carpeta", err)
		return
	}
	if !s.canWrite(w, r, vfs.ErrNotFound, id, cur.ParentID) {
		return
	}
	tree, err := s.vfs.FolderTree(r.Context(), id)
	if err != nil {
		s.vfsError(w, "borrar carpeta", err)
		return
	}
	a, ok := s.access(w, r)
	if !ok {
		return
	}
	for _, sub := range tree {
		if !a.CanRead(sub) || !a.CanWrite(sub) {
			writeError(w, http.StatusForbidden, "forbidden", "la carpeta contiene subcarpetas que no puedes borrar")
			return
		}
	}
	if err := s.vfs.DeleteFolderTree(r.Context(), id); err != nil {
		s.vfsError(w, "borrar carpeta", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSearch: GET /api/search?q=
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	a, ok := s.access(w, r)
	if !ok {
		return
	}
	res, err := s.vfs.Search(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		s.internal(w, "buscar", err)
		return
	}
	writeJSON(w, http.StatusOK, a.Search(res))
}

func (s *Server) vfsError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, vfs.ErrNotFound), errors.Is(err, vfs.ErrFileNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, vfs.ErrNotEmpty), errors.Is(err, vfs.ErrNameTaken):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, vfs.ErrInvalidName), errors.Is(err, vfs.ErrCycle), errors.Is(err, vfs.ErrRestrictRoot), errors.Is(err, vfs.ErrAccessLevel):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	default:
		s.internal(w, op, err)
	}
}

// handleGetFolderAccess: GET /api/folders/{id}/access — solo administradores.
func (s *Server) handleGetFolderAccess(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	acc, err := s.vfs.GetFolderAccess(r.Context(), id)
	if err != nil {
		s.vfsError(w, "acceso a carpeta", err)
		return
	}
	writeJSON(w, http.StatusOK, acc)
}

// handleSetFolderAccess: PUT /api/folders/{id}/access {restricted, grants:[{user_id, level}]}
func (s *Server) handleSetFolderAccess(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req vfs.FolderAccess
	if !decodeJSON(w, r, &req) {
		return
	}
	acc, err := s.vfs.SetFolderAccess(r.Context(), id, req.Restricted, req.Grants)
	if err != nil {
		if isForeignKey(err) {
			writeError(w, http.StatusBadRequest, "bad_request", "algún usuario de la lista no existe")
			return
		}
		s.vfsError(w, "acceso a carpeta", err)
		return
	}
	s.log.Info("acceso a carpeta", "folder", id, "restricted", acc.Restricted, "grants", len(acc.Grants), "by", userFrom(r.Context()).Email)
	writeJSON(w, http.StatusOK, acc)
}

func isForeignKey(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY")
}

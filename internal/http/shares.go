package http

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/httputil"
	"github.com/hirasolutions/cloudmesh/internal/shares"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

// shareCookie guarda el permiso de un enlace con contraseña. Su Path es el del
// enlace, así cada enlace tiene su propia cookie.
const shareCookie = "cloudmesh_share"

// shareFolder es la carpeta que decide quién ve un enlace: la compartida o la del archivo.
func shareFolder(sh shares.Share) int64 {
	if sh.Kind == shares.KindFolder {
		return sh.TargetID
	}
	return sh.ParentID
}

// handleListShares: GET /api/shares — los de lo que el usuario puede ver.
func (s *Server) handleListShares(w http.ResponseWriter, r *http.Request) {
	a, ok := s.access(w, r)
	if !ok {
		return
	}
	list, err := s.shares.List(r.Context())
	if err != nil {
		s.internal(w, "listar enlaces", err)
		return
	}
	visible := list[:0:0]
	for _, sh := range list {
		if a.CanRead(shareFolder(sh)) {
			visible = append(visible, sh)
		}
	}
	writeJSON(w, http.StatusOK, visible)
}

// canManageShare exige poder cambiar lo compartido por el enlace id.
func (s *Server) canManageShare(w http.ResponseWriter, r *http.Request, id int64) bool {
	list, err := s.shares.List(r.Context())
	if err != nil {
		s.internal(w, "enlace", err)
		return false
	}
	for _, sh := range list {
		if sh.ID == id {
			return s.canWrite(w, r, shares.ErrNotFound, shareFolder(sh))
		}
	}
	writeError(w, http.StatusNotFound, "not_found", shares.ErrNotFound.Error())
	return false
}

// handleCreateShare: POST /api/shares {kind, target_id, password?}
func (s *Server) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind     string `json:"kind"`
		TargetID int64  `json:"target_id"`
		shares.Options
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	// Compartir pide poder cambiar lo compartido: un enlace lo saca fuera.
	folder := req.TargetID
	if req.Kind == shares.KindFile {
		f, err := s.vfs.GetFile(r.Context(), req.TargetID)
		if err != nil {
			s.shareError(w, "crear enlace", err)
			return
		}
		folder = f.FolderID
	}
	if !s.canWrite(w, r, vfs.ErrNotFound, folder) {
		return
	}
	sh, err := s.shares.Create(r.Context(), userFrom(r.Context()).ID, req.Kind, req.TargetID, req.Options)
	if err != nil {
		s.shareError(w, "crear enlace", err)
		return
	}
	writeJSON(w, http.StatusCreated, sh)
}

// handleUpdateShare: PATCH /api/shares/{id} {password} — "" la quita.
func (s *Server) handleUpdateShare(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var opt shares.Options
	if !decodeJSON(w, r, &opt) {
		return
	}
	if !s.canManageShare(w, r, id) {
		return
	}
	sh, err := s.shares.Update(r.Context(), id, opt)
	if err != nil {
		s.shareError(w, "actualizar enlace", err)
		return
	}
	writeJSON(w, http.StatusOK, sh)
}

// handleDeleteShare: DELETE /api/shares/{id}
func (s *Server) handleDeleteShare(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !s.canManageShare(w, r, id) {
		return
	}
	if err := s.shares.Delete(r.Context(), id); err != nil {
		s.shareError(w, "borrar enlace", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// publicShare es lo que ve quien abre el enlace. Sin datos del dueño.
type publicShare struct {
	Kind   string    `json:"kind"`
	Name   string    `json:"name"`
	Size   int64     `json:"size"`
	Locked bool      `json:"locked"`
	File   *vfs.File `json:"file,omitempty"`
}

// handlePublicShare: GET /api/public/shares/{token}
func (s *Server) handlePublicShare(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.resolveShare(w, r)
	if !ok {
		return
	}
	out := publicShare{Kind: sh.Kind, Name: sh.Name, Size: sh.Size, Locked: s.shares.Locked(sh, shareGrant(r))}
	if sh.Kind == shares.KindFile && !out.Locked {
		f, err := s.vfs.GetFile(r.Context(), sh.TargetID)
		if err != nil {
			s.shareError(w, "enlace", err)
			return
		}
		out.File = &f
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUnlockShare: POST /api/public/shares/{token}/unlock {password}
func (s *Server) handleUnlockShare(w http.ResponseWriter, r *http.Request) {
	if !s.loginLimiter.allow(s.clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "demasiados intentos, espera un minuto")
		return
	}
	sh, ok := s.resolveShare(w, r)
	if !ok {
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	grant, err := s.shares.Unlock(sh, req.Password)
	if err != nil {
		s.shareError(w, "desbloquear enlace", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     shareCookie,
		Value:    grant,
		Path:     "/api/public/shares/" + sh.Token,
		MaxAge:   int((24 * time.Hour) / time.Second),
		HttpOnly: true,
		Secure:   s.isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// publicListing es una carpeta del enlace; Path va de la carpeta compartida a la actual.
type publicListing struct {
	Path    []vfs.Folder `json:"path"`
	Folders []vfs.Folder `json:"folders"`
	Files   []vfs.File   `json:"files"`
}

// handlePublicFolder: GET /api/public/shares/{token}/folders/{id} (0 = la carpeta compartida)
func (s *Server) handlePublicFolder(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.openShare(w, r)
	if !ok {
		return
	}
	id, ok := pathIDAllowZero(w, r)
	if !ok {
		return
	}
	folderID, err := s.shares.Folder(r.Context(), sh, id)
	if err != nil {
		s.shareError(w, "listar enlace", err)
		return
	}
	l, err := s.vfs.List(r.Context(), folderID)
	if err != nil {
		s.shareError(w, "listar enlace", err)
		return
	}
	l = vfs.WithoutRestricted(l)
	path := append(l.Breadcrumbs, l.Folder)
	for i, f := range path {
		if f.ID == sh.TargetID {
			path = path[i:]
			break
		}
	}
	writeJSON(w, http.StatusOK, publicListing{Path: path, Folders: l.Folders, Files: l.Files})
}

// handlePublicContent: GET|HEAD /api/public/shares/{token}/files/{id}/content
func (s *Server) handlePublicContent(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.openShare(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	f, err := s.shares.File(r.Context(), sh, id)
	if err != nil {
		s.shareError(w, "contenido de enlace", err)
		return
	}
	s.serveContent(w, r, f, true, func(resumed bool) bool {
		if resumed || r.Method == http.MethodHead {
			return true
		}
		if err := s.shares.CountDownload(r.Context(), sh); err != nil {
			s.shareError(w, "contar descarga", err)
			return false
		}
		return true
	})
}

// handlePublicZip: GET /api/public/shares/{token}/zip[?folder=id] — descarga la
// carpeta entera como ZIP sin compresión, en streaming. Cuenta como una descarga.
func (s *Server) handlePublicZip(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.openShare(w, r)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(r.URL.Query().Get("folder"), 10, 64)
	folderID, err := s.shares.Folder(r.Context(), sh, id)
	if err != nil {
		s.shareError(w, "zip de enlace", err)
		return
	}
	l, err := s.vfs.List(r.Context(), folderID)
	if err != nil {
		s.shareError(w, "zip de enlace", err)
		return
	}
	l = vfs.WithoutRestricted(l)
	if err := s.shares.CountDownload(r.Context(), sh); err != nil {
		s.shareError(w, "contar descarga", err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": l.Folder.Name + ".zip"}))
	w.Header().Set("Cache-Control", "private, no-cache")
	zw := zip.NewWriter(httputil.IdleWriter(w, transferIdle))
	if err := s.zipFolder(r.Context(), zw, l, ""); err != nil {
		s.log.Debug("zip cortado", "share", sh.ID, "err", err)
		return
	}
	if err := zw.Close(); err != nil {
		s.log.Debug("zip cortado", "share", sh.ID, "err", err)
	}
}

// zipFolder añade el contenido de l bajo prefix. Un archivo ilegible se omite.
func (s *Server) zipFolder(ctx context.Context, zw *zip.Writer, l vfs.Listing, prefix string) error {
	for _, f := range l.Files {
		st, err := s.transfer.Open(ctx, f.ID, 0, -1)
		if err != nil {
			s.log.Warn("zip: archivo omitido", "file", f.ID, "err", err)
			continue
		}
		hdr := &zip.FileHeader{Name: prefix + f.Name, Method: zip.Store, Modified: time.Unix(f.ModTime, 0)}
		hdr.UncompressedSize64 = uint64(f.Size)
		dst, err := zw.CreateHeader(hdr)
		if err == nil {
			_, err = io.Copy(dst, st.Body)
		}
		st.Body.Close()
		if err != nil {
			return err
		}
	}
	for _, d := range l.Folders {
		sub, err := s.vfs.List(ctx, d.ID)
		if err != nil {
			return err
		}
		sub = vfs.WithoutRestricted(sub)
		if _, err := zw.Create(prefix + d.Name + "/"); err != nil {
			return err
		}
		if err := s.zipFolder(ctx, zw, sub, prefix+d.Name+"/"); err != nil {
			return err
		}
	}
	return nil
}

// resolveShare abre el enlace del path sin exigir la contraseña.
func (s *Server) resolveShare(w http.ResponseWriter, r *http.Request) (shares.Share, bool) {
	sh, err := s.shares.Resolve(r.Context(), r.PathValue("token"))
	if err != nil {
		s.shareError(w, "abrir enlace", err)
		return shares.Share{}, false
	}
	return sh, true
}

// openShare es resolveShare más la contraseña, si el enlace la tiene.
func (s *Server) openShare(w http.ResponseWriter, r *http.Request) (shares.Share, bool) {
	sh, ok := s.resolveShare(w, r)
	if !ok {
		return sh, false
	}
	if s.shares.Locked(sh, shareGrant(r)) {
		writeError(w, http.StatusForbidden, "share_locked", "el enlace necesita contraseña")
		return sh, false
	}
	return sh, true
}

func shareGrant(r *http.Request) string {
	if c, err := r.Cookie(shareCookie); err == nil {
		return c.Value
	}
	return ""
}

func (s *Server) shareError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, shares.ErrNotFound), errors.Is(err, shares.ErrOutside):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, shares.ErrWrongPassword):
		writeError(w, http.StatusForbidden, "wrong_password", err.Error())
	case errors.Is(err, shares.ErrInvalidTarget), errors.Is(err, shares.ErrWeakPassword):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, vfs.ErrNotFound), errors.Is(err, vfs.ErrFileNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		s.fileError(w, op, err)
	}
}

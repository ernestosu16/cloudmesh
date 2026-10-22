package http

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/hirasolutions/cloudmesh/internal/httputil"
	"github.com/hirasolutions/cloudmesh/internal/settings"
	"github.com/hirasolutions/cloudmesh/internal/storage"
	"github.com/hirasolutions/cloudmesh/internal/transfer"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

// handleUpload: POST /api/files?folder=<id>&name=<nombre>, cuerpo crudo con Content-Length.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	folderID, err := strconv.ParseInt(r.URL.Query().Get("folder"), 10, 64)
	if err != nil || folderID < 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "folder inválido")
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "falta name")
		return
	}
	if r.ContentLength < 0 {
		writeError(w, http.StatusLengthRequired, "length_required", "la subida necesita Content-Length")
		return
	}
	if r.ContentLength > s.maxUpload {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("el archivo supera el máximo de %d bytes", s.maxUpload))
		return
	}
	if !s.canWrite(w, r, vfs.ErrNotFound, folderID) {
		return
	}
	// Cada lectura renueva el plazo: una subida lenta pero viva no caduca; una parada sí.
	body := httputil.IdleReader(w, r.Body, transferIdle)
	u := userFrom(r.Context())
	f, err := s.transfer.UploadFor(r.Context(), transfer.Owner{ID: u.ID, Quota: u.QuotaBytes}, folderID, name, r.ContentLength, body)
	if err != nil {
		s.fileError(w, "subir", err)
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

// handleFileDetail: GET /api/files/{id}
func (s *Server) handleFileDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	det, err := s.vfs.FileDetail(r.Context(), id)
	if err != nil {
		s.fileError(w, "detalle", err)
		return
	}
	if !s.canRead(w, r, det.File.FolderID, vfs.ErrFileNotFound) {
		return
	}
	writeJSON(w, http.StatusOK, det)
}

// handleFileContent: GET /api/files/{id}/content[?download=1], con Range opcional.
func (s *Server) handleFileContent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	f, err := s.vfs.GetFile(r.Context(), id)
	if err != nil {
		s.fileError(w, "contenido", err)
		return
	}
	if !s.canRead(w, r, f.FolderID, vfs.ErrFileNotFound) {
		return
	}
	s.serveContent(w, r, f, false, nil)
}

// serveContent emite el contenido de f con Range opcional. public fuerza la
// descarga de tipos que el navegador ejecutaría (HTML, SVG) en nuestro origen.
// before, si no es nil, se llama justo antes de abrir el flujo con resumed a
// true cuando el rango no empieza en el byte 0 (un salto en un vídeo, una
// descarga reanudada); si devuelve false ya ha escrito la respuesta de error.
func (s *Server) serveContent(w http.ResponseWriter, r *http.Request, f vfs.File, public bool, before func(resumed bool) bool) {
	// El contenido de un archivo no cambia: con su ETag el navegador revalida
	// (304) en vez de volver a descargarlo, y un 304 no cuenta como descarga.
	etag := contentETag(f)
	if etag != "" {
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "private, no-cache")
		if etagMatch(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	rangeHeader := r.Header.Get("Range")
	if ir := r.Header.Get("If-Range"); ir != "" && (etag == "" || ir != etag) {
		rangeHeader = "" // la copia del cliente es otra: va entero
	}
	offset, length, partial, err := httputil.ParseRange(rangeHeader, f.Size)
	if err != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", f.Size))
		writeError(w, http.StatusRequestedRangeNotSatisfiable, "bad_range", err.Error())
		return
	}
	if before != nil && !before(partial && offset > 0) {
		return
	}
	st, err := s.transfer.Open(r.Context(), f.ID, offset, length)
	if err != nil {
		s.fileError(w, "contenido", err)
		return
	}
	defer st.Body.Close()

	ct := f.Mime
	if ct == "" {
		ct = "application/octet-stream"
	}
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" || public && activeContent(ct) {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", ct)
	// El contenido lo sube cualquiera con permiso de escritura: abierto en una
	// pestaña o en el iframe de la vista previa no ejecuta scripts ni comparte
	// el origen de la app. El visor de PDF del navegador no funciona aislado.
	if mt, _, _ := mime.ParseMediaType(ct); mt != "application/pdf" {
		w.Header().Set("Content-Security-Policy", contentCSP)
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(st.Length, 10))
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": f.Name}))
	w.Header().Set("Cache-Control", "private, no-cache")
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", st.Offset, st.Offset+st.Length-1, f.Size))
		w.WriteHeader(http.StatusPartialContent)
	}
	if r.Method == http.MethodHead {
		return
	}
	out := httputil.IdleWriter(w, transferIdle)
	if _, err := io.Copy(out, st.Body); err != nil {
		s.log.Debug("stream cortado", "file", f.ID, "err", err)
	}
}

// contentETag es un ETag fuerte del contenido: el SHA-256 o, si falta (objetos
// compuestos de S3), el ETag de S3. Vacío si no hay ninguno.
func contentETag(f vfs.File) string {
	switch {
	case f.SHA256 != "":
		return `"` + f.SHA256 + `"`
	case f.ETag != "":
		return `"` + f.ETag + `"`
	}
	return ""
}

// etagMatch aplica If-None-Match (comparación débil, lista o «*»).
func etagMatch(header, etag string) bool {
	for tag := range strings.SplitSeq(header, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || strings.TrimPrefix(tag, "W/") == etag {
			return true
		}
	}
	return false
}

// contentCSP aísla el contenido servido: origen opaco y sin scripts, y solo
// carga estilos en línea e imágenes y medios propios.
const contentCSP = "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; media-src 'self'"

// activeContent son tipos que el navegador interpreta con scripts.
func activeContent(ct string) bool {
	mt, _, _ := mime.ParseMediaType(ct)
	switch mt {
	case "text/html", "application/xhtml+xml", "text/xml", "application/xml":
		return true
	}
	// image/svg+xml, application/atom+xml…: XML que puede llevar XHTML con scripts.
	return strings.HasSuffix(mt, "+xml")
}

// handleUpdateFile: PATCH /api/files/{id} {name?, folder_id?}
func (s *Server) handleUpdateFile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Name     *string `json:"name"`
		FolderID *int64  `json:"folder_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	cur, err := s.vfs.GetFile(r.Context(), id)
	if err != nil {
		s.fileError(w, "actualizar archivo", err)
		return
	}
	targets := []int64{cur.FolderID}
	if req.FolderID != nil {
		targets = append(targets, *req.FolderID)
	}
	if !s.canWrite(w, r, vfs.ErrFileNotFound, targets...) {
		return
	}
	f, err := s.vfs.UpdateFile(r.Context(), id, req.Name, req.FolderID)
	if err != nil {
		s.fileError(w, "actualizar archivo", err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// handleDeleteFile: DELETE /api/files/{id} — manda el archivo a la papelera.
func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	cur, err := s.vfs.GetFile(r.Context(), id)
	if err != nil {
		s.fileError(w, "borrar archivo", err)
		return
	}
	if !s.canWrite(w, r, vfs.ErrFileNotFound, cur.FolderID) {
		return
	}
	if err := s.vfs.Trash(r.Context(), id); err != nil {
		s.fileError(w, "borrar archivo", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetSettings: GET /api/settings
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.settings.Get(r.Context())
	if err != nil {
		s.internal(w, "ajustes", err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// handleUpdateSettings: PATCH /api/settings (cuerpo completo)
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var in settings.Settings
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := settings.Validate(in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	cfg, err := s.settings.Update(r.Context(), in)
	if err != nil {
		s.internal(w, "guardar ajustes", err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) fileError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, transfer.ErrNoSpace), errors.Is(err, transfer.ErrNoAccounts):
		writeError(w, http.StatusInsufficientStorage, "no_space", err.Error())
	case errors.Is(err, storage.ErrTransferQuota):
		writeError(w, http.StatusServiceUnavailable, "transfer_limited",
			"la cuenta que guarda este archivo alcanzó su límite de transferencia; reintenta más tarde")
	case errors.Is(err, transfer.ErrUnreadable):
		writeError(w, http.StatusBadGateway, "unreadable", err.Error())
	case errors.Is(err, transfer.ErrQuota):
		writeError(w, http.StatusForbidden, "quota_exceeded", err.Error())
	case errors.Is(err, transfer.ErrUnavailable):
		writeError(w, http.StatusConflict, "uploading", err.Error())
	case errors.Is(err, vfs.ErrNotFound), errors.Is(err, vfs.ErrFileNotFound), errors.Is(err, vfs.ErrInvalidName):
		s.vfsError(w, op, err)
	default:
		s.accountError(w, op, err)
	}
}

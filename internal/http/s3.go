package http

import (
	"errors"
	"net/http"

	"github.com/hirasolutions/cloudmesh/internal/s3"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

// handleS3Info: GET /api/s3 — si la API está activa, su URL pública y la región.
func (s *Server) handleS3Info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"enabled": s.s3Enabled, "endpoint": s.s3URL, "region": s.s3.Region(), "domain": s.s3.Domain()})
}

// handleListBuckets: GET /api/s3/buckets
func (s *Server) handleListBuckets(w http.ResponseWriter, r *http.Request) {
	list, err := s.s3.ListBuckets(r.Context())
	if err != nil {
		s.internal(w, "listar buckets", err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleCreateBucket: POST /api/s3/buckets {name, folder_id} (0 = Buckets/<nombre>)
func (s *Server) handleCreateBucket(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		FolderID int64  `json:"folder_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	b, err := s.s3.CreateBucket(r.Context(), req.Name, req.FolderID)
	if err != nil {
		s.s3Error(w, "crear bucket", err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

// handleConfigureBucket: PATCH /api/s3/buckets/{id} {public, cors, lifecycle} — sustituye la configuración.
func (s *Server) handleConfigureBucket(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req s3.BucketConfig
	if !decodeJSON(w, r, &req) {
		return
	}
	b, err := s.s3.Configure(r.Context(), id, req)
	if err != nil {
		s.s3Error(w, "configurar bucket", err)
		return
	}
	s.log.Info("bucket configurado", "bucket", b.Name, "public", b.Public, "cors", len(b.CORS), "lifecycle", len(b.Lifecycle), "by", userFrom(r.Context()).Email)
	writeJSON(w, http.StatusOK, b)
}

// handleUnlinkBucket: DELETE /api/s3/buckets/{id} — la carpeta y su contenido se quedan.
func (s *Server) handleUnlinkBucket(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.s3.UnlinkBucket(r.Context(), id); err != nil {
		s.s3Error(w, "quitar bucket", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListKeys: GET /api/s3/keys
func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	list, err := s.s3.ListKeys(r.Context())
	if err != nil {
		s.internal(w, "listar llaves", err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleCreateKey: POST /api/s3/keys {name, permission, bucket_ids} — el secreto solo viaja aquí.
func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string  `json:"name"`
		Permission string  `json:"permission"`
		BucketIDs  []int64 `json:"bucket_ids"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	k, err := s.s3.CreateKey(r.Context(), userFrom(r.Context()).ID, req.Name, req.Permission, req.BucketIDs)
	if err != nil {
		s.s3Error(w, "crear llave", err)
		return
	}
	s.log.Info("llave S3 creada", "access_key_id", k.AccessKeyID, "permission", k.Permission, "by", userFrom(r.Context()).Email)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, k)
}

// handleRegenerateKey: POST /api/s3/keys/{id}/regenerate — nuevo secreto; el anterior deja de valer.
func (s *Server) handleRegenerateKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	k, err := s.s3.RegenerateKey(r.Context(), id)
	if err != nil {
		s.s3Error(w, "regenerar llave", err)
		return
	}
	s.log.Info("secreto S3 regenerado", "access_key_id", k.AccessKeyID, "by", userFrom(r.Context()).Email)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, k)
}

// handleDeleteKey: DELETE /api/s3/keys/{id}
func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.s3.DeleteKey(r.Context(), id); err != nil {
		s.s3Error(w, "revocar llave", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) s3Error(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, s3.ErrBucketName), errors.Is(err, s3.ErrPermission), errors.Is(err, s3.ErrScope),
		errors.Is(err, s3.ErrKeyName), errors.Is(err, s3.ErrBucketConfig):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, s3.ErrBucketExists):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, s3.ErrBucketNotFound), errors.Is(err, s3.ErrKeyNotFound), errors.Is(err, vfs.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		s.internal(w, op, err)
	}
}

package http

import (
	"context"
	"net/http"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/transfer"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

type healthResponse struct {
	transfer.HealthSummary
	ManifestWrittenAt int64      `json:"manifest_written_at"`
	DegradedFiles     []vfs.File `json:"degraded_files"`
}

// handleHealth: GET /api/health
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	sum, err := s.transfer.Health(r.Context())
	if err != nil {
		s.internal(w, "salud", err)
		return
	}
	a, ok := s.access(w, r)
	if !ok {
		return
	}
	out := healthResponse{HealthSummary: sum, ManifestWrittenAt: s.manifest.WrittenAt(r.Context()), DegradedFiles: []vfs.File{}}
	for _, id := range sum.Degraded {
		if f, err := s.vfs.GetFile(r.Context(), id); err == nil && a.CanRead(f.FolderID) {
			out.DegradedFiles = append(out.DegradedFiles, f)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleScrub: POST /api/health/scrub — verifica todas las cuentas ahora.
func (s *Server) handleScrub(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	res := s.transfer.ScrubAll(ctx)
	if res == nil {
		res = []transfer.ScrubResult{}
	}
	writeJSON(w, http.StatusOK, res)
}

// handleRebuildAll: POST /api/health/rebuild — repara todos los archivos dañados.
func (s *Server) handleRebuildAll(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Hour)
	defer cancel()
	res := s.transfer.RebuildAll(ctx)
	if res == nil {
		res = []transfer.RebuildResult{}
	}
	writeJSON(w, http.StatusOK, res)
}

// handleRebuildFile: POST /api/files/{id}/rebuild
func (s *Server) handleRebuildFile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	res, err := s.transfer.Rebuild(r.Context(), id)
	if err != nil {
		s.fileError(w, "reparar archivo", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleFormat: POST /api/health/format — vacía el pool: borra todos los
// archivos, carpetas y bloques y deja el índice de cero. No depende de que el
// cliente siga conectado: el índice ya está vacío y los bloques deben irse.
func (s *Server) handleFormat(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Hour)
	defer cancel()
	n, err := s.transfer.Format(ctx)
	if err != nil {
		s.internal(w, "formatear pool", err)
		return
	}
	if _, err := s.manifest.WriteIfChanged(ctx, true); err != nil {
		s.log.Warn("formatear pool: manifiesto", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]int64{"files": n})
}

// handleWriteManifest: POST /api/health/manifest — fuerza la escritura del manifiesto.
func (s *Server) handleWriteManifest(w http.ResponseWriter, r *http.Request) {
	n, err := s.manifest.WriteIfChanged(r.Context(), true)
	if err != nil {
		s.internal(w, "manifiesto", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": n, "written_at": s.manifest.WrittenAt(r.Context())})
}

// handleExportManifest: GET /api/health/manifest/export — descarga el índice
// cifrado con la clave maestra; se restaura con `cloudmesh restore --file`.
func (s *Server) handleExportManifest(w http.ResponseWriter, r *http.Request) {
	m, _, err := s.manifest.Build(r.Context())
	if err != nil {
		s.internal(w, "exportar índice", err)
		return
	}
	data, err := s.manifest.Seal(m)
	if err != nil {
		s.internal(w, "exportar índice", err)
		return
	}
	name := "cloudmesh-" + time.Now().UTC().Format("20060102-150405") + ".manifest.enc"
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

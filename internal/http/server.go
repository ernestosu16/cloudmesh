// Package http monta el servidor: API bajo /api y la SPA en el resto.
package http

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/accounts"
	"github.com/hirasolutions/cloudmesh/internal/auth"
	"github.com/hirasolutions/cloudmesh/internal/mail"
	"github.com/hirasolutions/cloudmesh/internal/manifest"
	"github.com/hirasolutions/cloudmesh/internal/s3"
	"github.com/hirasolutions/cloudmesh/internal/settings"
	"github.com/hirasolutions/cloudmesh/internal/shares"
	syncpkg "github.com/hirasolutions/cloudmesh/internal/sync"
	"github.com/hirasolutions/cloudmesh/internal/transfer"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

// Server agrupa dependencias de los handlers.
type Server struct {
	db           *sql.DB
	auth         *auth.Service
	accounts     *accounts.Service
	vfs          *vfs.Service
	sync         *syncpkg.Syncer
	transfer     *transfer.Service
	settings     *settings.Service
	manifest     *manifest.Service
	shares       *shares.Service
	s3           *s3.Service
	mail         *mail.Sender // nil: sin correo
	s3URL        string
	s3Enabled    bool
	maxUpload    int64
	trustProxy   bool
	version      string
	log          *slog.Logger
	loginLimiter *ipLimiter
	// emailLimiter frena el ataque a una sola cuenta (contraseña o código 2FA)
	// repartido entre muchas IP.
	emailLimiter *ipLimiter
}

// transferIdle es el máximo sin tráfico que se tolera en una subida o descarga
// antes de cortar la conexión. Las transferencias largas pero activas no se ven afectadas.
const transferIdle = 2 * time.Minute

// Deps son las dependencias del servidor.
type Deps struct {
	DB       *sql.DB
	Auth     *auth.Service
	Accounts *accounts.Service
	VFS      *vfs.Service
	Sync     *syncpkg.Syncer
	Transfer *transfer.Service
	Settings *settings.Service
	Manifest *manifest.Service
	Shares   *shares.Service
	S3       *s3.Service
	// Mail envía las invitaciones; nil si no hay SMTP configurado.
	Mail *mail.Sender
	// S3URL es la URL pública de la API S3 que se muestra en la app.
	S3URL     string
	S3Enabled bool
	// MaxUploadBytes limita cada subida; 0 usa 5 GiB.
	MaxUploadBytes int64
	// TrustProxy acepta X-Forwarded-For y X-Forwarded-Proto del proxy inverso.
	TrustProxy bool
	Version    string
	Log        *slog.Logger
}

// New construye el handler raíz.
func New(d Deps) http.Handler {
	s := &Server{
		db: d.DB, auth: d.Auth, accounts: d.Accounts, vfs: d.VFS, sync: d.Sync, transfer: d.Transfer,
		settings: d.Settings, manifest: d.Manifest, shares: d.Shares, s3: d.S3, mail: d.Mail, s3URL: d.S3URL, s3Enabled: d.S3Enabled, maxUpload: d.MaxUploadBytes, trustProxy: d.TrustProxy,
		version: d.Version, log: d.Log, loginLimiter: newIPLimiter(10), emailLimiter: newIPLimiter(10),
	}
	if s.maxUpload <= 0 {
		s.maxUpload = 5 << 30
	}
	mux := http.NewServeMux()

	// Público
	mux.HandleFunc("GET /api/healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/setup", s.handleSetupStatus)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/public/invitations/{token}", s.handlePublicInvitation)
	mux.HandleFunc("POST /api/public/invitations/{token}", s.handleAcceptInvitation)
	mux.HandleFunc("GET /api/public/shares/{token}", s.handlePublicShare)
	mux.HandleFunc("POST /api/public/shares/{token}/unlock", s.handleUnlockShare)
	mux.HandleFunc("GET /api/public/shares/{token}/folders/{id}", s.handlePublicFolder)
	mux.HandleFunc("GET /api/public/shares/{token}/files/{id}/content", s.handlePublicContent)
	mux.HandleFunc("HEAD /api/public/shares/{token}/files/{id}/content", s.handlePublicContent)
	mux.HandleFunc("GET /api/public/shares/{token}/zip", s.handlePublicZip)

	// Privado: todo lo que cuelga de api va detrás de requireAuth. Sin s.role
	// basta con estar dentro (lector); s.role exige editor o administrador.
	api := http.NewServeMux()
	api.HandleFunc("GET /api/auth/me", s.handleMe)
	api.HandleFunc("PATCH /api/auth/me", s.handleUpdateMe)
	api.HandleFunc("POST /api/auth/password", s.handleChangePassword)
	api.HandleFunc("POST /api/auth/2fa/setup", s.handleTOTPSetup)
	api.HandleFunc("POST /api/auth/2fa/enable", s.handleTOTPEnable)
	api.HandleFunc("POST /api/auth/2fa/disable", s.handleTOTPDisable)
	api.HandleFunc("POST /api/auth/2fa/recovery", s.handleRecoveryCodes)
	api.HandleFunc("DELETE /api/users/{id}/2fa", s.role(auth.RoleAdmin, s.handleResetUserTOTP))
	api.HandleFunc("GET /api/auth/sessions", s.handleSessions)
	api.HandleFunc("DELETE /api/auth/sessions", s.handleLogoutOthers)
	api.HandleFunc("GET /api/users", s.role(auth.RoleAdmin, s.handleListUsers))
	api.HandleFunc("GET /api/invitations", s.role(auth.RoleAdmin, s.handleListInvitations))
	api.HandleFunc("POST /api/invitations", s.role(auth.RoleAdmin, s.handleCreateInvitation))
	api.HandleFunc("DELETE /api/invitations/{id}", s.role(auth.RoleAdmin, s.handleRevokeInvitation))
	api.HandleFunc("POST /api/users", s.role(auth.RoleAdmin, s.handleCreateUser))
	api.HandleFunc("PATCH /api/users/{id}", s.role(auth.RoleAdmin, s.handleUpdateUser))
	api.HandleFunc("DELETE /api/users/{id}", s.role(auth.RoleAdmin, s.handleDeleteUser))
	api.HandleFunc("GET /api/quota", s.handleQuota)
	api.HandleFunc("GET /api/providers", s.role(auth.RoleAdmin, s.handleProviders))
	api.HandleFunc("GET /api/accounts", s.role(auth.RoleAdmin, s.handleListAccounts))
	api.HandleFunc("POST /api/accounts", s.role(auth.RoleAdmin, s.handleConnectAccount))
	api.HandleFunc("POST /api/accounts/test", s.role(auth.RoleAdmin, s.handleTestAccount))
	api.HandleFunc("PATCH /api/accounts/{id}", s.role(auth.RoleAdmin, s.handleUpdateAccount))
	api.HandleFunc("DELETE /api/accounts/{id}", s.role(auth.RoleAdmin, s.handleDeleteAccount))
	api.HandleFunc("POST /api/accounts/{id}/reconnect", s.role(auth.RoleAdmin, s.handleReconnectAccount))
	api.HandleFunc("POST /api/accounts/{id}/sync", s.role(auth.RoleAdmin, s.handleSyncAccount))
	api.HandleFunc("GET /api/folders/{id}", s.handleListFolder)
	api.HandleFunc("POST /api/folders", s.role(auth.RoleEditor, s.handleCreateFolder))
	api.HandleFunc("PATCH /api/folders/{id}", s.role(auth.RoleEditor, s.handleUpdateFolder))
	api.HandleFunc("DELETE /api/folders/{id}", s.role(auth.RoleEditor, s.handleDeleteFolder))
	api.HandleFunc("GET /api/folders/{id}/access", s.role(auth.RoleAdmin, s.handleGetFolderAccess))
	api.HandleFunc("PUT /api/folders/{id}/access", s.role(auth.RoleAdmin, s.handleSetFolderAccess))
	api.HandleFunc("GET /api/search", s.handleSearch)
	api.HandleFunc("POST /api/files", s.role(auth.RoleEditor, s.handleUpload))
	api.HandleFunc("GET /api/files/{id}", s.handleFileDetail)
	api.HandleFunc("GET /api/files/{id}/content", s.handleFileContent)
	api.HandleFunc("HEAD /api/files/{id}/content", s.handleFileContent)
	api.HandleFunc("PATCH /api/files/{id}", s.role(auth.RoleEditor, s.handleUpdateFile))
	api.HandleFunc("DELETE /api/files/{id}", s.role(auth.RoleEditor, s.handleDeleteFile))
	api.HandleFunc("POST /api/files/{id}/rebuild", s.role(auth.RoleAdmin, s.handleRebuildFile))
	api.HandleFunc("GET /api/trash", s.role(auth.RoleEditor, s.handleListTrash))
	api.HandleFunc("DELETE /api/trash", s.role(auth.RoleAdmin, s.handleEmptyTrash))
	api.HandleFunc("POST /api/trash/{id}/restore", s.role(auth.RoleEditor, s.handleRestoreTrash))
	api.HandleFunc("DELETE /api/trash/{id}", s.role(auth.RoleEditor, s.handlePurgeTrash))
	api.HandleFunc("GET /api/health", s.handleHealth)
	api.HandleFunc("POST /api/health/scrub", s.role(auth.RoleAdmin, s.handleScrub))
	api.HandleFunc("POST /api/health/rebuild", s.role(auth.RoleAdmin, s.handleRebuildAll))
	api.HandleFunc("POST /api/health/manifest", s.role(auth.RoleAdmin, s.handleWriteManifest))
	api.HandleFunc("POST /api/health/format", s.role(auth.RoleAdmin, s.handleFormat))
	api.HandleFunc("GET /api/health/manifest/export", s.role(auth.RoleAdmin, s.handleExportManifest))
	api.HandleFunc("GET /api/shares", s.role(auth.RoleEditor, s.handleListShares))
	api.HandleFunc("POST /api/shares", s.role(auth.RoleEditor, s.handleCreateShare))
	api.HandleFunc("PATCH /api/shares/{id}", s.role(auth.RoleEditor, s.handleUpdateShare))
	api.HandleFunc("DELETE /api/shares/{id}", s.role(auth.RoleEditor, s.handleDeleteShare))
	api.HandleFunc("GET /api/s3", s.role(auth.RoleAdmin, s.handleS3Info))
	api.HandleFunc("GET /api/s3/buckets", s.role(auth.RoleAdmin, s.handleListBuckets))
	api.HandleFunc("POST /api/s3/buckets", s.role(auth.RoleAdmin, s.handleCreateBucket))
	api.HandleFunc("PATCH /api/s3/buckets/{id}", s.role(auth.RoleAdmin, s.handleConfigureBucket))
	api.HandleFunc("DELETE /api/s3/buckets/{id}", s.role(auth.RoleAdmin, s.handleUnlinkBucket))
	api.HandleFunc("GET /api/s3/keys", s.role(auth.RoleAdmin, s.handleListKeys))
	api.HandleFunc("POST /api/s3/keys", s.role(auth.RoleAdmin, s.handleCreateKey))
	api.HandleFunc("PATCH /api/s3/keys/{id}", s.role(auth.RoleAdmin, s.handleUpdateKey))
	api.HandleFunc("POST /api/s3/keys/{id}/regenerate", s.role(auth.RoleAdmin, s.handleRegenerateKey))
	api.HandleFunc("DELETE /api/s3/keys/{id}", s.role(auth.RoleAdmin, s.handleDeleteKey))
	api.HandleFunc("GET /api/settings", s.handleGetSettings)
	api.HandleFunc("PATCH /api/settings", s.role(auth.RoleAdmin, s.handleUpdateSettings))
	api.HandleFunc("/api/", s.notFound)
	mux.Handle("/api/", s.requireAuth(api))

	mux.Handle("/", spaHandler())

	// La cookie es SameSite=Lax, que deja pasar peticiones desde otro
	// subdominio del mismo sitio (un bucket público en s3.<dominio>, por
	// ejemplo). Se rechaza todo POST/PATCH/DELETE que el navegador marque como
	// de otro origen.
	csrf := http.NewCrossOriginProtection()
	return s.logging(s.securityHeaders(csrf.Handler(mux)))
}

// securityHeaders añade cabeceras defensivas a toda respuesta.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if s.isHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// handleHealthz comprueba que la base responde.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := s.db.PingContext(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "db_unavailable", "la base de datos no responde")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "ruta no encontrada")
}

// writeJSON serializa v con el código dado. Las respuestas de la API son
// datos privados: ninguna caché las guarda salvo que el handler diga otra cosa.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError emite el formato de error único de la API.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// tokenPaths son las rutas cuyo siguiente tramo es un token secreto (enlace o invitación).
var tokenPaths = []string{"/api/public/shares/", "/api/public/invitations/", "/s/", "/invite/"}

// redactPath oculta el token de la ruta: quien lea el log no debe poder abrir
// un enlace con contraseña ya desbloqueado ni aceptar una invitación.
func redactPath(p string) string {
	for _, prefix := range tokenPaths {
		if rest, ok := strings.CutPrefix(p, prefix); ok {
			_, tail, _ := strings.Cut(rest, "/")
			if tail != "" {
				tail = "/" + tail
			}
			return prefix + "***" + tail
		}
	}
	return p
}

// logging registra cada petición con método, ruta, estado y duración.
func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		s.log.Info("http",
			"method", r.Method,
			"path", redactPath(r.URL.Path),
			"status", sw.status,
			"dur", time.Since(start).Round(time.Millisecond),
		)
	})
}

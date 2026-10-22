package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/accounts"
	"github.com/hirasolutions/cloudmesh/internal/storage"
)

type connectRequest struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
	Email    string `json:"email"`
	Password string `json:"password"`
	MFA      string `json:"mfa"`
	// Aporte opcional; vacío es toda la cuenta.
	ContribMode  string `json:"contrib_mode"`
	ContribValue int64  `json:"contrib_value"`
}

// handleProviders: GET /api/providers
func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.accounts.Providers())
}

// handleListAccounts: GET /api/accounts
func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	list, err := s.accounts.List(r.Context())
	if err != nil {
		s.internal(w, "listar cuentas", err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleQuota: GET /api/quota
func (s *Server) handleQuota(w http.ResponseWriter, r *http.Request) {
	sum, err := s.accounts.Summarize(r.Context())
	if err != nil {
		s.internal(w, "cuota", err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

// handleConnectAccount: POST /api/accounts
func (s *Server) handleConnectAccount(w http.ResponseWriter, r *http.Request) {
	var req connectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "email y contraseña son obligatorios")
		return
	}
	if req.ContribMode == "" {
		req.ContribMode = accounts.ContribAll
	}
	// Se valida antes de iniciar sesión para no dar de alta una cuenta a medias.
	if err := accounts.ValidateContribution(req.ContribMode, req.ContribValue); err != nil {
		s.accountError(w, "conectar cuenta", err)
		return
	}
	acc, err := s.accounts.Connect(r.Context(), req.Provider, req.Label, req.Email, req.Password, req.MFA)
	if err != nil {
		s.accountError(w, "conectar cuenta", err)
		return
	}
	if req.ContribMode != accounts.ContribAll {
		acc, err = s.accounts.UpdateSettings(r.Context(), acc.ID, accounts.Settings{
			Label: acc.Label, Priority: acc.Priority, Enabled: acc.Enabled,
			ContribMode: req.ContribMode, ContribValue: req.ContribValue,
		})
		if err != nil {
			s.accountError(w, "aporte de la cuenta", err)
			return
		}
	}
	s.syncInBackground(acc.ID)
	writeJSON(w, http.StatusCreated, acc)
}

// handleTestAccount: POST /api/accounts/test — comprueba credenciales sin guardar la cuenta.
func (s *Server) handleTestAccount(w http.ResponseWriter, r *http.Request) {
	var req connectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "email y contraseña son obligatorios")
		return
	}
	res, err := s.accounts.Test(r.Context(), req.Provider, req.Email, req.Password, req.MFA)
	if err != nil {
		s.accountError(w, "probar conexión", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleReconnectAccount: POST /api/accounts/{id}/reconnect
func (s *Server) handleReconnectAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Password string `json:"password"`
		MFA      string `json:"mfa"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Password == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "la contraseña es obligatoria")
		return
	}
	acc, err := s.accounts.Reconnect(r.Context(), id, req.Password, req.MFA)
	if err != nil {
		s.accountError(w, "reconectar cuenta", err)
		return
	}
	s.syncInBackground(acc.ID)
	writeJSON(w, http.StatusOK, acc)
}

// handleUpdateAccount: PATCH /api/accounts/{id}
func (s *Server) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in accounts.Settings
	if !decodeJSON(w, r, &in) {
		return
	}
	acc, err := s.accounts.UpdateSettings(r.Context(), id, in)
	if err != nil {
		s.accountError(w, "actualizar cuenta", err)
		return
	}
	writeJSON(w, http.StatusOK, acc)
}

// handleDeleteAccount: DELETE /api/accounts/{id}
func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.accounts.Delete(r.Context(), id); err != nil {
		s.accountError(w, "borrar cuenta", err)
		return
	}
	// Sus bloques quedan sin cuenta: recalcular estados y reparar en segundo plano.
	if err := s.transfer.RecomputeAll(r.Context()); err != nil {
		s.log.Error("recalcular estados tras borrar cuenta", "err", err)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		s.transfer.RebuildAll(ctx)
	}()
	w.WriteHeader(http.StatusNoContent)
}

// handleSyncAccount: POST /api/accounts/{id}/sync — reconcilia archivos y cuota.
func (s *Server) handleSyncAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	res, err := s.sync.SyncAccount(r.Context(), id)
	if err != nil {
		s.accountError(w, "sincronizar cuenta", err)
		return
	}
	acc, err := s.accounts.Get(r.Context(), id)
	if err != nil {
		s.accountError(w, "sincronizar cuenta", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": acc, "result": res})
}

// syncInBackground lanza la sincronización inicial sin bloquear la respuesta.
func (s *Server) syncInBackground(accountID int64) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if _, err := s.sync.SyncAccount(ctx, accountID); err != nil {
			s.log.Warn("sync inicial", "account", accountID, "err", err)
		}
	}()
}

// accountError traduce errores de cuentas y proveedores a respuestas HTTP.
func (s *Server) accountError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, storage.ErrMFARequired):
		writeError(w, http.StatusPreconditionRequired, "mfa_required", "esta cuenta pide un código de verificación")
	case errors.Is(err, storage.ErrInvalidCredentials):
		writeError(w, http.StatusBadRequest, "provider_invalid_credentials", "el proveedor rechazó el email, la contraseña o el código")
	case errors.Is(err, accounts.ErrAlreadyLinked):
		writeError(w, http.StatusConflict, "already_linked", err.Error())
	case errors.Is(err, accounts.ErrUnknownProvider):
		writeError(w, http.StatusBadRequest, "unknown_provider", err.Error())
	case errors.Is(err, accounts.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, accounts.ErrInvalidContribution):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, storage.ErrSessionExpired):
		writeError(w, http.StatusConflict, "reconnect_required", "la sesión con el proveedor expiró; reconecta la cuenta")
	case errors.Is(err, storage.ErrBlocked):
		writeError(w, http.StatusConflict, "provider_blocked", err.Error())
	case errors.Is(err, storage.ErrOverQuota):
		writeError(w, http.StatusConflict, "provider_over_quota", err.Error())
	default:
		s.internal(w, op, err)
	}
}

func (s *Server) internal(w http.ResponseWriter, op string, err error) {
	s.log.Error(op, "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "error interno")
}

// decodeJSON lee un cuerpo JSON pequeño; responde 400 y devuelve false si falla.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "cuerpo JSON inválido")
		return false
	}
	return true
}

// pathID parsea {id}; responde 404 y devuelve false si no es numérico o es 0.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := pathIDAllowZero(w, r)
	if ok && id == 0 {
		writeError(w, http.StatusNotFound, "not_found", "id inválido")
		return 0, false
	}
	return id, ok
}

// pathIDAllowZero es pathID aceptando 0 (la raíz virtual).
func pathIDAllowZero(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 0 {
		writeError(w, http.StatusNotFound, "not_found", "id inválido")
		return 0, false
	}
	return id, true
}

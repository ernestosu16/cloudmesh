package http

import (
	"errors"
	"net/http"

	"github.com/hirasolutions/cloudmesh/internal/auth"
)

// handleListUsers: GET /api/users
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.auth.ListUsers(r.Context())
	if err != nil {
		s.internal(w, "listar usuarios", err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

// handleCreateUser: POST /api/users {email, password, role, quota_bytes}
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email      string `json:"email"`
		Password   string `json:"password"`
		Role       string `json:"role"`
		QuotaBytes int64  `json:"quota_bytes"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Role == "" {
		req.Role = auth.RoleEditor
	}
	u, err := s.auth.CreateUserAs(r.Context(), req.Email, req.Password, req.Role, req.QuotaBytes)
	if err != nil {
		s.userError(w, "crear usuario", err)
		return
	}
	s.log.Info("usuario creado", "email", u.Email, "by", userFrom(r.Context()).Email)
	writeJSON(w, http.StatusCreated, u)
}

// handleUpdateUser: PATCH /api/users/{id} {role, quota_bytes}
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Role       string `json:"role"`
		QuotaBytes int64  `json:"quota_bytes"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u, err := s.auth.SetAccess(r.Context(), id, req.Role, req.QuotaBytes)
	if err != nil {
		s.userError(w, "cambiar acceso", err)
		return
	}
	s.log.Info("acceso cambiado", "user", u.Email, "role", u.Role, "quota", u.QuotaBytes, "by", userFrom(r.Context()).Email)
	writeJSON(w, http.StatusOK, u)
}

// handleDeleteUser: DELETE /api/users/{id}. Nadie puede borrarse a sí mismo,
// así siempre queda al menos un usuario.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if id == userFrom(r.Context()).ID {
		writeError(w, http.StatusConflict, "conflict", "no puedes eliminar tu propio usuario")
		return
	}
	if err := s.auth.DeleteUser(r.Context(), id); err != nil {
		s.userError(w, "borrar usuario", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUpdateMe: PATCH /api/auth/me {name}
func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u, err := s.auth.SetName(r.Context(), userFrom(r.Context()).ID, req.Name)
	if err != nil {
		s.userError(w, "cambiar nombre", err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// handleChangePassword: POST /api/auth/password {current_password, new_password}
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.auth.ChangePassword(r.Context(), userFrom(r.Context()).ID, req.Current, req.New, sessionToken(r)); err != nil {
		s.userError(w, "cambiar contraseña", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSessions: GET /api/auth/sessions — cuántas sesiones vigentes tiene el usuario.
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	n, err := s.auth.CountSessions(r.Context(), userFrom(r.Context()).ID)
	if err != nil {
		s.internal(w, "sesiones", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"active": n})
}

// handleLogoutOthers: DELETE /api/auth/sessions — cierra todas menos la actual.
func (s *Server) handleLogoutOthers(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.LogoutOthers(r.Context(), userFrom(r.Context()).ID, sessionToken(r)); err != nil {
		s.internal(w, "cerrar sesiones", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTOTPSetup: POST /api/auth/2fa/setup — secreto nuevo pendiente de confirmar.
func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	setup, err := s.auth.BeginTOTP(r.Context(), userFrom(r.Context()).ID)
	if err != nil {
		s.userError(w, "preparar 2FA", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, setup)
}

// handleTOTPEnable: POST /api/auth/2fa/enable {code} — devuelve los códigos de recuperación.
func (s *Server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	codes, err := s.auth.ConfirmTOTP(r.Context(), userFrom(r.Context()).ID, req.Code, sessionToken(r))
	if err != nil {
		s.userError(w, "activar 2FA", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string][]string{"recovery_codes": codes})
}

// handleRecoveryCodes: POST /api/auth/2fa/recovery {password, code} — códigos nuevos; los anteriores dejan de valer.
func (s *Server) handleRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	codes, err := s.auth.RegenerateRecoveryCodes(r.Context(), userFrom(r.Context()).ID, req.Password, req.Code)
	if err != nil {
		s.userError(w, "códigos de recuperación", err)
		return
	}
	s.log.Info("códigos de recuperación regenerados", "user", userFrom(r.Context()).Email)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string][]string{"recovery_codes": codes})
}

// handleTOTPDisable: POST /api/auth/2fa/disable {password, code}
func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.auth.DisableTOTP(r.Context(), userFrom(r.Context()).ID, req.Password, req.Code); err != nil {
		s.userError(w, "desactivar 2FA", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleResetUserTOTP: DELETE /api/users/{id}/2fa — quita la 2FA de otro
// usuario que perdió su móvil. La propia se quita desde el perfil.
func (s *Server) handleResetUserTOTP(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	me := userFrom(r.Context())
	if id == me.ID {
		writeError(w, http.StatusConflict, "conflict", "quita tu propia verificación desde tu perfil")
		return
	}
	if err := s.auth.ResetTOTP(r.Context(), id); err != nil {
		s.userError(w, "quitar 2FA", err)
		return
	}
	s.log.Info("2FA quitada", "user", id, "by", me.Email)
	w.WriteHeader(http.StatusNoContent)
}

func sessionToken(r *http.Request) string {
	if c, err := r.Cookie(sessionCookie); err == nil {
		return c.Value
	}
	return ""
}

func (s *Server) userError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalidEmail), errors.Is(err, auth.ErrWeakPassword), errors.Is(err, auth.ErrInvalidName),
		errors.Is(err, auth.ErrInvalidRole), errors.Is(err, auth.ErrInvalidQuota):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, auth.ErrWrongPassword):
		writeError(w, http.StatusBadRequest, "wrong_password", err.Error())
	case errors.Is(err, auth.ErrTOTPRequired), errors.Is(err, auth.ErrInvalidTOTP):
		writeError(w, http.StatusBadRequest, "invalid_totp", err.Error())
	case errors.Is(err, auth.ErrTOTPEnabled), errors.Is(err, auth.ErrTOTPNotStarted):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, auth.ErrTOTPUnavailable), errors.Is(err, auth.ErrRecoveryCodes):
		writeError(w, http.StatusServiceUnavailable, "unavailable", err.Error())
	case errors.Is(err, auth.ErrEmailTaken), errors.Is(err, auth.ErrLastAdmin):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, auth.ErrInvitationInvalid):
		writeError(w, http.StatusGone, "invitation_invalid", err.Error())
	case errors.Is(err, auth.ErrUserNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		s.internal(w, op, err)
	}
}

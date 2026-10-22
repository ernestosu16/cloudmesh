package http

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/auth"
)

const sessionCookie = "cloudmesh_session"

type ctxKey int

const userKey ctxKey = iota

// userFrom devuelve el usuario autenticado puesto por requireAuth.
func userFrom(ctx context.Context) auth.User {
	u, _ := ctx.Value(userKey).(auth.User)
	return u
}

// requireAuth rechaza con 401 las peticiones sin sesión válida.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Cookie(sessionCookie)
		token := ""
		if c != nil {
			token = c.Value
		}
		u, err := s.auth.Authenticate(r.Context(), token)
		if errors.Is(err, auth.ErrNoSession) {
			s.clearSessionCookie(w, r)
			writeError(w, http.StatusUnauthorized, "unauthenticated", "inicia sesión")
			return
		}
		if err != nil {
			s.log.Error("auth", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "error interno")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Code     string `json:"code"` // TOTP, solo si el usuario tiene 2FA
}

// handleLogin: POST /api/auth/login
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.loginLimiter.allow(s.clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "demasiados intentos, espera un minuto")
		return
	}
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "cuerpo JSON inválido")
		return
	}
	if !s.emailLimiter.allow(strings.ToLower(strings.TrimSpace(req.Email))) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "demasiados intentos, espera un minuto")
		return
	}
	u, token, err := s.auth.Login(r.Context(), req.Email, req.Password, req.Code)
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "email o contraseña incorrectos")
		return
	case errors.Is(err, auth.ErrTOTPRequired):
		writeError(w, http.StatusPreconditionRequired, "totp_required", err.Error())
		return
	case errors.Is(err, auth.ErrInvalidTOTP):
		writeError(w, http.StatusUnauthorized, "invalid_totp", err.Error())
		return
	}
	if err != nil {
		s.log.Error("login", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "error interno")
		return
	}
	s.setSessionCookie(w, r, token)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(auth.SessionTTL / time.Second),
		HttpOnly: true,
		Secure:   s.isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// handleSetupStatus: GET /api/setup — público; dice si falta crear el administrador.
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	needed, err := s.auth.SetupNeeded(r.Context())
	if err != nil {
		s.internal(w, "setup status", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"needed": needed})
}

// handleSetup: POST /api/setup — crea el primer usuario con el código del log.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if !s.loginLimiter.allow(s.clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "demasiados intentos, espera un minuto")
		return
	}
	var req struct {
		Code     string `json:"code"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u, token, err := s.auth.CompleteSetup(r.Context(), req.Code, req.Email, req.Password)
	switch {
	case errors.Is(err, auth.ErrSetupDone):
		writeError(w, http.StatusConflict, "setup_done", err.Error())
		return
	case errors.Is(err, auth.ErrInvalidSetupCode):
		writeError(w, http.StatusForbidden, "invalid_setup_code", err.Error())
		return
	case errors.Is(err, auth.ErrInvalidEmail), errors.Is(err, auth.ErrWeakPassword):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	case err != nil:
		s.internal(w, "setup", err)
		return
	}
	s.setSessionCookie(w, r, token)
	s.log.Info("configuración inicial completada", "email", u.Email)
	writeJSON(w, http.StatusCreated, u)
}

// handleLogout: POST /api/auth/logout
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.auth.Logout(r.Context(), c.Value); err != nil {
			s.log.Error("logout", "err", err)
		}
	}
	s.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// handleMe: GET /api/auth/me — con lo que ocupa el usuario, para su cuota.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	used, err := s.auth.Usage(r.Context(), u.ID)
	if err != nil {
		s.internal(w, "uso", err)
		return
	}
	u.UsedBytes = used
	writeJSON(w, http.StatusOK, u)
}

// role envuelve un handler que exige al menos ese rol.
func (s *Server) role(min string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !userFrom(r.Context()).Can(min) {
			writeError(w, http.StatusForbidden, "forbidden", "tu rol no permite esta acción")
			return
		}
		h(w, r)
	}
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// isHTTPS detecta TLS directo o, si se confía en el proxy, X-Forwarded-Proto.
func (s *Server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return s.trustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// clientIP usa X-Forwarded-For solo si se confía en el proxy; si no, cualquiera
// podría falsearla para esquivar el límite de intentos.
func (s *Server) clientIP(r *http.Request) string {
	if s.trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.IndexByte(xff, ','); i > 0 {
				return strings.TrimSpace(xff[:i])
			}
			return strings.TrimSpace(xff)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ipLimiter limita intentos por IP en ventanas fijas de un minuto.
type ipLimiter struct {
	mu    sync.Mutex
	max   int
	hits  map[string]int
	reset time.Time
}

func newIPLimiter(max int) *ipLimiter {
	return &ipLimiter{max: max, hits: map[string]int{}, reset: time.Now().Add(time.Minute)}
}

func (l *ipLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now := time.Now(); now.After(l.reset) {
		l.hits = map[string]int{}
		l.reset = now.Add(time.Minute)
	}
	l.hits[ip]++
	return l.hits[ip] <= l.max
}

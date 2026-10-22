package http

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/auth"
)

// inviteBase es el origen de la app para construir el enlace: el Origin del
// navegador del administrador (así vale detrás de proxies y de Vite) o, si no
// viene, el esquema y host de la petición.
func (s *Server) inviteBase(r *http.Request) string {
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			return u.Scheme + "://" + u.Host
		}
	}
	scheme := "http"
	if s.isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// handleListInvitations: GET /api/invitations — pendientes y vigentes.
func (s *Server) handleListInvitations(w http.ResponseWriter, r *http.Request) {
	list, err := s.auth.ListInvitations(r.Context())
	if err != nil {
		s.internal(w, "listar invitaciones", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invitations": list, "mail_enabled": s.mail != nil})
}

// handleCreateInvitation: POST /api/invitations {email, role, quota_bytes}
// — devuelve el enlace (solo esta vez) y, con SMTP, lo manda por correo.
func (s *Server) handleCreateInvitation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email      string `json:"email"`
		Role       string `json:"role"`
		QuotaBytes int64  `json:"quota_bytes"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Role == "" {
		req.Role = auth.RoleEditor
	}
	me := userFrom(r.Context())
	inv, token, err := s.auth.Invite(r.Context(), me.ID, req.Email, req.Role, req.QuotaBytes)
	if err != nil {
		s.userError(w, "invitar", err)
		return
	}
	link := s.inviteBase(r) + "/invite/" + token
	out := map[string]any{"invitation": inv, "url": link, "emailed": false}
	if s.mail != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
		defer cancel()
		if err := s.mail.Send(ctx, inv.Email, "Te invitan a CloudMesh", inviteBody(me, inv, link)); err != nil {
			s.log.Warn("invitación: correo no enviado", "to", inv.Email, "err", err)
			out["email_error"] = "no se pudo enviar el correo; copia el enlace y pásalo tú"
		} else {
			out["emailed"] = true
		}
	}
	s.log.Info("invitación creada", "email", inv.Email, "role", inv.Role, "by", me.Email, "emailed", out["emailed"])
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, out)
}

func inviteBody(by auth.User, inv auth.Invitation, link string) string {
	who := by.Email
	if by.Name != "" {
		who = by.Name + " (" + by.Email + ")"
	}
	return fmt.Sprintf(`Hola:

%s te invita a CloudMesh.

Abre este enlace para elegir tu contraseña y entrar:

%s

El enlace sirve una sola vez y caduca el %s. Si no esperabas esta invitación, ignora este correo.
`, who, link, time.Unix(inv.ExpiresAt, 0).Format("02/01/2006 15:04"))
}

// handleRevokeInvitation: DELETE /api/invitations/{id}
func (s *Server) handleRevokeInvitation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.auth.RevokeInvitation(r.Context(), id); err != nil {
		s.userError(w, "anular invitación", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePublicInvitation: GET /api/public/invitations/{token} — a quién invita y hasta cuándo.
func (s *Server) handlePublicInvitation(w http.ResponseWriter, r *http.Request) {
	inv, err := s.auth.LookupInvitation(r.Context(), r.PathValue("token"))
	if err != nil {
		s.userError(w, "invitación", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"email": inv.Email, "role": inv.Role, "expires_at": inv.ExpiresAt})
}

// handleAcceptInvitation: POST /api/public/invitations/{token} {name, password} — crea la cuenta y entra.
func (s *Server) handleAcceptInvitation(w http.ResponseWriter, r *http.Request) {
	if !s.loginLimiter.allow(s.clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "demasiados intentos, espera un minuto")
		return
	}
	var req struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u, token, err := s.auth.AcceptInvitation(r.Context(), r.PathValue("token"), req.Name, req.Password)
	if err != nil {
		s.userError(w, "aceptar invitación", err)
		return
	}
	s.log.Info("invitación aceptada", "user", u.Email)
	s.setSessionCookie(w, r, token)
	writeJSON(w, http.StatusOK, u)
}

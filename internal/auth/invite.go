package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
)

// InvitationTTL es lo que vale un enlace de invitación.
const InvitationTTL = 7 * 24 * time.Hour

var ErrInvitationInvalid = errors.New("la invitación no existe, caducó o ya se usó")

// Invitation es una invitación pendiente, sin su token.
type Invitation struct {
	ID             int64  `json:"id"`
	Email          string `json:"email"`
	Role           string `json:"role"`
	QuotaBytes     int64  `json:"quota_bytes"`
	CreatedAt      int64  `json:"created_at"`
	ExpiresAt      int64  `json:"expires_at"`
	CreatedByEmail string `json:"created_by_email,omitempty"`
}

func toInvitation(r sqlcgen.Invitation) Invitation {
	return Invitation{ID: r.ID, Email: r.Email, Role: r.Role, QuotaBytes: r.QuotaBytes, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt}
}

func inviteHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Invite crea una invitación y devuelve su token, que solo existe en el enlace.
// Invitar otra vez al mismo email sustituye la invitación pendiente.
func (s *Service) Invite(ctx context.Context, by int64, email, role string, quota int64) (Invitation, string, error) {
	if roleLevel(role) == 0 {
		return Invitation{}, "", ErrInvalidRole
	}
	if quota < 0 {
		return Invitation{}, "", ErrInvalidQuota
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email {
		return Invitation{}, "", ErrInvalidEmail
	}
	if _, err := s.q.GetUserIDByEmail(ctx, email); err == nil {
		return Invitation{}, "", ErrEmailTaken
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Invitation{}, "", err
	}
	now := s.now()
	// Limpieza oportunista: caducadas y aceptadas hace más de un mes.
	_ = s.q.DeleteOldInvitations(ctx, sqlcgen.DeleteOldInvitationsParams{ExpiresAt: now.Unix(), AcceptedAt: now.Add(-30 * 24 * time.Hour).Unix()})
	if err := s.q.DeletePendingInvitations(ctx, email); err != nil {
		return Invitation{}, "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Invitation{}, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	row, err := s.q.CreateInvitation(ctx, sqlcgen.CreateInvitationParams{
		TokenHash: inviteHash(token), Email: email, Role: role, QuotaBytes: quota,
		CreatedBy: sql.NullInt64{Int64: by, Valid: by != 0}, CreatedAt: now.Unix(), ExpiresAt: now.Add(InvitationTTL).Unix(),
	})
	if err != nil {
		return Invitation{}, "", err
	}
	return toInvitation(row), token, nil
}

// ListInvitations devuelve las pendientes y vigentes, la más reciente primero.
func (s *Service) ListInvitations(ctx context.Context) ([]Invitation, error) {
	rows, err := s.q.ListPendingInvitations(ctx, s.now().Unix())
	if err != nil {
		return nil, err
	}
	out := make([]Invitation, 0, len(rows))
	for _, r := range rows {
		inv := Invitation{ID: r.ID, Email: r.Email, Role: r.Role, QuotaBytes: r.QuotaBytes, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, CreatedByEmail: r.CreatedByEmail}
		out = append(out, inv)
	}
	return out, nil
}

// RevokeInvitation anula una invitación pendiente.
func (s *Service) RevokeInvitation(ctx context.Context, id int64) error {
	n, err := s.q.DeleteInvitation(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrInvitationInvalid
	}
	return nil
}

// LookupInvitation devuelve la invitación vigente de un token.
func (s *Service) LookupInvitation(ctx context.Context, token string) (Invitation, error) {
	row, err := s.q.GetInvitationByToken(ctx, inviteHash(token))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (row.AcceptedAt != 0 || row.ExpiresAt <= s.now().Unix())) {
		return Invitation{}, ErrInvitationInvalid
	}
	if err != nil {
		return Invitation{}, err
	}
	return toInvitation(row), nil
}

// AcceptInvitation crea la cuenta invitada con su nombre y contraseña y abre
// una sesión. Devuelve el token de la cookie.
func (s *Service) AcceptInvitation(ctx context.Context, token, name, password string) (User, string, error) {
	inv, err := s.LookupInvitation(ctx, token)
	if err != nil {
		return User{}, "", err
	}
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > 80 {
		return User{}, "", ErrInvalidName
	}
	if len(password) < MinPasswordLen {
		return User{}, "", ErrWeakPassword
	}
	// Se marca primero: dos envíos a la vez no crean dos cuentas.
	n, err := s.q.AcceptInvitation(ctx, sqlcgen.AcceptInvitationParams{AcceptedAt: s.now().Unix(), ID: inv.ID, ExpiresAt: s.now().Unix()})
	if err != nil {
		return User{}, "", err
	}
	if n == 0 {
		return User{}, "", ErrInvitationInvalid
	}
	u, err := s.CreateUserAs(ctx, inv.Email, password, inv.Role, inv.QuotaBytes)
	if err != nil {
		if !errors.Is(err, ErrEmailTaken) {
			_ = s.q.ReopenInvitation(context.WithoutCancel(ctx), inv.ID)
		}
		return User{}, "", err
	}
	if name != "" {
		if u, err = s.SetName(ctx, u.ID, name); err != nil {
			return User{}, "", err
		}
	}
	session, err := s.startSession(ctx, u.ID)
	if err != nil {
		return User{}, "", err
	}
	return u, session, nil
}

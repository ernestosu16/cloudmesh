// Package auth gestiona usuarios y sesiones por cookie.
//
// La cookie lleva un token aleatorio de 32 bytes. En la base solo se guarda
// HMAC-SHA256(secret, token), así una copia de la base no sirve para
// suplantar sesiones sin el secreto del proceso.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hirasolutions/cloudmesh/internal/crypto"
	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
)

// SessionTTL es la vida de una sesión desde el login.
const SessionTTL = 30 * 24 * time.Hour

// MinPasswordLen evita contraseñas triviales en create-user.
const MinPasswordLen = 10

var (
	ErrInvalidCredentials = errors.New("credenciales inválidas")
	ErrNoSession          = errors.New("sin sesión")
	ErrEmailTaken         = errors.New("ya existe un usuario con ese email")
	ErrInvalidEmail       = errors.New("email inválido")
	ErrWeakPassword       = fmt.Errorf("la contraseña debe tener al menos %d caracteres", MinPasswordLen)
	ErrUserNotFound       = errors.New("usuario no encontrado")
	ErrWrongPassword      = errors.New("la contraseña actual no es correcta")
	ErrInvalidName        = errors.New("el nombre admite hasta 80 caracteres")
	ErrInvalidRole        = errors.New("rol desconocido: admin, editor o reader")
	ErrInvalidQuota       = errors.New("la cuota no puede ser negativa")
	ErrLastAdmin          = errors.New("debe quedar al menos un administrador")
)

// Roles. Admin lo gestiona todo; editor sube, organiza, comparte y usa la
// papelera; reader solo ve y descarga.
const (
	RoleAdmin  = "admin"
	RoleEditor = "editor"
	RoleReader = "reader"
)

// User es la identidad expuesta al resto de la app.
type User struct {
	ID          int64  `json:"id"`
	Email       string `json:"email"`
	Name        string `json:"name"`
	CreatedAt   int64  `json:"created_at"`
	TOTPEnabled bool   `json:"totp_enabled"`
	// RecoveryCodesLeft son los códigos de recuperación sin usar (solo con 2FA).
	RecoveryCodesLeft int64  `json:"recovery_codes_left"`
	Role              string `json:"role"`
	QuotaBytes        int64  `json:"quota_bytes"` // 0: sin límite
	UsedBytes         int64  `json:"used_bytes"`  // solo en listados y en /me
}

// Can dice si el rol llega al nivel pedido: reader < editor < admin.
func (u User) Can(role string) bool { return roleLevel(u.Role) >= roleLevel(role) }

func roleLevel(r string) int {
	switch r {
	case RoleAdmin:
		return 3
	case RoleEditor:
		return 2
	case RoleReader:
		return 1
	}
	return 0
}

// Service encapsula las operaciones de autenticación.
type Service struct {
	q      *sqlcgen.Queries
	secret []byte
	box    *crypto.Box // cifra secretos TOTP; ver UseBox
	now    func() time.Time
	setup  setupState
}

// NewService recibe la base y el secreto de 32 bytes de la configuración.
func NewService(sqlDB *sql.DB, secret []byte) *Service {
	return &Service{q: sqlcgen.New(sqlDB), secret: secret, now: time.Now}
}

// CreateUser da de alta un administrador (configuración inicial y la CLI).
func (s *Service) CreateUser(ctx context.Context, email, password string) (User, error) {
	return s.CreateUserAs(ctx, email, password, RoleAdmin, 0)
}

// CreateUserAs da de alta un usuario con rol y cuota. Normaliza el email a minúsculas.
func (s *Service) CreateUserAs(ctx context.Context, email, password, role string, quota int64) (User, error) {
	if roleLevel(role) == 0 {
		return User{}, ErrInvalidRole
	}
	if quota < 0 {
		return User{}, ErrInvalidQuota
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email {
		return User{}, ErrInvalidEmail
	}
	if len(password) < MinPasswordLen {
		return User{}, ErrWeakPassword
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	row, err := s.q.CreateUser(ctx, sqlcgen.CreateUserParams{
		Email:        email,
		PasswordHash: hash,
		CreatedAt:    s.now().Unix(),
		Role:         role,
		QuotaBytes:   quota,
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, ErrEmailTaken
		}
		return User{}, err
	}
	return User{ID: row.ID, Email: row.Email, Name: row.Name, CreatedAt: row.CreatedAt, Role: row.Role, QuotaBytes: row.QuotaBytes}, nil
}

// SetAccess cambia rol y cuota. Nunca deja la instancia sin administradores.
func (s *Service) SetAccess(ctx context.Context, id int64, role string, quota int64) (User, error) {
	if roleLevel(role) == 0 {
		return User{}, ErrInvalidRole
	}
	if quota < 0 {
		return User{}, ErrInvalidQuota
	}
	cur, err := s.userRow(ctx, id)
	if err != nil {
		return User{}, err
	}
	if cur.Role == RoleAdmin && role != RoleAdmin {
		if err := s.keepAnAdmin(ctx); err != nil {
			return User{}, err
		}
	}
	row, err := s.q.UpdateUserAccess(ctx, sqlcgen.UpdateUserAccessParams{Role: role, QuotaBytes: quota, ID: id})
	if err != nil {
		return User{}, err
	}
	used, err := s.Usage(ctx, id)
	if err != nil {
		return User{}, err
	}
	return User{ID: row.ID, Email: row.Email, Name: row.Name, CreatedAt: row.CreatedAt, TOTPEnabled: row.TotpEnabled != 0,
		Role: row.Role, QuotaBytes: row.QuotaBytes, UsedBytes: used}, nil
}

// Usage es lo que ocupan los archivos subidos por el usuario, papelera incluida.
func (s *Service) Usage(ctx context.Context, id int64) (int64, error) {
	return s.q.OwnerUsage(ctx, sql.NullInt64{Int64: id, Valid: true})
}

func (s *Service) keepAnAdmin(ctx context.Context) error {
	n, err := s.q.CountAdmins(ctx)
	if err != nil {
		return err
	}
	if n <= 1 {
		return ErrLastAdmin
	}
	return nil
}

// SetName cambia el nombre visible; vacío vuelve a mostrar el email.
func (s *Service) SetName(ctx context.Context, userID int64, name string) (User, error) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > 80 {
		return User{}, ErrInvalidName
	}
	row, err := s.q.UpdateUserName(ctx, sqlcgen.UpdateUserNameParams{Name: name, ID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, err
	}
	return User{ID: row.ID, Email: row.Email, Name: row.Name, CreatedAt: row.CreatedAt, TOTPEnabled: row.TotpEnabled != 0,
		Role: row.Role, QuotaBytes: row.QuotaBytes}, nil
}

// ListUsers devuelve todos los usuarios por orden de alta.
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.q.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	usage, err := s.q.UsageByOwner(ctx)
	if err != nil {
		return nil, err
	}
	used := map[int64]int64{}
	for _, u := range usage {
		used[u.OwnerID] = u.Used
	}
	out := make([]User, 0, len(rows))
	for _, r := range rows {
		out = append(out, User{ID: r.ID, Email: r.Email, Name: r.Name, CreatedAt: r.CreatedAt, TOTPEnabled: r.TotpEnabled != 0,
			Role: r.Role, QuotaBytes: r.QuotaBytes, UsedBytes: used[r.ID]})
	}
	return out, nil
}

// DeleteUser borra un usuario con sus sesiones y enlaces; sus archivos se quedan sin dueño.
func (s *Service) DeleteUser(ctx context.Context, id int64) error {
	if u, err := s.userRow(ctx, id); err == nil && u.Role == RoleAdmin {
		if err := s.keepAnAdmin(ctx); err != nil {
			return err
		}
	}
	n, err := s.q.DeleteUser(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// ChangePassword exige la contraseña actual y cierra las demás sesiones del usuario.
func (s *Service) ChangePassword(ctx context.Context, userID int64, current, next, keepToken string) error {
	u, err := s.q.GetUserWithHash(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return err
	}
	if !VerifyPassword(u.PasswordHash, current) {
		return ErrWrongPassword
	}
	if len(next) < MinPasswordLen {
		return ErrWeakPassword
	}
	hash, err := HashPassword(next)
	if err != nil {
		return err
	}
	if err := s.q.UpdateUserPassword(ctx, sqlcgen.UpdateUserPasswordParams{PasswordHash: hash, ID: userID}); err != nil {
		return err
	}
	return s.LogoutOthers(ctx, userID, keepToken)
}

// CountSessions cuenta las sesiones vigentes del usuario.
func (s *Service) CountSessions(ctx context.Context, userID int64) (int64, error) {
	return s.q.CountUserSessions(ctx, sqlcgen.CountUserSessionsParams{UserID: userID, ExpiresAt: s.now().Unix()})
}

// LogoutOthers cierra todas las sesiones del usuario salvo la del token dado.
func (s *Service) LogoutOthers(ctx context.Context, userID int64, keepToken string) error {
	return s.q.DeleteOtherSessions(ctx, sqlcgen.DeleteOtherSessionsParams{UserID: userID, ID: s.sessionID(keepToken)})
}

// Login valida credenciales y crea una sesión. Devuelve el token para la cookie.
// Con la 2FA activa exige code: sin él devuelve ErrTOTPRequired.
func (s *Service) Login(ctx context.Context, email, password, code string) (User, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	u, err := s.q.GetUserByEmail(ctx, email)
	if errors.Is(err, sql.ErrNoRows) {
		// Verificar contra un hash ficticio iguala el tiempo de respuesta
		// para emails existentes e inexistentes.
		VerifyPassword(dummyHash, password)
		return User{}, "", ErrInvalidCredentials
	}
	if err != nil {
		return User{}, "", err
	}
	if !VerifyPassword(u.PasswordHash, password) {
		return User{}, "", ErrInvalidCredentials
	}
	left := recoveryLeft(u.RecoveryCodes)
	if u.TotpEnabled != 0 {
		if err := s.checkSecondFactor(ctx, u, code); err != nil {
			return User{}, "", err
		}
		if isRecoveryCode(code) {
			left--
		}
	}

	token, err := s.startSession(ctx, u.ID)
	if err != nil {
		return User{}, "", err
	}

	return User{ID: u.ID, Email: u.Email, Name: u.Name, CreatedAt: u.CreatedAt, TOTPEnabled: u.TotpEnabled != 0, Role: u.Role, QuotaBytes: u.QuotaBytes,
		RecoveryCodesLeft: left}, token, nil
}

// startSession crea una sesión y devuelve el token para la cookie.
func (s *Service) startSession(ctx context.Context, userID int64) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()
	if err := s.q.CreateSession(ctx, sqlcgen.CreateSessionParams{
		ID:        s.sessionID(token),
		UserID:    userID,
		ExpiresAt: now.Add(SessionTTL).Unix(),
		CreatedAt: now.Unix(),
	}); err != nil {
		return "", err
	}
	// Limpieza oportunista; el error no afecta al login.
	_ = s.q.DeleteExpiredSessions(ctx, now.Unix())
	return token, nil
}

// Authenticate resuelve el usuario de un token de cookie.
func (s *Service) Authenticate(ctx context.Context, token string) (User, error) {
	if token == "" {
		return User{}, ErrNoSession
	}
	row, err := s.q.GetSessionUser(ctx, sqlcgen.GetSessionUserParams{
		ID:        s.sessionID(token),
		ExpiresAt: s.now().Unix(),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNoSession
	}
	if err != nil {
		return User{}, err
	}
	return User{ID: row.UserID, Email: row.Email, Name: row.Name, CreatedAt: row.CreatedAt, TOTPEnabled: row.TotpEnabled != 0,
		Role: row.Role, QuotaBytes: row.QuotaBytes, RecoveryCodesLeft: row.RecoveryLeft}, nil
}

// Logout invalida la sesión del token. Idempotente.
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.q.DeleteSession(ctx, s.sessionID(token))
}

// sessionID deriva la clave de la fila a partir del token de la cookie.
func (s *Service) sessionID(token string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

// dummyHash es un hash válido de una contraseña aleatoria; solo iguala tiempos.
var dummyHash = func() string {
	h, err := HashPassword("cloudmesh-dummy-password")
	if err != nil {
		panic(err)
	}
	return h
}()

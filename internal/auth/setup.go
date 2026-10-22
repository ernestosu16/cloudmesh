package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"strings"
	"sync"
)

var (
	ErrSetupDone        = errors.New("la configuración inicial ya se hizo")
	ErrInvalidSetupCode = errors.New("código de configuración incorrecto")
)

// Alfabeto sin caracteres ambiguos (0/O, 1/I/L).
const setupAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// setupState guarda el código de un solo uso que protege la creación del
// primer usuario. Vive solo en memoria: cada arranque sin usuarios genera uno
// nuevo y lo escribe en el log.
type setupState struct {
	mu   sync.Mutex
	code string // vacío cuando no hace falta setup
}

// InitSetup comprueba si hay usuarios. Si no hay, genera el código y lo
// devuelve para que el arranque lo registre. Si ya hay usuarios devuelve "".
func (s *Service) InitSetup(ctx context.Context) (string, error) {
	n, err := s.q.CountUsers(ctx)
	if err != nil {
		return "", err
	}
	s.setup.mu.Lock()
	defer s.setup.mu.Unlock()
	if n > 0 {
		s.setup.code = ""
		return "", nil
	}
	code, err := newSetupCode()
	if err != nil {
		return "", err
	}
	s.setup.code = code
	return code, nil
}

// SetupNeeded indica si la instancia aún no tiene usuarios.
func (s *Service) SetupNeeded(ctx context.Context) (bool, error) {
	n, err := s.q.CountUsers(ctx)
	return n == 0, err
}

// CompleteSetup crea el primer usuario si el código coincide y devuelve el
// token de sesión para dejarlo autenticado.
func (s *Service) CompleteSetup(ctx context.Context, code, email, password string) (User, string, error) {
	needed, err := s.SetupNeeded(ctx)
	if err != nil {
		return User{}, "", err
	}
	if !needed {
		return User{}, "", ErrSetupDone
	}
	s.setup.mu.Lock()
	expected := s.setup.code
	s.setup.mu.Unlock()
	if expected == "" || subtle.ConstantTimeCompare([]byte(normalizeSetupCode(code)), []byte(expected)) != 1 {
		return User{}, "", ErrInvalidSetupCode
	}
	u, err := s.CreateUser(ctx, email, password)
	if err != nil {
		return User{}, "", err
	}
	s.setup.mu.Lock()
	s.setup.code = ""
	s.setup.mu.Unlock()
	_, token, err := s.Login(ctx, u.Email, password, "")
	return u, token, err
}

// newSetupCode genera 8 caracteres en formato XXXX-XXXX.
func newSetupCode() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, 0, 9)
	for i, v := range b {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, setupAlphabet[int(v)%len(setupAlphabet)])
	}
	return string(out), nil
}

// normalizeSetupCode acepta el código con o sin guion, espacios o minúsculas.
func normalizeSetupCode(code string) string {
	code = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
	if len(code) != 8 {
		return code
	}
	return code[:4] + "-" + code[4:]
}

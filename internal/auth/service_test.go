package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/db"
)

func newService(t *testing.T) *Service {
	t.Helper()
	sqlDB, err := db.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return NewService(sqlDB, []byte("0123456789abcdef0123456789abcdef"))
}

func TestHashAndVerify(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(h, "correct horse battery") {
		t.Fatal("contraseña correcta rechazada")
	}
	if VerifyPassword(h, "wrong") {
		t.Fatal("contraseña incorrecta aceptada")
	}
	if VerifyPassword("$argon2id$basura", "x") {
		t.Fatal("hash inválido aceptado")
	}
}

func TestCreateUserValidation(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "no-es-email", "contraseña-larga"); !errors.Is(err, ErrInvalidEmail) {
		t.Fatalf("err = %v, want ErrInvalidEmail", err)
	}
	if _, err := s.CreateUser(ctx, "a@b.com", "corta"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("err = %v, want ErrWeakPassword", err)
	}
	if _, err := s.CreateUser(ctx, "  A@B.com ", "contraseña-larga"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "a@b.com", "contraseña-larga"); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("err = %v, want ErrEmailTaken", err)
	}
}

func TestLoginSessionLogout(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "a@b.com", "contraseña-larga"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.Login(ctx, "a@b.com", "mal", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
	if _, _, err := s.Login(ctx, "nadie@b.com", "mal", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}

	u, token, err := s.Login(ctx, "A@B.COM", "contraseña-larga", "")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "a@b.com" || token == "" {
		t.Fatalf("login = %+v %q", u, token)
	}

	got, err := s.Authenticate(ctx, token)
	if err != nil || got.ID != u.ID {
		t.Fatalf("authenticate = %+v, %v", got, err)
	}
	if _, err := s.Authenticate(ctx, token+"x"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("token alterado: err = %v, want ErrNoSession", err)
	}

	// Expirada
	s.now = func() time.Time { return time.Now().Add(SessionTTL + time.Minute) }
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expirada: err = %v, want ErrNoSession", err)
	}
	s.now = time.Now

	if err := s.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("tras logout: err = %v, want ErrNoSession", err)
	}
}

func TestSessionIDDependsOnSecret(t *testing.T) {
	a := NewService(nil, []byte("secreto-a"))
	b := NewService(nil, []byte("secreto-b"))
	if a.sessionID("tok") == b.sessionID("tok") {
		t.Fatal("el id de sesión debe depender del secreto")
	}
}

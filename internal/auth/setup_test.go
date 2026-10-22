package auth

import (
	"context"
	"errors"
	"regexp"
	"testing"
)

func TestSetupFlow(t *testing.T) {
	s := newService(t)
	ctx := context.Background()

	code, err := s.InitSetup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[A-HJ-KM-NP-Z2-9]{4}-[A-HJ-KM-NP-Z2-9]{4}$`).MatchString(code) {
		t.Fatalf("código = %q", code)
	}
	if needed, _ := s.SetupNeeded(ctx); !needed {
		t.Fatal("debería hacer falta setup")
	}

	if _, _, err := s.CompleteSetup(ctx, "AAAA-AAAA", "a@b.com", "contraseña-larga"); !errors.Is(err, ErrInvalidSetupCode) {
		t.Fatalf("código malo: %v", err)
	}
	if _, _, err := s.CompleteSetup(ctx, code, "a@b.com", "corta"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("contraseña débil: %v", err)
	}
	// Minúsculas, sin guion y con espacios se aceptan.
	loose := " " + string(code[0]|0x20) + code[1:4] + code[5:] + " "
	u, token, err := s.CompleteSetup(ctx, loose, "a@b.com", "contraseña-larga")
	if err != nil || u.Email != "a@b.com" || token == "" {
		t.Fatalf("setup = %+v %q %v", u, token, err)
	}
	if got, err := s.Authenticate(ctx, token); err != nil || got.ID != u.ID {
		t.Fatalf("sesión tras setup: %+v %v", got, err)
	}

	if needed, _ := s.SetupNeeded(ctx); needed {
		t.Fatal("ya no debería hacer falta setup")
	}
	if _, _, err := s.CompleteSetup(ctx, code, "otro@b.com", "contraseña-larga"); !errors.Is(err, ErrSetupDone) {
		t.Fatalf("segundo setup: %v", err)
	}
	// Reiniciar con usuarios no genera código.
	if code, _ := s.InitSetup(ctx); code != "" {
		t.Fatalf("código con usuarios = %q", code)
	}
}

package auth

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/crypto"
)

// Vectores del apéndice B de RFC 6238 (SHA-1), recortados a 6 dígitos.
func TestTOTPVectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for _, v := range []struct {
		unix int64
		want string
	}{{59, "287082"}, {1111111109, "081804"}, {1234567890, "005924"}, {2000000000, "279037"}} {
		if got := totpCode(secret, v.unix/totpPeriod); got != v.want {
			t.Fatalf("t=%d: %s, want %s", v.unix, got, v.want)
		}
	}
	now := time.Unix(1111111109, 0)
	if _, ok := totpMatch(secret, "081804", now.Add(29*time.Second)); !ok {
		t.Fatal("un paso de desfase debe valer")
	}
	if _, ok := totpMatch(secret, "081804", now.Add(2*time.Minute)); ok {
		t.Fatal("un código de hace dos minutos no debe valer")
	}
}

func TestTOTPFlow(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { return now }
	u, err := s.CreateUser(ctx, "a@b.com", "contraseña-larga")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginTOTP(ctx, u.ID); !errors.Is(err, ErrTOTPUnavailable) {
		t.Fatalf("sin caja = %v", err)
	}
	box, _ := crypto.NewBox(bytes.Repeat([]byte{7}, 32))
	s.UseBox(box)
	if _, err := s.ConfirmTOTP(ctx, u.ID, "123456", ""); !errors.Is(err, ErrTOTPNotStarted) {
		t.Fatalf("confirmar sin empezar = %v", err)
	}

	setup, err := s.BeginTOTP(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(setup.URL)
	if parsed.Scheme != "otpauth" || parsed.Query().Get("secret") != setup.Secret || !strings.Contains(setup.URL, "CloudMesh") {
		t.Fatalf("url = %s", setup.URL)
	}
	secret, err := b32.DecodeString(setup.Secret)
	if err != nil {
		t.Fatal(err)
	}
	code := func(at time.Time) string { return totpCode(secret, at.Unix()/totpPeriod) }

	// Pendiente: el login todavía no pide código.
	if _, _, err := s.Login(ctx, "a@b.com", "contraseña-larga", ""); err != nil {
		t.Fatalf("login con 2FA pendiente = %v", err)
	}
	if _, err := s.ConfirmTOTP(ctx, u.ID, "000000", ""); !errors.Is(err, ErrInvalidTOTP) {
		t.Fatalf("código malo = %v", err)
	}
	recovery, err := s.ConfirmTOTP(ctx, u.ID, code(now), "")
	if err != nil || len(recovery) != recoveryCount {
		t.Fatalf("activar = %v %v", recovery, err)
	}
	if _, err := s.BeginTOTP(ctx, u.ID); !errors.Is(err, ErrTOTPEnabled) {
		t.Fatalf("empezar con 2FA activa = %v", err)
	}

	// Activa: hace falta el código y no vale reutilizarlo.
	if _, _, err := s.Login(ctx, "a@b.com", "contraseña-larga", ""); !errors.Is(err, ErrTOTPRequired) {
		t.Fatalf("sin código = %v", err)
	}
	if _, _, err := s.Login(ctx, "a@b.com", "mal", code(now)); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("contraseña mala = %v", err)
	}
	if _, _, err := s.Login(ctx, "a@b.com", "contraseña-larga", code(now)); !errors.Is(err, ErrInvalidTOTP) {
		t.Fatalf("código ya usado al activar = %v", err)
	}
	now = now.Add(30 * time.Second)
	usr, token, err := s.Login(ctx, "a@b.com", "contraseña-larga", code(now))
	if err != nil || !usr.TOTPEnabled {
		t.Fatalf("login con código = %+v %v", usr, err)
	}
	if me, _ := s.Authenticate(ctx, token); !me.TOTPEnabled {
		t.Fatalf("me = %+v", me)
	}

	// Desactivar exige contraseña y un código nuevo.
	now = now.Add(30 * time.Second)
	if err := s.DisableTOTP(ctx, u.ID, "mal", code(now)); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("desactivar con contraseña mala = %v", err)
	}
	if err := s.DisableTOTP(ctx, u.ID, "contraseña-larga", ""); !errors.Is(err, ErrTOTPRequired) {
		t.Fatalf("desactivar sin código = %v", err)
	}
	if err := s.DisableTOTP(ctx, u.ID, "contraseña-larga", code(now)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Login(ctx, "a@b.com", "contraseña-larga", ""); err != nil {
		t.Fatalf("login tras desactivar = %v", err)
	}

	// Reset de emergencia (CLI o desde otro usuario).
	if _, err := s.BeginTOTP(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetTOTPByEmail(ctx, "A@b.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetTOTPByEmail(ctx, "nadie@b.com"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("reset inexistente = %v", err)
	}
}

func TestRecoveryCodes(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	s.now = func() time.Time { return now }
	box, _ := crypto.NewBox(bytes.Repeat([]byte{7}, 32))
	s.UseBox(box)
	u, err := s.CreateUser(ctx, "r@b.com", "contraseña-larga")
	if err != nil {
		t.Fatal(err)
	}
	setup, _ := s.BeginTOTP(ctx, u.ID)
	secret, _ := b32.DecodeString(setup.Secret)
	code := func() string { now = now.Add(30 * time.Second); return totpCode(secret, now.Unix()/totpPeriod) }
	codes, err := s.ConfirmTOTP(ctx, u.ID, code(), "")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != recoveryLen+2 || seen[c] {
			t.Fatalf("código mal formado o repetido: %q", c)
		}
		seen[c] = true
	}

	// Un código de recuperación entra en lugar del de la app, en mayúsculas y sin guiones también.
	usr, token, err := s.Login(ctx, "r@b.com", "contraseña-larga", strings.ToUpper(strings.ReplaceAll(codes[0], "-", "")))
	if err != nil || usr.RecoveryCodesLeft != recoveryCount-1 {
		t.Fatalf("login con recuperación = %+v %v", usr, err)
	}
	if me, _ := s.Authenticate(ctx, token); me.RecoveryCodesLeft != recoveryCount-1 {
		t.Fatalf("me = %+v", me)
	}
	// Solo sirve una vez, y uno inventado no vale.
	if _, _, err := s.Login(ctx, "r@b.com", "contraseña-larga", codes[0]); !errors.Is(err, ErrInvalidTOTP) {
		t.Fatalf("reutilizar = %v", err)
	}
	if _, _, err := s.Login(ctx, "r@b.com", "contraseña-larga", "aaaa-aaaa-aaaa"); !errors.Is(err, ErrInvalidTOTP) {
		t.Fatalf("inventado = %v", err)
	}

	// Regenerar pide contraseña y código de la app; los anteriores dejan de valer.
	if _, err := s.RegenerateRecoveryCodes(ctx, u.ID, "mal", code()); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("regenerar con contraseña mala = %v", err)
	}
	fresh, err := s.RegenerateRecoveryCodes(ctx, u.ID, "contraseña-larga", code())
	if err != nil || len(fresh) != recoveryCount {
		t.Fatalf("regenerar = %v %v", fresh, err)
	}
	if _, _, err := s.Login(ctx, "r@b.com", "contraseña-larga", codes[1]); !errors.Is(err, ErrInvalidTOTP) {
		t.Fatalf("código viejo tras regenerar = %v", err)
	}

	// Quien perdió el móvil desactiva la 2FA con un código de recuperación; se van todos.
	if err := s.DisableTOTP(ctx, u.ID, "contraseña-larga", fresh[0]); err != nil {
		t.Fatal(err)
	}
	usr, _, err = s.Login(ctx, "r@b.com", "contraseña-larga", "")
	if err != nil || usr.RecoveryCodesLeft != 0 {
		t.Fatalf("tras desactivar = %+v %v", usr, err)
	}
}

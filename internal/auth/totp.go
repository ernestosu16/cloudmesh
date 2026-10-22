package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/crypto"
	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
)

// TOTP según RFC 6238 con los parámetros que entienden todas las apps de
// autenticación: SHA-1, 6 dígitos, pasos de 30 s. Se acepta un paso de
// desfase en cada sentido.
const (
	totpPeriod = 30
	totpDigits = 6
	totpSkew   = 1
)

var (
	ErrTOTPRequired    = errors.New("falta el código de verificación")
	ErrInvalidTOTP     = errors.New("el código de verificación no es válido")
	ErrTOTPEnabled     = errors.New("la verificación en dos pasos ya está activa")
	ErrTOTPNotStarted  = errors.New("primero genera el código QR")
	ErrTOTPUnavailable = errors.New("verificación en dos pasos no disponible en esta instancia")
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// UseBox fija la caja con la que se cifran los secretos TOTP (clave maestra,
// así sobreviven a un restore). Sin caja la 2FA no se puede activar.
func (s *Service) UseBox(box *crypto.Box) { s.box = box }

// TOTPSetup es lo que se muestra para escanear o teclear en la app.
type TOTPSetup struct {
	Secret string `json:"secret"` // base32, para teclearlo
	URL    string `json:"url"`    // otpauth://, para el QR
}

// BeginTOTP genera un secreto nuevo pendiente de confirmar. Repetirla antes de
// confirmar reemplaza el anterior.
func (s *Service) BeginTOTP(ctx context.Context, userID int64) (TOTPSetup, error) {
	if s.box == nil {
		return TOTPSetup{}, ErrTOTPUnavailable
	}
	u, err := s.userRow(ctx, userID)
	if err != nil {
		return TOTPSetup{}, err
	}
	if u.TotpEnabled != 0 {
		return TOTPSetup{}, ErrTOTPEnabled
	}
	secret := make([]byte, 20)
	if _, err := rand.Read(secret); err != nil {
		return TOTPSetup{}, err
	}
	enc, err := s.box.Seal(secret)
	if err != nil {
		return TOTPSetup{}, err
	}
	if _, err := s.q.SetUserTOTP(ctx, sqlcgen.SetUserTOTPParams{TotpSecret: enc, TotpEnabled: 0, ID: userID}); err != nil {
		return TOTPSetup{}, err
	}
	return TOTPSetup{Secret: b32.EncodeToString(secret), URL: totpURL(u.Email, secret)}, nil
}

// ConfirmTOTP activa la 2FA si el código corresponde al secreto pendiente,
// cierra las demás sesiones del usuario y devuelve sus códigos de
// recuperación, que solo se muestran esta vez.
func (s *Service) ConfirmTOTP(ctx context.Context, userID int64, code, keepToken string) ([]string, error) {
	u, err := s.userRow(ctx, userID)
	if err != nil {
		return nil, err
	}
	if u.TotpEnabled != 0 {
		return nil, ErrTOTPEnabled
	}
	if len(u.TotpSecret) == 0 {
		return nil, ErrTOTPNotStarted
	}
	if err := s.checkTOTP(ctx, u, code); err != nil {
		return nil, err
	}
	if _, err := s.q.EnableUserTOTP(ctx, userID); err != nil {
		return nil, err
	}
	codes, err := s.issueRecovery(ctx, userID)
	if err != nil {
		return nil, err
	}
	return codes, s.LogoutOthers(ctx, userID, keepToken)
}

// DisableTOTP quita la 2FA propia; pide la contraseña y un código vigente de
// la app o de recuperación (para quien entró con uno tras perder el móvil).
func (s *Service) DisableTOTP(ctx context.Context, userID int64, password, code string) error {
	u, err := s.userRow(ctx, userID)
	if err != nil {
		return err
	}
	if !VerifyPassword(u.PasswordHash, password) {
		return ErrWrongPassword
	}
	if u.TotpEnabled != 0 {
		if err := s.checkSecondFactor(ctx, u, code); err != nil {
			return err
		}
	}
	return s.ResetTOTP(ctx, userID)
}

// ResetTOTP quita la 2FA sin comprobaciones: la usa otro usuario desde la app
// o `cloudmesh reset-2fa` cuando alguien pierde su móvil.
func (s *Service) ResetTOTP(ctx context.Context, userID int64) error {
	n, err := s.q.SetUserTOTP(ctx, sqlcgen.SetUserTOTPParams{TotpSecret: []byte{}, TotpEnabled: 0, ID: userID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// ResetTOTPByEmail es ResetTOTP para la CLI.
func (s *Service) ResetTOTPByEmail(ctx context.Context, email string) error {
	id, err := s.q.GetUserIDByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return err
	}
	return s.ResetTOTP(ctx, id)
}

// checkTOTP valida el código y lo consume: el mismo paso no vale dos veces.
func (s *Service) checkTOTP(ctx context.Context, u sqlcgen.User, code string) error {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if code == "" {
		return ErrTOTPRequired
	}
	if s.box == nil {
		return ErrTOTPUnavailable
	}
	secret, err := s.box.Open(u.TotpSecret)
	if err != nil {
		return fmt.Errorf("descifrar secreto TOTP: %w", err)
	}
	step, ok := totpMatch(secret, code, s.now())
	if !ok || step <= u.TotpLastStep {
		return ErrInvalidTOTP
	}
	n, err := s.q.UseTOTPStep(ctx, sqlcgen.UseTOTPStepParams{TotpLastStep: step, ID: u.ID, TotpLastStep_2: step})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrInvalidTOTP // otra petición usó este código a la vez
	}
	return nil
}

func (s *Service) userRow(ctx context.Context, id int64) (sqlcgen.User, error) {
	u, err := s.q.GetUserWithHash(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return sqlcgen.User{}, ErrUserNotFound
	}
	return u, err
}

// totpCode calcula el código de un paso (HOTP con contador = paso).
func totpCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	n := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, n%1_000_000)
}

// totpMatch busca el paso cuyo código coincide, dentro del desfase admitido.
func totpMatch(secret []byte, code string, now time.Time) (int64, bool) {
	if len(code) != totpDigits {
		return 0, false
	}
	cur := now.Unix() / totpPeriod
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		if subtle.ConstantTimeCompare([]byte(totpCode(secret, cur+d)), []byte(code)) == 1 {
			return cur + d, true
		}
	}
	return 0, false
}

func totpURL(account string, secret []byte) string {
	v := url.Values{}
	v.Set("secret", b32.EncodeToString(secret))
	v.Set("issuer", "CloudMesh")
	v.Set("algorithm", "SHA1")
	v.Set("digits", fmt.Sprint(totpDigits))
	v.Set("period", fmt.Sprint(totpPeriod))
	return "otpauth://totp/" + url.PathEscape("CloudMesh:"+account) + "?" + v.Encode()
}

// Códigos de recuperación: diez de un solo uso que se muestran al activar la
// 2FA y sustituyen al código de la app si se pierde el móvil. 12 caracteres
// de un alfabeto sin confusiones (≈59 bits); se guarda su SHA-256 atado al
// usuario, no el código.
const (
	recoveryCount    = 10
	recoveryLen      = 12
	recoveryAlphabet = "23456789abcdefghjkmnpqrstuvwxyz"
)

var ErrRecoveryCodes = errors.New("no se pudieron generar los códigos de recuperación")

// RegenerateRecoveryCodes invalida los códigos anteriores y devuelve diez
// nuevos. Pide la contraseña y un código de la app.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, userID int64, password, code string) ([]string, error) {
	u, err := s.userRow(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !VerifyPassword(u.PasswordHash, password) {
		return nil, ErrWrongPassword
	}
	if u.TotpEnabled == 0 {
		return nil, ErrTOTPNotStarted
	}
	if err := s.checkTOTP(ctx, u, code); err != nil {
		return nil, err
	}
	return s.issueRecovery(ctx, userID)
}

func (s *Service) issueRecovery(ctx context.Context, userID int64) ([]string, error) {
	codes := make([]string, recoveryCount)
	hashes := make([]string, recoveryCount)
	for i := range codes {
		raw := make([]byte, recoveryLen)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		for j, b := range raw {
			// 256 % 31 sesga un poco los primeros símbolos; con 59 bits no importa.
			raw[j] = recoveryAlphabet[int(b)%len(recoveryAlphabet)]
		}
		c := string(raw)
		codes[i] = c[:4] + "-" + c[4:8] + "-" + c[8:]
		hashes[i] = recoveryHash(userID, c)
	}
	raw, err := json.Marshal(hashes)
	if err != nil {
		return nil, err
	}
	n, err := s.q.SetRecoveryCodes(ctx, sqlcgen.SetRecoveryCodesParams{RecoveryCodes: string(raw), ID: userID})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrRecoveryCodes
	}
	return codes, nil
}

func recoveryHash(userID int64, code string) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "cloudmesh/recovery/v1/%d/%s", userID, code))
	return hex.EncodeToString(sum[:])
}

// normalizeRecovery quita guiones y espacios y pasa a minúsculas.
func normalizeRecovery(code string) string {
	return strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
}

// isRecoveryCode distingue un código de recuperación de uno de la app (6 cifras).
func isRecoveryCode(code string) bool { return len(normalizeRecovery(code)) == recoveryLen }

// useRecovery consume un código de recuperación. Dos peticiones con el mismo
// código a la vez: solo una cambia la lista que leyó.
func (s *Service) useRecovery(ctx context.Context, u sqlcgen.User, code string) error {
	var hashes []string
	if err := json.Unmarshal([]byte(u.RecoveryCodes), &hashes); err != nil {
		return ErrInvalidTOTP
	}
	want := recoveryHash(u.ID, normalizeRecovery(code))
	for i, h := range hashes {
		if subtle.ConstantTimeCompare([]byte(h), []byte(want)) != 1 {
			continue
		}
		rest, err := json.Marshal(append(hashes[:i:i], hashes[i+1:]...))
		if err != nil {
			return err
		}
		n, err := s.q.UseRecoveryCode(ctx, sqlcgen.UseRecoveryCodeParams{RecoveryCodes: string(rest), ID: u.ID, RecoveryCodes_2: u.RecoveryCodes})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrInvalidTOTP
		}
		return nil
	}
	return ErrInvalidTOTP
}

// checkSecondFactor acepta un código de la app o uno de recuperación.
func (s *Service) checkSecondFactor(ctx context.Context, u sqlcgen.User, code string) error {
	if isRecoveryCode(code) {
		return s.useRecovery(ctx, u, code)
	}
	return s.checkTOTP(ctx, u, code)
}

// recoveryLeft cuenta los códigos sin usar.
func recoveryLeft(raw string) int64 {
	var hashes []string
	_ = json.Unmarshal([]byte(raw), &hashes)
	return int64(len(hashes))
}

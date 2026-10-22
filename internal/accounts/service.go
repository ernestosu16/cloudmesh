// Package accounts gestiona las cuentas de nube conectadas: alta con
// contraseña (y 2FA), credenciales cifradas, cuota y sesiones vivas.
package accounts

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/crypto"
	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
	"github.com/hirasolutions/cloudmesh/internal/storage"
)

const (
	StatusActive            = "active"
	StatusReconnectRequired = "reconnect_required"
)

// Cuánto puede llenar CloudMesh de cada cuenta.
const (
	ContribAll     = "all"     // toda la cuenta
	ContribReserve = "reserve" // dejar contrib_value % libre
	ContribLimit   = "limit"   // no pasar de contrib_value bytes usados
)

// testedTTL es cuánto se guarda la sesión de «Probar conexión» para el alta.
const testedTTL = 5 * time.Minute

var (
	ErrNotFound            = errors.New("cuenta no encontrada")
	ErrUnknownProvider     = errors.New("proveedor desconocido")
	ErrAlreadyLinked       = errors.New("esa cuenta ya está conectada")
	ErrInvalidContribution = errors.New("aporte inválido: deja entre 1 y 90 % libre o un límite de al menos 1 GiB")
)

// Account es la vista pública (sin credenciales).
type Account struct {
	ID       int64  `json:"id"`
	Provider string `json:"provider"`
	Label    string `json:"label"`
	Email    string `json:"email"`
	Status   string `json:"status"`
	Enabled  bool   `json:"enabled"`
	Priority int64  `json:"priority"`
	// QuotaUsed son los bytes que CloudMesh ocupa en la cuenta (bloques sin faltar).
	QuotaUsed int64 `json:"quota_used"`
	// ProviderUsed es lo que ocupa la cuenta entera: lo ajeno al último sondeo más los bloques propios.
	ProviderUsed int64 `json:"provider_used"`
	QuotaTotal   int64 `json:"quota_total"`
	// Capacity es hasta dónde puede llenar CloudMesh la cuenta según el aporte.
	Capacity int64 `json:"capacity"`
	// Occupied es la parte del aporte que ya no admite subidas: lo propio o,
	// si es más, lo que no cabe porque la cuenta real está llena de lo ajeno.
	Occupied     int64  `json:"occupied"`
	ContribMode  string `json:"contrib_mode"`
	ContribValue int64  `json:"contrib_value"`
	LastError    string `json:"last_error,omitempty"`
	LastSyncAt   int64  `json:"last_sync_at"`
	CreatedAt    int64  `json:"created_at"`
	// ThrottledUntil > 0: el proveedor rechazó descargas por límite de
	// transferencia y hasta entonces se evita leer de la cuenta (unix).
	ThrottledUntil int64 `json:"throttled_until,omitempty"`
}

// Free es cuánto puede recibir todavía la cuenta: lo que queda del aporte y
// lo que cabe en la cuenta real del proveedor, lo menor.
func (a Account) Free() int64 {
	return min(a.Capacity-a.QuotaUsed, a.QuotaTotal-a.ProviderUsed)
}

// Throttled dice si la cuenta estaba apartada por límite de transferencia al leerla.
func (a Account) Throttled() bool { return a.ThrottledUntil > 0 }

// Summary agrega la cuota de todas las cuentas.
type Summary struct {
	Used     int64     `json:"used"`
	Total    int64     `json:"total"`
	Accounts []Account `json:"accounts"`
}

// Settings son los campos editables por el usuario. ContribMode vacío
// conserva el aporte actual.
type Settings struct {
	Label        string `json:"label"`
	Priority     int64  `json:"priority"`
	Enabled      bool   `json:"enabled"`
	ContribMode  string `json:"contrib_mode"`
	ContribValue int64  `json:"contrib_value"`
}

// TestResult es lo que devuelve «Probar conexión».
type TestResult struct {
	Email         string `json:"email"`
	QuotaUsed     int64  `json:"quota_used"`
	QuotaTotal    int64  `json:"quota_total"`
	AlreadyLinked bool   `json:"already_linked"`
}

// ValidateContribution comprueba un modo de aporte y su valor.
func ValidateContribution(mode string, value int64) error {
	switch mode {
	case ContribAll:
		return nil
	case ContribReserve:
		if value >= 1 && value <= 90 {
			return nil
		}
	case ContribLimit:
		if value >= 1<<30 {
			return nil
		}
	}
	return ErrInvalidContribution
}

// capacity aplica el aporte al total de la cuenta.
func capacity(total int64, mode string, value int64) int64 {
	switch mode {
	case ContribReserve:
		return total * (100 - value) / 100
	case ContribLimit:
		return min(total, value)
	}
	return total
}

// tested es una sesión abierta por Test a la espera del alta.
type tested struct {
	sess storage.Session
	pass [32]byte
	at   time.Time
}

// Service orquesta proveedores, base y sesiones en memoria.
type Service struct {
	q         *sqlcgen.Queries
	box       *crypto.Box
	providers map[string]storage.Provider
	log       *slog.Logger
	now       func() time.Time

	mu        sync.Mutex
	sessions  map[int64]storage.Session
	tested    map[string]tested
	throttled map[int64]time.Time // límite de transferencia: hasta cuándo evitar leer
}

// TransferCooldown es cuánto se evita leer de una cuenta tras chocar con su
// límite de transferencia. Mega no dice cuándo se renueva (la cuota gratuita
// es una ventana móvil de horas): pasado este tiempo se vuelve a probar y, si
// sigue limitada, se aparta otra vez. Vive en memoria: no es parte del índice
// y un reinicio solo provoca un intento más.
const TransferCooldown = time.Hour

func NewService(sqlDB *sql.DB, box *crypto.Box, log *slog.Logger, providers ...storage.Provider) *Service {
	s := &Service{
		q:         sqlcgen.New(sqlDB),
		box:       box,
		providers: map[string]storage.Provider{},
		log:       log,
		now:       time.Now,
		sessions:  map[int64]storage.Session{},
		tested:    map[string]tested{},
		throttled: map[int64]time.Time{},
	}
	for _, p := range providers {
		s.providers[p.Name()] = p
	}
	return s
}

// Providers lista los nombres de proveedor disponibles, ordenados.
func (s *Service) Providers() []string {
	out := make([]string, 0, len(s.providers))
	for name := range s.providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Connect da de alta una cuenta. Devuelve storage.ErrMFARequired si el
// proveedor pide código y no se envió.
func (s *Service) Connect(ctx context.Context, provider, label, email, password, mfa string) (Account, error) {
	p, ok := s.providers[provider]
	if !ok {
		return Account{}, ErrUnknownProvider
	}
	sess, ok := s.takeTested(provider, email, password)
	if !ok {
		var err error
		if sess, err = p.Login(ctx, email, password, mfa); err != nil {
			return Account{}, err
		}
	}
	enc, rootID, quota, err := s.prepare(ctx, sess)
	if err != nil {
		sess.Close()
		return Account{}, err
	}
	if strings.TrimSpace(label) == "" {
		label = sess.Email()
	}
	row, err := s.q.CreateAccount(ctx, sqlcgen.CreateAccountParams{
		Provider:       provider,
		Label:          label,
		Email:          sess.Email(),
		EncCredentials: enc,
		RootRemoteID:   rootID,
		QuotaUsed:      quota.Used,
		ForeignUsed:    quota.Used, // cuenta nueva: nada es todavía de CloudMesh
		QuotaTotal:     quota.Total,
		LastSyncAt:     s.now().Unix(),
		CreatedAt:      s.now().Unix(),
	})
	if err != nil {
		sess.Close()
		if strings.Contains(err.Error(), "UNIQUE") {
			return Account{}, ErrAlreadyLinked
		}
		return Account{}, err
	}
	s.putSession(row.ID, sess)
	return s.toAccount(row, 0), nil
}

// Test abre sesión con las credenciales sin dar de alta la cuenta y devuelve
// su cuota. La sesión se guarda unos minutos: si después se conecta la misma
// cuenta con la misma contraseña, Connect la reutiliza en vez de volver a
// iniciar sesión (un código 2FA no sirve dos veces).
func (s *Service) Test(ctx context.Context, provider, email, password, mfa string) (TestResult, error) {
	p, ok := s.providers[provider]
	if !ok {
		return TestResult{}, ErrUnknownProvider
	}
	sess, err := p.Login(ctx, email, password, mfa)
	if err != nil {
		return TestResult{}, err
	}
	quota, err := sess.Quota(ctx)
	if err != nil {
		sess.Close()
		return TestResult{}, err
	}
	out := TestResult{Email: sess.Email(), QuotaUsed: quota.Used, QuotaTotal: quota.Total}
	accs, err := s.q.ListAccounts(ctx)
	if err != nil {
		sess.Close()
		return TestResult{}, err
	}
	for _, a := range accs {
		if a.Provider == provider && strings.EqualFold(a.Email, out.Email) {
			out.AlreadyLinked = true
		}
	}
	if out.AlreadyLinked {
		sess.Close()
		return out, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireTestedLocked()
	key := testedKey(provider, email)
	if old, ok := s.tested[key]; ok {
		old.sess.Close()
	}
	s.tested[key] = tested{sess: sess, pass: sha256.Sum256([]byte(password)), at: s.now()}
	return out, nil
}

// takeTested devuelve la sesión de un Test reciente con esas credenciales.
func (s *Service) takeTested(provider, email, password string) (storage.Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireTestedLocked()
	key := testedKey(provider, email)
	t, ok := s.tested[key]
	if !ok {
		return nil, false
	}
	pass := sha256.Sum256([]byte(password))
	if subtle.ConstantTimeCompare(t.pass[:], pass[:]) != 1 {
		return nil, false
	}
	delete(s.tested, key)
	return t.sess, true
}

func (s *Service) expireTestedLocked() {
	for k, t := range s.tested {
		if s.now().Sub(t.at) > testedTTL {
			t.sess.Close()
			delete(s.tested, k)
		}
	}
}

func testedKey(provider, email string) string {
	return provider + "\x00" + strings.ToLower(strings.TrimSpace(email))
}

// Reconnect renueva las credenciales de una cuenta existente con contraseña.
func (s *Service) Reconnect(ctx context.Context, id int64, password, mfa string) (Account, error) {
	row, err := s.get(ctx, id)
	if err != nil {
		return Account{}, err
	}
	p := s.providers[row.Provider]
	sess, err := p.Login(ctx, row.Email, password, mfa)
	if err != nil {
		return Account{}, err
	}
	if !strings.EqualFold(sess.Email(), row.Email) {
		sess.Close()
		return Account{}, fmt.Errorf("%w: la sesión pertenece a %s", storage.ErrInvalidCredentials, sess.Email())
	}
	enc, rootID, quota, err := s.prepare(ctx, sess)
	if err != nil {
		sess.Close()
		return Account{}, err
	}
	if err := s.q.UpdateAccountCredentials(ctx, sqlcgen.UpdateAccountCredentialsParams{
		EncCredentials: enc, RootRemoteID: rootID, ID: id,
	}); err != nil {
		sess.Close()
		return Account{}, err
	}
	if err := s.saveQuota(ctx, id, quota); err != nil {
		sess.Close()
		return Account{}, err
	}
	s.putSession(id, sess)
	return s.Get(ctx, id)
}

// prepare cifra credenciales, asegura la raíz y lee la cuota de una sesión nueva.
func (s *Service) prepare(ctx context.Context, sess storage.Session) (enc []byte, rootID string, quota storage.Quota, err error) {
	if enc, err = s.box.Seal(sess.Credentials()); err != nil {
		return
	}
	if rootID, err = sess.EnsureRoot(ctx); err != nil {
		return
	}
	quota, err = sess.Quota(ctx)
	return
}

func (s *Service) List(ctx context.Context) ([]Account, error) {
	rows, err := s.q.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	used, err := s.usedBytes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.toAccount(r, used[r.ID]))
	}
	return out, nil
}

// usedBytes suma por cuenta los bytes de bloques de CloudMesh que siguen en ella.
func (s *Service) usedBytes(ctx context.Context) (map[int64]int64, error) {
	rows, err := s.q.AccountChunkBytes(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]int64, len(rows))
	for _, r := range rows {
		out[r.AccountID.Int64] = r.Used
	}
	return out, nil
}

// saveQuota guarda la cuota que dio el proveedor y separa lo ajeno a CloudMesh
// (lo que el proveedor cuenta menos los bloques propios en este momento).
func (s *Service) saveQuota(ctx context.Context, id int64, quota storage.Quota) error {
	used, err := s.usedBytes(ctx)
	if err != nil {
		return err
	}
	return s.q.UpdateAccountQuota(ctx, sqlcgen.UpdateAccountQuotaParams{
		QuotaUsed: quota.Used, ForeignUsed: max(quota.Used-used[id], 0), QuotaTotal: quota.Total,
		LastSyncAt: s.now().Unix(), ID: id,
	})
}

// view devuelve la cuenta con su uso propio ya calculado.
func (s *Service) view(ctx context.Context, r sqlcgen.Account) (Account, error) {
	used, err := s.usedBytes(ctx)
	if err != nil {
		return Account{}, err
	}
	return s.toAccount(r, used[r.ID]), nil
}

// Summarize devuelve la cuota agregada y las cuentas.
func (s *Service) Summarize(ctx context.Context) (Summary, error) {
	accs, err := s.List(ctx)
	if err != nil {
		return Summary{}, err
	}
	sum := Summary{Accounts: accs}
	for _, a := range accs {
		sum.Used += a.Occupied
		sum.Total += a.Capacity
	}
	return sum, nil
}

func (s *Service) Get(ctx context.Context, id int64) (Account, error) {
	row, err := s.get(ctx, id)
	if err != nil {
		return Account{}, err
	}
	return s.view(ctx, row)
}

func (s *Service) UpdateSettings(ctx context.Context, id int64, in Settings) (Account, error) {
	if strings.TrimSpace(in.Label) == "" {
		return Account{}, errors.New("la etiqueta no puede estar vacía")
	}
	if in.ContribMode == "" {
		cur, err := s.get(ctx, id)
		if err != nil {
			return Account{}, err
		}
		in.ContribMode, in.ContribValue = cur.ContribMode, cur.ContribValue
	}
	if in.ContribMode == ContribAll {
		in.ContribValue = 0
	}
	if err := ValidateContribution(in.ContribMode, in.ContribValue); err != nil {
		return Account{}, err
	}
	row, err := s.q.UpdateAccountSettings(ctx, sqlcgen.UpdateAccountSettingsParams{
		Label: in.Label, Priority: in.Priority, Enabled: boolToInt(in.Enabled),
		ContribMode: in.ContribMode, ContribValue: in.ContribValue, ID: id,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, err
	}
	return s.view(ctx, row)
}

// Delete borra la cuenta y cierra su sesión. Los archivos en el proveedor se conservan.
func (s *Service) Delete(ctx context.Context, id int64) error {
	n, err := s.q.DeleteAccount(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	s.mu.Lock()
	if sess, ok := s.sessions[id]; ok {
		sess.Close()
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	return nil
}

// RefreshQuota consulta la cuota al proveedor y la guarda. Si la sesión ya no
// vale, marca la cuenta como reconnect_required y devuelve ErrSessionExpired.
func (s *Service) RefreshQuota(ctx context.Context, id int64) (Account, error) {
	sess, err := s.Session(ctx, id)
	if err != nil {
		return Account{}, err
	}
	quota, err := sess.Quota(ctx)
	if err != nil {
		return Account{}, s.handleSessionError(ctx, id, err)
	}
	if err := s.saveQuota(ctx, id, quota); err != nil {
		return Account{}, err
	}
	return s.Get(ctx, id)
}

// RootRemoteID devuelve el id de la carpeta cloudmesh/ en el proveedor.
func (s *Service) RootRemoteID(ctx context.Context, id int64) (string, error) {
	row, err := s.get(ctx, id)
	if err != nil {
		return "", err
	}
	return row.RootRemoteID, nil
}

// Session devuelve la sesión viva de la cuenta, reanudándola si hace falta.
func (s *Service) Session(ctx context.Context, id int64) (storage.Session, error) {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	s.mu.Unlock()
	if ok {
		return sess, nil
	}
	row, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if row.Status != StatusActive {
		return nil, storage.ErrSessionExpired
	}
	raw, err := s.box.Open(row.EncCredentials)
	if err != nil {
		return nil, fmt.Errorf("descifrar credenciales de la cuenta %d: %w", id, err)
	}
	sess, err = s.providers[row.Provider].Resume(ctx, raw)
	if err != nil {
		return nil, s.handleSessionError(ctx, id, err)
	}
	return s.putSession(id, sess), nil
}

// putSession guarda la sesión; si otra goroutine ganó, cierra la nueva y devuelve la existente.
func (s *Service) putSession(id int64, sess storage.Session) storage.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.sessions[id]; ok && old != sess {
		sess.Close()
		return old
	}
	s.sessions[id] = sess
	return sess
}

// HandleSessionError marca la cuenta como reconnect_required ante sesión
// expirada o bloqueo, o la aparta un rato ante límite de transferencia, y
// devuelve el mismo error. Para usar desde otros paquetes que operan con la sesión.
func (s *Service) HandleSessionError(ctx context.Context, id int64, err error) error {
	return s.handleSessionError(ctx, id, err)
}

// Throttled dice si la cuenta está apartada por límite de transferencia.
func (s *Service) Throttled(id int64) bool { return s.throttledUntil(id) > 0 }

func (s *Service) throttledUntil(id int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.throttled[id]
	if !ok {
		return 0
	}
	if !s.now().Before(until) {
		delete(s.throttled, id)
		return 0
	}
	return until.Unix()
}

// handleSessionError marca reconnect_required ante sesión expirada o bloqueo
// y aparta la cuenta un rato si el proveedor rechazó por límite de transferencia.
func (s *Service) handleSessionError(ctx context.Context, id int64, err error) error {
	if errors.Is(err, storage.ErrTransferQuota) {
		s.mu.Lock()
		first := s.throttled[id].IsZero()
		s.throttled[id] = s.now().Add(TransferCooldown)
		s.mu.Unlock()
		if first {
			s.log.Warn("cuenta con límite de transferencia: se evita leer de ella", "account", id, "for", TransferCooldown)
		}
		return err
	}
	if errors.Is(err, storage.ErrSessionExpired) || errors.Is(err, storage.ErrBlocked) {
		s.mu.Lock()
		if sess, ok := s.sessions[id]; ok {
			sess.Close()
			delete(s.sessions, id)
		}
		s.mu.Unlock()
		if uerr := s.q.SetAccountStatus(ctx, sqlcgen.SetAccountStatusParams{
			Status: StatusReconnectRequired, LastError: err.Error(), ID: id,
		}); uerr != nil {
			s.log.Error("marcar reconnect_required", "account", id, "err", uerr)
		}
	}
	return err
}

func (s *Service) get(ctx context.Context, id int64) (sqlcgen.Account, error) {
	row, err := s.q.GetAccount(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return sqlcgen.Account{}, ErrNotFound
	}
	return row, err
}

func (s *Service) toAccount(r sqlcgen.Account, used int64) Account {
	a := Account{
		ThrottledUntil: s.throttledUntil(r.ID),
		ID:             r.ID, Provider: r.Provider, Label: r.Label, Email: r.Email, Status: r.Status,
		Enabled: r.Enabled != 0, Priority: r.Priority, QuotaUsed: used, ProviderUsed: r.ForeignUsed + used, QuotaTotal: r.QuotaTotal,
		Capacity: capacity(r.QuotaTotal, r.ContribMode, r.ContribValue), ContribMode: r.ContribMode, ContribValue: r.ContribValue,
		LastError: r.LastError, LastSyncAt: r.LastSyncAt, CreatedAt: r.CreatedAt,
	}
	a.Occupied = min(max(a.Capacity-a.Free(), 0), a.Capacity)
	return a
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

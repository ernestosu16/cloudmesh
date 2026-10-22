package accounts

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/crypto"
	"github.com/hirasolutions/cloudmesh/internal/db"
	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
	"github.com/hirasolutions/cloudmesh/internal/storage"
	"github.com/hirasolutions/cloudmesh/internal/storage/fake"
)

func newService(t *testing.T) (*Service, *fake.Provider) {
	t.Helper()
	sqlDB, err := db.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	box, _ := crypto.NewBox(bytes.Repeat([]byte{1}, 32))
	p := fake.New()
	return NewService(sqlDB, box, slog.New(slog.DiscardHandler), p), p
}

func TestConnectAndList(t *testing.T) {
	s, p := newService(t)
	ctx := context.Background()

	if _, err := s.Connect(ctx, "nope", "", "a@x.com", "ok", ""); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.Connect(ctx, "fake", "", "a@x.com", "mal", ""); !errors.Is(err, storage.ErrInvalidCredentials) {
		t.Fatalf("err = %v", err)
	}

	acc, err := s.Connect(ctx, "fake", "", "a@x.com", "ok", "")
	if err != nil {
		t.Fatal(err)
	}
	if acc.Label != "a@x.com" || acc.Status != StatusActive || acc.QuotaUsed != 0 || acc.ProviderUsed != 10 || acc.QuotaTotal != 100 || !acc.Enabled {
		t.Fatalf("acc = %+v", acc)
	}
	if _, err := s.Connect(ctx, "fake", "", "a@x.com", "ok", ""); !errors.Is(err, ErrAlreadyLinked) {
		t.Fatalf("duplicada: err = %v", err)
	}

	// La sesión del connect se reutiliza: no se abre otra.
	opened := p.Opened
	if _, err := s.Session(ctx, acc.ID); err != nil {
		t.Fatal(err)
	}
	if p.Opened != opened {
		t.Fatalf("se abrió una sesión nueva: %d -> %d", opened, p.Opened)
	}

	list, err := s.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v, %v", list, err)
	}
	// Los 10 bytes ajenos del fake ocupan parte del aporte aunque CloudMesh no haya subido nada.
	sum, _ := s.Summarize(ctx)
	if sum.Used != 10 || sum.Total != 100 {
		t.Fatalf("sum = %+v", sum)
	}
}

func TestConnectMFA(t *testing.T) {
	s, p := newService(t)
	ctx := context.Background()
	p.MFAEmails["m@x.com"] = true

	if _, err := s.Connect(ctx, "fake", "", "m@x.com", "ok", ""); !errors.Is(err, storage.ErrMFARequired) {
		t.Fatalf("sin código: err = %v", err)
	}
	if _, err := s.Connect(ctx, "fake", "", "m@x.com", "ok", "000000"); !errors.Is(err, storage.ErrInvalidCredentials) {
		t.Fatalf("código malo: err = %v", err)
	}
	if _, err := s.Connect(ctx, "fake", "Mi Mega", "m@x.com", "ok", "123456"); err != nil {
		t.Fatal(err)
	}
}

func TestResumeExpiredMarksReconnect(t *testing.T) {
	s, p := newService(t)
	ctx := context.Background()
	acc, err := s.Connect(ctx, "fake", "", "a@x.com", "ok", "")
	if err != nil {
		t.Fatal(err)
	}

	p.Expire("a@x.com")
	if _, err := s.RefreshQuota(ctx, acc.ID); !errors.Is(err, storage.ErrSessionExpired) {
		t.Fatalf("err = %v", err)
	}
	got, _ := s.Get(ctx, acc.ID)
	if got.Status != StatusReconnectRequired || got.LastError == "" {
		t.Fatalf("acc = %+v", got)
	}
	// Con la cuenta marcada, Session no intenta reanudar.
	if _, err := s.Session(ctx, acc.ID); !errors.Is(err, storage.ErrSessionExpired) {
		t.Fatalf("err = %v", err)
	}

	// Reconectar con otra cuenta debe fallar; con la misma, volver a active.
	if _, err := s.Reconnect(ctx, acc.ID, "mal", ""); !errors.Is(err, storage.ErrInvalidCredentials) {
		t.Fatalf("err = %v", err)
	}
	delete(p.MFAEmails, "a@x.com")
	got, err = s.Reconnect(ctx, acc.ID, "ok", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusActive || got.LastError != "" {
		t.Fatalf("acc = %+v", got)
	}
}

func TestSettingsAndDelete(t *testing.T) {
	s, p := newService(t)
	ctx := context.Background()
	acc, _ := s.Connect(ctx, "fake", "", "a@x.com", "ok", "")

	got, err := s.UpdateSettings(ctx, acc.ID, Settings{Label: "Principal", Priority: 1, Enabled: false})
	if err != nil || got.Label != "Principal" || got.Priority != 1 || got.Enabled {
		t.Fatalf("got = %+v, %v", got, err)
	}
	if _, err := s.UpdateSettings(ctx, acc.ID, Settings{Label: "  "}); err == nil {
		t.Fatal("etiqueta vacía aceptada")
	}
	if _, err := s.UpdateSettings(ctx, 999, Settings{Label: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}

	closed := p.Closed
	if err := s.Delete(ctx, acc.ID); err != nil {
		t.Fatal(err)
	}
	if p.Closed != closed+1 {
		t.Fatal("la sesión no se cerró al borrar")
	}
	if err := s.Delete(ctx, acc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestContribution(t *testing.T) {
	s, p := newService(t)
	ctx := context.Background()
	p.Quota = storage.Quota{Used: 10 << 30, Total: 100 << 30}
	acc, err := s.Connect(ctx, "fake", "", "a@x.com", "ok", "")
	if err != nil {
		t.Fatal(err)
	}
	if acc.ContribMode != ContribAll || acc.Capacity != 100<<30 {
		t.Fatalf("por defecto = %+v", acc)
	}
	set := func(mode string, value int64) (Account, error) {
		return s.UpdateSettings(ctx, acc.ID, Settings{Label: "A", Priority: 1, Enabled: true, ContribMode: mode, ContribValue: value})
	}
	for _, bad := range []struct {
		mode  string
		value int64
	}{{ContribReserve, 0}, {ContribReserve, 91}, {ContribLimit, 1 << 20}, {"otro", 5}} {
		if _, err := set(bad.mode, bad.value); !errors.Is(err, ErrInvalidContribution) {
			t.Fatalf("%v = %v", bad, err)
		}
	}
	if a, _ := set(ContribReserve, 20); a.Capacity != 80<<30 {
		t.Fatalf("reserva 20%% = %d", a.Capacity)
	}
	if a, _ := set(ContribLimit, 30<<30); a.Capacity != 30<<30 {
		t.Fatalf("límite = %d", a.Capacity)
	}
	// Un límite mayor que la cuenta se queda en el total.
	if a, _ := set(ContribLimit, 500<<30); a.Capacity != 100<<30 {
		t.Fatalf("límite grande = %d", a.Capacity)
	}
	// Sin modo se conserva el aporte; el disco suma capacidades, no totales.
	if a, _ := set(ContribReserve, 50); a.Capacity != 50<<30 {
		t.Fatalf("reserva 50%% = %d", a.Capacity)
	}
	if a, err := set("", 0); err != nil || a.ContribMode != ContribReserve || a.ContribValue != 50 {
		t.Fatalf("conservar = %+v %v", a, err)
	}
	if sum, _ := s.Summarize(ctx); sum.Total != 50<<30 || sum.Used != 0 {
		t.Fatalf("resumen = %+v", sum)
	}
}

func TestTestThenConnectReusesSession(t *testing.T) {
	s, p := newService(t)
	ctx := context.Background()
	p.Quota = storage.Quota{Used: 1, Total: 1 << 30}
	p.MFAEmails["m@x.com"] = true

	if _, err := s.Test(ctx, "fake", "m@x.com", "ok", ""); !errors.Is(err, storage.ErrMFARequired) {
		t.Fatalf("sin código = %v", err)
	}
	res, err := s.Test(ctx, "fake", "m@x.com", "ok", "123456")
	if err != nil || res.QuotaTotal != 1<<30 || res.AlreadyLinked {
		t.Fatalf("test = %+v %v", res, err)
	}
	if accs, _ := s.List(ctx); len(accs) != 0 {
		t.Fatalf("probar no debe dar de alta: %d", len(accs))
	}
	// Otra contraseña no recoge la sesión probada.
	if _, err := s.Connect(ctx, "fake", "", "m@x.com", "mal", ""); !errors.Is(err, storage.ErrInvalidCredentials) {
		t.Fatalf("contraseña distinta = %v", err)
	}
	// Sin repetir el código 2FA: se reutiliza la sesión de la prueba.
	if _, err := s.Connect(ctx, "fake", "", "M@x.com", "ok", ""); err != nil {
		t.Fatalf("connect tras test = %v", err)
	}
	if res, err := s.Test(ctx, "fake", "m@x.com", "ok", "123456"); err != nil || !res.AlreadyLinked {
		t.Fatalf("ya conectada = %+v %v", res, err)
	}

	// La sesión probada caduca.
	if _, err := s.Test(ctx, "fake", "c@x.com", "ok", ""); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Now().Add(testedTTL + time.Minute) }
	p.MFAEmails["c@x.com"] = true
	if _, err := s.Connect(ctx, "fake", "", "c@x.com", "ok", ""); !errors.Is(err, storage.ErrMFARequired) {
		t.Fatalf("sesión caducada = %v", err)
	}
}

func TestOccupied(t *testing.T) {
	s := &Service{now: time.Now}
	const gb = int64(1) << 30
	cases := []struct {
		name                  string
		total, foreign, own   int64
		mode                  string
		value, occupied, free int64
	}{
		{"límite en cuenta vacía", 20 * gb, 0, 0, ContribLimit, 5 * gb, 0, 5 * gb},
		{"límite con la cuenta casi llena", 20 * gb, 18 * gb, 0, ContribLimit, 5 * gb, 3 * gb, 2 * gb},
		{"todo con datos ajenos", 20 * gb, 8 * gb, 0, ContribAll, 0, 8 * gb, 12 * gb},
		{"lo propio manda", 20 * gb, 0, 2 * gb, ContribLimit, 5 * gb, 2 * gb, 3 * gb},
		{"cuenta llena del todo", 20 * gb, 21 * gb, 0, ContribLimit, 5 * gb, 5 * gb, -1 * gb},
	}
	for _, c := range cases {
		a := s.toAccount(sqlcgen.Account{
			QuotaTotal: c.total, ForeignUsed: c.foreign, ContribMode: c.mode, ContribValue: c.value,
		}, c.own)
		if a.Occupied != c.occupied || a.Free() != c.free {
			t.Errorf("%s: occupied=%d free=%d, quería %d y %d", c.name, a.Occupied, a.Free(), c.occupied, c.free)
		}
	}
}

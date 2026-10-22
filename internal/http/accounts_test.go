package http

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"testing"

	"github.com/hirasolutions/cloudmesh/internal/accounts"
	"github.com/hirasolutions/cloudmesh/internal/auth"
	"github.com/hirasolutions/cloudmesh/internal/crypto"
	"github.com/hirasolutions/cloudmesh/internal/db"
	"github.com/hirasolutions/cloudmesh/internal/manifest"
	"github.com/hirasolutions/cloudmesh/internal/s3"
	"github.com/hirasolutions/cloudmesh/internal/settings"
	"github.com/hirasolutions/cloudmesh/internal/shares"
	"github.com/hirasolutions/cloudmesh/internal/storage"
	"github.com/hirasolutions/cloudmesh/internal/storage/fake"
	syncpkg "github.com/hirasolutions/cloudmesh/internal/sync"
	"github.com/hirasolutions/cloudmesh/internal/transfer"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

// newFullHandler: usuario a@b.com / contraseña-larga y proveedor fake.
func newFullHandler(t *testing.T) (http.Handler, *fake.Provider, *http.Cookie) {
	t.Helper()
	return newFullHandlerWith(t, nil)
}

// newFullHandlerWith deja retocar las dependencias antes de construir el servidor.
func newFullHandlerWith(t *testing.T, tweak func(*Deps)) (http.Handler, *fake.Provider, *http.Cookie) {
	t.Helper()
	sqlDB, err := db.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	authSvc := auth.NewService(sqlDB, make([]byte, 32))
	box, _ := crypto.NewBox(bytes.Repeat([]byte{1}, 32))
	authSvc.UseBox(box)
	if _, err := authSvc.CreateUser(context.Background(), "a@b.com", "contraseña-larga"); err != nil {
		t.Fatal(err)
	}
	p := fake.New()
	log := slog.New(slog.DiscardHandler)
	accSvc := accounts.NewService(sqlDB, box, log, p)
	vfsSvc := vfs.NewService(sqlDB)
	stSvc := settings.NewService(sqlDB)
	trSvc := transfer.New(sqlDB, accSvc, vfsSvc, stSvc, box, log, 2, 4)
	d := Deps{
		DB: sqlDB, Auth: authSvc, Accounts: accSvc, VFS: vfsSvc, Sync: syncpkg.New(accSvc, vfsSvc, log),
		Transfer: trSvc, Settings: stSvc,
		Manifest: manifest.NewService(sqlDB, box, accSvc, log), Shares: shares.NewService(sqlDB, vfsSvc, make([]byte, 32)),
		S3: s3.NewService(sqlDB, vfsSvc, trSvc, box, "auto", "", 1<<20, log), S3URL: "https://s3.ejemplo.test", S3Enabled: true,
		MaxUploadBytes: 1 << 20, Log: log,
	}
	if tweak != nil {
		tweak(&d)
	}
	h := New(d)
	rec := do(h, "POST", "/api/auth/login", `{"email":"a@b.com","password":"contraseña-larga"}`, nil)
	return h, p, sessionCookieOf(t, rec)
}

func TestAccountsAPI(t *testing.T) {
	h, p, c := newFullHandler(t)
	p.MFAEmails["m@x.com"] = true

	if rec := do(h, "GET", "/api/accounts", "", nil); rec.Code != 401 {
		t.Fatalf("sin sesión = %d", rec.Code)
	}

	// Validación y errores del proveedor
	if rec := do(h, "POST", "/api/accounts", `{"provider":"fake","email":"a@x.com"}`, c); rec.Code != 400 {
		t.Fatalf("sin contraseña = %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/accounts", `{"provider":"fake","email":"a@x.com","password":"mal"}`, c); rec.Code != 400 || !bytes.Contains(rec.Body.Bytes(), []byte("provider_invalid_credentials")) {
		t.Fatalf("contraseña mala = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/accounts", `{"provider":"fake","email":"m@x.com","password":"ok"}`, c); rec.Code != 428 || !bytes.Contains(rec.Body.Bytes(), []byte("mfa_required")) {
		t.Fatalf("mfa = %d %s", rec.Code, rec.Body)
	}

	// Alta
	rec := do(h, "POST", "/api/accounts", `{"provider":"fake","label":"Uno","email":"a@x.com","password":"ok"}`, c)
	if rec.Code != 201 {
		t.Fatalf("alta = %d %s", rec.Code, rec.Body)
	}
	var acc accounts.Account
	_ = json.NewDecoder(rec.Body).Decode(&acc)
	if acc.Label != "Uno" || acc.QuotaTotal != 100 {
		t.Fatalf("acc = %+v", acc)
	}
	if rec := do(h, "POST", "/api/accounts", `{"provider":"fake","email":"a@x.com","password":"ok"}`, c); rec.Code != 409 {
		t.Fatalf("duplicada = %d", rec.Code)
	}

	// Cuota agregada: los 10 bytes ajenos del fake ya ocupan parte del aporte.
	rec = do(h, "GET", "/api/quota", "", c)
	var sum accounts.Summary
	_ = json.NewDecoder(rec.Body).Decode(&sum)
	if rec.Code != 200 || sum.Used != 10 || len(sum.Accounts) != 1 {
		t.Fatalf("quota = %d %+v", rec.Code, sum)
	}

	// Ajustes
	rec = do(h, "PATCH", "/api/accounts/1", `{"label":"Principal","priority":5,"enabled":false}`, c)
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"priority":5`)) {
		t.Fatalf("patch = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "PATCH", "/api/accounts/abc", `{"label":"x"}`, c); rec.Code != 404 {
		t.Fatalf("id inválido = %d", rec.Code)
	}

	// Sesión expirada: sync devuelve reconnect_required y la cuenta queda marcada
	p.Expire("a@x.com")
	if rec := do(h, "POST", "/api/accounts/1/sync", "", c); rec.Code != 409 || !bytes.Contains(rec.Body.Bytes(), []byte("reconnect_required")) {
		t.Fatalf("sync expirado = %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "GET", "/api/accounts", "", c)
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"status":"reconnect_required"`)) {
		t.Fatalf("list = %s", rec.Body)
	}
	if rec := do(h, "POST", "/api/accounts/1/reconnect", `{"password":"ok"}`, c); rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"status":"active"`)) {
		t.Fatalf("reconnect = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/accounts/1/sync", "", c); rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"result"`)) {
		t.Fatalf("sync = %d %s", rec.Code, rec.Body)
	}

	// Borrado
	if rec := do(h, "DELETE", "/api/accounts/1", "", c); rec.Code != 204 {
		t.Fatalf("delete = %d", rec.Code)
	}
	if rec := do(h, "DELETE", "/api/accounts/1", "", c); rec.Code != 404 {
		t.Fatalf("delete repetido = %d", rec.Code)
	}
}

func TestAccountContributionAPI(t *testing.T) {
	h, p, c := newFullHandler(t)
	p.Quota = storage.Quota{Used: 0, Total: 100 << 30}

	if rec := do(h, "POST", "/api/accounts", `{"provider":"fake","email":"a@x.com","password":"ok","contrib_mode":"reserve","contrib_value":95}`, c); rec.Code != 400 {
		t.Fatalf("aporte inválido = %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/accounts", "", c); !bytes.Contains(rec.Body.Bytes(), []byte("[]")) {
		t.Fatalf("aporte inválido no debe dar de alta: %s", rec.Body)
	}
	rec := do(h, "POST", "/api/accounts/test", `{"provider":"fake","email":"a@x.com","password":"ok"}`, c)
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"already_linked":false`)) {
		t.Fatalf("probar = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/accounts/test", `{"provider":"fake","email":"a@x.com","password":"mal"}`, c); rec.Code != 400 {
		t.Fatalf("probar con contraseña mala = %d", rec.Code)
	}
	rec = do(h, "POST", "/api/accounts", `{"provider":"fake","email":"a@x.com","password":"ok","contrib_mode":"limit","contrib_value":`+strconv.Itoa(40<<30)+`}`, c)
	var acc accounts.Account
	_ = json.NewDecoder(rec.Body).Decode(&acc)
	if rec.Code != 201 || acc.ContribMode != "limit" || acc.Capacity != 40<<30 {
		t.Fatalf("alta con límite = %d %+v", rec.Code, acc)
	}
	if rec := do(h, "GET", "/api/quota", "", c); !bytes.Contains(rec.Body.Bytes(), []byte(`"total":`+strconv.Itoa(40<<30))) {
		t.Fatalf("cuota = %s", rec.Body)
	}
	// El PATCH sin aporte (clientes viejos) lo conserva.
	rec = do(h, "PATCH", "/api/accounts/"+strconv.FormatInt(acc.ID, 10), `{"label":"A","priority":1,"enabled":true}`, c)
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"contrib_mode":"limit"`)) {
		t.Fatalf("patch = %s", rec.Body)
	}
}

func TestS3AdminAPI(t *testing.T) {
	h, _, c := newFullHandler(t)
	if rec := do(h, "GET", "/api/s3", "", c); !bytes.Contains(rec.Body.Bytes(), []byte(`"endpoint":"https://s3.ejemplo.test"`)) {
		t.Fatalf("info = %s", rec.Body)
	}
	if rec := do(h, "POST", "/api/s3/buckets", `{"name":"Malo"}`, c); rec.Code != 400 {
		t.Fatalf("nombre malo = %d", rec.Code)
	}
	rec := do(h, "POST", "/api/s3/buckets", `{"name":"respaldos"}`, c)
	var b s3.Bucket
	_ = json.NewDecoder(rec.Body).Decode(&b)
	if rec.Code != 201 || b.FolderPath != "/Buckets/respaldos" {
		t.Fatalf("crear bucket = %d %+v", rec.Code, b)
	}
	if rec := do(h, "POST", "/api/s3/buckets", `{"name":"respaldos"}`, c); rec.Code != 409 {
		t.Fatalf("repetido = %d", rec.Code)
	}
	do(h, "POST", "/api/folders", `{"parent_id":0,"name":"Web"}`, c)
	rec = do(h, "POST", "/api/s3/buckets", `{"name":"web-assets","folder_id":3}`, c)
	if rec.Code != 201 || !bytes.Contains(rec.Body.Bytes(), []byte(`"folder_path":"/Web"`)) {
		t.Fatalf("bucket sobre carpeta = %d %s", rec.Code, rec.Body)
	}

	if rec := do(h, "POST", "/api/s3/keys", `{"name":"x","permission":"root"}`, c); rec.Code != 400 {
		t.Fatalf("permiso malo = %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/s3/keys", `{"name":"x","permission":"admin_rw","bucket_ids":[1]}`, c); rec.Code != 400 {
		t.Fatalf("admin con buckets = %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/s3/keys", `{"name":"","permission":"object_ro"}`, c); rec.Code != 400 {
		t.Fatalf("sin nombre = %d", rec.Code)
	}
	rec = do(h, "POST", "/api/s3/keys", `{"name":"restic","permission":"object_rw","bucket_ids":[`+strconv.FormatInt(b.ID, 10)+`]}`, c)
	var k s3.NewKey
	_ = json.NewDecoder(rec.Body).Decode(&k)
	if rec.Code != 201 || len(k.AccessKeyID) != 32 || len(k.SecretAccessKey) != 64 || len(k.Buckets) != 1 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("crear llave = %d %+v", rec.Code, k)
	}
	rec = do(h, "GET", "/api/s3/keys", "", c)
	if !bytes.Contains(rec.Body.Bytes(), []byte(k.AccessKeyID)) || bytes.Contains(rec.Body.Bytes(), []byte(k.SecretAccessKey)) {
		t.Fatalf("la lista no debe llevar el secreto: %s", rec.Body)
	}
	if rec := do(h, "DELETE", "/api/s3/keys/"+strconv.FormatInt(k.ID, 10), "", c); rec.Code != 204 {
		t.Fatalf("revocar = %d", rec.Code)
	}
	if rec := do(h, "DELETE", "/api/s3/buckets/"+strconv.FormatInt(b.ID, 10), "", c); rec.Code != 204 {
		t.Fatalf("quitar bucket = %d", rec.Code)
	}
	// La carpeta sigue en el disco.
	if rec := do(h, "GET", "/api/folders/"+strconv.FormatInt(b.FolderID, 10), "", c); rec.Code != 200 {
		t.Fatalf("carpeta tras quitar bucket = %d", rec.Code)
	}
}

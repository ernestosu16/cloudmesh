// cloudmesh arranca el servidor (por defecto) o ejecuta un subcomando.
//
//	cloudmesh [serve]           levanta API y frontend
//	cloudmesh healthcheck       consulta /api/healthz y la API S3; sale 0/1 (HEALTHCHECK de Docker)
//	cloudmesh create-user EMAIL crea un usuario; la contraseña se lee de stdin
//	cloudmesh reset-2fa EMAIL   quita la verificación en dos pasos de un usuario
//	cloudmesh backup [RUTA]     copia consistente de la base (por defecto en DATA_DIR/backups)
//	cloudmesh restore PROVIDER EMAIL [MFA]
//	                            reconstruye una instancia vacía desde el manifiesto
//	                            guardado en esa cuenta; la contraseña se lee de stdin
//	cloudmesh restore --file RUTA
//	                            igual, desde un índice exportado en la app
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	stdhttp "net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"log/slog"

	"github.com/hirasolutions/cloudmesh/internal/accounts"
	"github.com/hirasolutions/cloudmesh/internal/auth"
	"github.com/hirasolutions/cloudmesh/internal/config"
	"github.com/hirasolutions/cloudmesh/internal/crypto"
	"github.com/hirasolutions/cloudmesh/internal/db"
	"github.com/hirasolutions/cloudmesh/internal/http"
	"github.com/hirasolutions/cloudmesh/internal/mail"
	"github.com/hirasolutions/cloudmesh/internal/maintenance"
	"github.com/hirasolutions/cloudmesh/internal/manifest"
	"github.com/hirasolutions/cloudmesh/internal/s3"
	"github.com/hirasolutions/cloudmesh/internal/settings"
	"github.com/hirasolutions/cloudmesh/internal/shares"
	"github.com/hirasolutions/cloudmesh/internal/storage"
	"github.com/hirasolutions/cloudmesh/internal/storage/fake"
	"github.com/hirasolutions/cloudmesh/internal/storage/mega"
	syncpkg "github.com/hirasolutions/cloudmesh/internal/sync"
	"github.com/hirasolutions/cloudmesh/internal/transfer"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

// version se inyecta con -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "healthcheck":
		err = healthcheck()
	case "create-user":
		err = createUser(os.Args[2:])
	case "reset-2fa":
		err = resetTOTP(os.Args[2:])
	case "restore":
		err = restore(os.Args[2:])
	case "backup":
		err = backup(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		err = fmt.Errorf("subcomando desconocido: %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sqlDB, err := db.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	box, err := crypto.NewBox(cfg.MasterKey)
	if err != nil {
		return err
	}
	authSvc := auth.NewService(sqlDB, cfg.SessionSecret)
	authSvc.UseBox(box)
	if code, err := authSvc.InitSetup(ctx); err != nil {
		return err
	} else if code != "" {
		log.Warn("CONFIGURACIÓN INICIAL: no hay usuarios. Abre la app en el navegador y usa este código para crear el administrador",
			"setup_code", code)
	}

	providers := []storage.Provider{mega.New(log)}
	if os.Getenv("CLOUDMESH_DEV_FAKE_PROVIDER") == "1" {
		// Solo desarrollo: proveedor en memoria (contraseña "ok") para probar sin Mega.
		f, err := fake.NewPersistent(filepath.Join(cfg.DataDir, "fake"))
		if err != nil {
			return err
		}
		f.Quota.Total = 20 << 30
		log.Warn("proveedor fake activado (solo desarrollo)", "dir", filepath.Join(cfg.DataDir, "fake"))
		providers = append(providers, f)
	}
	accountsSvc := accounts.NewService(sqlDB, box, log, providers...)
	vfsSvc := vfs.NewService(sqlDB)
	settingsSvc := settings.NewService(sqlDB)
	syncer := syncpkg.New(accountsSvc, vfsSvc, log)
	transferSvc := transfer.New(sqlDB, accountsSvc, vfsSvc, settingsSvc, box, log, cfg.MaxUploads, cfg.MaxDownloads)
	manifestSvc := manifest.NewService(sqlDB, box, accountsSvc, log)
	backupFn := func(ctx context.Context) (string, error) {
		return db.BackupRotate(ctx, sqlDB, filepath.Join(cfg.DataDir, "backups"), 7)
	}
	s3Svc := s3.NewService(sqlDB, vfsSvc, transferSvc, box, cfg.S3Region, cfg.S3Domain, cfg.MaxUploadBytes, log)
	// Como R2, una subida multipart sin completar caduca a los 7 días.
	cleanupFn := func(ctx context.Context) (int, error) { return s3Svc.CleanUp(ctx, 7*24*time.Hour) }
	go maintenance.New(syncer, transferSvc, manifestSvc, backupFn, cleanupFn, log).Run(ctx, 15*time.Minute)

	mailer, err := mail.New(cfg.SMTPURL, cfg.SMTPFrom)
	if err != nil {
		return err
	}
	srv := &stdhttp.Server{
		Addr: cfg.Addr,
		Handler: http.New(http.Deps{
			DB:             sqlDB,
			Auth:           authSvc,
			Accounts:       accountsSvc,
			VFS:            vfsSvc,
			Sync:           syncer,
			Transfer:       transferSvc,
			Settings:       settingsSvc,
			Manifest:       manifestSvc,
			Shares:         shares.NewService(sqlDB, vfsSvc, cfg.SessionSecret),
			S3:             s3Svc,
			Mail:           mailer,
			S3URL:          cfg.S3URL,
			S3Enabled:      cfg.S3Addr != "",
			MaxUploadBytes: cfg.MaxUploadBytes,
			TrustProxy:     cfg.TrustProxy,
			Version:        version,
			Log:            log,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		// Sin ReadTimeout/WriteTimeout globales: las transferencias renuevan su
		// propio plazo en cada lectura o escritura (ver idleReader/idleWriter).
	}
	log.Info("cloudmesh", "version", version, "max_uploads", cfg.MaxUploads, "max_downloads", cfg.MaxDownloads, "trust_proxy", cfg.TrustProxy)

	errCh := make(chan error, 2)
	go func() {
		log.Info("servidor escuchando", "addr", cfg.Addr, "data_dir", cfg.DataDir)
		if err := srv.ListenAndServe(); !errors.Is(err, stdhttp.ErrServerClosed) {
			errCh <- err
		}
	}()
	// API S3 en su propio puerto: las rutas /<bucket>/<clave> no caben junto a la app.
	var s3Srv *stdhttp.Server
	if cfg.S3Addr != "" {
		s3Srv = &stdhttp.Server{
			Addr:              cfg.S3Addr,
			Handler:           s3Svc.Handler(),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       2 * time.Minute,
			MaxHeaderBytes:    64 << 10,
		}
		go func() {
			log.Info("API S3 escuchando", "addr", cfg.S3Addr, "region", cfg.S3Region, "domain", cfg.S3Domain)
			if err := s3Srv.ListenAndServe(); !errors.Is(err, stdhttp.ErrServerClosed) {
				errCh <- err
			}
		}()
	}

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Info("apagando")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if s3Srv != nil {
		if err := s3Srv.Shutdown(shutdownCtx); err != nil {
			log.Warn("apagar API S3", "err", err)
		}
	}
	return srv.Shutdown(shutdownCtx)
}

// createUser da de alta un usuario leyendo la contraseña de stdin
// (sin eco si es una terminal). Uso: cloudmesh create-user EMAIL
func createUser(args []string) error {
	if len(args) != 1 {
		return errors.New("uso: cloudmesh create-user EMAIL")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	password, err := readPassword()
	if err != nil {
		return err
	}
	ctx := context.Background()
	sqlDB, err := db.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	u, err := auth.NewService(sqlDB, cfg.SessionSecret).CreateUser(ctx, args[0], password)
	if err != nil {
		return err
	}
	fmt.Printf("usuario creado: %s (id %d)\n", u.Email, u.ID)
	return nil
}

// resetTOTP quita la 2FA de un usuario que perdió su móvil.
func resetTOTP(args []string) error {
	if len(args) != 1 {
		return errors.New("uso: cloudmesh reset-2fa EMAIL")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	sqlDB, err := db.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	if err := auth.NewService(sqlDB, cfg.SessionSecret).ResetTOTPByEmail(ctx, args[0]); err != nil {
		return err
	}
	fmt.Printf("verificación en dos pasos quitada a %s\n", args[0])
	return nil
}

func readPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, "Contraseña: ")
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	// Sin terminal (docker exec con pipe): primera línea de stdin.
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 1024))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

// restore descarga el manifiesto de una cuenta, lo descifra con la clave
// maestra y vuelca el índice en una base vacía. Después reconecta esa cuenta.
func restore(args []string) error {
	if len(args) == 2 && args[0] == "--file" {
		return restoreFile(args[1])
	}
	if len(args) < 2 || len(args) > 3 {
		return errors.New("uso: cloudmesh restore PROVIDER EMAIL [MFA] | cloudmesh restore --file RUTA")
	}
	providerName, email := args[0], args[1]
	mfa := ""
	if len(args) == 3 {
		mfa = args[2]
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	password, err := readPassword()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx := context.Background()
	sqlDB, err := db.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	box, err := crypto.NewBox(cfg.MasterKey)
	if err != nil {
		return err
	}
	providers := map[string]storage.Provider{"mega": mega.New(log)}
	if os.Getenv("CLOUDMESH_DEV_FAKE_PROVIDER") == "1" {
		f, err := fake.NewPersistent(filepath.Join(cfg.DataDir, "fake"))
		if err != nil {
			return err
		}
		providers["fake"] = f
	}
	p, ok := providers[providerName]
	if !ok {
		return fmt.Errorf("proveedor desconocido: %q", providerName)
	}
	sess, err := p.Login(ctx, email, password, mfa)
	if err != nil {
		return err
	}
	defer sess.Close()

	data, err := manifest.Fetch(ctx, sess)
	if err != nil {
		return err
	}
	var provs []storage.Provider
	for _, pr := range providers {
		provs = append(provs, pr)
	}
	accountsSvc := accounts.NewService(sqlDB, box, log, provs...)
	manifestSvc := manifest.NewService(sqlDB, box, accountsSvc, log)
	m, err := manifestSvc.Open(data)
	if err != nil {
		return fmt.Errorf("descifrar manifiesto (¿CLOUDMESH_MASTER_KEY correcta?): %w", err)
	}
	if err := manifestSvc.Restore(ctx, m); err != nil {
		return err
	}
	fmt.Printf("restaurado: %d usuarios, %d cuentas, %d carpetas, %d archivos, %d bloques\n",
		len(m.Users), len(m.Accounts), len(m.Folders), len(m.Files), len(m.Chunks))

	// La cuenta usada para restaurar queda activa con la sesión recién abierta.
	for _, a := range m.Accounts {
		if a.Provider == providerName && strings.EqualFold(a.Email, email) {
			if _, err := accountsSvc.Reconnect(ctx, a.ID, password, mfa); err != nil {
				return fmt.Errorf("reconectar %s: %w", email, err)
			}
			fmt.Printf("cuenta %s reconectada; el resto quedan pendientes de reconectar desde la app\n", email)
		}
	}
	return nil
}

// restoreFile restaura desde un índice exportado. Todas las cuentas quedan
// pendientes de reconectar desde la app.
func restoreFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx := context.Background()
	sqlDB, err := db.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	box, err := crypto.NewBox(cfg.MasterKey)
	if err != nil {
		return err
	}
	manifestSvc := manifest.NewService(sqlDB, box, accounts.NewService(sqlDB, box, log), log)
	m, err := manifestSvc.Open(data)
	if err != nil {
		return fmt.Errorf("descifrar manifiesto (¿CLOUDMESH_MASTER_KEY correcta?): %w", err)
	}
	if err := manifestSvc.Restore(ctx, m); err != nil {
		return err
	}
	fmt.Printf("restaurado: %d usuarios, %d cuentas, %d carpetas, %d archivos, %d bloques; reconecta las cuentas desde la app\n",
		len(m.Users), len(m.Accounts), len(m.Folders), len(m.Files), len(m.Chunks))
	return nil
}

// backup escribe una copia consistente de la base. Uso: cloudmesh backup [RUTA]
func backup(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	sqlDB, err := db.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	var path string
	if len(args) > 0 {
		path = args[0]
		err = db.Backup(ctx, sqlDB, path)
	} else {
		path, err = db.BackupRotate(ctx, sqlDB, filepath.Join(cfg.DataDir, "backups"), 7)
	}
	if err != nil {
		return err
	}
	fmt.Println("backup escrito en", path)
	return nil
}

// healthcheck comprueba que el arranque terminó: /api/healthz responde 200
// (la app solo escucha tras abrir la base y migrar) y, salvo que esté
// desactivada, la API S3 contesta en su puerto. Usa CLOUDMESH_ADDR y
// CLOUDMESH_S3_ADDR con los mismos valores por defecto que serve.
func healthcheck() error {
	client := &stdhttp.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + localAddr(os.Getenv("CLOUDMESH_ADDR"), ":8080") + "/api/healthz")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusOK {
		return fmt.Errorf("healthz devolvió %d", resp.StatusCode)
	}
	s3Addr := os.Getenv("CLOUDMESH_S3_ADDR")
	if strings.EqualFold(s3Addr, "off") {
		return nil
	}
	// Sin firma la API S3 responde un error XML: basta con que conteste.
	resp, err = client.Get("http://" + localAddr(s3Addr, ":9000") + "/")
	if err != nil {
		return fmt.Errorf("API S3: %w", err)
	}
	resp.Body.Close()
	return nil
}

// localAddr pasa una dirección de escucha (":8080") a una a la que conectar.
func localAddr(addr, def string) string {
	if addr == "" {
		addr = def
	}
	if addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	return addr
}

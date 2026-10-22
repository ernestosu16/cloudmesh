// Package db abre la base SQLite y aplica las migraciones embebidas.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open crea la carpeta de datos si falta, abre (o crea) cloudmesh.db con WAL
// y deja el esquema en la última migración.
func Open(ctx context.Context, dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("crear data dir: %w", err)
	}
	path := filepath.Join(dataDir, "cloudmesh.db")
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("abrir sqlite: %w", err)
	}
	// SQLite es monoescritor; una conexión evita SQLITE_BUSY en escrituras concurrentes.
	sqlDB.SetMaxOpenConns(1)

	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := migrate(ctx, sqlDB); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return sqlDB, nil
}

func migrate(ctx context.Context, sqlDB *sql.DB) error {
	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("migrations fs: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, sqlDB, dir)
	if err != nil {
		return fmt.Errorf("goose provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrar: %w", err)
	}
	return nil
}

// Backup escribe una copia consistente de la base en path (VACUUM INTO).
// Falla si el archivo ya existe.
func Backup(ctx context.Context, sqlDB *sql.DB, path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("ya existe %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if _, err := sqlDB.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("vacuum into: %w", err)
	}
	return nil
}

// BackupRotate guarda una copia fechada en dir y conserva solo las keep más recientes.
func BackupRotate(ctx context.Context, sqlDB *sql.DB, dir string, keep int) (string, error) {
	name := "cloudmesh-" + time.Now().UTC().Format("20060102-150405") + ".db"
	path := filepath.Join(dir, name)
	if err := Backup(ctx, sqlDB, path); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return path, err
	}
	var old []string
	for _, e := range entries {
		if n := e.Name(); strings.HasPrefix(n, "cloudmesh-") && strings.HasSuffix(n, ".db") {
			old = append(old, n)
		}
	}
	sort.Strings(old)
	for len(old) > keep {
		_ = os.Remove(filepath.Join(dir, old[0]))
		old = old[1:]
	}
	return path, nil
}

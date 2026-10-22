package db

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
)

func TestOpenMigratesAndSeeds(t *testing.T) {
	ctx := context.Background()
	sqlDB, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	q := sqlcgen.New(sqlDB)
	v, err := q.GetSetting(ctx, "routing_policy")
	if err != nil {
		t.Fatal(err)
	}
	if v != "most_free" {
		t.Fatalf("routing_policy = %q, want most_free", v)
	}

	// Reabrir no debe fallar ni repetir migraciones.
	sqlDB.Close()
	if _, err := Open(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestBackupRotate(t *testing.T) {
	ctx := context.Background()
	sqlDB, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	dir := t.TempDir()
	var last string
	for i := range 3 {
		p, err := BackupRotate(ctx, sqlDB, dir, 2)
		if err != nil {
			t.Fatalf("backup %d: %v", i, err)
		}
		last = p
		time.Sleep(1100 * time.Millisecond) // nombres con resolución de segundo
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("copias = %d, want 2", len(entries))
	}
	// La copia es una base válida con el esquema migrado.
	copyDB, err := sql.Open("sqlite", "file:"+last+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	var v string
	if err := copyDB.QueryRow("SELECT value FROM settings WHERE key = 'routing_policy'").Scan(&v); err != nil || v != "most_free" {
		t.Fatalf("copia ilegible: %q %v", v, err)
	}
	if err := Backup(ctx, sqlDB, last); err == nil {
		t.Fatal("sobrescribió una copia existente")
	}
}

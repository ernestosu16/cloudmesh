// Package maintenance encadena las tareas periódicas: sincronizar cuentas,
// verificar bloques, reparar archivos, cifrar bloques antiguos, limpiar subidas multipart abandonadas,
// purgar la papelera caducada y escribir el manifiesto.
package maintenance

import (
	"context"
	"log/slog"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/manifest"
	syncpkg "github.com/hirasolutions/cloudmesh/internal/sync"
	"github.com/hirasolutions/cloudmesh/internal/transfer"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

type Runner struct {
	sync     *syncpkg.Syncer
	transfer *transfer.Service
	manifest *manifest.Service
	backup   func(ctx context.Context) (string, error) // copia local de la base; nil desactiva
	cleanup  func(ctx context.Context) (int, error)    // subidas multipart abandonadas; nil desactiva
	log      *slog.Logger

	lastBackup time.Time
}

// EncryptPerPass limita los bloques antiguos que se cifran en cada pasada:
// cada uno se descarga y se vuelve a subir, y Mega gratis limita la
// transferencia. Con bloques de 32 MiB son unos 512 MiB por pasada.
const EncryptPerPass = 16

// BackupEvery separa dos copias locales de la base.
const BackupEvery = 24 * time.Hour

func New(s *syncpkg.Syncer, t *transfer.Service, m *manifest.Service, backup func(ctx context.Context) (string, error), cleanup func(ctx context.Context) (int, error), log *slog.Logger) *Runner {
	return &Runner{sync: s, transfer: t, manifest: m, backup: backup, cleanup: cleanup, log: log}
}

// Run ejecuta una pasada cada interval hasta que ctx termine. La primera
// arranca tras un breve retardo para no competir con el arranque.
func (r *Runner) Run(ctx context.Context, interval time.Duration) {
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		r.Pass(ctx)
		timer.Reset(interval)
	}
}

// Pass es una pasada completa.
func (r *Runner) Pass(ctx context.Context) {
	start := time.Now()
	r.sync.SyncAll(ctx)
	r.transfer.ScrubAll(ctx)
	r.transfer.RebuildAll(ctx)
	if n, err := r.transfer.EncryptPending(ctx, EncryptPerPass); err != nil {
		r.log.Warn("mantenimiento: cifrar bloques antiguos", "err", err)
	} else if n > 0 {
		r.log.Info("bloques antiguos cifrados", "n", n)
	}
	if r.cleanup != nil {
		if n, err := r.cleanup(ctx); err != nil {
			r.log.Warn("mantenimiento: subidas S3", "err", err)
		} else if n > 0 {
			r.log.Info("subidas S3 abandonadas limpiadas", "n", n)
		}
	}
	if n, err := r.transfer.PurgeTrash(ctx, time.Now().Add(-vfs.TrashRetention)); err != nil {
		r.log.Warn("mantenimiento: papelera", "err", err)
	} else if n > 0 {
		r.log.Info("papelera purgada", "files", n)
	}
	if _, err := r.manifest.WriteIfChanged(ctx, false); err != nil {
		r.log.Warn("mantenimiento: manifiesto", "err", err)
	}
	if r.backup != nil && time.Since(r.lastBackup) >= BackupEvery {
		if path, err := r.backup(ctx); err != nil {
			r.log.Warn("mantenimiento: backup", "err", err)
		} else {
			r.lastBackup = time.Now()
			r.log.Info("backup local de la base", "path", path)
		}
	}
	r.log.Info("mantenimiento", "dur", time.Since(start).Round(time.Millisecond))
}

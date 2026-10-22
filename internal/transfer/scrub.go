package transfer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/accounts"
	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
	"github.com/hirasolutions/cloudmesh/internal/storage"
)

// ScrubResult resume una pasada de verificación de una cuenta.
type ScrubResult struct {
	AccountID int64 `json:"account_id"`
	Checked   int   `json:"checked"`  // bloques del índice comprobados
	Missing   int   `json:"missing"`  // bloques que faltan o cambiaron de tamaño
	Restored  int   `json:"restored"` // bloques antes ausentes que volvieron a aparecer
	Orphans   int   `json:"orphans"`  // bloques remotos sin fila en el índice, borrados
	Moved     int   `json:"moved"`    // bloques sueltos en blocks/ pasados a su subcarpeta
	Files     int   `json:"files"`    // archivos cuyo estado se recalculó
}

// orphanGrace es la edad mínima para borrar un bloque que el índice no conoce.
const orphanGrace = time.Hour

// moveBatch acota cuántos bloques sueltos (de antes de las subcarpetas) se
// recolocan por cuenta y pasada: cada uno es una llamada al proveedor.
const moveBatch = 500

// Scrub comprueba, sin descargar, que cada bloque del índice sigue en la
// cuenta con el tamaño esperado, borra bloques huérfanos de cloudmesh/blocks/
// y recalcula el estado de los archivos afectados.
func (s *Service) Scrub(ctx context.Context, accountID int64) (ScrubResult, error) {
	res := ScrubResult{AccountID: accountID}
	sess, root, err := s.session(ctx, accountID)
	if err != nil {
		return res, err
	}
	remote := map[string]storage.Entry{}
	collect := func(parent string) error {
		entries, err := sess.List(ctx, parent)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !e.IsDir {
				remote[e.ID] = e
			}
		}
		return nil
	}
	if err := collect(root); err != nil {
		return res, s.accounts.HandleSessionError(ctx, accountID, err)
	}
	// Bloques: los de cada subcarpeta de blocks/ y los sueltos en blocks/
	// (subidos antes de que existieran las subcarpetas).
	blocksDir, err := s.blockDir(ctx, accountID, sess, root, "")
	if err != nil {
		return res, err
	}
	top, err := sess.List(ctx, blocksDir)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return res, s.accounts.HandleSessionError(ctx, accountID, err)
	}
	var blockEntries, loose []storage.Entry
	for _, e := range top {
		if !e.IsDir {
			blockEntries, loose = append(blockEntries, e), append(loose, e)
			continue
		}
		sub, err := sess.List(ctx, e.ID)
		if err != nil {
			return res, s.accounts.HandleSessionError(ctx, accountID, err)
		}
		for _, c := range sub {
			if !c.IsDir {
				blockEntries = append(blockEntries, c)
			}
		}
	}
	for _, e := range blockEntries {
		remote[e.ID] = e
	}

	rows, err := s.q.ListChunksByAccount(ctx, nullID(accountID))
	if err != nil {
		return res, err
	}
	now := s.now().Unix()
	indexed := map[string]bool{}
	touched := map[int64]bool{}
	for _, c := range rows {
		indexed[c.RemoteID] = true
		e, ok := remote[c.RemoteID]
		missing := !ok || e.Size != c.Size
		res.Checked++
		if missing && c.Missing == 0 {
			res.Missing++
			touched[c.FileID] = true
		}
		if !missing && c.Missing != 0 {
			res.Restored++
			touched[c.FileID] = true
		}
		if err := s.q.MarkChunkMissing(ctx, sqlcgen.MarkChunkMissingParams{Missing: boolInt(missing), CheckedAt: now, ID: c.ID}); err != nil {
			return res, err
		}
	}

	// Huérfanos: bloques en cloudmesh/blocks/ que el índice no conoce. Se respetan
	// los de archivos aún subiéndose (la fila llega cuando termina el bloque) y
	// los recién subidos: reparar o cifrar sube el bloque nuevo antes de
	// apuntarlo en el índice.
	for _, e := range blockEntries {
		if indexed[e.ID] {
			continue
		}
		if !e.ModTime.IsZero() && s.now().Sub(e.ModTime) < orphanGrace {
			continue
		}
		if fileID, ok := blockFileID(e.Name); ok {
			if f, err := s.q.GetFile(ctx, fileID); err == nil && f.Status == StatusUploading {
				continue
			}
		}
		if err := sess.Delete(ctx, e.ID, true); err != nil {
			s.log.Warn("scrub: borrar huérfano", "account", accountID, "remote", e.ID, "err", err)
			continue
		}
		res.Orphans++
	}

	// Los bloques sueltos del índice pasan a su subcarpeta; el id remoto no cambia.
	for _, e := range loose {
		if res.Moved == moveBatch {
			break
		}
		fileID, ok := blockFileID(e.Name)
		if !ok || !indexed[e.ID] {
			continue
		}
		dir, err := s.blocks(ctx, accountID, sess, root, fileID)
		if err != nil {
			return res, err
		}
		if err := sess.Move(ctx, e.ID, dir); err != nil {
			s.log.Warn("scrub: recolocar bloque", "account", accountID, "remote", e.ID, "err", err)
			continue
		}
		res.Moved++
	}

	for fileID := range touched {
		if _, err := s.RecomputeStatus(ctx, fileID); err != nil {
			return res, err
		}
		res.Files++
	}
	s.log.Info("scrub", "account", accountID, "checked", res.Checked, "missing", res.Missing, "restored", res.Restored, "orphans", res.Orphans, "moved", res.Moved)
	return res, nil
}

// ScrubAll recorre las cuentas activas. Los errores se registran y no detienen el resto.
func (s *Service) ScrubAll(ctx context.Context) []ScrubResult {
	list, err := s.accounts.List(ctx)
	if err != nil {
		s.log.Error("scrub: listar cuentas", "err", err)
		return nil
	}
	var out []ScrubResult
	for _, a := range list {
		if a.Status != accounts.StatusActive {
			continue
		}
		r, err := s.Scrub(ctx, a.ID)
		if err != nil {
			s.log.Warn("scrub: cuenta", "account", a.ID, "err", err)
			continue
		}
		out = append(out, r)
	}
	return out
}

// RecomputeStatus deriva el estado del archivo de sus bloques: lost si alguna
// franja tiene menos de k bloques, degraded si falta alguno, ready si no.
func (s *Service) RecomputeStatus(ctx context.Context, fileID int64) (string, error) {
	f, err := s.q.GetFile(ctx, fileID)
	if err != nil {
		return "", err
	}
	if f.Status == StatusUploading {
		return f.Status, nil
	}
	if f.Layout == LayoutParts {
		return s.recomputeComposite(ctx, f)
	}
	rows, err := s.q.ListChunkRowsByFile(ctx, fileID)
	if err != nil {
		return "", err
	}
	status := StatusReady
	if f.Size > 0 {
		present := map[int64]int{}
		stripes := map[int64]bool{}
		anyMissing := false
		for _, c := range rows {
			stripes[c.Stripe] = true
			if c.AccountID.Valid && c.Missing == 0 {
				present[c.Stripe]++
			} else {
				anyMissing = true
			}
		}
		k := int(f.DataShards)
		for st := range stripes {
			if present[st] < k {
				status = StatusLost
				break
			}
		}
		if status == StatusReady && (anyMissing || len(stripes) == 0) {
			status = StatusDegraded
		}
		if len(stripes) == 0 {
			status = StatusLost
		}
	}
	if status != f.Status {
		if err := s.q.SetFileStatus(ctx, sqlcgen.SetFileStatusParams{Status: status, ID: fileID}); err != nil {
			return "", err
		}
		// El estado de una parte se refleja en el objeto al que pertenece.
		if f.Internal != 0 {
			owners, err := s.q.ListCompositesOfPart(ctx, fileID)
			if err != nil {
				return "", err
			}
			for _, id := range owners {
				owner, err := s.q.GetFile(ctx, id)
				if err != nil {
					return "", err
				}
				if _, err := s.recomputeComposite(ctx, owner); err != nil {
					return "", err
				}
			}
		}
	}
	return status, nil
}

// recomputeComposite toma el peor estado de las partes.
func (s *Service) recomputeComposite(ctx context.Context, f sqlcgen.File) (string, error) {
	parts, err := s.q.ListFileParts(ctx, f.ID)
	if err != nil {
		return "", err
	}
	rank := map[string]int{StatusReady: 0, StatusDegraded: 1, StatusLost: 2}
	status := StatusReady
	for _, p := range parts {
		part, err := s.q.GetFile(ctx, p.PartFileID)
		if errors.Is(err, sql.ErrNoRows) {
			status = StatusLost
			break
		}
		if err != nil {
			return "", err
		}
		if rank[part.Status] > rank[status] {
			status = part.Status
		}
	}
	if status != f.Status {
		if err := s.q.SetFileStatus(ctx, sqlcgen.SetFileStatusParams{Status: status, ID: f.ID}); err != nil {
			return "", err
		}
	}
	return status, nil
}

// RecomputeAll recalcula el estado de todos los archivos con bloques perdidos
// o sin cuenta (por ejemplo tras borrar una cuenta).
func (s *Service) RecomputeAll(ctx context.Context) error {
	ids, err := s.q.ListFileIDsNeedingRepair(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.RecomputeStatus(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// blockFileID extrae el id de archivo del nombre <file>-<stripe>-<idx>.cmb.
func blockFileID(name string) (int64, bool) {
	base, ok := strings.CutSuffix(name, ".cmb")
	if !ok {
		return 0, false
	}
	first, _, ok := strings.Cut(base, "-")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(first, 10, 64)
	return id, err == nil
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// HealthSummary es la foto del estado del pool.
type HealthSummary struct {
	Files         map[string]int64 `json:"files"` // por estado
	MissingChunks int64            `json:"missing_chunks"`
	// PlainChunks son bloques anteriores al cifrado que el mantenimiento aún
	// no ha convertido; WholeFiles, archivos enteros encontrados en el
	// proveedor por el sync, que no se cifran (son visibles allí).
	PlainChunks int64           `json:"plain_chunks"`
	WholeFiles  int64           `json:"whole_files"`
	Accounts    []AccountHealth `json:"accounts"`
	Degraded    []int64         `json:"degraded_file_ids"`
}

type AccountHealth struct {
	AccountID int64 `json:"account_id"`
	Chunks    int64 `json:"chunks"`
	Missing   int64 `json:"missing"`
}

func (s *Service) Health(ctx context.Context) (HealthSummary, error) {
	out := HealthSummary{Files: map[string]int64{StatusReady: 0, StatusDegraded: 0, StatusLost: 0, StatusUploading: 0}, Accounts: []AccountHealth{}, Degraded: []int64{}}
	counts, err := s.q.CountFilesByStatus(ctx)
	if err != nil {
		return out, err
	}
	for _, c := range counts {
		out.Files[c.Status] = c.N
	}
	if out.MissingChunks, err = s.q.CountMissingChunks(ctx); err != nil {
		return out, err
	}
	if out.PlainChunks, err = s.q.CountPlainChunks(ctx); err != nil {
		return out, err
	}
	if out.WholeFiles, err = s.q.CountWholeFiles(ctx); err != nil {
		return out, err
	}
	per, err := s.q.CountChunksPerAccount(ctx)
	if err != nil {
		return out, err
	}
	for _, p := range per {
		missing, _ := toInt64(p.Missing)
		out.Accounts = append(out.Accounts, AccountHealth{AccountID: p.AccountID.Int64, Chunks: p.N, Missing: missing})
	}
	ids, err := s.q.ListFileIDsNeedingRepair(ctx)
	if err != nil {
		return out, err
	}
	// Una parte con bloques perdidos se muestra como su objeto.
	seen := map[int64]bool{}
	for _, id := range ids {
		owners, err := s.q.ListCompositesOfPart(ctx, id)
		if err != nil {
			return out, err
		}
		if len(owners) == 0 {
			owners = []int64{id}
		}
		for _, o := range owners {
			if !seen[o] {
				seen[o] = true
				out.Degraded = append(out.Degraded, o)
			}
		}
	}
	return out, nil
}

func toInt64(v any) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case float64:
		return int64(n), nil
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("tipo inesperado %T", v)
	}
}

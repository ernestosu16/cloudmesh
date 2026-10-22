// Package settings guarda los ajustes globales editables en la tabla settings.
package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
	"github.com/hirasolutions/cloudmesh/internal/erasure"
)

const (
	PolicyMostFree   = "most_free"
	PolicyRoundRobin = "round_robin"
	PolicyPriority   = "priority"

	MinChunk = 1 << 20   // 1 MiB
	MaxChunk = 256 << 20 // 256 MiB
)

// Settings es el conjunto completo de ajustes.
type Settings struct {
	RoutingPolicy string `json:"routing_policy"`
	DataShards    int    `json:"data_shards"`
	ParityShards  int    `json:"parity_shards"`
	ChunkSize     int64  `json:"chunk_size"`
}

// Defaults se aplican si falta alguna clave en la tabla.
var Defaults = Settings{RoutingPolicy: PolicyMostFree, DataShards: 2, ParityShards: 1, ChunkSize: 32 << 20}

type Service struct {
	q *sqlcgen.Queries
}

func NewService(sqlDB *sql.DB) *Service { return &Service{q: sqlcgen.New(sqlDB)} }

func (s *Service) Get(ctx context.Context) (Settings, error) {
	out := Defaults
	var err error
	if out.RoutingPolicy, err = s.str(ctx, "routing_policy", out.RoutingPolicy); err != nil {
		return out, err
	}
	if out.DataShards, err = s.int(ctx, "data_shards", out.DataShards); err != nil {
		return out, err
	}
	if out.ParityShards, err = s.int(ctx, "parity_shards", out.ParityShards); err != nil {
		return out, err
	}
	cs, err := s.int(ctx, "chunk_size", int(out.ChunkSize))
	if err != nil {
		return out, err
	}
	out.ChunkSize = int64(cs)
	return out, nil
}

// Update valida y guarda todos los ajustes.
func (s *Service) Update(ctx context.Context, in Settings) (Settings, error) {
	if err := Validate(in); err != nil {
		return Settings{}, err
	}
	pairs := map[string]string{
		"routing_policy": in.RoutingPolicy,
		"data_shards":    strconv.Itoa(in.DataShards),
		"parity_shards":  strconv.Itoa(in.ParityShards),
		"chunk_size":     strconv.FormatInt(in.ChunkSize, 10),
	}
	for k, v := range pairs {
		if err := s.q.UpsertSetting(ctx, sqlcgen.UpsertSettingParams{Key: k, Value: v}); err != nil {
			return Settings{}, err
		}
	}
	return in, nil
}

// Validate comprueba rangos. Exportada para que el handler devuelva 400 con el motivo.
func Validate(in Settings) error {
	switch in.RoutingPolicy {
	case PolicyMostFree, PolicyRoundRobin, PolicyPriority:
	default:
		return fmt.Errorf("política de enrutado desconocida: %q", in.RoutingPolicy)
	}
	if in.DataShards < 1 || in.ParityShards < 0 || in.DataShards+in.ParityShards > erasure.MaxShards {
		return fmt.Errorf("k+m inválido: %d+%d (k ≥ 1, m ≥ 0, k+m ≤ %d)", in.DataShards, in.ParityShards, erasure.MaxShards)
	}
	if in.ChunkSize < MinChunk || in.ChunkSize > MaxChunk || in.ChunkSize%MinChunk != 0 {
		return errors.New("el tamaño de bloque debe ser un múltiplo de 1 MiB entre 1 y 256 MiB")
	}
	return nil
}

func (s *Service) str(ctx context.Context, key, def string) (string, error) {
	v, err := s.q.GetSetting(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return def, nil
	}
	return v, err
}

func (s *Service) int(ctx context.Context, key string, def int) (int, error) {
	v, err := s.str(ctx, key, "")
	if err != nil || v == "" {
		return def, err
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def, fmt.Errorf("ajuste %s corrupto: %q", key, v)
	}
	return n, nil
}

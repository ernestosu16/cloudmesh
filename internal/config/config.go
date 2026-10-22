// Package config carga la configuración del proceso desde variables de entorno CLOUDMESH_*.
package config

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config es la configuración inmutable del proceso.
type Config struct {
	Addr           string
	DataDir        string
	MasterKey      []byte // 32 bytes para AES-256-GCM
	SessionSecret  []byte // 32 bytes para firmar cookies
	MaxUploadBytes int64
	MaxUploads     int  // subidas simultáneas por proceso
	MaxDownloads   int  // descargas simultáneas por proceso
	TrustProxy     bool // fiarse de X-Forwarded-For / X-Forwarded-Proto (detrás de Traefik)
	LogLevel       slog.Level
	// API S3: dirección de escucha (vacía la desactiva), URL pública que se
	// muestra en la app y región que se anuncia (R2 usa «auto»). S3Domain no
	// se configura: es el host de S3URL y activa el estilo virtual-host
	// (<bucket>.<host>).
	S3Addr   string
	S3URL    string
	S3Region string
	S3Domain string
	// Correo para las invitaciones: smtp://usuario:clave@host:587 o smtps://…:465.
	// Vacío: las invitaciones solo dan el enlace para copiarlo.
	SMTPURL  string
	SMTPFrom string
}

const defaultMaxUpload = 5 << 30 // 5 GiB

// Load lee el entorno y valida. Devuelve error ante secretos ausentes o mal formados.
func Load() (Config, error) {
	cfg := Config{
		Addr:           getenv("CLOUDMESH_ADDR", ":8080"),
		DataDir:        getenv("CLOUDMESH_DATA_DIR", "./data"),
		MaxUploadBytes: defaultMaxUpload,
		MaxUploads:     4,
		MaxDownloads:   8,
		TrustProxy:     os.Getenv("CLOUDMESH_TRUST_PROXY") == "1" || strings.EqualFold(os.Getenv("CLOUDMESH_TRUST_PROXY"), "true"),
		LogLevel:       slog.LevelInfo,
		S3Addr:         getenv("CLOUDMESH_S3_ADDR", ":9000"),
		S3URL:          strings.TrimRight(os.Getenv("CLOUDMESH_S3_URL"), "/"),
		S3Region:       getenv("CLOUDMESH_S3_REGION", "auto"),
		SMTPURL:        os.Getenv("CLOUDMESH_SMTP_URL"),
		SMTPFrom:       os.Getenv("CLOUDMESH_SMTP_FROM"),
	}
	if strings.EqualFold(cfg.S3Addr, "off") {
		cfg.S3Addr = ""
	}
	// Con una IP no hay subdominios posibles: solo estilo de ruta.
	if u, err := url.Parse(cfg.S3URL); err == nil && net.ParseIP(u.Hostname()) == nil {
		cfg.S3Domain = u.Hostname()
	}

	var err error
	if cfg.MasterKey, err = key32("CLOUDMESH_MASTER_KEY"); err != nil {
		return Config{}, err
	}
	if cfg.SessionSecret, err = key32("CLOUDMESH_SESSION_SECRET"); err != nil {
		return Config{}, err
	}
	if v := os.Getenv("CLOUDMESH_MAX_UPLOAD_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("CLOUDMESH_MAX_UPLOAD_BYTES inválido: %q", v)
		}
		cfg.MaxUploadBytes = n
	}
	if cfg.MaxUploads, err = smallInt("CLOUDMESH_MAX_UPLOADS", cfg.MaxUploads); err != nil {
		return Config{}, err
	}
	if cfg.MaxDownloads, err = smallInt("CLOUDMESH_MAX_DOWNLOADS", cfg.MaxDownloads); err != nil {
		return Config{}, err
	}
	if v := os.Getenv("CLOUDMESH_LOG_LEVEL"); v != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(v)); err != nil {
			return Config{}, fmt.Errorf("CLOUDMESH_LOG_LEVEL inválido: %q", v)
		}
	}
	return cfg, nil
}

// smallInt lee un entero entre 1 y 64.
func smallInt(name string, def int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 64 {
		return 0, fmt.Errorf("%s debe ser un entero entre 1 y 64: %q", name, v)
	}
	return n, nil
}

func getenv(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// key32 lee una clave base64 de exactamente 32 bytes.
func key32(name string) ([]byte, error) {
	v := os.Getenv(name)
	if v == "" {
		return nil, fmt.Errorf("%s no definida (generar con: openssl rand -base64 32)", name)
	}
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, fmt.Errorf("%s no es base64 válido", name)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("%s debe tener 32 bytes, tiene %d", name, len(b))
	}
	return b, nil
}

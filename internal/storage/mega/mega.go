// Package mega adapta github.com/t3rm1n4l/go-mega al contrato storage.
package mega

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	gomega "github.com/t3rm1n4l/go-mega"

	"github.com/hirasolutions/cloudmesh/internal/storage"
)

// RootFolder es la carpeta bajo la raíz de Mega donde CloudMesh guarda todo.
const RootFolder = "cloudmesh"

// Provider implementa storage.Provider para Mega.
type Provider struct {
	log *slog.Logger
}

func New(log *slog.Logger) *Provider { return &Provider{log: log} }

func (p *Provider) Name() string { return "mega" }

// creds es lo que se guarda cifrado: permite LoginWithKeys sin contraseña.
type creds struct {
	Email     string `json:"email"`
	SessionID string `json:"sid"`
	MasterKey []byte `json:"k"`
}

func (p *Provider) Login(ctx context.Context, email, password, mfa string) (storage.Session, error) {
	s := p.newSession()
	if err := s.m.MultiFactorLogin(email, password, mfa); err != nil {
		s.Close()
		return nil, mapError(err)
	}
	// Mega normaliza el email; se toma el suyo.
	u, err := s.m.GetUser()
	if err != nil {
		s.Close()
		return nil, mapError(err)
	}
	s.email = u.Email
	return s, nil
}

func (p *Provider) Resume(ctx context.Context, raw storage.Credentials) (storage.Session, error) {
	var c creds
	if err := json.Unmarshal(raw, &c); err != nil || c.SessionID == "" || len(c.MasterKey) == 0 {
		return nil, storage.ErrSessionExpired
	}
	s := p.newSession()
	if err := s.m.LoginWithKeys(c.SessionID, c.MasterKey); err != nil {
		s.Close()
		return nil, mapError(err)
	}
	s.email = c.Email
	return s, nil
}

func (p *Provider) newSession() *session {
	s := &session{}
	s.m = gomega.New().
		SetClient(&http.Client{Timeout: 60 * time.Second, Transport: &closableTransport{closed: &s.closed}}).
		SetLogger(func(format string, v ...any) { p.log.Warn("mega: " + fmt.Sprintf(format, v...)) })
	return s
}

type session struct {
	m      *gomega.Mega
	email  string
	closed atomic.Bool
	links  links
}

func (s *session) Email() string { return s.email }

func (s *session) Credentials() storage.Credentials {
	b, _ := json.Marshal(creds{Email: s.email, SessionID: s.m.GetSessionID(), MasterKey: s.m.GetMasterKey()})
	return b
}

func (s *session) Quota(ctx context.Context) (storage.Quota, error) {
	q, err := s.m.GetQuota()
	if err != nil {
		return storage.Quota{}, mapError(err)
	}
	return storage.Quota{Used: int64(q.Cstrg), Total: int64(q.Mstrg)}, nil
}

func (s *session) EnsureRoot(ctx context.Context) (string, error) {
	root := s.m.FS.GetRoot()
	children, err := s.m.FS.GetChildren(root)
	if err != nil {
		return "", mapError(err)
	}
	for _, n := range children {
		if n.GetType() == gomega.FOLDER && n.GetName() == RootFolder {
			return n.GetHash(), nil
		}
	}
	n, err := s.m.CreateDir(RootFolder, root)
	if err != nil {
		return "", mapError(err)
	}
	return n.GetHash(), nil
}

func (s *session) List(ctx context.Context, parentID string) ([]storage.Entry, error) {
	parent := s.m.FS.HashLookup(parentID)
	if parent == nil {
		return nil, storage.ErrNotFound
	}
	children, err := s.m.FS.GetChildren(parent)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]storage.Entry, 0, len(children))
	for _, n := range children {
		switch n.GetType() {
		case gomega.FILE, gomega.FOLDER:
			out = append(out, storage.Entry{
				ID:      n.GetHash(),
				Name:    n.GetName(),
				Size:    n.GetSize(),
				ModTime: n.GetTimeStamp(),
				IsDir:   n.GetType() == gomega.FOLDER,
			})
		}
	}
	return out, nil
}

func (s *session) EnsureDir(ctx context.Context, parentID, name string) (string, error) {
	parent := s.m.FS.HashLookup(parentID)
	if parent == nil {
		return "", storage.ErrNotFound
	}
	children, err := s.m.FS.GetChildren(parent)
	if err != nil {
		return "", mapError(err)
	}
	for _, n := range children {
		if n.GetType() == gomega.FOLDER && n.GetName() == name {
			return n.GetHash(), nil
		}
	}
	n, err := s.m.CreateDir(name, parent)
	if err != nil {
		return "", mapError(err)
	}
	return n.GetHash(), nil
}

func (s *session) Upload(ctx context.Context, parentID, name string, size int64, r io.Reader) (storage.Entry, error) {
	parent := s.m.FS.HashLookup(parentID)
	if parent == nil {
		return storage.Entry{}, storage.ErrNotFound
	}
	u, err := s.m.NewUpload(parent, name, size)
	if err != nil {
		return storage.Entry{}, mapError(err)
	}
	buf := make([]byte, 0, 1<<20)
	for id := 0; id < u.Chunks(); id++ {
		if err := ctx.Err(); err != nil {
			return storage.Entry{}, err
		}
		_, n, err := u.ChunkLocation(id)
		if err != nil {
			return storage.Entry{}, mapError(err)
		}
		buf = buf[:n]
		if _, err := io.ReadFull(r, buf); err != nil {
			return storage.Entry{}, fmt.Errorf("leer cuerpo: %w", err)
		}
		if err := u.UploadChunk(id, buf); err != nil {
			return storage.Entry{}, mapError(err)
		}
	}
	node, err := u.Finish()
	if err != nil {
		return storage.Entry{}, mapError(err)
	}
	return storage.Entry{ID: node.GetHash(), Name: node.GetName(), Size: node.GetSize(), ModTime: node.GetTimeStamp()}, nil
}

func (s *session) Download(ctx context.Context, id string, offset, length int64) (io.ReadCloser, error) {
	node := s.m.FS.HashLookup(id)
	if node == nil || node.GetType() != gomega.FILE {
		return nil, storage.ErrNotFound
	}
	total := node.GetSize()
	if offset < 0 || offset > total {
		return nil, fmt.Errorf("offset fuera de rango")
	}
	if length < 0 || offset+length > total {
		length = total - offset
	}
	l, err := s.links.get(id, func() (chunkSource, error) {
		d, err := s.m.NewDownload(node)
		if err != nil {
			return nil, err
		}
		return d, nil
	})
	if err != nil {
		return nil, downloadError(err)
	}
	pr, pw := io.Pipe()
	go func() {
		err := pump(ctx, l, total, offset, length, pw)
		if err != nil {
			s.links.forget(id, l)
		}
		pw.CloseWithError(err)
	}()
	return pr, nil
}

func (s *session) Move(ctx context.Context, id, parentID string) error {
	node, parent := s.m.FS.HashLookup(id), s.m.FS.HashLookup(parentID)
	if node == nil || parent == nil {
		return storage.ErrNotFound
	}
	if err := s.m.Move(node, parent); err != nil {
		if errors.Is(err, gomega.ENOENT) {
			return storage.ErrNotFound
		}
		return mapError(err)
	}
	return nil
}

func (s *session) Delete(ctx context.Context, id string, destroy bool) error {
	node := s.m.FS.HashLookup(id)
	if node == nil {
		return storage.ErrNotFound
	}
	s.links.forget(id, nil)
	if err := s.m.Delete(node, destroy); err != nil {
		if errors.Is(err, gomega.ENOENT) {
			return storage.ErrNotFound
		}
		return mapError(err)
	}
	return nil
}

// Close corta el transporte. go-mega lanza una goroutine de sondeo de eventos
// (pollEvents) sin forma de pararla; closableTransport la termina en su
// siguiente petición.
func (s *session) Close() { s.closed.Store(true) }

var errClosed = errors.New("sesión mega cerrada")

// pollPath es el endpoint que solo usa el bucle pollEvents de go-mega.
const pollPath = "/sc"

// transport deja abiertas más conexiones por servidor que el de por defecto
// (2): pump pide varios trozos a la vez y sin esto renegociaría TLS.
var transport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 16
	return t
}()

type closableTransport struct {
	closed *atomic.Bool
}

func (t *closableTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.closed.Load() {
		// pollEvents reintenta cada pocos segundos para siempre, avisando
		// cada vez y reteniendo el árbol de la cuenta. Como esa ruta solo la
		// pide su goroutine, se termina aquí mismo en vez de devolver error.
		if r.URL.Path == pollPath {
			runtime.Goexit()
		}
		return nil, errClosed
	}
	return transport.RoundTrip(r)
}

// downloadError traduce un error de descarga. Al pedir la descarga, Mega
// responde EOVERQUOTA cuando se agotó la cuota de transferencia (no la de
// espacio); a mitad de descarga, los servidores de almacenamiento responden
// HTTP 509, que go-mega devuelve como texto («Http Status: 509 …») tras sus
// reintentos. Se busca ese texto exacto: un error de red lleva la URL del
// bloque, cuyos offsets pueden contener «509».
func downloadError(err error) error {
	switch {
	case errors.Is(err, gomega.EOVERQUOTA):
		return fmt.Errorf("%w: %v", storage.ErrTransferQuota, err)
	case err != nil && strings.Contains(err.Error(), "Http Status: 509"):
		return fmt.Errorf("%w: %v", storage.ErrTransferQuota, err)
	}
	return mapError(err)
}

// mapError traduce errores de go-mega al vocabulario de storage.
func mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gomega.EMFAREQUIRED):
		return storage.ErrMFARequired
	case errors.Is(err, gomega.ENOENT), errors.Is(err, gomega.EKEY), errors.Is(err, gomega.EFAILED), errors.Is(err, gomega.EARGS):
		// Mega responde ENOENT a email inexistente y EKEY/EFAILED a contraseña o 2FA mal.
		// Fuera del login, ENOENT lo convierte cada método en ErrNotFound antes de llegar aquí.
		return fmt.Errorf("%w: %v", storage.ErrInvalidCredentials, err)
	case errors.Is(err, gomega.ESID), errors.Is(err, gomega.EEXPIRED):
		return storage.ErrSessionExpired
	case errors.Is(err, gomega.EBLOCKED):
		return storage.ErrBlocked
	case errors.Is(err, gomega.EOVERQUOTA):
		return storage.ErrOverQuota
	default:
		return fmt.Errorf("mega: %w", err)
	}
}

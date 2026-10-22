// Package fake es un proveedor en memoria para tests.
package fake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/storage"
)

// Provider acepta la contraseña "ok" y el código "123456" en cuentas con 2FA.
type Provider struct {
	Quota     storage.Quota
	MFAEmails map[string]bool // emails que exigen 2FA
	// Tree simula el árbol remoto por email: parentID -> hijos. La raíz de
	// CloudMesh de cada email es "root-<email>".
	Tree map[string]map[string][]storage.Entry
	// Blobs guarda el contenido de los archivos subidos por id remoto.
	Blobs map[string][]byte
	// FailDownloads hace fallar las descargas de esos ids (simula bloques perdidos).
	FailDownloads map[string]bool
	// TransferLimited hace que las descargas de esos emails fallen con
	// ErrTransferQuota (simula la cuota de transferencia de Mega agotada).
	TransferLimited map[string]bool
	// Offline hace que las descargas de esos emails fallen como un corte de red.
	Offline map[string]bool
	// Downloads cuenta las descargas servidas por email.
	Downloads map[string]int
	seq       int
	dir       string // vacío: solo memoria

	mu      sync.Mutex
	expired map[string]bool     // sids invalidados
	issued  map[string][]string // sids emitidos por email
	logins  int
	Opened  int
	Closed  int
}

func New() *Provider {
	return &Provider{
		Quota:           storage.Quota{Used: 10, Total: 100},
		MFAEmails:       map[string]bool{},
		expired:         map[string]bool{},
		issued:          map[string][]string{},
		Tree:            map[string]map[string][]storage.Entry{},
		Blobs:           map[string][]byte{},
		FailDownloads:   map[string]bool{},
		TransferLimited: map[string]bool{},
		Offline:         map[string]bool{},
		Downloads:       map[string]int{},
	}
}

func (p *Provider) Name() string { return "fake" }

func (p *Provider) Login(ctx context.Context, email, password, mfa string) (storage.Session, error) {
	if password != "ok" {
		return nil, storage.ErrInvalidCredentials
	}
	if p.MFAEmails[email] {
		if mfa == "" {
			return nil, storage.ErrMFARequired
		}
		if mfa != "123456" {
			return nil, storage.ErrInvalidCredentials
		}
	}
	// Cada login emite un sid nuevo, como hace Mega.
	p.mu.Lock()
	p.logins++
	sid := fmt.Sprintf("sid-%s-%d", email, p.logins)
	p.issued[email] = append(p.issued[email], sid)
	p.mu.Unlock()
	return p.open(email, sid), nil
}

func (p *Provider) Resume(ctx context.Context, raw storage.Credentials) (storage.Session, error) {
	var c creds
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, storage.ErrSessionExpired
	}
	p.mu.Lock()
	expired := p.expired[c.SID]
	p.mu.Unlock()
	if expired {
		return nil, storage.ErrSessionExpired
	}
	return p.open(c.Email, c.SID), nil
}

// Expire invalida todas las sesiones emitidas hasta ahora para un email.
func (p *Provider) Expire(email string) {
	p.mu.Lock()
	for _, sid := range p.issued[email] {
		p.expired[sid] = true
	}
	p.mu.Unlock()
}

func (p *Provider) open(email, sid string) *Session {
	p.mu.Lock()
	p.Opened++
	p.mu.Unlock()
	return &Session{p: p, email: email, sid: sid}
}

type creds struct {
	Email string
	SID   string
}

type Session struct {
	p     *Provider
	email string
	sid   string
}

func (s *Session) Email() string { return s.email }

func (s *Session) Credentials() storage.Credentials {
	b, _ := json.Marshal(creds{Email: s.email, SID: s.sid})
	return b
}

func (s *Session) Quota(ctx context.Context) (storage.Quota, error) {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	if s.p.expired[s.sid] {
		return storage.Quota{}, storage.ErrSessionExpired
	}
	// Cada cuenta tiene su cuota: lo base más lo que su email tiene en el árbol.
	used := s.p.Quota.Used
	for _, entries := range s.p.tree(s.email) {
		for _, e := range entries {
			used += e.Size
		}
	}
	return storage.Quota{Used: used, Total: s.p.Quota.Total}, nil
}

func (s *Session) EnsureRoot(ctx context.Context) (string, error) { return "root-" + s.email, nil }

// SetRemote fija los hijos de una carpeta remota de un email (para tests).
func (p *Provider) SetRemote(email, parentID string, entries ...storage.Entry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Tree[email] == nil {
		p.Tree[email] = map[string][]storage.Entry{}
	}
	p.Tree[email][parentID] = entries
}

func (s *Session) EnsureDir(ctx context.Context, parentID, name string) (string, error) {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	tree := s.p.tree(s.email)
	for _, e := range tree[parentID] {
		if e.IsDir && e.Name == name {
			return e.ID, nil
		}
	}
	s.p.seq++
	id := fmt.Sprintf("dir-%s-%d", s.email, s.p.seq)
	tree[parentID] = append(tree[parentID], storage.Entry{ID: id, Name: name, IsDir: true})
	tree[id] = nil
	s.p.save()
	return id, nil
}

func (s *Session) Upload(ctx context.Context, parentID, name string, size int64, r io.Reader) (storage.Entry, error) {
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return storage.Entry{}, err
	}
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	if s.p.expired[s.sid] {
		return storage.Entry{}, storage.ErrSessionExpired
	}
	s.p.seq++
	id := fmt.Sprintf("blob-%s-%d", s.email, s.p.seq)
	e := storage.Entry{ID: id, Name: name, Size: size, ModTime: time.Now()}
	tree := s.p.tree(s.email)
	tree[parentID] = append(tree[parentID], e)
	if s.p.dir == "" {
		s.p.Blobs[id] = data
	} else {
		s.p.saveBlob(id, data)
	}
	s.p.save()
	return e, nil
}

func (s *Session) Download(ctx context.Context, id string, offset, length int64) (io.ReadCloser, error) {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	if s.p.FailDownloads[id] {
		return nil, storage.ErrNotFound
	}
	if s.p.TransferLimited[s.email] {
		return nil, storage.ErrTransferQuota
	}
	if s.p.Offline[s.email] {
		return nil, errors.New("fake: dial tcp: no such host")
	}
	s.p.Downloads[s.email]++
	if s.p.dir != "" {
		f, err := os.Open(filepath.Join(s.p.dir, "blobs", id))
		if err != nil {
			return nil, storage.ErrNotFound
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, err
		}
		if length < 0 || offset+length > st.Size() {
			length = st.Size() - offset
		}
		return &sectionCloser{SectionReader: io.NewSectionReader(f, offset, length), f: f}, nil
	}
	data, ok := s.p.Blobs[id]
	if !ok {
		return nil, storage.ErrNotFound
	}
	if length < 0 || offset+length > int64(len(data)) {
		length = int64(len(data)) - offset
	}
	return io.NopCloser(bytes.NewReader(data[offset : offset+length])), nil
}

type sectionCloser struct {
	*io.SectionReader
	f *os.File
}

func (s *sectionCloser) Close() error { return s.f.Close() }

func (s *Session) Delete(ctx context.Context, id string, destroy bool) error {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	if s.p.dir == "" {
		if _, ok := s.p.Blobs[id]; !ok {
			return storage.ErrNotFound
		}
		delete(s.p.Blobs, id)
	} else {
		if _, err := os.Stat(filepath.Join(s.p.dir, "blobs", id)); err != nil {
			return storage.ErrNotFound
		}
		s.p.removeBlob(id)
	}
	defer s.p.save()
	for parent, entries := range s.p.tree(s.email) {
		for i, e := range entries {
			if e.ID == id {
				s.p.Tree[s.email][parent] = append(entries[:i:i], entries[i+1:]...)
				return nil
			}
		}
	}
	return nil
}

func (s *Session) Move(ctx context.Context, id, parentID string) error {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	tree := s.p.tree(s.email)
	if _, ok := tree[parentID]; !ok {
		return storage.ErrNotFound
	}
	for parent, entries := range tree {
		for i, e := range entries {
			if e.ID == id && !e.IsDir {
				tree[parent] = append(entries[:i:i], entries[i+1:]...)
				tree[parentID] = append(tree[parentID], e)
				s.p.save()
				return nil
			}
		}
	}
	return storage.ErrNotFound
}

// tree devuelve (creando) el árbol de un email. Llamar con el mutex tomado.
func (p *Provider) tree(email string) map[string][]storage.Entry {
	if p.Tree[email] == nil {
		p.Tree[email] = map[string][]storage.Entry{}
	}
	return p.Tree[email]
}

func (s *Session) List(ctx context.Context, parentID string) ([]storage.Entry, error) {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	if s.p.expired[s.sid] {
		return nil, storage.ErrSessionExpired
	}
	tree := s.p.Tree[s.email]
	if parentID != "root-"+s.email {
		if _, ok := tree[parentID]; !ok {
			return nil, storage.ErrNotFound
		}
	}
	return append([]storage.Entry(nil), tree[parentID]...), nil
}

func (s *Session) Close() {
	s.p.mu.Lock()
	s.p.Closed++
	s.p.mu.Unlock()
}

// ---- Persistencia opcional (desarrollo) ------------------------------------

// NewPersistent devuelve un proveedor que guarda el árbol y los blobs en dir
// para sobrevivir a reinicios del proceso. Los blobs viven solo en disco, no
// en memoria. Solo para desarrollo.
func NewPersistent(dir string) (*Provider, error) {
	p := New()
	p.dir = dir
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o750); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	var st persisted
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("estado del proveedor fake corrupto: %w", err)
	}
	p.Tree, p.seq, p.Quota.Used = st.Tree, st.Seq, st.Used
	if p.Tree == nil {
		p.Tree = map[string]map[string][]storage.Entry{}
	}
	return p, nil
}

type persisted struct {
	Tree map[string]map[string][]storage.Entry `json:"tree"`
	Seq  int                                   `json:"seq"`
	Used int64                                 `json:"used"`
}

// save escribe el estado. Llamar con el mutex tomado.
func (p *Provider) save() {
	if p.dir == "" {
		return
	}
	raw, err := json.Marshal(persisted{Tree: p.Tree, Seq: p.seq, Used: p.Quota.Used})
	if err == nil {
		err = os.WriteFile(filepath.Join(p.dir, "state.json"), raw, 0o640)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake: guardar estado:", err)
	}
}

func (p *Provider) saveBlob(id string, data []byte) {
	if p.dir == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(p.dir, "blobs", id), data, 0o640); err != nil {
		fmt.Fprintln(os.Stderr, "fake: guardar blob:", err)
	}
}

func (p *Provider) removeBlob(id string) {
	if p.dir == "" {
		return
	}
	_ = os.Remove(filepath.Join(p.dir, "blobs", id))
}

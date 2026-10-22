// Package storage define el contrato que cumple cada proveedor de nube.
//
// Un Provider abre sesiones (login inicial o reanudación con credenciales
// guardadas). Una Session es una cuenta viva con la que se opera.
package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	// ErrMFARequired: la cuenta tiene 2FA y falta el código.
	ErrMFARequired = errors.New("se requiere código de verificación")
	// ErrInvalidCredentials: email, contraseña o código incorrectos.
	ErrInvalidCredentials = errors.New("credenciales del proveedor inválidas")
	// ErrSessionExpired: las credenciales guardadas ya no sirven; hay que reconectar.
	ErrSessionExpired = errors.New("la sesión del proveedor expiró")
	// ErrBlocked: el proveedor bloqueó la cuenta.
	ErrBlocked = errors.New("cuenta bloqueada por el proveedor")
	// ErrOverQuota: el proveedor rechazó la operación por cuota de espacio.
	ErrOverQuota = errors.New("cuota del proveedor superada")
	// ErrTransferQuota: el proveedor rechazó una descarga por límite de
	// transferencia (las cuentas gratuitas de Mega). Es temporal.
	ErrTransferQuota = errors.New("límite de transferencia del proveedor alcanzado")
	// ErrNotFound: el nodo remoto ya no existe.
	ErrNotFound = errors.New("nodo remoto no encontrado")
)

// BlocksFolder es la subcarpeta de la raíz remota donde van los bloques
// troceados, repartidos en subcarpetas. El sync la ignora: su contenido solo
// lo entiende CloudMesh.
const BlocksFolder = "blocks"

// Credentials es el material opaco (por proveedor) que permite reanudar una
// sesión sin contraseña. Se guarda cifrado.
type Credentials []byte

// Quota en bytes.
type Quota struct {
	Used  int64
	Total int64
}

// Entry es un nodo remoto (archivo o carpeta).
type Entry struct {
	ID      string
	Name    string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// Provider construye sesiones para un tipo de nube.
type Provider interface {
	// Name es el identificador guardado en accounts.provider.
	Name() string
	// Login autentica con contraseña. mfa puede ir vacío; si la cuenta lo
	// exige devuelve ErrMFARequired.
	Login(ctx context.Context, email, password, mfa string) (Session, error)
	// Resume reanuda con credenciales guardadas. ErrSessionExpired si no valen.
	Resume(ctx context.Context, creds Credentials) (Session, error)
}

// Session es una cuenta autenticada.
type Session interface {
	// Email de la cuenta según el proveedor.
	Email() string
	// Credentials serializa lo necesario para Resume.
	Credentials() Credentials
	Quota(ctx context.Context) (Quota, error)
	// EnsureRoot garantiza la carpeta raíz de CloudMesh y devuelve su id remoto.
	EnsureRoot(ctx context.Context) (string, error)
	// List devuelve los hijos directos de una carpeta remota. ErrNotFound si no existe.
	List(ctx context.Context, parentID string) ([]Entry, error)
	// EnsureDir devuelve la subcarpeta (parent, name), creándola si falta.
	EnsureDir(ctx context.Context, parentID, name string) (string, error)
	// Upload sube exactamente size bytes leídos de r como un archivo nuevo.
	// El tamaño debe conocerse de antemano (Mega lo exige).
	Upload(ctx context.Context, parentID, name string, size int64, r io.Reader) (Entry, error)
	// Download devuelve un lector de [offset, offset+length) del archivo.
	// length < 0 significa hasta el final. ErrNotFound si no existe.
	Download(ctx context.Context, id string, offset, length int64) (io.ReadCloser, error)
	// Move pasa un archivo a otra carpeta conservando su id. ErrNotFound si
	// falta alguno de los dos.
	Move(ctx context.Context, id, parentID string) error
	// Delete borra un archivo. destroy=true lo elimina sin pasar por la papelera.
	Delete(ctx context.Context, id string, destroy bool) error
	// Close libera recursos. La sesión no se usa después.
	Close()
}

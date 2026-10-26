// Package s3 implementa una API compatible con Amazon S3 al estilo de
// Cloudflare R2: buckets, llaves de acceso con permisos de administración u
// objetos (lectura o lectura/escritura, opcionalmente limitadas a ciertos
// buckets), firmas SigV4, URLs prefirmadas y subida multipart.
//
// Cada bucket apunta a una carpeta del disco. La clave «a/b/c.txt» es el
// archivo c.txt dentro de las subcarpetas a/b; una clave terminada en «/» es
// una carpeta. Sobrescribir o borrar un objeto lo manda a la papelera.
package s3

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/crypto"
	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
	"github.com/hirasolutions/cloudmesh/internal/transfer"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

// Permisos de una llave, los mismos que ofrece R2.
const (
	PermAdminRW  = "admin_rw"  // buckets y objetos, lectura y escritura
	PermAdminRO  = "admin_ro"  // listar buckets y leer objetos
	PermObjectRW = "object_rw" // leer y escribir objetos
	PermObjectRO = "object_ro" // leer objetos
)

// BucketsFolder agrupa las carpetas de los buckets creados por la API.
const BucketsFolder = "Buckets"

var (
	ErrBucketName     = errors.New("nombre de bucket inválido: 3–63 caracteres en minúsculas, números y guiones")
	ErrBucketExists   = errors.New("ya existe un bucket con ese nombre")
	ErrBucketNotFound = errors.New("bucket no encontrado")
	ErrKeyNotFound    = errors.New("llave no encontrada")
	ErrPermission     = errors.New("permiso desconocido")
	ErrScope          = errors.New("solo las llaves de objetos se limitan a buckets concretos")
	ErrKeyName        = errors.New("el nombre de la llave es obligatorio (hasta 80 caracteres)")
)

var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

// Bucket es un bucket con la ruta de su carpeta.
type Bucket struct {
	ID         int64           `json:"id"`
	Name       string          `json:"name"`
	FolderID   int64           `json:"folder_id"`
	FolderPath string          `json:"folder_path"`
	CreatedAt  int64           `json:"created_at"`
	Public     bool            `json:"public"`
	CORS       []CORSRule      `json:"cors"`
	Lifecycle  []LifecycleRule `json:"lifecycle"`
}

// Key es una llave de acceso sin su secreto.
type Key struct {
	ID             int64    `json:"id"`
	AccessKeyID    string   `json:"access_key_id"`
	Name           string   `json:"name"`
	Permission     string   `json:"permission"`
	Buckets        []string `json:"buckets"` // vacío: todos
	BucketIDs      []int64  `json:"bucket_ids"`
	CreatedAt      int64    `json:"created_at"`
	CreatedByEmail string   `json:"created_by_email,omitempty"`
	LastUsedAt     int64    `json:"last_used_at"`

	bucketIDs map[int64]bool
	createdBy int64
}

// NewKey es una llave recién creada; el secreto solo se muestra esta vez.
type NewKey struct {
	Key
	SecretAccessKey string `json:"secret_access_key"`
}

type Service struct {
	db        *sql.DB
	q         *sqlcgen.Queries
	vfs       *vfs.Service
	transfer  *transfer.Service
	box       *crypto.Box
	region    string
	domain    string // con él, también <bucket>.<domain> (virtual-host)
	maxObject int64
	log       *slog.Logger
	now       func() time.Time
}

// NewService crea el servicio. domain (sin puerto, p. ej. «s3.ejemplo.com»)
// activa el estilo virtual-host; vacío deja solo el estilo de ruta.
func NewService(sqlDB *sql.DB, v *vfs.Service, t *transfer.Service, box *crypto.Box, region, domain string, maxObject int64, log *slog.Logger) *Service {
	return &Service{db: sqlDB, q: sqlcgen.New(sqlDB), vfs: v, transfer: t, box: box, region: region, domain: strings.ToLower(domain), maxObject: maxObject, log: log, now: time.Now}
}

// Domain es el dominio del estilo virtual-host, o vacío.
func (s *Service) Domain() string { return s.domain }

// Region es la región que se anuncia; los clientes pueden firmar con cualquiera.
func (s *Service) Region() string { return s.region }

// ---- Buckets ---------------------------------------------------------------

// CreateBucket crea un bucket. Con folderID 0 crea la carpeta Buckets/<nombre>.
func (s *Service) CreateBucket(ctx context.Context, name string, folderID int64) (Bucket, error) {
	if !bucketName.MatchString(name) || strings.Contains(name, "--") {
		return Bucket{}, ErrBucketName
	}
	if _, err := s.q.GetBucketByName(ctx, name); err == nil {
		return Bucket{}, ErrBucketExists
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Bucket{}, err
	}
	if folderID == vfs.RootID {
		parent, err := s.vfs.EnsureFolder(ctx, vfs.RootID, BucketsFolder)
		if err != nil {
			return Bucket{}, err
		}
		f, err := s.vfs.EnsureFolder(ctx, parent.ID, name)
		if err != nil {
			return Bucket{}, err
		}
		folderID = f.ID
	} else if err := s.vfs.FolderExists(ctx, folderID); err != nil {
		return Bucket{}, err
	}
	row, err := s.q.CreateBucket(ctx, sqlcgen.CreateBucketParams{Name: name, FolderID: folderID, CreatedAt: s.now().Unix()})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Bucket{}, ErrBucketExists
		}
		return Bucket{}, err
	}
	return s.toBucket(ctx, row)
}

// ListBuckets devuelve los buckets por nombre.
func (s *Service) ListBuckets(ctx context.Context) ([]Bucket, error) {
	rows, err := s.q.ListBuckets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Bucket, 0, len(rows))
	for _, r := range rows {
		b, err := s.toBucket(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// UnlinkBucket quita el bucket; su carpeta y su contenido siguen en el disco.
func (s *Service) UnlinkBucket(ctx context.Context, id int64) error {
	n, err := s.q.DeleteBucket(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrBucketNotFound
	}
	return nil
}

func (s *Service) bucket(ctx context.Context, name string) (sqlcgen.S3Bucket, error) {
	b, err := s.q.GetBucketByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return b, errNoSuchBucket
	}
	return b, err
}

func (s *Service) toBucket(ctx context.Context, r sqlcgen.S3Bucket) (Bucket, error) {
	l, err := s.vfs.List(ctx, r.FolderID)
	if err != nil {
		return Bucket{}, err
	}
	names := make([]string, 0, len(l.Breadcrumbs)+1)
	for _, f := range append(l.Breadcrumbs, l.Folder) {
		names = append(names, f.Name)
	}
	cors, lifecycle := parseCORS(r.Cors), parseLifecycle(r.Lifecycle)
	if cors == nil {
		cors = []CORSRule{}
	}
	if lifecycle == nil {
		lifecycle = []LifecycleRule{}
	}
	return Bucket{
		ID: r.ID, Name: r.Name, FolderID: r.FolderID, FolderPath: "/" + strings.Join(names, "/"), CreatedAt: r.CreatedAt,
		Public: r.Public == 1, CORS: cors, Lifecycle: lifecycle,
	}, nil
}

// ---- Llaves ------------------------------------------------------------------

// checkKey valida nombre, permiso y buckets de una llave; devuelve el nombre limpio.
func (s *Service) checkKey(ctx context.Context, name, perm string, bucketIDs []int64) (string, error) {
	switch perm {
	case PermAdminRW, PermAdminRO:
		if len(bucketIDs) > 0 {
			return "", ErrScope
		}
	case PermObjectRW, PermObjectRO:
	default:
		return "", ErrPermission
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return "", ErrKeyName
	}
	for _, id := range bucketIDs {
		if _, err := s.q.GetBucket(ctx, id); errors.Is(err, sql.ErrNoRows) {
			return "", ErrBucketNotFound
		} else if err != nil {
			return "", err
		}
	}
	return name, nil
}

// CreateKey genera una llave. bucketIDs limita una llave de objetos a esos buckets.
func (s *Service) CreateKey(ctx context.Context, userID int64, name, perm string, bucketIDs []int64) (NewKey, error) {
	name, err := s.checkKey(ctx, name, perm, bucketIDs)
	if err != nil {
		return NewKey{}, err
	}
	// Como R2: 32 caracteres hex de id y 64 de secreto.
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return NewKey{}, err
	}
	secretHex, enc, err := s.newSecret()
	if err != nil {
		return NewKey{}, err
	}
	row, err := s.q.CreateKey(ctx, sqlcgen.CreateKeyParams{
		AccessKeyID: hex.EncodeToString(id), EncSecret: enc, Name: name, Permission: perm, CreatedBy: userID, CreatedAt: s.now().Unix(),
	})
	if err != nil {
		return NewKey{}, err
	}
	for _, b := range bucketIDs {
		if err := s.q.AddKeyBucket(ctx, sqlcgen.AddKeyBucketParams{KeyID: row.ID, BucketID: b}); err != nil {
			return NewKey{}, err
		}
	}
	return s.withSecret(ctx, row.ID, secretHex)
}

// UpdateKey cambia el nombre, el permiso y los buckets de una llave. El id de
// acceso y el secreto siguen igual, así que los clientes ya configurados pasan
// a tener los permisos nuevos en su siguiente petición.
func (s *Service) UpdateKey(ctx context.Context, id int64, name, perm string, bucketIDs []int64) (Key, error) {
	name, err := s.checkKey(ctx, name, perm, bucketIDs)
	if err != nil {
		return Key{}, err
	}
	// En una transacción: a medias, una llave sin buckets valdría para todos.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Key{}, err
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)
	n, err := q.UpdateKey(ctx, sqlcgen.UpdateKeyParams{Name: name, Permission: perm, ID: id})
	if err != nil {
		return Key{}, err
	}
	if n == 0 {
		return Key{}, ErrKeyNotFound
	}
	if err := q.ClearKeyBuckets(ctx, id); err != nil {
		return Key{}, err
	}
	seen := map[int64]bool{}
	for _, b := range bucketIDs {
		if seen[b] {
			continue
		}
		seen[b] = true
		if err := q.AddKeyBucket(ctx, sqlcgen.AddKeyBucketParams{KeyID: id, BucketID: b}); err != nil {
			return Key{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Key{}, err
	}
	k, err := s.withSecret(ctx, id, "")
	return k.Key, err
}

// RegenerateKey cambia el secreto de una llave y conserva su id de acceso,
// permisos y buckets. El secreto anterior deja de valer al momento.
func (s *Service) RegenerateKey(ctx context.Context, id int64) (NewKey, error) {
	secretHex, enc, err := s.newSecret()
	if err != nil {
		return NewKey{}, err
	}
	n, err := s.q.SetKeySecret(ctx, sqlcgen.SetKeySecretParams{EncSecret: enc, ID: id})
	if err != nil {
		return NewKey{}, err
	}
	if n == 0 {
		return NewKey{}, ErrKeyNotFound
	}
	return s.withSecret(ctx, id, secretHex)
}

// newSecret genera un secreto de 64 caracteres hex y su versión cifrada.
func (s *Service) newSecret() (string, []byte, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", nil, err
	}
	secretHex := hex.EncodeToString(secret)
	enc, err := s.box.Seal([]byte(secretHex))
	return secretHex, enc, err
}

// withSecret devuelve la llave id junto al secreto en claro recién generado.
func (s *Service) withSecret(ctx context.Context, id int64, secretHex string) (NewKey, error) {
	keys, err := s.ListKeys(ctx)
	if err != nil {
		return NewKey{}, err
	}
	for _, k := range keys {
		if k.ID == id {
			return NewKey{Key: k, SecretAccessKey: secretHex}, nil
		}
	}
	return NewKey{}, ErrKeyNotFound
}

// ListKeys devuelve las llaves, la más reciente primero.
func (s *Service) ListKeys(ctx context.Context) ([]Key, error) {
	rows, err := s.q.ListKeys(ctx)
	if err != nil {
		return nil, err
	}
	buckets, err := s.q.ListBuckets(ctx)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	for _, b := range buckets {
		names[b.ID] = b.Name
	}
	out := make([]Key, 0, len(rows))
	for _, r := range rows {
		k := Key{
			ID: r.ID, AccessKeyID: r.AccessKeyID, Name: r.Name, Permission: r.Permission, Buckets: []string{}, BucketIDs: []int64{},
			CreatedAt: r.CreatedAt, CreatedByEmail: r.CreatedByEmail, LastUsedAt: r.LastUsedAt,
		}
		ids, err := s.q.ListKeyBuckets(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			k.Buckets = append(k.Buckets, names[id])
		}
		k.BucketIDs = ids
		sort.Strings(k.Buckets)
		out = append(out, k)
	}
	return out, nil
}

// DeleteKey revoca una llave.
func (s *Service) DeleteKey(ctx context.Context, id int64) error {
	n, err := s.q.DeleteKey(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrKeyNotFound
	}
	return nil
}

// authKey carga una llave y su secreto por el id de acceso.
func (s *Service) authKey(ctx context.Context, accessKey string) (Key, string, error) {
	r, err := s.q.GetKeyByAccessKeyID(ctx, accessKey)
	if errors.Is(err, sql.ErrNoRows) {
		return Key{}, "", errInvalidAccessKey
	}
	if err != nil {
		return Key{}, "", err
	}
	secret, err := s.box.Open(r.EncSecret)
	if err != nil {
		return Key{}, "", fmt.Errorf("descifrar llave: %w", err)
	}
	ids, err := s.q.ListKeyBuckets(ctx, r.ID)
	if err != nil {
		return Key{}, "", err
	}
	k := Key{ID: r.ID, AccessKeyID: r.AccessKeyID, Name: r.Name, Permission: r.Permission, LastUsedAt: r.LastUsedAt, bucketIDs: map[int64]bool{}, createdBy: r.CreatedBy}
	for _, id := range ids {
		k.bucketIDs[id] = true
	}
	// last_used_at con resolución de un minuto: no hace falta escribir en cada petición.
	now := s.now().Unix()
	if now-r.LastUsedAt >= 60 {
		_ = s.q.TouchKey(ctx, sqlcgen.TouchKeyParams{LastUsedAt: now, ID: r.ID, LastUsedAt_2: now - 60})
	}
	return k, string(secret), nil
}

// Acciones que se autorizan por llave.
type action int

const (
	actListBuckets action = iota
	actAdminBucket        // crear o borrar buckets
	actRead
	actWrite
)

// allowed decide si la llave puede hacer act en el bucket (0: ninguno concreto).
func (k Key) allowed(act action, bucketID int64) bool {
	switch k.Permission {
	case PermAdminRW:
		return true
	case PermAdminRO:
		return act == actListBuckets || act == actRead
	case PermObjectRW, PermObjectRO:
		if act == actListBuckets || act == actAdminBucket {
			return false
		}
		if act == actWrite && k.Permission == PermObjectRO {
			return false
		}
		return len(k.bucketIDs) == 0 || k.bucketIDs[bucketID]
	}
	return false
}

// ---- Claves ↔ disco ------------------------------------------------------------

// objectKey es una clave validada: carpetas intermedias y nombre (vacío si es «dir/»).
type objectKey struct {
	dirs []string
	name string
}

func parseKey(key string) (objectKey, error) {
	if key == "" || len(key) > 1024 {
		return objectKey{}, errInvalidArgument("la clave debe tener entre 1 y 1024 bytes")
	}
	segs := strings.Split(key, "/")
	var k objectKey
	last := len(segs) - 1
	for i, seg := range segs {
		if i == last && seg == "" {
			break // «a/b/»: carpeta
		}
		clean, err := vfs.CleanName(seg)
		if err != nil || clean != seg {
			return objectKey{}, errInvalidArgument(fmt.Sprintf("la clave %q no es válida aquí: cada tramo entre «/» debe ser un nombre de archivo (sin «..», sin vacíos ni espacios en los extremos)", key))
		}
		if i == last {
			k.name = seg
		} else {
			k.dirs = append(k.dirs, seg)
		}
	}
	return k, nil
}

// folderFor recorre las carpetas de la clave desde la del bucket; con create las crea.
func (s *Service) folderFor(ctx context.Context, root int64, dirs []string, create bool) (int64, error) {
	id := root
	for _, d := range dirs {
		var f vfs.Folder
		var err error
		if create {
			f, err = s.vfs.EnsureFolder(ctx, id, d)
		} else {
			f, err = s.vfs.ChildFolder(ctx, id, d)
		}
		if err != nil {
			return 0, err
		}
		id = f.ID
	}
	return id, nil
}

// lookup devuelve el archivo de una clave (la versión más reciente si hay varias).
func (s *Service) lookup(ctx context.Context, b sqlcgen.S3Bucket, key string) (vfs.File, error) {
	k, err := parseKey(key)
	if err != nil || k.name == "" {
		return vfs.File{}, errNoSuchKey
	}
	folder, err := s.folderFor(ctx, b.FolderID, k.dirs, false)
	if errors.Is(err, vfs.ErrNotFound) {
		return vfs.File{}, errNoSuchKey
	}
	if err != nil {
		return vfs.File{}, err
	}
	files, err := s.vfs.FindFiles(ctx, folder, k.name)
	if err != nil {
		return vfs.File{}, err
	}
	if len(files) == 0 {
		return vfs.File{}, errNoSuchKey
	}
	return files[0], nil
}

// replaced manda a la papelera las versiones anteriores de una clave.
func (s *Service) replaced(ctx context.Context, folder int64, name string, keep int64) {
	files, err := s.vfs.FindFiles(ctx, folder, name)
	if err != nil {
		s.log.Warn("s3: versiones anteriores", "name", name, "err", err)
		return
	}
	for _, f := range files {
		if f.ID != keep {
			if err := s.vfs.Trash(ctx, f.ID); err != nil {
				s.log.Warn("s3: a la papelera", "file", f.ID, "err", err)
			}
		}
	}
}

// etag es el ETag de un archivo: su MD5, o un valor estable que no parezca un
// MD5 (con «-») para archivos anteriores a la API, así los clientes no lo
// comparan con el MD5 del contenido.
func etag(f vfs.File) string {
	if f.ETag != "" {
		return f.ETag
	}
	seed := f.SHA256
	if seed == "" {
		seed = fmt.Sprintf("%d:%d:%d", f.ID, f.Size, f.ModTime)
	}
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:16]) + "-1"
}

// listEntry es un objeto o un prefijo común en un listado.
type listEntry struct {
	key    string
	file   *vfs.File // nil en carpetas vacías y prefijos comunes
	prefix bool
}

// list recorre la carpeta del bucket en orden lexicográfico de clave y
// devuelve hasta limit entradas posteriores a after que empiecen por prefix.
// Con delimitador «/» no baja a las carpetas: las da como prefijo común.
func (s *Service) list(ctx context.Context, b sqlcgen.S3Bucket, prefix, delim, after string, limit int) ([]listEntry, bool, error) {
	var out []listEntry
	truncated := false
	lastPrefix := ""
	emit := func(e listEntry) bool {
		if delim != "" && !e.prefix {
			if i := strings.Index(e.key[len(prefix):], delim); i >= 0 {
				e = listEntry{key: e.key[:len(prefix)+i+len(delim)], prefix: true}
			}
		}
		if e.key <= after || (e.prefix && e.key == lastPrefix) {
			return true
		}
		if len(out) == limit {
			truncated = true
			return false
		}
		if e.prefix {
			lastPrefix = e.key
		}
		out = append(out, e)
		return true
	}
	var walk func(folderID int64, base string) (bool, error)
	walk = func(folderID int64, base string) (bool, error) {
		l, err := s.vfs.List(ctx, folderID)
		if err != nil {
			return false, err
		}
		type item struct {
			key    string
			folder int64
			file   *vfs.File
		}
		items := make([]item, 0, len(l.Folders)+len(l.Files))
		for _, f := range l.Folders {
			items = append(items, item{key: base + f.Name + "/", folder: f.ID})
		}
		for i := range l.Files {
			items = append(items, item{key: base + l.Files[i].Name, file: &l.Files[i]})
		}
		// Orden por bytes; una carpeta ordena como «nombre/» y todo lo suyo
		// empieza así, de modo que bajar en orden da el orden global.
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].key != items[j].key {
				return items[i].key < items[j].key
			}
			return items[i].file != nil && items[j].file != nil && items[i].file.ID > items[j].file.ID
		})
		for i, it := range items {
			if i > 0 && it.key == items[i-1].key && it.file != nil {
				continue // varias versiones con el mismo nombre: la más reciente va primero
			}
			if !strings.HasPrefix(it.key, prefix) && !strings.HasPrefix(prefix, it.key) {
				continue
			}
			if it.file != nil {
				if !strings.HasPrefix(it.key, prefix) {
					continue
				}
				if !emit(listEntry{key: it.key, file: it.file}) {
					return false, nil
				}
				continue
			}
			// Carpeta: nada suyo pasa de after si after ya la dejó atrás.
			if after > it.key && !strings.HasPrefix(after, it.key) {
				continue
			}
			// La carpeta que es el propio prefijo se lista por dentro.
			if delim == "/" && strings.HasPrefix(it.key, prefix) && it.key != prefix {
				if !emit(listEntry{key: it.key, prefix: true}) {
					return false, nil
				}
				continue
			}
			sub, err := s.vfs.List(ctx, it.folder)
			if err != nil {
				return false, err
			}
			if len(sub.Folders) == 0 && len(sub.Files) == 0 && strings.HasPrefix(it.key, prefix) {
				if !emit(listEntry{key: it.key}) {
					return false, nil
				}
				continue
			}
			if ok, err := walk(it.folder, it.key); !ok || err != nil {
				return ok, err
			}
		}
		return true, nil
	}
	if _, err := walk(b.FolderID, ""); err != nil {
		return nil, false, err
	}
	return out, truncated, nil
}

// bucketEmpty dice si la carpeta del bucket no tiene nada visible ni subidas a medias.
func (s *Service) bucketEmpty(ctx context.Context, b sqlcgen.S3Bucket) (bool, error) {
	l, err := s.vfs.List(ctx, b.FolderID)
	if err != nil {
		return false, err
	}
	n, err := s.q.CountBucketUploads(ctx, b.ID)
	if err != nil {
		return false, err
	}
	return len(l.Folders) == 0 && len(l.Files) == 0 && n == 0, nil
}

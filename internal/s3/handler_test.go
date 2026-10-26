package s3

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/accounts"
	"github.com/hirasolutions/cloudmesh/internal/auth"
	"github.com/hirasolutions/cloudmesh/internal/crypto"
	"github.com/hirasolutions/cloudmesh/internal/db"
	"github.com/hirasolutions/cloudmesh/internal/settings"
	"github.com/hirasolutions/cloudmesh/internal/storage"
	"github.com/hirasolutions/cloudmesh/internal/storage/fake"
	"github.com/hirasolutions/cloudmesh/internal/transfer"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

type env struct {
	s    *Service
	auth *auth.Service
	v    *vfs.Service
	p    *fake.Provider
	srv  *httptest.Server
	uid  int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	sqlDB, err := db.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	box, _ := crypto.NewBox(bytes.Repeat([]byte{3}, 32))
	log := slog.New(slog.DiscardHandler)
	p := fake.New()
	p.Quota = storage.Quota{Total: 1 << 30}
	acc := accounts.NewService(sqlDB, box, log, p)
	for _, e := range []string{"a@x.com", "b@x.com", "c@x.com"} {
		if _, err := acc.Connect(ctx, "fake", "", e, "ok", ""); err != nil {
			t.Fatal(err)
		}
	}
	st := settings.NewService(sqlDB)
	if _, err := st.Update(ctx, settings.Settings{RoutingPolicy: settings.PolicyMostFree, DataShards: 2, ParityShards: 1, ChunkSize: settings.MinChunk}); err != nil {
		t.Fatal(err)
	}
	v := vfs.NewService(sqlDB)
	authSvc := auth.NewService(sqlDB, make([]byte, 32))
	u, err := authSvc.CreateUser(ctx, "a@b.com", "contraseña-larga")
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(sqlDB, v, transfer.New(sqlDB, acc, v, st, box, log, 4, 4), box, "auto", "s3.test", 64<<20, log)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &env{s: s, auth: authSvc, v: v, p: p, srv: srv, uid: u.ID}
}

// client firma como un SDK: cabecera SigV4 con el SHA-256 del cuerpo.
type client struct {
	t          *testing.T
	base, ak   string
	secret     string
	unsigned   bool   // UNSIGNED-PAYLOAD
	host       string // Host distinto del servidor (virtual-host)
	httpClient *http.Client
}

func (e *env) client(t *testing.T, perm string, buckets ...int64) *client {
	k, err := e.s.CreateKey(context.Background(), e.uid, "test "+perm, perm, buckets)
	if err != nil {
		t.Fatal(err)
	}
	return &client{t: t, base: e.srv.URL, ak: k.AccessKeyID, secret: k.SecretAccessKey, httpClient: e.srv.Client()}
}

func (c *client) do(method, path string, body []byte, hdr map[string]string) *http.Response {
	c.t.Helper()
	req, err := http.NewRequest(method, c.base+path, bytes.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	now := time.Now().UTC()
	amzDate := now.Format(amzDateFormat)
	payload := payloadUnsigned
	if !c.unsigned {
		sum := sha256.Sum256(body)
		payload = hex.EncodeToString(sum[:])
	}
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payload)
	signed := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	if c.host != "" {
		req.Host = c.host
	}
	r := req.Clone(context.Background())
	r.Host = req.URL.Host
	if c.host != "" {
		r.Host = c.host
	}
	canon := canonicalRequest(r, signed, payload, nil)
	sum := sha256.Sum256([]byte(canon))
	scope := amzDate[:8] + "/auto/s3/aws4_request"
	sig := hex.EncodeToString(hmacSHA256(signingKey(c.secret, amzDate[:8], "auto"), sigAlgorithm+"\n"+amzDate+"\n"+scope+"\n"+hex.EncodeToString(sum[:])))
	req.Header.Set("Authorization", fmt.Sprintf("%s Credential=%s/%s,SignedHeaders=%s,Signature=%s", sigAlgorithm, c.ak, scope, strings.Join(signed, ";"), sig))
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp
}

func read(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func expect(t *testing.T, resp *http.Response, status int, what string) []byte {
	t.Helper()
	b := read(t, resp)
	if resp.StatusCode != status {
		t.Fatalf("%s = %d, want %d: %s", what, resp.StatusCode, status, b)
	}
	return b
}

type listV2 struct {
	Contents []struct {
		Key  string
		Size int64
		ETag string
	}
	CommonPrefixes []struct{ Prefix string }
	IsTruncated    bool
	NextToken      string `xml:"NextContinuationToken"`
	KeyCount       int
}

func (c *client) list(query string) listV2 {
	c.t.Helper()
	var l listV2
	if err := xml.Unmarshal(expect(c.t, c.do("GET", "/fotos?list-type=2"+query, nil, nil), 200, "list "+query), &l); err != nil {
		c.t.Fatal(err)
	}
	return l
}

func keys(l listV2) string {
	var out []string
	for _, o := range l.Contents {
		out = append(out, o.Key)
	}
	for _, p := range l.CommonPrefixes {
		out = append(out, p.Prefix+"(p)")
	}
	return strings.Join(out, ",")
}

func TestObjects(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.client(t, PermAdminRW)

	// Buckets por la API
	expect(t, admin.do("PUT", "/Mal_Nombre", nil, nil), 400, "nombre inválido")
	expect(t, admin.do("PUT", "/fotos", nil, nil), 200, "crear bucket")
	expect(t, admin.do("PUT", "/fotos", nil, nil), 409, "bucket repetido")
	if b := read(t, admin.do("GET", "/", nil, nil)); !bytes.Contains(b, []byte("<Name>fotos</Name>")) {
		t.Fatalf("ListBuckets = %s", b)
	}
	expect(t, admin.do("HEAD", "/fotos", nil, nil), 200, "HeadBucket")
	expect(t, admin.do("HEAD", "/nada", nil, nil), 404, "HeadBucket inexistente")

	// Objetos
	data := make([]byte, 3<<20+17)
	rand.Read(data)
	resp := admin.do("PUT", "/fotos/2026/boda/IMG_1.jpg", data, nil)
	expect(t, resp, 200, "PutObject")
	sum := md5Hex(data)
	if resp.Header.Get("ETag") != `"`+sum+`"` {
		t.Fatalf("ETag = %s, want md5 %s", resp.Header.Get("ETag"), sum)
	}
	expect(t, admin.do("PUT", "/fotos/2026/boda/IMG_2.jpg", []byte("dos"), map[string]string{"X-Amz-Meta-Mtime": "1700000000.123456789", "X-Amz-Meta-Autor": "ana"}), 200, "PutObject 2")
	if resp := admin.do("HEAD", "/fotos/2026/boda/IMG_2.jpg", nil, nil); resp.Header.Get("X-Amz-Meta-Mtime") != "1700000000.123456789" ||
		resp.Header.Get("X-Amz-Meta-Autor") != "ana" ||
		resp.Header.Get("Last-Modified") != time.Unix(1700000000, 0).UTC().Format(http.TimeFormat) {
		t.Fatalf("mtime del cliente = %v", resp.Header)
	}
	expect(t, admin.do("PUT", "/fotos/2026/notas.txt", []byte("hola"), nil), 200, "PutObject 3")
	expect(t, admin.do("PUT", "/fotos/vacia/", nil, nil), 200, "carpeta vacía")
	expect(t, admin.do("PUT", "/fotos/a//b", []byte("x"), nil), 400, "clave inválida")
	expect(t, admin.do("PUT", "/fotos/../x", []byte("x"), nil), 400, "clave con ..")

	// Las claves son carpetas y archivos del disco.
	b, _ := e.s.q.GetBucketByName(ctx, "fotos")
	l, _ := e.v.List(ctx, b.FolderID)
	if len(l.Folders) != 2 || l.Folders[0].Name != "2026" {
		t.Fatalf("carpetas del bucket = %+v", l.Folders)
	}

	got := expect(t, admin.do("GET", "/fotos/2026/boda/IMG_1.jpg", nil, nil), 200, "GetObject")
	if !bytes.Equal(got, data) {
		t.Fatal("contenido distinto")
	}
	resp = admin.do("GET", "/fotos/2026/boda/IMG_1.jpg", nil, map[string]string{"Range": "bytes=10-19"})
	if got := expect(t, resp, 206, "Range"); !bytes.Equal(got, data[10:20]) || resp.Header.Get("Content-Range") != fmt.Sprintf("bytes 10-19/%d", len(data)) {
		t.Fatalf("Range = %q %s", got, resp.Header.Get("Content-Range"))
	}
	resp = admin.do("HEAD", "/fotos/2026/notas.txt", nil, nil)
	expect(t, resp, 200, "HeadObject")
	if resp.Header.Get("Content-Length") != "4" || resp.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("HeadObject = %v", resp.Header)
	}
	expect(t, admin.do("GET", "/fotos/no/existe", nil, nil), 404, "NoSuchKey")

	// Listados
	if k := keys(admin.list("&delimiter=/")); k != "2026/(p),vacia/(p)" {
		t.Fatalf("raíz con delimitador = %s", k)
	}
	if k := keys(admin.list("&prefix=2026/&delimiter=/")); k != "2026/notas.txt,2026/boda/(p)" {
		t.Fatalf("2026/ = %s", k)
	}
	if k := keys(admin.list("")); k != "2026/boda/IMG_1.jpg,2026/boda/IMG_2.jpg,2026/notas.txt,vacia/" {
		t.Fatalf("recursivo = %s", k)
	}
	if k := keys(admin.list("&prefix=2026/bo")); k != "2026/boda/IMG_1.jpg,2026/boda/IMG_2.jpg" {
		t.Fatalf("prefijo parcial = %s", k)
	}
	// Paginación de uno en uno
	var all []string
	tok := ""
	for i := 0; i < 10; i++ {
		q := "&max-keys=1"
		if tok != "" {
			q += "&continuation-token=" + tok
		}
		page := admin.list(q)
		all = append(all, keys(page))
		if !page.IsTruncated {
			break
		}
		tok = page.NextToken
	}
	if strings.Join(all, ",") != "2026/boda/IMG_1.jpg,2026/boda/IMG_2.jpg,2026/notas.txt,vacia/" {
		t.Fatalf("paginado = %v", all)
	}

	// Sobrescribir y borrar mandan a la papelera.
	expect(t, admin.do("PUT", "/fotos/2026/notas.txt", []byte("nuevo"), nil), 200, "sobrescribir")
	if got := read(t, admin.do("GET", "/fotos/2026/notas.txt", nil, nil)); string(got) != "nuevo" {
		t.Fatalf("tras sobrescribir = %q", got)
	}
	expect(t, admin.do("DELETE", "/fotos/2026/boda/IMG_2.jpg", nil, nil), 204, "DeleteObject")
	expect(t, admin.do("DELETE", "/fotos/2026/boda/IMG_2.jpg", nil, nil), 204, "DeleteObject repetido")
	trash, _ := e.v.ListTrash(ctx)
	if len(trash) != 2 {
		t.Fatalf("papelera = %d, want 2", len(trash))
	}
	del := `<Delete><Object><Key>2026/notas.txt</Key></Object><Object><Key>vacia/</Key></Object></Delete>`
	if b := read(t, admin.do("POST", "/fotos?delete", []byte(del), nil)); bytes.Count(b, []byte("<Deleted>")) != 2 {
		t.Fatalf("DeleteObjects = %s", b)
	}
	// Borrar lo último de una carpeta la quita: el prefijo deja de existir.
	expect(t, admin.do("DELETE", "/fotos/2026/boda/IMG_1.jpg", nil, nil), 204, "último de boda/")
	if k := keys(admin.list("")); k != "" {
		t.Fatalf("tras vaciar = %q", k)
	}
	expect(t, admin.do("PUT", "/fotos/2026/boda/IMG_1.jpg", data, nil), 200, "volver a subir")

	// Copia en el servidor
	expect(t, admin.do("PUT", "/fotos/copia.jpg", nil, map[string]string{"X-Amz-Copy-Source": "/fotos/2026/boda/IMG_1.jpg"}), 200, "CopyObject")
	if got := read(t, admin.do("GET", "/fotos/copia.jpg", nil, nil)); !bytes.Equal(got, data) {
		t.Fatal("copia distinta")
	}

	// El bucket no se borra con contenido.
	expect(t, admin.do("DELETE", "/fotos", nil, nil), 409, "DeleteBucket con contenido")

	// Firma mala
	bad := *admin
	bad.secret = strings.Repeat("0", 64)
	expect(t, bad.do("GET", "/", nil, nil), 403, "firma mala")
	bad.ak = "nope"
	expect(t, bad.do("GET", "/", nil, nil), 403, "llave inexistente")
}

func TestMultipart(t *testing.T) {
	e := newEnv(t)
	admin := e.client(t, PermAdminRW)
	admin.unsigned = true
	expect(t, admin.do("PUT", "/bkt", nil, nil), 200, "crear bucket")

	var init struct{ UploadId string }
	if err := xml.Unmarshal(expect(t, admin.do("POST", "/bkt/big/file.bin?uploads", nil, nil), 200, "CreateMultipartUpload"), &init); err != nil || init.UploadId == "" {
		t.Fatalf("init = %+v %v", init, err)
	}
	p1 := make([]byte, minPartSize)
	p2 := []byte("final")
	rand.Read(p1)
	etags := []string{}
	for i, part := range [][]byte{p1, p2} {
		resp := admin.do("PUT", fmt.Sprintf("/bkt/big/file.bin?partNumber=%d&uploadId=%s", i+1, init.UploadId), part, nil)
		expect(t, resp, 200, "UploadPart")
		etags = append(etags, resp.Header.Get("ETag"))
	}
	// Repetir una parte la sustituye.
	resp := admin.do("PUT", "/bkt/big/file.bin?partNumber=2&uploadId="+init.UploadId, p2, nil)
	expect(t, resp, 200, "UploadPart repetida")
	if l, _ := e.v.List(context.Background(), mustBucket(t, e, "bkt").FolderID); len(l.Files) != 0 {
		t.Fatalf("las partes no deben verse: %+v", l.Files)
	}
	if b := read(t, admin.do("GET", "/bkt/big/file.bin?uploadId="+init.UploadId, nil, nil)); bytes.Count(b, []byte("<Part>")) != 2 {
		t.Fatalf("ListParts = %s", b)
	}
	complete := func(parts string) *http.Response {
		return admin.do("POST", "/bkt/big/file.bin?uploadId="+init.UploadId, []byte("<CompleteMultipartUpload>"+parts+"</CompleteMultipartUpload>"), nil)
	}
	part := func(n int, tag string) string {
		return fmt.Sprintf("<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>", n, tag)
	}
	expect(t, complete(part(2, etags[1])+part(1, etags[0])), 400, "orden")
	expect(t, complete(part(1, `"mal"`)+part(2, etags[1])), 400, "etag malo")
	b := expect(t, complete(part(1, etags[0])+part(2, etags[1])), 200, "Complete")
	if !bytes.Contains(b, []byte("-2&#34;</ETag>")) && !bytes.Contains(b, []byte(`-2"</ETag>`)) {
		t.Fatalf("ETag multipart = %s", b)
	}
	got := read(t, admin.do("GET", "/bkt/big/file.bin", nil, nil))
	if !bytes.Equal(got, append(append([]byte{}, p1...), p2...)) {
		t.Fatal("objeto multipart distinto")
	}
	r := admin.do("GET", "/bkt/big/file.bin", nil, map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", minPartSize-2, minPartSize+1)})
	if got := expect(t, r, 206, "Range entre partes"); string(got) != string(p1[minPartSize-2:])+"fi" {
		t.Fatalf("rango = %q", got)
	}
	expect(t, complete(part(1, etags[0])), 404, "completar dos veces")

	// Abortar
	if err := xml.Unmarshal(read(t, admin.do("POST", "/bkt/otro?uploads", nil, nil)), &init); err != nil {
		t.Fatal(err)
	}
	expect(t, admin.do("PUT", "/bkt/otro?partNumber=1&uploadId="+init.UploadId, []byte("x"), nil), 200, "parte")
	if b := read(t, admin.do("GET", "/bkt?uploads", nil, nil)); !bytes.Contains(b, []byte(init.UploadId)) {
		t.Fatalf("ListMultipartUploads = %s", b)
	}
	expect(t, admin.do("DELETE", "/bkt/otro?uploadId="+init.UploadId, nil, nil), 204, "Abort")
	expect(t, admin.do("DELETE", "/bkt/big/file.bin", nil, nil), 204, "borrar objeto")
	// En la papelera sigue ocupando; al purgarla no queda nada.
	if _, err := e.s.transfer.PurgeTrash(context.Background(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(e.p.Blobs) != 0 {
		t.Fatalf("blobs tras purgar = %d", len(e.p.Blobs))
	}
}

func TestPermissions(t *testing.T) {
	e := newEnv(t)
	admin := e.client(t, PermAdminRW)
	expect(t, admin.do("PUT", "/uno", nil, nil), 200, "bucket uno")
	expect(t, admin.do("PUT", "/dos", nil, nil), 200, "bucket dos")
	expect(t, admin.do("PUT", "/uno/k", []byte("v"), nil), 200, "put")
	uno := mustBucket(t, e, "uno")
	dos := mustBucket(t, e, "dos")

	ro := e.client(t, PermObjectRO)
	expect(t, ro.do("GET", "/uno/k", nil, nil), 200, "object_ro lee")
	expect(t, ro.do("PUT", "/uno/k", []byte("x"), nil), 403, "object_ro no escribe")
	expect(t, ro.do("GET", "/", nil, nil), 403, "object_ro no lista buckets")

	scoped := e.client(t, PermObjectRW, uno.ID)
	expect(t, scoped.do("PUT", "/uno/k2", []byte("x"), nil), 200, "scoped escribe en su bucket")
	expect(t, scoped.do("PUT", "/dos/k2", []byte("x"), nil), 403, "scoped no entra en otro bucket")
	expect(t, scoped.do("PUT", "/tres", nil, nil), 403, "object_rw no crea buckets")
	expect(t, scoped.do("PUT", "/uno", nil, nil), 409, "CreateBucket de su bucket (rclone)")
	expect(t, scoped.do("PUT", "/dos", nil, nil), 403, "CreateBucket de otro bucket")

	adminRO := e.client(t, PermAdminRO)
	expect(t, adminRO.do("GET", "/", nil, nil), 200, "admin_ro lista")
	expect(t, adminRO.do("DELETE", "/uno/k", nil, nil), 403, "admin_ro no borra")

	if _, err := e.s.CreateKey(context.Background(), e.uid, "x", PermAdminRW, []int64{uno.ID}); err == nil {
		t.Fatal("una llave admin no se limita a buckets")
	}
	keys, _ := e.s.ListKeys(context.Background())
	for _, k := range keys {
		if k.Permission == PermObjectRW && (len(k.Buckets) != 1 || k.Buckets[0] != "uno") {
			t.Fatalf("buckets de la llave = %v", k.Buckets)
		}
	}
	// Regenerar: el secreto anterior deja de valer y el nuevo sí.
	for _, k := range keys {
		if k.AccessKeyID == adminRO.ak {
			nk, err := e.s.RegenerateKey(context.Background(), k.ID)
			if err != nil {
				t.Fatal(err)
			}
			if nk.AccessKeyID != adminRO.ak || nk.SecretAccessKey == adminRO.secret {
				t.Fatalf("llave regenerada = %+v", nk.Key)
			}
			expect(t, adminRO.do("GET", "/", nil, nil), 403, "secreto anterior")
			adminRO.secret = nk.SecretAccessKey
			expect(t, adminRO.do("GET", "/", nil, nil), 200, "secreto nuevo")
		}
	}
	if _, err := e.s.RegenerateKey(context.Background(), 9999); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("regenerar llave inexistente: %v", err)
	}
	// Editar: mismo id y secreto, permisos nuevos al momento.
	ctx := context.Background()
	for _, k := range keys {
		if k.AccessKeyID != scoped.ak {
			continue
		}
		if _, err := e.s.UpdateKey(ctx, k.ID, "x", PermAdminRW, []int64{uno.ID}); !errors.Is(err, ErrScope) {
			t.Fatalf("editar a admin con buckets: %v", err)
		}
		if _, err := e.s.UpdateKey(ctx, k.ID, " ", PermObjectRW, nil); !errors.Is(err, ErrKeyName) {
			t.Fatalf("editar sin nombre: %v", err)
		}
		ek, err := e.s.UpdateKey(ctx, k.ID, " solo dos ", PermObjectRO, []int64{dos.ID, dos.ID})
		if err != nil {
			t.Fatal(err)
		}
		if ek.Name != "solo dos" || ek.Permission != PermObjectRO || len(ek.BucketIDs) != 1 || ek.Buckets[0] != "dos" || ek.AccessKeyID != scoped.ak {
			t.Fatalf("llave editada = %+v", ek)
		}
		expect(t, scoped.do("GET", "/uno/k2", nil, nil), 403, "ya no entra en uno")
		expect(t, scoped.do("PUT", "/dos/k3", []byte("x"), nil), 403, "ahora es de solo lectura")
		expect(t, scoped.do("GET", "/dos/k2", nil, nil), 404, "lee en dos")
		if ek, err = e.s.UpdateKey(ctx, k.ID, "admin", PermAdminRW, nil); err != nil || len(ek.Buckets) != 0 {
			t.Fatalf("a admin: %+v %v", ek, err)
		}
		expect(t, scoped.do("PUT", "/tres", nil, nil), 200, "admin crea buckets")
	}
	if _, err := e.s.UpdateKey(ctx, 9999, "x", PermObjectRW, nil); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("editar llave inexistente: %v", err)
	}
	if _, err := e.s.UpdateKey(ctx, keys[0].ID, "x", PermObjectRW, []int64{9999}); !errors.Is(err, ErrBucketNotFound) {
		t.Fatalf("editar con bucket inexistente: %v", err)
	}
	// Revocar
	for _, k := range keys {
		if k.AccessKeyID == ro.ak {
			if err := e.s.DeleteKey(context.Background(), k.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	expect(t, ro.do("GET", "/uno/k", nil, nil), 403, "llave revocada")
}

func TestPresignedAndChunked(t *testing.T) {
	e := newEnv(t)
	admin := e.client(t, PermAdminRW)
	expect(t, admin.do("PUT", "/pub", nil, nil), 200, "bucket")
	expect(t, admin.do("PUT", "/pub/hola.txt", []byte("hola mundo"), nil), 200, "put")

	u, err := presign("GET", e.srv.URL+"/pub/hola.txt", admin.ak, admin.secret, "auto", time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	if got := expect(t, resp, 200, "prefirmada"); string(got) != "hola mundo" {
		t.Fatalf("prefirmada = %q", got)
	}

	// Los parámetros response-* firmados sustituyen las cabeceras, como en S3.
	disp := `attachment; filename="hola.txt"`
	u, err = presign("GET", e.srv.URL+"/pub/hola.txt?response-content-disposition="+url.QueryEscape(disp)+"&response-content-type=text%2Fplain",
		admin.ak, admin.secret, "auto", time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	expect(t, resp, 200, "prefirmada con response-*")
	if got := resp.Header.Get("Content-Disposition"); got != disp {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/plain" {
		t.Fatalf("Content-Type = %q", got)
	}

	// aws-chunked sin firmar con trailer de checksum, como los SDK recientes.
	body := "5\r\nhola \r\n5\r\nchunk\r\n0\r\nx-amz-checksum-crc32:AAAAAA==\r\n\r\n"
	// aws-chunked va firmado en modo streaming, así que se firma a mano.
	c := *admin
	c.unsigned = true
	req, _ := http.NewRequest("PUT", e.srv.URL+"/pub/chunk.txt", strings.NewReader(body))
	req.Header.Set("Content-Encoding", "aws-chunked")
	req.Header.Set("X-Amz-Decoded-Content-Length", "10")
	signStreaming(t, req, c.ak, c.secret)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	expect(t, resp, 200, "aws-chunked")
	if got := read(t, admin.do("GET", "/pub/chunk.txt", nil, nil)); string(got) != "hola chunk" {
		t.Fatalf("aws-chunked = %q", got)
	}
}

func signStreaming(t *testing.T, req *http.Request, ak, secret string) {
	t.Helper()
	amzDate := time.Now().UTC().Format(amzDateFormat)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadStreamUnsigned)
	signed := []string{"content-encoding", "host", "x-amz-content-sha256", "x-amz-date", "x-amz-decoded-content-length"}
	r := req.Clone(context.Background())
	r.Host = req.URL.Host
	canon := canonicalRequest(r, signed, payloadStreamUnsigned, nil)
	sum := sha256.Sum256([]byte(canon))
	scope := amzDate[:8] + "/auto/s3/aws4_request"
	sig := hex.EncodeToString(hmacSHA256(signingKey(secret, amzDate[:8], "auto"), sigAlgorithm+"\n"+amzDate+"\n"+scope+"\n"+hex.EncodeToString(sum[:])))
	req.Header.Set("Authorization", fmt.Sprintf("%s Credential=%s/%s,SignedHeaders=%s,Signature=%s", sigAlgorithm, ak, scope, strings.Join(signed, ";"), sig))
}

func mustBucket(t *testing.T, e *env, name string) Bucket {
	t.Helper()
	bs, err := e.s.ListBuckets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bs {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("no hay bucket %s", name)
	return Bucket{}
}

func md5Hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// Lo subido con una llave cuenta para la cuota de quien la creó.
func TestQuotaByKeyOwner(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.client(t, PermAdminRW)
	expect(t, admin.do("PUT", "/cuota", nil, nil), 200, "bucket")
	u, err := e.auth.CreateUserAs(ctx, "q@b.com", "contraseña-larga", auth.RoleEditor, 100)
	if err != nil {
		t.Fatal(err)
	}
	k, err := e.s.CreateKey(ctx, u.ID, "limitada", PermObjectRW, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, base: e.srv.URL, ak: k.AccessKeyID, secret: k.SecretAccessKey, httpClient: e.srv.Client()}
	expect(t, c.do("PUT", "/cuota/a", bytes.Repeat([]byte("a"), 80), nil), 200, "dentro de la cuota")
	b := expect(t, c.do("PUT", "/cuota/b", bytes.Repeat([]byte("b"), 30), nil), 403, "fuera de la cuota")
	if !bytes.Contains(b, []byte("QuotaExceeded")) {
		t.Fatalf("error = %s", b)
	}
	if used, _ := e.auth.Usage(ctx, u.ID); used != 80 {
		t.Fatalf("uso = %d", used)
	}
}

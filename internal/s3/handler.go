package s3

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
	"github.com/hirasolutions/cloudmesh/internal/httputil"
	"github.com/hirasolutions/cloudmesh/internal/transfer"
	"github.com/hirasolutions/cloudmesh/internal/vfs"
)

// transferIdle es el máximo sin tráfico en una subida o descarga.
const transferIdle = 2 * time.Minute

// Límites de multipart iguales a los de S3 y R2.
const (
	minPartSize = 5 << 20
	maxParts    = 10000
	maxListKeys = 1000
)

type ctxKey struct{}

// Handler sirve la API S3 en estilo de ruta (/<bucket>/<clave>) y, con
// dominio, también en estilo virtual-host (<bucket>.<dominio>/<clave>).
func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := make([]byte, 8)
		_, _ = rand.Read(reqID)
		w.Header().Set("X-Amz-Request-Id", strings.ToUpper(hex.EncodeToString(reqID)))
		w.Header().Set("Server", "CloudMesh")
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		s.serve(sw, r)
		s.log.Info("s3", "method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery, "status", sw.status, "dur", time.Since(start).Round(time.Millisecond))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// target separa bucket y clave. Con dominio configurado, «bucket.dominio/clave»
// (estilo virtual-host) equivale a «dominio/bucket/clave» (estilo ruta).
func (s *Service) target(r *http.Request) (bucket, objKey string) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if s.domain != "" {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if b, ok := strings.CutSuffix(strings.ToLower(host), "."+s.domain); ok && b != "" {
			return b, path
		}
	}
	bucket, objKey, _ = strings.Cut(path, "/")
	return bucket, objKey
}

// Subrecursos de S3 que CloudMesh no implementa. Sin esta lista, un GET con
// ?acl devolvería el listado o el contenido del objeto en lugar de un error.
var (
	bucketSubresources = []string{"acl", "policy", "policyStatus", "website", "encryption", "replication", "logging", "notification",
		"ownershipControls", "object-lock", "publicAccessBlock", "accelerate", "requestPayment", "analytics", "metrics", "inventory",
		"intelligent-tiering", "tagging"}
	objectSubresources = []string{"acl", "attributes", "retention", "legal-hold", "torrent", "restore", "select"}

	// responseOverrides son los parámetros de GET/HEAD que sustituyen cabeceras de la respuesta.
	responseOverrides = map[string]string{
		"response-content-type":        "Content-Type",
		"response-content-disposition": "Content-Disposition",
		"response-content-language":    "Content-Language",
		"response-content-encoding":    "Content-Encoding",
		"response-cache-control":       "Cache-Control",
		"response-expires":             "Expires",
	}
)

func unsupportedSubresource(q url.Values, names []string) bool {
	for _, n := range names {
		if q.Has(n) {
			return true
		}
	}
	return false
}

// anonymous dice si la petición no trae ninguna firma.
func anonymous(r *http.Request) bool {
	return r.Header.Get("Authorization") == "" && r.URL.Query().Get("X-Amz-Algorithm") == ""
}

func (s *Service) serve(w http.ResponseWriter, r *http.Request) {
	bucket, objKey := s.target(r)
	q := r.URL.Query()
	ctx := r.Context()

	// El bucket se busca antes de la firma: el preflight CORS llega sin firmar
	// y un error de firma también necesita las cabeceras CORS para que el
	// navegador deje leerlo.
	var b sqlcgen.S3Bucket
	found := false
	if bucket != "" {
		row, err := s.q.GetBucketByName(ctx, bucket)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			s.fail(w, r, err)
			return
		}
		b, found = row, err == nil
	}
	if r.Method == http.MethodOptions {
		if !found {
			s.fail(w, r, errNoSuchBucket)
			return
		}
		s.preflight(w, r, b)
		return
	}
	if found {
		applyCORS(w, r, b)
	}
	// Bucket público: se leen objetos sin firma, como el dominio público de R2.
	// Listar sigue pidiendo una llave.
	if found && b.Public == 1 && objKey != "" && anonymous(r) && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		ok, err := s.publicVisible(ctx, b, objKey)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if !ok {
			s.fail(w, r, errNoSuchKey)
			return
		}
		s.getObject(w, r, b, objKey)
		return
	}

	var key Key
	sr, err := verify(r, func(ak string) (string, error) {
		k, secret, err := s.authKey(ctx, ak)
		key = k
		return secret, err
	}, s.now())
	if err != nil {
		s.fail(w, r, err)
		return
	}

	if bucket == "" {
		if r.Method != http.MethodGet {
			s.fail(w, r, errNotImplemented("operación no soportada en la raíz"))
			return
		}
		if !key.allowed(actListBuckets, 0) {
			s.fail(w, r, errAccessDenied)
			return
		}
		s.listBuckets(w, r)
		return
	}

	if objKey == "" {
		switch {
		case r.Method == http.MethodPut && !q.Has("cors") && !q.Has("lifecycle"):
			// rclone crea el bucket antes de subir y da por bueno
			// BucketAlreadyOwnedByYou: una llave que ya ve el bucket recibe
			// ese error aunque no pueda crear buckets.
			if found && key.allowed(actRead, b.ID) {
				s.fail(w, r, errBucketExists)
				return
			}
			s.createBucket(w, r, key, bucket)
			return
		case r.Method == http.MethodDelete && !q.Has("cors") && !q.Has("lifecycle"):
			s.deleteBucket(w, r, key, bucket)
			return
		}
	}
	if !found {
		s.fail(w, r, errNoSuchBucket)
		return
	}
	if objKey == "" && (q.Has("cors") || q.Has("lifecycle")) {
		sub := "cors"
		if q.Has("lifecycle") {
			sub = "lifecycle"
		}
		s.bucketConfig(w, r, sr, key, b, sub)
		return
	}
	need := actRead
	if r.Method == http.MethodPut || r.Method == http.MethodPost || r.Method == http.MethodDelete {
		need = actWrite
	}
	if !key.allowed(need, b.ID) {
		s.fail(w, r, errAccessDenied)
		return
	}

	if objKey == "" {
		switch {
		case r.Method == http.MethodHead:
			w.Header().Set("X-Amz-Bucket-Region", s.region)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && q.Has("location"):
			writeXML(w, http.StatusOK, struct {
				XMLName xml.Name `xml:"LocationConstraint"`
				NS      string   `xml:"xmlns,attr"`
				Value   string   `xml:",chardata"`
			}{NS: s3NS, Value: s.region})
		case r.Method == http.MethodGet && q.Has("uploads"):
			s.listUploads(w, r, b)
		case r.Method == http.MethodGet && q.Has("versioning"):
			// Como R2: sin versionado.
			writeXML(w, http.StatusOK, struct {
				XMLName xml.Name `xml:"VersioningConfiguration"`
				NS      string   `xml:"xmlns,attr"`
			}{NS: s3NS})
		case q.Has("tagging") && r.Method == http.MethodGet:
			s.fail(w, r, &Error{http.StatusNotFound, "NoSuchTagSet", "el bucket no tiene etiquetas"})
		case unsupportedSubresource(q, bucketSubresources):
			s.fail(w, r, errNotImplemented("esta configuración de bucket no está disponible"))
		case r.Method == http.MethodGet:
			s.listObjects(w, r, b)
		case r.Method == http.MethodPost && q.Has("delete"):
			s.deleteObjects(w, r, b)
		default:
			s.fail(w, r, errNotImplemented("operación de bucket no soportada"))
		}
		return
	}

	switch {
	case q.Has("tagging"):
		// Sin etiquetas, como R2. aws-cli las pide antes de copiar un objeto.
		switch r.Method {
		case http.MethodGet:
			if _, err := s.lookup(ctx, b, objKey); err != nil {
				s.fail(w, r, err)
				return
			}
			writeXML(w, http.StatusOK, struct {
				XMLName xml.Name `xml:"Tagging"`
				NS      string   `xml:"xmlns,attr"`
				TagSet  struct{} `xml:"TagSet"`
			}{NS: s3NS})
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			s.fail(w, r, errNotImplemented("las etiquetas de objeto no están disponibles"))
		}
	case unsupportedSubresource(q, objectSubresources):
		s.fail(w, r, errNotImplemented("esta operación de objeto no está disponible"))
	case r.Method == http.MethodPost && q.Has("uploads"):
		s.createUpload(w, r, b, objKey)
	case r.Method == http.MethodPost && q.Has("uploadId"):
		s.completeUpload(w, r, sr, key, b, objKey, q.Get("uploadId"))
	case r.Method == http.MethodPut && q.Has("uploadId") && r.Header.Get("X-Amz-Copy-Source") != "":
		s.uploadPartCopy(w, r, key, b, objKey, q.Get("uploadId"), q.Get("partNumber"))
	case r.Method == http.MethodPut && q.Has("uploadId"):
		s.uploadPart(w, r, sr, key, b, objKey, q.Get("uploadId"), q.Get("partNumber"))
	case r.Method == http.MethodGet && q.Has("uploadId"):
		s.listParts(w, r, b, objKey, q.Get("uploadId"))
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		s.abortUpload(w, r, b, q.Get("uploadId"))
	case r.Method == http.MethodPut && r.Header.Get("X-Amz-Copy-Source") != "":
		s.copyObject(w, r, key, b, objKey)
	case r.Method == http.MethodPut:
		s.putObject(w, r, sr, key, b, objKey)
	case r.Method == http.MethodGet, r.Method == http.MethodHead:
		s.getObject(w, r, b, objKey)
	case r.Method == http.MethodDelete:
		s.deleteObject(w, r, b, objKey)
	default:
		s.fail(w, r, errNotImplemented("operación de objeto no soportada"))
	}
}

// publicVisible dice si una lectura sin firma llega a la clave: como en los
// enlaces, lo que cuelga de una subcarpeta restringida no es público.
func (s *Service) publicVisible(ctx context.Context, b sqlcgen.S3Bucket, objKey string) (bool, error) {
	k, err := parseKey(objKey)
	if err != nil {
		return false, nil
	}
	folder, err := s.folderFor(ctx, b.FolderID, k.dirs, false)
	if errors.Is(err, vfs.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return s.vfs.IsWithinOpen(ctx, folder, b.FolderID)
}

// owner es el dueño de lo que sube una llave: quien la creó, con su cuota.
func (s *Service) owner(ctx context.Context, k Key) transfer.Owner {
	u, err := s.q.GetUserWithHash(ctx, k.createdBy)
	if err != nil {
		return transfer.Owner{}
	}
	return transfer.Owner{ID: u.ID, Quota: u.QuotaBytes}
}

// keepMeta guarda los metadatos del cliente y, si trae fecha, la fecha
// original del archivo: rclone las compara para saber si algo cambió.
func (s *Service) keepMeta(ctx context.Context, f vfs.File, mtime int64, meta string) {
	if meta == "" {
		return
	}
	if mtime <= 0 {
		mtime = f.ModTime
	}
	if err := s.vfs.SetClientMeta(ctx, f.ID, mtime, meta); err != nil {
		s.log.Warn("s3: metadatos", "file", f.ID, "err", err)
	}
}

// fail traduce cualquier error a la respuesta S3 correspondiente.
func (s *Service) fail(w http.ResponseWriter, r *http.Request, err error) {
	var e *Error
	switch {
	case errors.As(err, &e):
	case errors.Is(err, transfer.ErrNoSpace), errors.Is(err, transfer.ErrNoAccounts):
		e = errNoSpace
	case errors.Is(err, transfer.ErrUnreadable):
		e = errUnreadable
	case errors.Is(err, transfer.ErrQuota):
		e = errQuota
	case errors.Is(err, vfs.ErrInvalidName):
		e = errInvalidArgument(err.Error())
	case errors.Is(err, vfs.ErrFileNotFound):
		e = errNoSuchKey
	case errors.Is(err, context.Canceled):
		return
	default:
		s.log.Error("s3", "method", r.Method, "path", r.URL.Path, "err", err)
		e = errInternal
	}
	writeError(w, r, e)
}

// ---- Buckets ---------------------------------------------------------------

type bucketXML struct {
	Name         string `xml:"Name"`
	CreationDate string `xml:"CreationDate"`
}

type ownerXML struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

func (s *Service) listBuckets(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.ListBuckets(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := struct {
		XMLName xml.Name    `xml:"ListAllMyBucketsResult"`
		NS      string      `xml:"xmlns,attr"`
		Owner   ownerXML    `xml:"Owner"`
		Buckets []bucketXML `xml:"Buckets>Bucket"`
	}{NS: s3NS, Owner: ownerXML{ID: "cloudmesh", DisplayName: "cloudmesh"}, Buckets: []bucketXML{}}
	for _, b := range rows {
		out.Buckets = append(out.Buckets, bucketXML{Name: b.Name, CreationDate: isoTime(b.CreatedAt)})
	}
	writeXML(w, http.StatusOK, out)
}

func (s *Service) createBucket(w http.ResponseWriter, r *http.Request, key Key, name string) {
	if !key.allowed(actAdminBucket, 0) {
		s.fail(w, r, errAccessDenied)
		return
	}
	if _, err := s.CreateBucket(r.Context(), name, vfs.RootID); err != nil {
		switch {
		case errors.Is(err, ErrBucketName):
			s.fail(w, r, errInvalidBucketName)
		case errors.Is(err, ErrBucketExists):
			s.fail(w, r, errBucketExists)
		default:
			s.fail(w, r, err)
		}
		return
	}
	w.Header().Set("Location", "/"+name)
	w.WriteHeader(http.StatusOK)
}

// deleteBucket exige el bucket vacío, como S3; la carpeta queda en el disco.
func (s *Service) deleteBucket(w http.ResponseWriter, r *http.Request, key Key, name string) {
	if !key.allowed(actAdminBucket, 0) {
		s.fail(w, r, errAccessDenied)
		return
	}
	b, err := s.bucket(r.Context(), name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	empty, err := s.bucketEmpty(r.Context(), b)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !empty {
		s.fail(w, r, errBucketNotEmpty)
		return
	}
	if err := s.UnlinkBucket(r.Context(), b.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Listados ----------------------------------------------------------------

type objectXML struct {
	Key          string    `xml:"Key"`
	LastModified string    `xml:"LastModified"`
	ETag         string    `xml:"ETag"`
	Size         int64     `xml:"Size"`
	StorageClass string    `xml:"StorageClass"`
	Owner        *ownerXML `xml:"Owner,omitempty"`
}

type prefixXML struct {
	Prefix string `xml:"Prefix"`
}

func (s *Service) listObjects(w http.ResponseWriter, r *http.Request, b sqlcgen.S3Bucket) {
	q := r.URL.Query()
	v2 := q.Get("list-type") == "2"
	prefix, delim := q.Get("prefix"), q.Get("delimiter")
	limit := maxListKeys
	if v := q.Get("max-keys"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			s.fail(w, r, errInvalidArgument("max-keys inválido"))
			return
		}
		limit = min(n, maxListKeys)
	}
	after := q.Get("marker")
	if v2 {
		after = q.Get("start-after")
		if tok := q.Get("continuation-token"); tok != "" {
			raw, err := hex.DecodeString(tok)
			if err != nil {
				s.fail(w, r, errInvalidArgument("continuation-token inválido"))
				return
			}
			after = max(after, string(raw))
		}
	}
	var entries []listEntry
	truncated := false
	if limit > 0 {
		var err error
		if entries, truncated, err = s.list(r.Context(), b, prefix, delim, after, limit); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	enc := func(v string) string { return v }
	if q.Get("encoding-type") == "url" {
		enc = func(v string) string { return uriEncode(v, false) }
	}
	var objects []objectXML
	var prefixes []prefixXML
	for _, e := range entries {
		if e.prefix {
			prefixes = append(prefixes, prefixXML{Prefix: enc(e.key)})
			continue
		}
		o := objectXML{Key: enc(e.key), StorageClass: "STANDARD", ETag: `"d41d8cd98f00b204e9800998ecf8427e"`, LastModified: isoTime(time.Now().Unix())}
		if e.file != nil {
			o.Size, o.ETag, o.LastModified = e.file.Size, `"`+etag(*e.file)+`"`, isoTime(e.file.ModTime)
		}
		if !v2 || q.Get("fetch-owner") == "true" {
			o.Owner = &ownerXML{ID: "cloudmesh", DisplayName: "cloudmesh"}
		}
		objects = append(objects, o)
	}
	last := ""
	if len(entries) > 0 {
		last = entries[len(entries)-1].key
	}
	if v2 {
		res := struct {
			XMLName               xml.Name    `xml:"ListBucketResult"`
			NS                    string      `xml:"xmlns,attr"`
			Name                  string      `xml:"Name"`
			Prefix                string      `xml:"Prefix"`
			Delimiter             string      `xml:"Delimiter,omitempty"`
			MaxKeys               int         `xml:"MaxKeys"`
			EncodingType          string      `xml:"EncodingType,omitempty"`
			KeyCount              int         `xml:"KeyCount"`
			IsTruncated           bool        `xml:"IsTruncated"`
			ContinuationToken     string      `xml:"ContinuationToken,omitempty"`
			NextContinuationToken string      `xml:"NextContinuationToken,omitempty"`
			StartAfter            string      `xml:"StartAfter,omitempty"`
			Contents              []objectXML `xml:"Contents"`
			CommonPrefixes        []prefixXML `xml:"CommonPrefixes"`
		}{NS: s3NS, Name: b.Name, Prefix: enc(prefix), Delimiter: enc(delim), MaxKeys: limit,
			EncodingType: q.Get("encoding-type"), KeyCount: len(entries), IsTruncated: truncated,
			ContinuationToken: q.Get("continuation-token"), StartAfter: enc(q.Get("start-after")), Contents: objects, CommonPrefixes: prefixes}
		if truncated {
			res.NextContinuationToken = hex.EncodeToString([]byte(last))
		}
		writeXML(w, http.StatusOK, res)
		return
	}
	res := struct {
		XMLName        xml.Name    `xml:"ListBucketResult"`
		NS             string      `xml:"xmlns,attr"`
		Name           string      `xml:"Name"`
		Prefix         string      `xml:"Prefix"`
		Marker         string      `xml:"Marker"`
		NextMarker     string      `xml:"NextMarker,omitempty"`
		Delimiter      string      `xml:"Delimiter,omitempty"`
		MaxKeys        int         `xml:"MaxKeys"`
		EncodingType   string      `xml:"EncodingType,omitempty"`
		IsTruncated    bool        `xml:"IsTruncated"`
		Contents       []objectXML `xml:"Contents"`
		CommonPrefixes []prefixXML `xml:"CommonPrefixes"`
	}{NS: s3NS, Name: b.Name, Prefix: enc(prefix), Marker: enc(q.Get("marker")), Delimiter: enc(delim),
		MaxKeys: limit, EncodingType: q.Get("encoding-type"), IsTruncated: truncated, Contents: objects, CommonPrefixes: prefixes}
	if truncated {
		res.NextMarker = enc(last)
	}
	writeXML(w, http.StatusOK, res)
}

// ---- Objetos -----------------------------------------------------------------

func (s *Service) getObject(w http.ResponseWriter, r *http.Request, b sqlcgen.S3Bucket, key string) {
	if strings.HasSuffix(key, "/") {
		s.getDirMarker(w, r, b, key)
		return
	}
	f, err := s.lookup(r.Context(), b, key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	h := w.Header()
	h.Set("ETag", `"`+etag(f)+`"`)
	h.Set("Last-Modified", time.Unix(f.ModTime, 0).UTC().Format(http.TimeFormat))
	writeMeta(h, f.Meta)
	h.Set("Accept-Ranges", "bytes")
	ct := f.Mime
	if ct == "" {
		ct = "application/octet-stream"
	}
	h.Set("Content-Type", ct)
	// Como en S3, solo una petición firmada puede sustituir las cabeceras de la respuesta:
	// así los enlaces prefirmados fuerzan la descarga con un nombre, y un bucket público no.
	if !anonymous(r) {
		q := r.URL.Query()
		for param, name := range responseOverrides {
			if v := q.Get(param); v != "" {
				h.Set(name, v)
			}
		}
	}
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Trim(match, `"`) == etag(f) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	offset, length, partial, err := httputil.ParseRange(r.Header.Get("Range"), f.Size)
	if err != nil {
		h.Set("Content-Range", fmt.Sprintf("bytes */%d", f.Size))
		s.fail(w, r, errInvalidRange)
		return
	}
	if length < 0 {
		length = f.Size - offset
	}
	h.Set("Content-Length", strconv.FormatInt(length, 10))
	if partial {
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, f.Size))
	}
	if r.Method == http.MethodHead {
		if partial {
			w.WriteHeader(http.StatusPartialContent)
		}
		return
	}
	st, err := s.transfer.Open(r.Context(), f.ID, offset, length)
	if err != nil {
		h.Del("Content-Length")
		h.Del("Content-Range")
		s.fail(w, r, err)
		return
	}
	defer st.Body.Close()
	if partial {
		w.WriteHeader(http.StatusPartialContent)
	}
	if _, err := io.Copy(httputil.IdleWriter(w, transferIdle), st.Body); err != nil {
		s.log.Debug("s3: descarga cortada", "key", key, "err", err)
	}
}

// getDirMarker responde a «dir/» como un objeto vacío si la carpeta existe.
func (s *Service) getDirMarker(w http.ResponseWriter, r *http.Request, b sqlcgen.S3Bucket, key string) {
	k, err := parseKey(key)
	if err != nil {
		s.fail(w, r, errNoSuchKey)
		return
	}
	if _, err := s.folderFor(r.Context(), b.FolderID, k.dirs, false); err != nil {
		s.fail(w, r, errNoSuchKey)
		return
	}
	w.Header().Set("Content-Type", "application/x-directory")
	w.Header().Set("Content-Length", "0")
	w.Header().Set("ETag", `"d41d8cd98f00b204e9800998ecf8427e"`)
	w.WriteHeader(http.StatusOK)
}

func (s *Service) putObject(w http.ResponseWriter, r *http.Request, sr *signedRequest, key Key, b sqlcgen.S3Bucket, objKey string) {
	k, err := parseKey(objKey)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	body, size, err := sr.body(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if k.name == "" {
		// «dir/»: crea la carpeta. El cuerpo debe venir vacío.
		if size > 0 {
			s.fail(w, r, errInvalidArgument("una clave terminada en «/» es una carpeta y no admite contenido"))
			return
		}
		if _, err := s.folderFor(r.Context(), b.FolderID, k.dirs, true); err != nil {
			s.fail(w, r, err)
			return
		}
		w.Header().Set("ETag", `"d41d8cd98f00b204e9800998ecf8427e"`)
		w.WriteHeader(http.StatusOK)
		return
	}
	if size < 0 {
		s.fail(w, r, errMissingLength)
		return
	}
	if size > s.maxObject {
		s.fail(w, r, errTooLarge)
		return
	}
	mtime, meta, err := clientMeta(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	folder, err := s.folderFor(r.Context(), b.FolderID, k.dirs, true)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	f, err := s.transfer.UploadFor(r.Context(), s.owner(r.Context(), key), folder, k.name, size, httputil.IdleReader(w, body, transferIdle))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.keepMeta(r.Context(), f, mtime, meta)
	s.replaced(r.Context(), folder, k.name, f.ID)
	w.Header().Set("ETag", `"`+f.ETag+`"`)
	w.WriteHeader(http.StatusOK)
}

// copySource resuelve x-amz-copy-source («bucket/clave») y comprueba que la
// llave pueda leerlo.
func (s *Service) copySource(r *http.Request, key Key) (vfs.File, error) {
	src, err := url.PathUnescape(strings.TrimPrefix(r.Header.Get("X-Amz-Copy-Source"), "/"))
	if err != nil {
		return vfs.File{}, errInvalidArgument("x-amz-copy-source inválido")
	}
	src, _, _ = strings.Cut(src, "?versionId=")
	srcBucket, srcKey, ok := strings.Cut(src, "/")
	if !ok || srcKey == "" {
		return vfs.File{}, errInvalidArgument("x-amz-copy-source debe ser bucket/clave")
	}
	sb, err := s.bucket(r.Context(), srcBucket)
	if err != nil {
		return vfs.File{}, err
	}
	if !key.allowed(actRead, sb.ID) {
		return vfs.File{}, errAccessDenied
	}
	return s.lookup(r.Context(), sb, srcKey)
}

// copyObject copia en el servidor leyendo el origen y subiéndolo de nuevo.
func (s *Service) copyObject(w http.ResponseWriter, r *http.Request, key Key, dst sqlcgen.S3Bucket, dstKey string) {
	f, err := s.copySource(r, key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	k, err := parseKey(dstKey)
	if err != nil || k.name == "" {
		s.fail(w, r, errInvalidArgument("clave de destino inválida"))
		return
	}
	folder, err := s.folderFor(r.Context(), dst.FolderID, k.dirs, true)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	st, err := s.transfer.Open(r.Context(), f.ID, 0, -1)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer st.Body.Close()
	nf, err := s.transfer.UploadFor(r.Context(), s.owner(r.Context(), key), folder, k.name, f.Size, st.Body)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if strings.EqualFold(r.Header.Get("X-Amz-Metadata-Directive"), "REPLACE") {
		mtime, meta, err := clientMeta(r)
		if err == nil {
			s.keepMeta(r.Context(), nf, mtime, meta)
		}
	} else {
		s.keepMeta(r.Context(), nf, f.ModTime, f.Meta)
	}
	s.replaced(r.Context(), folder, k.name, nf.ID)
	writeXML(w, http.StatusOK, struct {
		XMLName      xml.Name `xml:"CopyObjectResult"`
		NS           string   `xml:"xmlns,attr"`
		LastModified string   `xml:"LastModified"`
		ETag         string   `xml:"ETag"`
	}{NS: s3NS, LastModified: isoTime(nf.ModTime), ETag: `"` + nf.ETag + `"`})
}

// deleteObject manda el objeto a la papelera; «dir/» borra la carpeta si está vacía.
// Como en S3, borrar lo que no existe no es un error.
func (s *Service) deleteObject(w http.ResponseWriter, r *http.Request, b sqlcgen.S3Bucket, key string) {
	if err := s.remove(r.Context(), b, key); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) remove(ctx context.Context, b sqlcgen.S3Bucket, key string) error {
	k, err := parseKey(key)
	if err != nil {
		return nil
	}
	if k.name == "" {
		id, err := s.folderFor(ctx, b.FolderID, k.dirs, false)
		if err != nil || id == b.FolderID {
			return nil
		}
		if err := s.vfs.DeleteFolder(ctx, id); err != nil && !errors.Is(err, vfs.ErrNotEmpty) && !errors.Is(err, vfs.ErrNotFound) {
			return err
		}
		return nil
	}
	f, err := s.lookup(ctx, b, key)
	if err != nil {
		return nil
	}
	if err := s.vfs.Trash(ctx, f.ID); err != nil {
		return err
	}
	s.prune(ctx, b, f.FolderID)
	return nil
}

// prune quita las carpetas que quedan vacías desde folderID hasta el bucket
// (excluido): en S3 un prefijo sin objetos deja de existir.
func (s *Service) prune(ctx context.Context, b sqlcgen.S3Bucket, folderID int64) {
	for folderID != b.FolderID && folderID != vfs.RootID {
		l, err := s.vfs.List(ctx, folderID)
		if err != nil || len(l.Folders) > 0 || len(l.Files) > 0 {
			return
		}
		if err := s.vfs.DeleteFolder(ctx, folderID); err != nil {
			return
		}
		folderID = l.Folder.ParentID
	}
}

func (s *Service) deleteObjects(w http.ResponseWriter, r *http.Request, b sqlcgen.S3Bucket) {
	var req struct {
		Quiet   bool `xml:"Quiet"`
		Objects []struct {
			Key string `xml:"Key"`
		} `xml:"Object"`
	}
	if err := xml.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req); err != nil || len(req.Objects) > 1000 {
		s.fail(w, r, errMalformedXML)
		return
	}
	type deleted struct {
		Key string `xml:"Key"`
	}
	type failed struct {
		Key     string `xml:"Key"`
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	out := struct {
		XMLName xml.Name  `xml:"DeleteResult"`
		NS      string    `xml:"xmlns,attr"`
		Deleted []deleted `xml:"Deleted"`
		Errors  []failed  `xml:"Error"`
	}{NS: s3NS}
	for _, o := range req.Objects {
		if err := s.remove(r.Context(), b, o.Key); err != nil {
			out.Errors = append(out.Errors, failed{Key: o.Key, Code: "InternalError", Message: err.Error()})
			continue
		}
		if !req.Quiet {
			out.Deleted = append(out.Deleted, deleted{Key: o.Key})
		}
	}
	writeXML(w, http.StatusOK, out)
}

// ---- Multipart ---------------------------------------------------------------

func (s *Service) createUpload(w http.ResponseWriter, r *http.Request, b sqlcgen.S3Bucket, key string) {
	k, err := parseKey(key)
	if err != nil || k.name == "" {
		s.fail(w, r, errInvalidArgument("clave inválida para una subida multipart"))
		return
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		s.fail(w, r, err)
		return
	}
	id := hex.EncodeToString(raw)
	mtime, meta, err := clientMeta(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.q.CreateUpload(r.Context(), sqlcgen.CreateUploadParams{ID: id, BucketID: b.ID, ObjectKey: key, CreatedAt: s.now().Unix(), Mtime: mtime, Meta: meta}); err != nil {
		s.fail(w, r, err)
		return
	}
	writeXML(w, http.StatusOK, struct {
		XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
		NS       string   `xml:"xmlns,attr"`
		Bucket   string   `xml:"Bucket"`
		Key      string   `xml:"Key"`
		UploadID string   `xml:"UploadId"`
	}{NS: s3NS, Bucket: b.Name, Key: key, UploadID: id})
}

func (s *Service) upload(ctx context.Context, b sqlcgen.S3Bucket, key, id string) (sqlcgen.S3Upload, error) {
	u, err := s.q.GetUpload(ctx, id)
	if err != nil || u.BucketID != b.ID || (key != "" && u.ObjectKey != key) {
		return u, errNoSuchUpload
	}
	return u, nil
}

func (s *Service) uploadPart(w http.ResponseWriter, r *http.Request, sr *signedRequest, key Key, b sqlcgen.S3Bucket, objKey, id, num string) {
	u, err := s.upload(r.Context(), b, objKey, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	n, err := partNumber(num)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	body, size, err := sr.body(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if size < 0 {
		s.fail(w, r, errMissingLength)
		return
	}
	if size > s.maxObject {
		s.fail(w, r, errTooLarge)
		return
	}
	f, err := s.storePart(r.Context(), key, b, u, n, size, httputil.IdleReader(w, body, transferIdle))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("ETag", `"`+f.ETag+`"`)
	w.WriteHeader(http.StatusOK)
}

// partNumber valida el número de parte de la query.
func partNumber(num string) (int, error) {
	n, err := strconv.Atoi(num)
	if err != nil || n < 1 || n > maxParts {
		return 0, errInvalidArgument("partNumber debe estar entre 1 y 10000")
	}
	return n, nil
}

// storePart sube una parte como archivo interno y la registra. Reenviar una
// parte la sustituye; la anterior se destruye.
func (s *Service) storePart(ctx context.Context, key Key, b sqlcgen.S3Bucket, u sqlcgen.S3Upload, n int, size int64, body io.Reader) (vfs.File, error) {
	f, err := s.transfer.UploadPart(ctx, s.owner(ctx, key), b.FolderID, fmt.Sprintf("%s.%05d.part", u.ID, n), size, body)
	if err != nil {
		return f, err
	}
	old, oldErr := s.q.GetUploadPart(ctx, sqlcgen.GetUploadPartParams{UploadID: u.ID, PartNumber: int64(n)})
	if err := s.q.PutUploadPart(ctx, sqlcgen.PutUploadPartParams{
		UploadID: u.ID, PartNumber: int64(n), FileID: f.ID, Size: size, Etag: f.ETag, CreatedAt: s.now().Unix(),
	}); err != nil {
		_ = s.transfer.Delete(context.WithoutCancel(ctx), f.ID)
		return f, err
	}
	if oldErr == nil {
		_ = s.transfer.Delete(context.WithoutCancel(ctx), old.FileID)
	}
	return f, nil
}

// uploadPartCopy crea una parte con un rango de otro objeto (x-amz-copy-source
// y, opcional, x-amz-copy-source-range «bytes=a-b»).
func (s *Service) uploadPartCopy(w http.ResponseWriter, r *http.Request, key Key, b sqlcgen.S3Bucket, objKey, id, num string) {
	u, err := s.upload(r.Context(), b, objKey, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	n, err := partNumber(num)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	src, err := s.copySource(r, key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	offset, length := int64(0), src.Size
	if rg := r.Header.Get("X-Amz-Copy-Source-Range"); rg != "" {
		// A diferencia de Range, aquí S3 exige un rango cerrado dentro del objeto.
		a, b2, ok := strings.Cut(strings.TrimPrefix(rg, "bytes="), "-")
		first, err1 := strconv.ParseInt(a, 10, 64)
		last, err2 := strconv.ParseInt(b2, 10, 64)
		if !strings.HasPrefix(rg, "bytes=") || !ok || err1 != nil || err2 != nil || first < 0 || last < first || last >= src.Size {
			s.fail(w, r, errInvalidArgument("x-amz-copy-source-range debe ser bytes=inicio-fin dentro del objeto"))
			return
		}
		offset, length = first, last-first+1
	}
	if length > s.maxObject {
		s.fail(w, r, errTooLarge)
		return
	}
	st, err := s.transfer.Open(r.Context(), src.ID, offset, length)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer st.Body.Close()
	f, err := s.storePart(r.Context(), key, b, u, n, length, st.Body)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeXML(w, http.StatusOK, struct {
		XMLName      xml.Name `xml:"CopyPartResult"`
		NS           string   `xml:"xmlns,attr"`
		LastModified string   `xml:"LastModified"`
		ETag         string   `xml:"ETag"`
	}{NS: s3NS, LastModified: isoTime(f.ModTime), ETag: `"` + f.ETag + `"`})
}

func (s *Service) completeUpload(w http.ResponseWriter, r *http.Request, sr *signedRequest, key Key, b sqlcgen.S3Bucket, objKey, id string) {
	u, err := s.upload(r.Context(), b, objKey, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var req struct {
		Parts []struct {
			PartNumber int64  `xml:"PartNumber"`
			ETag       string `xml:"ETag"`
		} `xml:"Part"`
	}
	body, _, err := sr.body(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := xml.NewDecoder(io.LimitReader(body, 4<<20)).Decode(&req); err != nil || len(req.Parts) == 0 {
		s.fail(w, r, errMalformedXML)
		return
	}
	uploaded, err := s.q.ListUploadParts(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	byNum := map[int64]sqlcgen.S3UploadPart{}
	for _, p := range uploaded {
		byNum[p.PartNumber] = p
	}
	parts := make([]transfer.Part, 0, len(req.Parts))
	sums := md5.New()
	used := map[int64]bool{}
	for i, p := range req.Parts {
		if i > 0 && p.PartNumber <= req.Parts[i-1].PartNumber {
			s.fail(w, r, errInvalidPartOrder)
			return
		}
		up, ok := byNum[p.PartNumber]
		if !ok || strings.Trim(p.ETag, `" `) != up.Etag {
			s.fail(w, r, errInvalidPart)
			return
		}
		if i < len(req.Parts)-1 && up.Size < minPartSize {
			s.fail(w, r, errTooSmall)
			return
		}
		raw, _ := hex.DecodeString(up.Etag)
		sums.Write(raw)
		used[up.FileID] = true
		parts = append(parts, transfer.Part{Number: p.PartNumber, FileID: up.FileID, Size: up.Size})
	}
	k, err := parseKey(u.ObjectKey)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	folder, err := s.folderFor(r.Context(), b.FolderID, k.dirs, true)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tag := fmt.Sprintf("%s-%d", hex.EncodeToString(sums.Sum(nil)), len(parts))
	f, err := s.transfer.Compose(r.Context(), key.createdBy, folder, k.name, parts, tag)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.keepMeta(r.Context(), f, u.Mtime, u.Meta)
	// Las partes usadas pasan al objeto; las que sobran se destruyen.
	if err := s.q.DeleteUpload(r.Context(), u.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	for _, p := range uploaded {
		if !used[p.FileID] {
			_ = s.transfer.Delete(context.WithoutCancel(r.Context()), p.FileID)
		}
	}
	s.replaced(r.Context(), folder, k.name, f.ID)
	writeXML(w, http.StatusOK, struct {
		XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
		NS       string   `xml:"xmlns,attr"`
		Location string   `xml:"Location"`
		Bucket   string   `xml:"Bucket"`
		Key      string   `xml:"Key"`
		ETag     string   `xml:"ETag"`
	}{NS: s3NS, Location: "/" + b.Name + "/" + u.ObjectKey, Bucket: b.Name, Key: u.ObjectKey, ETag: `"` + tag + `"`})
}

func (s *Service) abortUpload(w http.ResponseWriter, r *http.Request, b sqlcgen.S3Bucket, id string) {
	if _, err := s.upload(r.Context(), b, "", id); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.Abort(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Abort cancela una subida multipart y destruye sus partes.
func (s *Service) Abort(ctx context.Context, id string) error {
	parts, err := s.q.ListUploadParts(ctx, id)
	if err != nil {
		return err
	}
	if err := s.q.DeleteUpload(ctx, id); err != nil {
		return err
	}
	for _, p := range parts {
		if err := s.transfer.Delete(ctx, p.FileID); err != nil && !errors.Is(err, vfs.ErrFileNotFound) {
			s.log.Warn("s3: borrar parte", "upload", id, "file", p.FileID, "err", err)
		}
	}
	return nil
}

func (s *Service) listParts(w http.ResponseWriter, r *http.Request, b sqlcgen.S3Bucket, key, id string) {
	u, err := s.upload(r.Context(), b, key, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	parts, err := s.q.ListUploadParts(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	type partXML struct {
		PartNumber   int64  `xml:"PartNumber"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
		Size         int64  `xml:"Size"`
	}
	out := struct {
		XMLName     xml.Name  `xml:"ListPartsResult"`
		NS          string    `xml:"xmlns,attr"`
		Bucket      string    `xml:"Bucket"`
		Key         string    `xml:"Key"`
		UploadID    string    `xml:"UploadId"`
		IsTruncated bool      `xml:"IsTruncated"`
		Parts       []partXML `xml:"Part"`
	}{NS: s3NS, Bucket: b.Name, Key: u.ObjectKey, UploadID: u.ID}
	for _, p := range parts {
		out.Parts = append(out.Parts, partXML{PartNumber: p.PartNumber, LastModified: isoTime(p.CreatedAt), ETag: `"` + p.Etag + `"`, Size: p.Size})
	}
	writeXML(w, http.StatusOK, out)
}

func (s *Service) listUploads(w http.ResponseWriter, r *http.Request, b sqlcgen.S3Bucket) {
	ups, err := s.q.ListUploads(r.Context(), b.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	prefix := r.URL.Query().Get("prefix")
	type uploadXML struct {
		Key       string `xml:"Key"`
		UploadID  string `xml:"UploadId"`
		Initiated string `xml:"Initiated"`
	}
	out := struct {
		XMLName     xml.Name    `xml:"ListMultipartUploadsResult"`
		NS          string      `xml:"xmlns,attr"`
		Bucket      string      `xml:"Bucket"`
		IsTruncated bool        `xml:"IsTruncated"`
		Uploads     []uploadXML `xml:"Upload"`
	}{NS: s3NS, Bucket: b.Name}
	sort.Slice(ups, func(i, j int) bool { return ups[i].ObjectKey < ups[j].ObjectKey })
	for _, u := range ups {
		if strings.HasPrefix(u.ObjectKey, prefix) {
			out.Uploads = append(out.Uploads, uploadXML{Key: u.ObjectKey, UploadID: u.ID, Initiated: isoTime(u.CreatedAt)})
		}
	}
	writeXML(w, http.StatusOK, out)
}

// CleanUp aplica el ciclo de vida de los buckets, aborta subidas abandonadas y
// destruye partes huérfanas (una caída a mitad de subida, o un restore que
// trajo partes sin su subida). La llama el mantenimiento.
func (s *Service) CleanUp(ctx context.Context, staleUploads time.Duration) (int, error) {
	n, err := s.ApplyLifecycle(ctx)
	if err != nil {
		return n, err
	}
	ids, err := s.q.ListStaleUploads(ctx, s.now().Add(-staleUploads).Unix())
	if err != nil {
		return n, err
	}
	for _, id := range ids {
		if err := s.Abort(ctx, id); err != nil {
			return n, err
		}
		n++
	}
	orphans, err := s.q.ListOrphanInternalFiles(ctx, s.now().Add(-24*time.Hour).Unix())
	if err != nil {
		return n, err
	}
	for _, id := range orphans {
		if err := s.transfer.Delete(ctx, id); err != nil && !errors.Is(err, vfs.ErrFileNotFound) {
			return n, err
		}
		n++
	}
	return n, nil
}

// maxMetaBytes es el límite de S3 para los metadatos de usuario.
const maxMetaBytes = 2 << 10

// clientMeta recoge las cabeceras x-amz-meta-* y, si viene x-amz-meta-mtime
// (rclone, en segundos con decimales), la fecha original del archivo.
func clientMeta(r *http.Request) (mtime int64, meta string, err error) {
	m := map[string]string{}
	size := 0
	for name, vals := range r.Header {
		if k, ok := strings.CutPrefix(strings.ToLower(name), "x-amz-meta-"); ok && k != "" {
			m[k] = strings.Join(vals, ",")
			size += len(k) + len(m[k])
		}
	}
	if len(m) == 0 {
		return 0, "", nil
	}
	if size > maxMetaBytes {
		return 0, "", &Error{http.StatusBadRequest, "MetadataTooLarge", "los metadatos x-amz-meta-* superan 2 KB"}
	}
	if f, err := strconv.ParseFloat(m["mtime"], 64); err == nil && f > 0 && f < 1e11 {
		mtime = int64(f)
	}
	raw, err := json.Marshal(m)
	return mtime, string(raw), err
}

// writeMeta devuelve los metadatos de usuario tal como llegaron.
func writeMeta(h http.Header, meta string) {
	if meta == "" {
		return
	}
	var m map[string]string
	if json.Unmarshal([]byte(meta), &m) != nil {
		return
	}
	for k, v := range m {
		h.Set("X-Amz-Meta-"+k, v)
	}
}

func isoTime(unix int64) string { return time.Unix(unix, 0).UTC().Format("2006-01-02T15:04:05.000Z") }

package s3

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Firma AWS Signature Version 4 tal como la usa S3: en cabecera
// (Authorization) o en la URL (prefirmada), con el cuerpo firmado entero, sin
// firmar o en trozos (aws-chunked).

const (
	sigAlgorithm   = "AWS4-HMAC-SHA256"
	amzDateFormat  = "20060102T150405Z"
	maxClockSkew   = 15 * time.Minute
	maxPresignTime = 7 * 24 * time.Hour

	payloadUnsigned       = "UNSIGNED-PAYLOAD"
	payloadStreamSigned   = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	payloadStreamSignedTr = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"
	payloadStreamUnsigned = "STREAMING-UNSIGNED-PAYLOAD-TRAILER"
	emptySHA256           = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// signedRequest es una petición con la firma ya comprobada.
type signedRequest struct {
	accessKey  string
	amzDate    string
	scope      string // fecha/región/s3/aws4_request
	signingKey []byte
	signature  string // firma de la petición; semilla de los trozos en aws-chunked
	payload    string // valor de x-amz-content-sha256 (o UNSIGNED-PAYLOAD si es prefirmada)
}

// lookupSecret devuelve el secreto de una llave de acceso.
type lookupSecret func(accessKey string) (string, error)

// verify comprueba la firma de r. now se inyecta para los tests.
func verify(r *http.Request, lookup lookupSecret, now time.Time) (*signedRequest, error) {
	if q := r.URL.Query(); q.Get("X-Amz-Algorithm") != "" {
		return verifyPresigned(r, q, lookup, now)
	}
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return nil, errAccessDenied
	}
	alg, rest, _ := strings.Cut(auth, " ")
	if alg != sigAlgorithm {
		return nil, errNotImplemented("solo se admite AWS4-HMAC-SHA256")
	}
	fields := map[string]string{}
	for _, part := range strings.Split(rest, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			fields[k] = v
		}
	}
	cred, signedHeaders, signature := fields["Credential"], fields["SignedHeaders"], fields["Signature"]
	if cred == "" || signedHeaders == "" || signature == "" {
		return nil, errAuthMalformed
	}
	amzDate := r.Header.Get("X-Amz-Date")
	if amzDate == "" {
		amzDate = r.Header.Get("Date")
	}
	t, err := time.Parse(amzDateFormat, amzDate)
	if err != nil {
		return nil, errAuthMalformed
	}
	if d := now.Sub(t); d > maxClockSkew || d < -maxClockSkew {
		return nil, errTimeSkewed
	}
	payload := r.Header.Get("X-Amz-Content-Sha256")
	if payload == "" {
		return nil, errInvalidArgument("falta x-amz-content-sha256")
	}
	sr, err := checkSignature(r, lookup, cred, amzDate, strings.Split(signedHeaders, ";"), signature, payload, nil)
	if err != nil {
		return nil, err
	}
	sr.payload = payload
	return sr, nil
}

func verifyPresigned(r *http.Request, q url.Values, lookup lookupSecret, now time.Time) (*signedRequest, error) {
	if q.Get("X-Amz-Algorithm") != sigAlgorithm {
		return nil, errNotImplemented("solo se admite AWS4-HMAC-SHA256")
	}
	amzDate := q.Get("X-Amz-Date")
	t, err := time.Parse(amzDateFormat, amzDate)
	if err != nil {
		return nil, errAuthMalformed
	}
	expires, err := strconv.Atoi(q.Get("X-Amz-Expires"))
	if err != nil || expires < 1 || time.Duration(expires)*time.Second > maxPresignTime {
		return nil, errAuthMalformed
	}
	if now.Before(t.Add(-maxClockSkew)) {
		return nil, errTimeSkewed
	}
	if now.After(t.Add(time.Duration(expires) * time.Second)) {
		return nil, &Error{Status: http.StatusForbidden, Code: "AccessDenied", Message: "la URL prefirmada caducó"}
	}
	signedHeaders := q.Get("X-Amz-SignedHeaders")
	if signedHeaders == "" {
		return nil, errAuthMalformed
	}
	sr, err := checkSignature(r, lookup, q.Get("X-Amz-Credential"), amzDate, strings.Split(signedHeaders, ";"),
		q.Get("X-Amz-Signature"), payloadUnsigned, map[string]bool{"X-Amz-Signature": true})
	if err != nil {
		return nil, err
	}
	sr.payload = payloadUnsigned
	return sr, nil
}

// checkSignature reconstruye la petición canónica y compara la firma.
func checkSignature(r *http.Request, lookup lookupSecret, cred, amzDate string, signedHeaders []string, signature, payload string, skipQuery map[string]bool) (*signedRequest, error) {
	parts := strings.Split(cred, "/")
	if len(parts) != 5 || parts[3] != "s3" || parts[4] != "aws4_request" || len(parts[1]) != 8 || !strings.HasPrefix(amzDate, parts[1]) {
		return nil, errAuthMalformed
	}
	accessKey, date, region := parts[0], parts[1], parts[2]
	secret, err := lookup(accessKey)
	if err != nil {
		return nil, err
	}
	scope := date + "/" + region + "/s3/aws4_request"
	key := signingKey(secret, date, region)
	matches := func(r *http.Request) bool {
		canon := canonicalRequest(r, signedHeaders, payload, skipQuery)
		sum := sha256.Sum256([]byte(canon))
		toSign := sigAlgorithm + "\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
		want := hex.EncodeToString(hmacSHA256(key, toSign))
		return subtle.ConstantTimeCompare([]byte(want), []byte(signature)) == 1
	}
	if matches(r) {
		return &signedRequest{accessKey: accessKey, amzDate: amzDate, scope: scope, signingKey: key, signature: signature}, nil
	}
	if slices.Contains(signedHeaders, "accept-encoding") {
		for _, enc := range signedEncodings {
			if r.Header.Get("Accept-Encoding") == enc {
				continue
			}
			rr := *r
			rr.Header = r.Header.Clone()
			rr.Header.Set("Accept-Encoding", enc)
			if matches(&rr) {
				return &signedRequest{accessKey: accessKey, amzDate: amzDate, scope: scope, signingKey: key, signature: signature}, nil
			}
		}
	}
	return nil, errSignatureMismatch
}

// signedEncodings son los valores de accept-encoding que firma rclone
// (identity en general, gzip en GetObject). Cloudflare reescribe la cabecera
// («gzip, br») antes de llegar al origen, así que si la firma no cuadra se
// prueba con ellos. No afecta a lo que se lee ni se escribe: aceptar la firma
// original es seguro.
var signedEncodings = []string{"identity", "gzip"}

func canonicalRequest(r *http.Request, signedHeaders []string, payload string, skipQuery map[string]bool) string {
	var b strings.Builder
	b.WriteString(r.Method)
	b.WriteByte('\n')
	b.WriteString(uriEncode(r.URL.Path, false))
	b.WriteByte('\n')
	b.WriteString(canonicalQuery(r.URL.RawQuery, skipQuery))
	b.WriteByte('\n')
	for _, h := range signedHeaders {
		b.WriteString(h)
		b.WriteByte(':')
		b.WriteString(headerValue(r, h))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(strings.Join(signedHeaders, ";"))
	b.WriteByte('\n')
	b.WriteString(payload)
	return b.String()
}

func headerValue(r *http.Request, name string) string {
	if name == "host" {
		return r.Host
	}
	vals := r.Header.Values(name)
	// El cliente CRT de aws-cli firma «content-length: 0» en GET y HEAD, y un
	// proxy inverso (Traefik) no reenvía esa cabecera si no hay cuerpo.
	if name == "content-length" && len(vals) == 0 && r.ContentLength == 0 {
		return "0"
	}
	for i, v := range vals {
		vals[i] = strings.Join(strings.Fields(v), " ")
	}
	return strings.Join(vals, ",")
}

func canonicalQuery(raw string, skip map[string]bool) string {
	type kv struct{ k, v string }
	var pairs []kv
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		k, _ = url.QueryUnescape(k)
		v, _ = url.QueryUnescape(v)
		if skip[k] {
			continue
		}
		pairs = append(pairs, kv{uriEncode(k, true), uriEncode(v, true)})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.k + "=" + p.v
	}
	return strings.Join(out, "&")
}

// uriEncode codifica todo salvo los caracteres no reservados de RFC 3986; la
// barra se respeta en rutas y se codifica en la query.
func uriEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func signingKey(secret, date, region string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, "s3")
	return hmacSHA256(k, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// body devuelve el cuerpo ya desempaquetado y verificado según el tipo de firma,
// y su tamaño real (-1 si no se conoce).
func (sr *signedRequest) body(r *http.Request) (io.Reader, int64, error) {
	switch sr.payload {
	case payloadUnsigned:
		return r.Body, r.ContentLength, nil
	case payloadStreamSigned, payloadStreamSignedTr, payloadStreamUnsigned:
		n, err := strconv.ParseInt(r.Header.Get("X-Amz-Decoded-Content-Length"), 10, 64)
		if err != nil || n < 0 {
			return nil, 0, &Error{Status: http.StatusLengthRequired, Code: "MissingContentLength", Message: "falta x-amz-decoded-content-length"}
		}
		cr := &chunkedReader{r: bufio.NewReaderSize(r.Body, 64<<10), signed: sr.payload != payloadStreamUnsigned, sr: sr, prev: sr.signature}
		return cr, n, nil
	default:
		if len(sr.payload) != 64 {
			return nil, 0, errInvalidArgument("x-amz-content-sha256 inválido")
		}
		return &hashCheckReader{r: r.Body, h: sha256.New(), want: sr.payload}, r.ContentLength, nil
	}
}

// hashCheckReader falla al llegar al final si el SHA-256 del cuerpo no es el firmado.
type hashCheckReader struct {
	r    io.Reader
	h    hash.Hash
	want string
}

func (c *hashCheckReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.h.Write(p[:n])
	if err == io.EOF && hex.EncodeToString(c.h.Sum(nil)) != c.want {
		return n, errContentSHA256Mismatch
	}
	return n, err
}

// chunkedReader desempaqueta aws-chunked: «tamaño-hex[;chunk-signature=…]\r\n
// datos\r\n» hasta un trozo de tamaño 0, opcionalmente seguido de trailers.
// Con firma, cada trozo encadena su firma con la anterior; un trozo alterado
// falla al terminar de leerlo y la subida se descarta.
type chunkedReader struct {
	r      *bufio.Reader
	signed bool
	sr     *signedRequest
	prev   string // firma anterior (la semilla para el primero)

	left int64     // bytes que quedan del trozo actual
	sig  string    // firma declarada del trozo actual
	h    hash.Hash // SHA-256 del trozo actual
	done bool
}

const maxChunkHeader = 4 << 10

func (c *chunkedReader) Read(p []byte) (int, error) {
	if c.done {
		return 0, io.EOF
	}
	if c.left == 0 {
		if err := c.nextChunk(); err != nil {
			return 0, err
		}
		if c.done {
			return 0, io.EOF
		}
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	c.h.Write(p[:n])
	if err == io.EOF {
		return n, io.ErrUnexpectedEOF
	}
	if err != nil {
		return n, err
	}
	if c.left == 0 {
		if err := c.endChunk(); err != nil {
			return n, err
		}
	}
	return n, nil
}

// nextChunk lee la cabecera del siguiente trozo; con tamaño 0 consume los trailers.
func (c *chunkedReader) nextChunk() error {
	line, err := c.readLine()
	if err != nil {
		return err
	}
	sizeHex, ext, _ := strings.Cut(line, ";")
	size, err := strconv.ParseInt(strings.TrimSpace(sizeHex), 16, 64)
	if err != nil || size < 0 {
		return errIncompleteBody
	}
	c.sig = ""
	if c.signed {
		sig, ok := strings.CutPrefix(strings.TrimSpace(ext), "chunk-signature=")
		if !ok || len(sig) != 64 {
			return errSignatureMismatch
		}
		c.sig = sig
	}
	c.h = sha256.New()
	c.left = size
	if size > 0 {
		return nil
	}
	// Último trozo: su firma cubre un cuerpo vacío.
	if err := c.checkChunkSig(); err != nil {
		return err
	}
	for {
		trailer, err := c.readLine()
		if err != nil {
			return err
		}
		if trailer == "" {
			break
		}
	}
	c.done = true
	return nil
}

func (c *chunkedReader) endChunk() error {
	crlf := make([]byte, 2)
	if _, err := io.ReadFull(c.r, crlf); err != nil || !bytes.Equal(crlf, []byte("\r\n")) {
		return errIncompleteBody
	}
	return c.checkChunkSig()
}

func (c *chunkedReader) checkChunkSig() error {
	if !c.signed {
		return nil
	}
	toSign := "AWS4-HMAC-SHA256-PAYLOAD\n" + c.sr.amzDate + "\n" + c.sr.scope + "\n" + c.prev + "\n" + emptySHA256 + "\n" + hex.EncodeToString(c.h.Sum(nil))
	want := hex.EncodeToString(hmacSHA256(c.sr.signingKey, toSign))
	if subtle.ConstantTimeCompare([]byte(want), []byte(c.sig)) != 1 {
		return errSignatureMismatch
	}
	c.prev = c.sig
	return nil
}

func (c *chunkedReader) readLine() (string, error) {
	line, err := c.r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(line) > maxChunkHeader {
		return "", errIncompleteBody
	}
	if err != nil {
		if err == io.EOF {
			return "", io.ErrUnexpectedEOF
		}
		return "", err
	}
	return strings.TrimRight(string(line), "\r\n"), nil
}

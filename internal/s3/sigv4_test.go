package s3

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Ejemplos de la documentación de AWS «Signature Calculations for the
// Authorization Header» y «Query String Authentication» para S3.
const (
	exampleAccessKey = "AKIAIOSFODNN7EXAMPLE"
	exampleSecret    = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

var exampleTime = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)

func exampleLookup(ak string) (string, error) {
	if ak != exampleAccessKey {
		return "", errInvalidAccessKey
	}
	return exampleSecret, nil
}

func TestVerifyHeaderAWSExample(t *testing.T) {
	r := httptest.NewRequest("GET", "http://examplebucket.s3.amazonaws.com/test.txt", nil)
	r.Header.Set("Range", "bytes=0-9")
	r.Header.Set("X-Amz-Content-Sha256", emptySHA256)
	r.Header.Set("X-Amz-Date", "20130524T000000Z")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request,"+
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date,"+
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41")
	if _, err := verify(r, exampleLookup, exampleTime); err != nil {
		t.Fatalf("firma de ejemplo rechazada: %v", err)
	}
	r.Header.Set("Range", "bytes=0-10")
	if _, err := verify(r, exampleLookup, exampleTime); !errors.Is(err, errSignatureMismatch) {
		t.Fatalf("cabecera alterada = %v", err)
	}
	r.Header.Set("Range", "bytes=0-9")
	if _, err := verify(r, exampleLookup, exampleTime.Add(time.Hour)); !errors.Is(err, errTimeSkewed) {
		t.Fatalf("reloj desfasado = %v", err)
	}
}

// aws-cli (CRT) firma «content-length: 0» en un GET; tras un proxy la cabecera
// llega sin valor y la firma debe seguir valiendo.
func TestVerifyContentLengthDroppedByProxy(t *testing.T) {
	r := httptest.NewRequest("GET", "http://localhost:9000/b/k.txt", nil)
	r.Header.Set("Content-Length", "0")
	r.Header.Set("X-Amz-Content-Sha256", payloadUnsigned)
	r.Header.Set("X-Amz-Date", "20130524T000000Z")
	signed := []string{"content-length", "host", "x-amz-content-sha256", "x-amz-date"}
	canon := canonicalRequest(r, signed, payloadUnsigned, nil)
	sum := sha256.Sum256([]byte(canon))
	scope := "20130524/auto/s3/aws4_request"
	sig := hex.EncodeToString(hmacSHA256(signingKey(exampleSecret, "20130524", "auto"), sigAlgorithm+"\n20130524T000000Z\n"+scope+"\n"+hex.EncodeToString(sum[:])))
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+exampleAccessKey+"/"+scope+",SignedHeaders="+strings.Join(signed, ";")+",Signature="+sig)
	r.Header.Del("Content-Length")
	if _, err := verify(r, exampleLookup, exampleTime); err != nil {
		t.Fatalf("GET sin content-length tras proxy: %v", err)
	}
}

// rclone firma «accept-encoding: identity» y Cloudflare la reescribe antes de
// llegar al origen; la firma debe seguir valiendo, pero no con otro valor.
func TestVerifyAcceptEncodingRewrittenByProxy(t *testing.T) {
	sign := func(enc string) *http.Request {
		r := httptest.NewRequest("GET", "http://localhost:9000/", nil)
		r.Header.Set("Accept-Encoding", enc)
		r.Header.Set("X-Amz-Content-Sha256", emptySHA256)
		r.Header.Set("X-Amz-Date", "20130524T000000Z")
		signed := []string{"accept-encoding", "host", "x-amz-content-sha256", "x-amz-date"}
		canon := canonicalRequest(r, signed, emptySHA256, nil)
		sum := sha256.Sum256([]byte(canon))
		scope := "20130524/auto/s3/aws4_request"
		sig := hex.EncodeToString(hmacSHA256(signingKey(exampleSecret, "20130524", "auto"), sigAlgorithm+"\n20130524T000000Z\n"+scope+"\n"+hex.EncodeToString(sum[:])))
		r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+exampleAccessKey+"/"+scope+",SignedHeaders="+strings.Join(signed, ";")+",Signature="+sig)
		return r
	}
	r := sign("identity")
	r.Header.Set("Accept-Encoding", "gzip, br")
	if _, err := verify(r, exampleLookup, exampleTime); err != nil {
		t.Fatalf("accept-encoding reescrito por el proxy: %v", err)
	}
	if got := r.Header.Get("Accept-Encoding"); got != "gzip, br" {
		t.Fatalf("verify cambió la petición: accept-encoding = %q", got)
	}
	// GetObject de rclone firma «gzip».
	r = sign("gzip")
	r.Header.Set("Accept-Encoding", "gzip, br")
	if _, err := verify(r, exampleLookup, exampleTime); err != nil {
		t.Fatalf("accept-encoding gzip reescrito por el proxy: %v", err)
	}
	r = sign("br")
	r.Header.Set("Accept-Encoding", "gzip")
	if _, err := verify(r, exampleLookup, exampleTime); !errors.Is(err, errSignatureMismatch) {
		t.Fatalf("firma con otro accept-encoding = %v", err)
	}
}

func TestVerifyPresignedAWSExample(t *testing.T) {
	raw := "http://examplebucket.s3.amazonaws.com/test.txt?X-Amz-Algorithm=AWS4-HMAC-SHA256" +
		"&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request" +
		"&X-Amz-Date=20130524T000000Z&X-Amz-Expires=86400&X-Amz-SignedHeaders=host" +
		"&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	if _, err := verify(httptest.NewRequest("GET", raw, nil), exampleLookup, exampleTime.Add(time.Hour)); err != nil {
		t.Fatalf("URL de ejemplo rechazada: %v", err)
	}
	if _, err := verify(httptest.NewRequest("GET", raw, nil), exampleLookup, exampleTime.Add(25*time.Hour)); err == nil {
		t.Fatal("URL caducada aceptada")
	}
	// Una URL firmada por nosotros pasa la verificación.
	u, err := presign("GET", "http://localhost:9000/b/a%20b+c.txt", exampleAccessKey, exampleSecret, "auto", time.Hour, exampleTime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verify(httptest.NewRequest("GET", u, nil), exampleLookup, exampleTime); err != nil {
		t.Fatalf("presign propio: %v", err)
	}
}

// Ejemplo «Signature Calculation: Transferring Payload in Multiple Chunks».
func TestChunkedAWSExample(t *testing.T) {
	chunk := func(n int, sig string) string {
		return fmt.Sprintf("%x;chunk-signature=%s\r\n%s\r\n", n, sig, strings.Repeat("a", n))
	}
	body := chunk(65536, "ad80c730a21e5b8d04586a2213dd63b9a0e99e0e2307b0ade35a65485a288648") +
		chunk(1024, "0055627c9e194cb4542bae2aa5492e3c1575bbb81b612b7d234b86a503ef5497") +
		"0;chunk-signature=b6c6ea8a5354eaf15b3cb7646744f4275b71ea724fed81ceb9323e279d449df9\r\n\r\n"
	r := httptest.NewRequest("PUT", "http://s3.amazonaws.com/examplebucket/chunkObject.txt", strings.NewReader(body))
	r.Header.Set("X-Amz-Date", "20130524T000000Z")
	r.Header.Set("X-Amz-Storage-Class", "REDUCED_REDUNDANCY")
	r.Header.Set("X-Amz-Content-Sha256", payloadStreamSigned)
	r.Header.Set("Content-Encoding", "aws-chunked")
	r.Header.Set("X-Amz-Decoded-Content-Length", "66560")
	r.Header.Set("Content-Length", "66824")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request,"+
		"SignedHeaders=content-encoding;content-length;host;x-amz-content-sha256;x-amz-date;x-amz-decoded-content-length;x-amz-storage-class,"+
		"Signature=4f232c4386841ef735655705268965c44a0e4690baa4adea153f7db9fa80a0a9")
	sr, err := verify(r, exampleLookup, exampleTime)
	if err != nil {
		t.Fatalf("semilla: %v", err)
	}
	rd, n, err := sr.body(r)
	if err != nil || n != 66560 {
		t.Fatalf("body = %d %v", n, err)
	}
	got, err := io.ReadAll(rd)
	if err != nil || !bytes.Equal(got, bytes.Repeat([]byte("a"), 66560)) {
		t.Fatalf("contenido = %d bytes, %v", len(got), err)
	}

	// Un byte cambiado en el segundo trozo rompe su firma.
	bad := strings.Replace(body, strings.Repeat("a", 1024)+"\r\n0;", strings.Repeat("a", 1023)+"b\r\n0;", 1)
	r.Body = io.NopCloser(strings.NewReader(bad))
	rd, _, _ = sr.body(r)
	if _, err := io.ReadAll(rd); !errors.Is(err, errSignatureMismatch) {
		t.Fatalf("trozo alterado = %v", err)
	}
}

func TestChunkedUnsignedTrailer(t *testing.T) {
	body := "5\r\nhola \r\n6\r\nmundo!\r\n0\r\nx-amz-checksum-crc32:AAAAAA==\r\n\r\n"
	r := httptest.NewRequest("PUT", "http://h/b/k", strings.NewReader(body))
	r.Header.Set("X-Amz-Decoded-Content-Length", "11")
	sr := &signedRequest{payload: payloadStreamUnsigned}
	rd, n, err := sr.body(r)
	if err != nil || n != 11 {
		t.Fatal(n, err)
	}
	got, err := io.ReadAll(rd)
	if err != nil || string(got) != "hola mundo!" {
		t.Fatalf("%q %v", got, err)
	}
	r = httptest.NewRequest("PUT", "http://h/b/k", strings.NewReader("5\r\nhola"))
	r.Header.Set("X-Amz-Decoded-Content-Length", "5")
	rd, _, _ = sr.body(r)
	if _, err := io.ReadAll(rd); err == nil {
		t.Fatal("cuerpo cortado aceptado")
	}
}

func TestHashCheck(t *testing.T) {
	r := httptest.NewRequest("PUT", "http://h/b/k", strings.NewReader("x"))
	sr := &signedRequest{payload: emptySHA256}
	rd, _, _ := sr.body(r)
	if _, err := io.ReadAll(rd); !errors.Is(err, errContentSHA256Mismatch) {
		t.Fatalf("hash = %v", err)
	}
}

// presign firma una URL como lo hacen los SDK, para probar verifyPresigned.
func presign(method, rawURL, accessKey, secret, region string, expires time.Duration, now time.Time) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	amzDate := now.UTC().Format(amzDateFormat)
	date := amzDate[:8]
	q := u.Query()
	q.Set("X-Amz-Algorithm", sigAlgorithm)
	q.Set("X-Amz-Credential", accessKey+"/"+date+"/"+region+"/s3/aws4_request")
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", strconv.Itoa(int(expires/time.Second)))
	q.Set("X-Amz-SignedHeaders", "host")
	u.RawQuery = q.Encode()
	r := &http.Request{Method: method, URL: u, Host: u.Host, Header: http.Header{}}
	canon := canonicalRequest(r, []string{"host"}, payloadUnsigned, nil)
	sum := sha256.Sum256([]byte(canon))
	scope := date + "/" + region + "/s3/aws4_request"
	sig := hex.EncodeToString(hmacSHA256(signingKey(secret, date, region), sigAlgorithm+"\n"+amzDate+"\n"+scope+"\n"+hex.EncodeToString(sum[:])))
	return u.String() + "&X-Amz-Signature=" + sig, nil
}

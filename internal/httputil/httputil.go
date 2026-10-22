// Package httputil reúne piezas HTTP que comparten la API de la app y la API
// S3: rangos de bytes y plazos que se renuevan en cada lectura o escritura.
package httputil

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// IdleReader envuelve el cuerpo de una petición: una subida lenta pero viva no
// caduca; una parada más de timeout, sí.
func IdleReader(w http.ResponseWriter, r io.Reader, timeout time.Duration) io.Reader {
	return &idleReader{r: r, rc: http.NewResponseController(w), timeout: timeout}
}

// IdleWriter es IdleReader para las respuestas.
func IdleWriter(w http.ResponseWriter, timeout time.Duration) io.Writer {
	return &idleWriter{w: w, rc: http.NewResponseController(w), timeout: timeout}
}

// idleReader renueva el plazo de lectura de la conexión en cada Read.
type idleReader struct {
	r       io.Reader
	rc      *http.ResponseController
	timeout time.Duration
}

func (i *idleReader) Read(p []byte) (int, error) {
	_ = i.rc.SetReadDeadline(time.Now().Add(i.timeout))
	return i.r.Read(p)
}

// idleWriter renueva el plazo de escritura en cada Write.
type idleWriter struct {
	w       io.Writer
	rc      *http.ResponseController
	timeout time.Duration
}

func (i *idleWriter) Write(p []byte) (int, error) {
	_ = i.rc.SetWriteDeadline(time.Now().Add(i.timeout))
	return i.w.Write(p)
}

// ParseRange admite un único rango "bytes=a-b", "bytes=a-" o "bytes=-n".
func ParseRange(h string, size int64) (offset, length int64, partial bool, err error) {
	if h == "" {
		return 0, -1, false, nil
	}
	spec, ok := strings.CutPrefix(h, "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return 0, 0, false, errors.New("rango no soportado")
	}
	a, b, _ := strings.Cut(spec, "-")
	switch {
	case a == "" && b != "":
		n, err := strconv.ParseInt(b, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false, errors.New("rango inválido")
		}
		n = min(n, size)
		return size - n, n, true, nil
	case a != "":
		start, err := strconv.ParseInt(a, 10, 64)
		if err != nil || start < 0 || start >= size && size > 0 {
			return 0, 0, false, errors.New("rango fuera del archivo")
		}
		if b == "" {
			return start, size - start, true, nil
		}
		end, err := strconv.ParseInt(b, 10, 64)
		if err != nil || end < start {
			return 0, 0, false, errors.New("rango inválido")
		}
		end = min(end, size-1)
		return start, end - start + 1, true, nil
	}
	return 0, 0, false, errors.New("rango inválido")
}

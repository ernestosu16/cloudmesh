package s3

import (
	"encoding/xml"
	"net/http"
)

// Error es un error de la API S3 con su código estándar; los clientes
// (aws-cli, rclone, SDKs) deciden reintentos y mensajes según Code.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

var (
	errAccessDenied          = &Error{http.StatusForbidden, "AccessDenied", "acceso denegado"}
	errInvalidAccessKey      = &Error{http.StatusForbidden, "InvalidAccessKeyId", "la llave de acceso no existe"}
	errSignatureMismatch     = &Error{http.StatusForbidden, "SignatureDoesNotMatch", "la firma no coincide; revisa la llave secreta"}
	errAuthMalformed         = &Error{http.StatusBadRequest, "AuthorizationHeaderMalformed", "cabecera de autorización mal formada"}
	errTimeSkewed            = &Error{http.StatusForbidden, "RequestTimeTooSkewed", "el reloj del cliente difiere demasiado del servidor"}
	errContentSHA256Mismatch = &Error{http.StatusBadRequest, "XAmzContentSHA256Mismatch", "el SHA-256 del cuerpo no coincide con el firmado"}
	errIncompleteBody        = &Error{http.StatusBadRequest, "IncompleteBody", "cuerpo aws-chunked mal formado o incompleto"}
	errNoSuchBucket          = &Error{http.StatusNotFound, "NoSuchBucket", "el bucket no existe"}
	errNoSuchKey             = &Error{http.StatusNotFound, "NoSuchKey", "el objeto no existe"}
	errNoSuchUpload          = &Error{http.StatusNotFound, "NoSuchUpload", "la subida multipart no existe"}
	errBucketExists          = &Error{http.StatusConflict, "BucketAlreadyOwnedByYou", "ya existe un bucket con ese nombre"}
	errBucketNotEmpty        = &Error{http.StatusConflict, "BucketNotEmpty", "el bucket no está vacío"}
	errInvalidBucketName     = &Error{http.StatusBadRequest, "InvalidBucketName", "nombre de bucket inválido: 3–63 caracteres, minúsculas, números y guiones"}
	errInvalidRange          = &Error{http.StatusRequestedRangeNotSatisfiable, "InvalidRange", "el rango pedido no es válido"}
	errMissingLength         = &Error{http.StatusLengthRequired, "MissingContentLength", "falta Content-Length"}
	errTooLarge              = &Error{http.StatusBadRequest, "EntityTooLarge", "el objeto supera el tamaño máximo"}
	errTooSmall              = &Error{http.StatusBadRequest, "EntityTooSmall", "cada parte salvo la última debe tener al menos 5 MiB"}
	errInvalidPart           = &Error{http.StatusBadRequest, "InvalidPart", "una de las partes no existe o su ETag no coincide"}
	errInvalidPartOrder      = &Error{http.StatusBadRequest, "InvalidPartOrder", "las partes deben ir en orden ascendente"}
	errMalformedXML          = &Error{http.StatusBadRequest, "MalformedXML", "el XML no es válido"}
	errInternal              = &Error{http.StatusInternalServerError, "InternalError", "error interno"}
	errNoSpace               = &Error{http.StatusInsufficientStorage, "InsufficientStorage", "no queda espacio en las cuentas del pool"}
	errQuota                 = &Error{http.StatusForbidden, "QuotaExceeded", "la subida supera la cuota de espacio de quien creó la llave"}
	errUnreadable            = &Error{http.StatusServiceUnavailable, "ServiceUnavailable", "faltan bloques para leer el objeto; reintenta cuando el pool se repare"}
)

func errInvalidArgument(msg string) *Error {
	return &Error{http.StatusBadRequest, "InvalidArgument", msg}
}

func errNotImplemented(msg string) *Error {
	return &Error{http.StatusNotImplemented, "NotImplemented", msg}
}

type xmlError struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	Resource  string   `xml:"Resource,omitempty"`
	RequestID string   `xml:"RequestId"`
}

func writeError(w http.ResponseWriter, r *http.Request, e *Error) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(e.Status)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(xmlError{Code: e.Code, Message: e.Message, Resource: r.URL.Path, RequestID: w.Header().Get("X-Amz-Request-Id")})
}

func writeXML(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(v)
}

const s3NS = "http://s3.amazonaws.com/doc/2006-03-01/"

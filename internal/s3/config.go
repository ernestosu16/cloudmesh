package s3

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hirasolutions/cloudmesh/internal/db/sqlcgen"
)

// ErrBucketConfig envuelve los errores de validación de CORS y ciclo de vida.
var ErrBucketConfig = errors.New("configuración de bucket inválida")

func configError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBucketConfig, fmt.Sprintf(format, args...))
}

// ---- CORS ----------------------------------------------------------------------

// CORSRule es una regla CORS. Las etiquetas JSON son el formato del panel de
// R2 y las XML el de PutBucketCors, así la misma regla sirve para ambos.
type CORSRule struct {
	ID             string   `json:"ID,omitempty" xml:"ID,omitempty"`
	AllowedOrigins []string `json:"AllowedOrigins" xml:"AllowedOrigin"`
	AllowedMethods []string `json:"AllowedMethods" xml:"AllowedMethod"`
	AllowedHeaders []string `json:"AllowedHeaders,omitempty" xml:"AllowedHeader,omitempty"`
	ExposeHeaders  []string `json:"ExposeHeaders,omitempty" xml:"ExposeHeader,omitempty"`
	MaxAgeSeconds  int      `json:"MaxAgeSeconds,omitempty" xml:"MaxAgeSeconds,omitempty"`
}

var corsMethods = []string{http.MethodGet, http.MethodPut, http.MethodPost, http.MethodDelete, http.MethodHead}

func validateCORS(rules []CORSRule) error {
	if len(rules) > 100 {
		return configError("como mucho 100 reglas CORS")
	}
	for i, r := range rules {
		if len(r.AllowedOrigins) == 0 || len(r.AllowedMethods) == 0 {
			return configError("la regla CORS %d necesita al menos un origen y un método", i+1)
		}
		for _, o := range append(slices.Clone(r.AllowedOrigins), r.AllowedHeaders...) {
			if strings.Count(o, "*") > 1 {
				return configError("«%s»: solo se admite un comodín «*»", o)
			}
		}
		for _, m := range r.AllowedMethods {
			if !slices.Contains(corsMethods, m) {
				return configError("método CORS «%s» no válido: GET, PUT, POST, DELETE o HEAD", m)
			}
		}
		if r.MaxAgeSeconds < 0 {
			return configError("MaxAgeSeconds no puede ser negativo")
		}
	}
	return nil
}

// wildcard compara con un patrón que admite un solo «*».
func wildcard(pattern, v string, fold bool) bool {
	if fold {
		pattern, v = strings.ToLower(pattern), strings.ToLower(v)
	}
	pre, suf, ok := strings.Cut(pattern, "*")
	if !ok {
		return pattern == v
	}
	return len(v) >= len(pre)+len(suf) && strings.HasPrefix(v, pre) && strings.HasSuffix(v, suf)
}

// corsMatch devuelve la primera regla que admite el origen, el método y las cabeceras.
func corsMatch(rules []CORSRule, origin, method string, headers []string) (CORSRule, bool) {
	for _, r := range rules {
		if !slices.ContainsFunc(r.AllowedOrigins, func(p string) bool { return wildcard(p, origin, false) }) ||
			!slices.Contains(r.AllowedMethods, method) {
			continue
		}
		ok := true
		for _, h := range headers {
			if !slices.ContainsFunc(r.AllowedHeaders, func(p string) bool { return wildcard(p, h, true) }) {
				ok = false
				break
			}
		}
		if ok {
			return r, true
		}
	}
	return CORSRule{}, false
}

func parseCORS(raw string) []CORSRule {
	var rules []CORSRule
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &rules)
	}
	return rules
}

func allowOrigin(h http.Header, rule CORSRule, origin string) {
	if slices.Contains(rule.AllowedOrigins, "*") {
		h.Set("Access-Control-Allow-Origin", "*")
	} else {
		h.Set("Access-Control-Allow-Origin", origin)
	}
}

// applyCORS añade las cabeceras CORS a una petición normal si alguna regla la admite.
func applyCORS(w http.ResponseWriter, r *http.Request, b sqlcgen.S3Bucket) {
	if b.Cors == "" {
		return
	}
	h := w.Header()
	// También sin Origin: si no, el navegador reutiliza para un fetch la
	// respuesta que guardó al cargar un <img>, que no lleva cabeceras CORS.
	h.Add("Vary", "Origin")
	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}
	rule, ok := corsMatch(parseCORS(b.Cors), origin, r.Method, nil)
	if !ok {
		return
	}
	allowOrigin(h, rule, origin)
	if len(rule.ExposeHeaders) > 0 {
		h.Set("Access-Control-Expose-Headers", strings.Join(rule.ExposeHeaders, ", "))
	}
}

// preflight responde a OPTIONS, que el navegador manda sin firma.
func (s *Service) preflight(w http.ResponseWriter, r *http.Request, b sqlcgen.S3Bucket) {
	origin, method := r.Header.Get("Origin"), r.Header.Get("Access-Control-Request-Method")
	var headers []string
	for _, h := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
		if h = strings.TrimSpace(h); h != "" {
			headers = append(headers, h)
		}
	}
	rule, ok := corsMatch(parseCORS(b.Cors), origin, method, headers)
	if origin == "" || method == "" || !ok {
		s.fail(w, r, &Error{http.StatusForbidden, "AccessForbidden", "CORS no permite esta petición; revisa las reglas CORS del bucket"})
		return
	}
	h := w.Header()
	allowOrigin(h, rule, origin)
	h.Add("Vary", "Origin")
	h.Set("Access-Control-Allow-Methods", strings.Join(rule.AllowedMethods, ", "))
	if len(headers) > 0 {
		h.Set("Access-Control-Allow-Headers", strings.Join(headers, ", "))
	}
	if len(rule.ExposeHeaders) > 0 {
		h.Set("Access-Control-Expose-Headers", strings.Join(rule.ExposeHeaders, ", "))
	}
	if rule.MaxAgeSeconds > 0 {
		h.Set("Access-Control-Max-Age", strconv.Itoa(rule.MaxAgeSeconds))
	}
	w.WriteHeader(http.StatusOK)
}

// ---- Ciclo de vida -----------------------------------------------------------------

// LifecycleRule es una regla de ciclo de vida, como las de R2: borrar los
// objetos de un prefijo pasados N días desde su subida y cancelar las subidas
// multipart que lleven N días sin completarse. 0 desactiva cada parte.
type LifecycleRule struct {
	ID         string `json:"id"`
	Enabled    bool   `json:"enabled"`
	Prefix     string `json:"prefix"`
	ExpireDays int    `json:"expire_days"`
	AbortDays  int    `json:"abort_days"`
}

func validateLifecycle(rules []LifecycleRule) error {
	if len(rules) > 1000 {
		return configError("como mucho 1000 reglas de ciclo de vida")
	}
	ids := map[string]bool{}
	for i, r := range rules {
		if r.ID == "" || len(r.ID) > 255 {
			return configError("la regla %d necesita un ID de hasta 255 caracteres", i+1)
		}
		if ids[r.ID] {
			return configError("el ID «%s» está repetido", r.ID)
		}
		ids[r.ID] = true
		if r.ExpireDays < 0 || r.AbortDays < 0 {
			return configError("la regla «%s» tiene días negativos", r.ID)
		}
		if r.ExpireDays == 0 && r.AbortDays == 0 {
			return configError("la regla «%s» no hace nada: indica días para borrar objetos o para cancelar subidas", r.ID)
		}
	}
	return nil
}

func parseLifecycle(raw string) []LifecycleRule {
	var rules []LifecycleRule
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &rules)
	}
	return rules
}

// Formato XML de Put/GetBucketLifecycleConfiguration.
type lifecycleXML struct {
	XMLName xml.Name `xml:"LifecycleConfiguration"`
	NS      string   `xml:"xmlns,attr,omitempty"`
	Rules   []struct {
		ID     string  `xml:"ID"`
		Status string  `xml:"Status"`
		Prefix *string `xml:"Prefix,omitempty"` // forma antigua, sin Filter
		Filter *struct {
			Prefix string    `xml:"Prefix"`
			And    *struct{} `xml:"And,omitempty"`
			Tag    *struct{} `xml:"Tag,omitempty"`
		} `xml:"Filter,omitempty"`
		Expiration *struct {
			Days int     `xml:"Days,omitempty"`
			Date *string `xml:"Date,omitempty"`
		} `xml:"Expiration,omitempty"`
		Abort *struct {
			Days int `xml:"DaysAfterInitiation"`
		} `xml:"AbortIncompleteMultipartUpload,omitempty"`
		Transitions []struct{} `xml:"Transition,omitempty"`
	} `xml:"Rule"`
}

func lifecycleFromXML(body io.Reader) ([]LifecycleRule, error) {
	var in lifecycleXML
	if err := xml.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&in); err != nil {
		return nil, errMalformedXML
	}
	rules := make([]LifecycleRule, 0, len(in.Rules))
	for _, r := range in.Rules {
		if len(r.Transitions) > 0 {
			return nil, errNotImplemented("CloudMesh no tiene clases de almacenamiento: las transiciones no están disponibles")
		}
		if r.Filter != nil && (r.Filter.And != nil || r.Filter.Tag != nil) {
			return nil, errNotImplemented("solo se filtra por prefijo; sin etiquetas")
		}
		if r.Expiration != nil && r.Expiration.Date != nil {
			return nil, errNotImplemented("la expiración por fecha no está disponible; usa Days")
		}
		lr := LifecycleRule{ID: r.ID, Enabled: r.Status == "Enabled"}
		switch {
		case r.Filter != nil:
			lr.Prefix = r.Filter.Prefix
		case r.Prefix != nil:
			lr.Prefix = *r.Prefix
		}
		if r.Expiration != nil {
			lr.ExpireDays = r.Expiration.Days
		}
		if r.Abort != nil {
			lr.AbortDays = r.Abort.Days
		}
		rules = append(rules, lr)
	}
	return rules, nil
}

func lifecycleToXML(rules []LifecycleRule) any {
	type expiration struct {
		Days int `xml:"Days"`
	}
	type abort struct {
		Days int `xml:"DaysAfterInitiation"`
	}
	type rule struct {
		ID     string `xml:"ID"`
		Filter struct {
			Prefix string `xml:"Prefix"`
		} `xml:"Filter"`
		Status     string      `xml:"Status"`
		Expiration *expiration `xml:"Expiration,omitempty"`
		Abort      *abort      `xml:"AbortIncompleteMultipartUpload,omitempty"`
	}
	out := struct {
		XMLName xml.Name `xml:"LifecycleConfiguration"`
		NS      string   `xml:"xmlns,attr"`
		Rules   []rule   `xml:"Rule"`
	}{NS: s3NS}
	for _, r := range rules {
		x := rule{ID: r.ID, Status: "Disabled"}
		x.Filter.Prefix = r.Prefix
		if r.Enabled {
			x.Status = "Enabled"
		}
		if r.ExpireDays > 0 {
			x.Expiration = &expiration{Days: r.ExpireDays}
		}
		if r.AbortDays > 0 {
			x.Abort = &abort{Days: r.AbortDays}
		}
		out.Rules = append(out.Rules, x)
	}
	return out
}

// ApplyLifecycle aplica las reglas activas de todos los buckets: los objetos
// caducados van a la papelera (como un borrado normal) y las subidas a medias
// se cancelan. Devuelve cuántas cosas tocó. La llama CleanUp.
func (s *Service) ApplyLifecycle(ctx context.Context) (int, error) {
	buckets, err := s.q.ListLifecycleBuckets(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	day := 24 * time.Hour
	for _, b := range buckets {
		for _, rule := range parseLifecycle(b.Lifecycle) {
			if !rule.Enabled {
				continue
			}
			if rule.AbortDays > 0 {
				ups, err := s.q.ListBucketStaleUploads(ctx, sqlcgen.ListBucketStaleUploadsParams{
					BucketID: b.ID, CreatedAt: s.now().Add(-time.Duration(rule.AbortDays) * day).Unix(),
				})
				if err != nil {
					return n, err
				}
				for _, u := range ups {
					if strings.HasPrefix(u.ObjectKey, rule.Prefix) {
						if err := s.Abort(ctx, u.ID); err != nil {
							return n, err
						}
						n++
					}
				}
			}
			if rule.ExpireDays > 0 {
				m, err := s.expire(ctx, b, rule.Prefix, s.now().Add(-time.Duration(rule.ExpireDays)*day).Unix())
				n += m
				if err != nil {
					return n, err
				}
			}
		}
	}
	return n, nil
}

// expire manda a la papelera los objetos del prefijo subidos antes de cutoff.
func (s *Service) expire(ctx context.Context, b sqlcgen.S3Bucket, prefix string, cutoff int64) (int, error) {
	n, after := 0, ""
	for {
		entries, truncated, err := s.list(ctx, b, prefix, "", after, maxListKeys)
		if err != nil {
			return n, err
		}
		for _, e := range entries {
			if e.file != nil && e.file.CreatedAt < cutoff {
				if err := s.remove(ctx, b, e.key); err != nil {
					return n, err
				}
				n++
			}
		}
		if !truncated || len(entries) == 0 {
			return n, nil
		}
		after = entries[len(entries)-1].key
	}
}

// ---- Configuración desde la app ---------------------------------------------------

// BucketConfig es lo que se puede cambiar de un bucket desde la app.
type BucketConfig struct {
	Public    bool            `json:"public"`
	CORS      []CORSRule      `json:"cors"`
	Lifecycle []LifecycleRule `json:"lifecycle"`
}

// Configure sustituye la configuración del bucket.
func (s *Service) Configure(ctx context.Context, id int64, c BucketConfig) (Bucket, error) {
	if err := validateCORS(c.CORS); err != nil {
		return Bucket{}, err
	}
	if err := validateLifecycle(c.Lifecycle); err != nil {
		return Bucket{}, err
	}
	public := int64(0)
	if c.Public {
		public = 1
	}
	n, err := s.q.SetBucketPublic(ctx, sqlcgen.SetBucketPublicParams{Public: public, ID: id})
	if err != nil {
		return Bucket{}, err
	}
	if n == 0 {
		return Bucket{}, ErrBucketNotFound
	}
	if _, err := s.q.SetBucketCORS(ctx, sqlcgen.SetBucketCORSParams{Cors: encodeRules(c.CORS), ID: id}); err != nil {
		return Bucket{}, err
	}
	if _, err := s.q.SetBucketLifecycle(ctx, sqlcgen.SetBucketLifecycleParams{Lifecycle: encodeRules(c.Lifecycle), ID: id}); err != nil {
		return Bucket{}, err
	}
	row, err := s.q.GetBucket(ctx, id)
	if err != nil {
		return Bucket{}, err
	}
	return s.toBucket(ctx, row)
}

// encodeRules guarda una lista de reglas en JSON; vacía queda como «».
func encodeRules[T any](rules []T) string {
	if len(rules) == 0 {
		return ""
	}
	raw, _ := json.Marshal(rules)
	return string(raw)
}

// ---- Subrecursos ?cors y ?lifecycle de la API S3 -------------------------------------

// bucketConfig sirve GET/PUT/DELETE de ?cors y ?lifecycle. Leer pide una llave
// de administración; cambiar, una de administración con escritura.
func (s *Service) bucketConfig(w http.ResponseWriter, r *http.Request, sr *signedRequest, key Key, b sqlcgen.S3Bucket, sub string) {
	need := actListBuckets
	if r.Method != http.MethodGet {
		need = actAdminBucket
	}
	if !key.allowed(need, b.ID) {
		s.fail(w, r, errAccessDenied)
		return
	}
	ctx := r.Context()
	switch {
	case sub == "cors" && r.Method == http.MethodGet:
		rules := parseCORS(b.Cors)
		if len(rules) == 0 {
			s.fail(w, r, &Error{http.StatusNotFound, "NoSuchCORSConfiguration", "el bucket no tiene reglas CORS"})
			return
		}
		writeXML(w, http.StatusOK, struct {
			XMLName xml.Name   `xml:"CORSConfiguration"`
			NS      string     `xml:"xmlns,attr"`
			Rules   []CORSRule `xml:"CORSRule"`
		}{NS: s3NS, Rules: rules})
	case sub == "cors" && r.Method == http.MethodPut:
		body, _, err := sr.body(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		var in struct {
			Rules []CORSRule `xml:"CORSRule"`
		}
		if err := xml.NewDecoder(io.LimitReader(body, 64<<10)).Decode(&in); err != nil {
			s.fail(w, r, errMalformedXML)
			return
		}
		if err := validateCORS(in.Rules); err != nil {
			s.fail(w, r, errInvalidArgument(err.Error()))
			return
		}
		if _, err := s.q.SetBucketCORS(ctx, sqlcgen.SetBucketCORSParams{Cors: encodeRules(in.Rules), ID: b.ID}); err != nil {
			s.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusOK)
	case sub == "cors" && r.Method == http.MethodDelete:
		if _, err := s.q.SetBucketCORS(ctx, sqlcgen.SetBucketCORSParams{Cors: "", ID: b.ID}); err != nil {
			s.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case sub == "lifecycle" && r.Method == http.MethodGet:
		rules := parseLifecycle(b.Lifecycle)
		if len(rules) == 0 {
			s.fail(w, r, &Error{http.StatusNotFound, "NoSuchLifecycleConfiguration", "el bucket no tiene reglas de ciclo de vida"})
			return
		}
		writeXML(w, http.StatusOK, lifecycleToXML(rules))
	case sub == "lifecycle" && r.Method == http.MethodPut:
		body, _, err := sr.body(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		rules, err := lifecycleFromXML(body)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if err := validateLifecycle(rules); err != nil {
			s.fail(w, r, errInvalidArgument(err.Error()))
			return
		}
		if _, err := s.q.SetBucketLifecycle(ctx, sqlcgen.SetBucketLifecycleParams{Lifecycle: encodeRules(rules), ID: b.ID}); err != nil {
			s.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusOK)
	case sub == "lifecycle" && r.Method == http.MethodDelete:
		if _, err := s.q.SetBucketLifecycle(ctx, sqlcgen.SetBucketLifecycleParams{Lifecycle: "", ID: b.ID}); err != nil {
			s.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		s.fail(w, r, errNotImplemented("operación de configuración no soportada"))
	}
}

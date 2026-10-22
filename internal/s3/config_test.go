package s3

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestVirtualHost(t *testing.T) {
	e := newEnv(t)
	admin := e.client(t, PermAdminRW)
	expect(t, admin.do("PUT", "/vhost", nil, nil), 200, "crear bucket")
	admin.host = "vhost.s3.test"
	expect(t, admin.do("PUT", "/carpeta/hola.txt", []byte("hola"), nil), 200, "PUT virtual-host")
	if got := expect(t, admin.do("GET", "/carpeta/hola.txt", nil, nil), 200, "GET virtual-host"); string(got) != "hola" {
		t.Fatalf("contenido = %q", got)
	}
	if b := expect(t, admin.do("GET", "/?list-type=2", nil, nil), 200, "listar virtual-host"); !bytes.Contains(b, []byte("<Key>carpeta/hola.txt</Key>")) {
		t.Fatalf("listado = %s", b)
	}
	// Con el puerto en Host también vale, y el estilo de ruta sigue igual.
	admin.host = "vhost.s3.test:9000"
	expect(t, admin.do("HEAD", "/carpeta/hola.txt", nil, nil), 200, "HEAD con puerto")
	admin.host = ""
	expect(t, admin.do("GET", "/vhost/carpeta/hola.txt", nil, nil), 200, "estilo ruta")
}

func TestPublicBucket(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.client(t, PermAdminRW)
	expect(t, admin.do("PUT", "/web", nil, nil), 200, "crear bucket")
	expect(t, admin.do("PUT", "/web/index.html", []byte("<h1>hola</h1>"), map[string]string{"Content-Type": "text/html"}), 200, "PUT")
	get := func(method, path string) *http.Response {
		req, _ := http.NewRequest(method, e.srv.URL+path, nil)
		resp, err := e.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	expect(t, get("GET", "/web/index.html"), 403, "privado sin firma")
	b := mustBucket(t, e, "web")
	if _, err := e.s.Configure(ctx, b.ID, BucketConfig{Public: true}); err != nil {
		t.Fatal(err)
	}
	if got := expect(t, get("GET", "/web/index.html"), 200, "público sin firma"); string(got) != "<h1>hola</h1>" {
		t.Fatalf("contenido = %q", got)
	}
	expect(t, get("HEAD", "/web/index.html"), 200, "HEAD público")
	expect(t, get("GET", "/web/nada.html"), 404, "inexistente")
	// Lo que cuelga de una subcarpeta restringida no es público, como en los enlaces.
	expect(t, admin.do("PUT", "/web/privado/nota.txt", []byte("secreto"), nil), 200, "PUT privado")
	priv, err := e.v.ChildFolder(ctx, b.FolderID, "privado")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.v.SetFolderAccess(ctx, priv.ID, true, nil); err != nil {
		t.Fatal(err)
	}
	expect(t, get("GET", "/web/privado/nota.txt"), 404, "restringido sin firma")
	expect(t, admin.do("GET", "/web/privado/nota.txt", nil, nil), 200, "restringido con firma")
	// Listar, escribir o borrar siguen pidiendo firma.
	expect(t, get("GET", "/web?list-type=2"), 403, "listar sin firma")
	expect(t, get("DELETE", "/web/index.html"), 403, "borrar sin firma")
	expect(t, get("PUT", "/web/otro.html"), 403, "subir sin firma")
}

func TestCORS(t *testing.T) {
	e := newEnv(t)
	admin := e.client(t, PermAdminRW)
	expect(t, admin.do("PUT", "/app", nil, nil), 200, "crear bucket")
	preflight := func(origin, method, headers string) *http.Response {
		req, _ := http.NewRequest("OPTIONS", e.srv.URL+"/app/sube.bin", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", method)
		if headers != "" {
			req.Header.Set("Access-Control-Request-Headers", headers)
		}
		resp, err := e.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	expect(t, admin.do("GET", "/app?cors", nil, nil), 404, "sin CORS")
	expect(t, preflight("https://app.ejemplo.com", "PUT", ""), 403, "preflight sin reglas")

	cfg := `<CORSConfiguration><CORSRule><AllowedOrigin>https://*.ejemplo.com</AllowedOrigin><AllowedMethod>GET</AllowedMethod><AllowedMethod>PUT</AllowedMethod>` +
		`<AllowedHeader>*</AllowedHeader><ExposeHeader>ETag</ExposeHeader><MaxAgeSeconds>600</MaxAgeSeconds></CORSRule></CORSConfiguration>`
	expect(t, admin.do("PUT", "/app?cors", []byte(cfg), nil), 200, "PutBucketCors")
	if b := expect(t, admin.do("GET", "/app?cors", nil, nil), 200, "GetBucketCors"); !bytes.Contains(b, []byte("<AllowedOrigin>https://*.ejemplo.com</AllowedOrigin>")) {
		t.Fatalf("GetBucketCors = %s", b)
	}
	resp := preflight("https://app.ejemplo.com", "PUT", "content-type, x-amz-date")
	expect(t, resp, 200, "preflight permitido")
	if h := resp.Header; h.Get("Access-Control-Allow-Origin") != "https://app.ejemplo.com" || h.Get("Access-Control-Max-Age") != "600" ||
		!strings.Contains(h.Get("Access-Control-Allow-Methods"), "PUT") || h.Get("Access-Control-Allow-Headers") != "content-type, x-amz-date" {
		t.Fatalf("cabeceras preflight = %v", h)
	}
	expect(t, preflight("https://otro.com", "PUT", ""), 403, "origen no permitido")
	expect(t, preflight("https://app.ejemplo.com", "DELETE", ""), 403, "método no permitido")

	// Una petición firmada desde un origen permitido lleva las cabeceras CORS.
	resp = admin.do("PUT", "/app/sube.bin", []byte("x"), map[string]string{"Origin": "https://app.ejemplo.com"})
	expect(t, resp, 200, "PUT con Origin")
	if resp.Header.Get("Access-Control-Allow-Origin") != "https://app.ejemplo.com" || resp.Header.Get("Access-Control-Expose-Headers") != "ETag" {
		t.Fatalf("cabeceras CORS = %v", resp.Header)
	}
	// Sin Origin (un <img>) también lleva Vary, para que el navegador no reutilice esa respuesta en un fetch.
	resp = admin.do("GET", "/app/sube.bin", nil, nil)
	expect(t, resp, 200, "GET sin Origin")
	if resp.Header.Get("Vary") != "Origin" || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("cabeceras sin Origin = %v", resp.Header)
	}

	// Solo una llave de administración cambia la configuración.
	obj := e.client(t, PermObjectRW)
	expect(t, obj.do("DELETE", "/app?cors", nil, nil), 403, "llave de objetos")
	expect(t, admin.do("PUT", "/app?cors", []byte(`<CORSConfiguration><CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>PATCH</AllowedMethod></CORSRule></CORSConfiguration>`), nil), 400, "método inválido")
	expect(t, admin.do("DELETE", "/app?cors", nil, nil), 204, "DeleteBucketCors")
	expect(t, admin.do("GET", "/app?cors", nil, nil), 404, "CORS borrado")
	// Borrar la configuración no borra el bucket.
	expect(t, admin.do("HEAD", "/app", nil, nil), 200, "bucket sigue")
}

func TestUploadPartCopy(t *testing.T) {
	e := newEnv(t)
	admin := e.client(t, PermAdminRW)
	admin.unsigned = true
	expect(t, admin.do("PUT", "/copias", nil, nil), 200, "crear bucket")
	src := make([]byte, minPartSize+100)
	rand.Read(src)
	expect(t, admin.do("PUT", "/copias/origen.bin", src, nil), 200, "origen")

	var init struct{ UploadId string }
	if err := xml.Unmarshal(expect(t, admin.do("POST", "/copias/destino.bin?uploads", nil, nil), 200, "init"), &init); err != nil {
		t.Fatal(err)
	}
	part := func(n int, rng string) string {
		hdr := map[string]string{"X-Amz-Copy-Source": "/copias/origen.bin"}
		if rng != "" {
			hdr["X-Amz-Copy-Source-Range"] = rng
		}
		b := expect(t, admin.do("PUT", fmt.Sprintf("/copias/destino.bin?partNumber=%d&uploadId=%s", n, init.UploadId), nil, hdr), 200, "UploadPartCopy")
		var res struct{ ETag string }
		if err := xml.Unmarshal(b, &res); err != nil || res.ETag == "" {
			t.Fatalf("CopyPartResult = %s", b)
		}
		return res.ETag
	}
	e1 := part(1, fmt.Sprintf("bytes=0-%d", minPartSize-1))
	e2 := part(2, fmt.Sprintf("bytes=%d-%d", minPartSize, len(src)-1))
	expect(t, admin.do("PUT", "/copias/destino.bin?partNumber=3&uploadId="+init.UploadId, nil,
		map[string]string{"X-Amz-Copy-Source": "/copias/origen.bin", "X-Amz-Copy-Source-Range": "bytes=0-99999999"}), 400, "rango fuera")
	body := fmt.Sprintf("<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>%s</ETag></Part><Part><PartNumber>2</PartNumber><ETag>%s</ETag></Part></CompleteMultipartUpload>", e1, e2)
	expect(t, admin.do("POST", "/copias/destino.bin?uploadId="+init.UploadId, []byte(body), nil), 200, "Complete")
	if got := read(t, admin.do("GET", "/copias/destino.bin", nil, nil)); !bytes.Equal(got, src) {
		t.Fatal("la copia por partes no coincide con el origen")
	}

	// Copiar de un bucket que la llave no puede leer.
	expect(t, admin.do("PUT", "/ajeno", nil, nil), 200, "bucket ajeno")
	expect(t, admin.do("PUT", "/ajeno/x.bin", []byte("secreto"), nil), 200, "objeto ajeno")
	limited := e.client(t, PermObjectRW, mustBucket(t, e, "copias").ID)
	if err := xml.Unmarshal(read(t, limited.do("POST", "/copias/robo.bin?uploads", nil, nil)), &init); err != nil {
		t.Fatal(err)
	}
	expect(t, limited.do("PUT", "/copias/robo.bin?partNumber=1&uploadId="+init.UploadId, nil, map[string]string{"X-Amz-Copy-Source": "/ajeno/x.bin"}), 403, "origen sin permiso")
}

func TestLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.client(t, PermAdminRW)
	expect(t, admin.do("PUT", "/logs", nil, nil), 200, "crear bucket")
	for _, k := range []string{"tmp/a.log", "tmp/sub/b.log", "keep/c.log"} {
		expect(t, admin.do("PUT", "/logs/"+k, []byte(k), nil), 200, "PUT "+k)
	}
	var init struct{ UploadId string }
	if err := xml.Unmarshal(read(t, admin.do("POST", "/logs/tmp/grande.bin?uploads", nil, nil)), &init); err != nil {
		t.Fatal(err)
	}
	cfg := `<LifecycleConfiguration><Rule><ID>tmp</ID><Filter><Prefix>tmp/</Prefix></Filter><Status>Enabled</Status>` +
		`<Expiration><Days>1</Days></Expiration><AbortIncompleteMultipartUpload><DaysAfterInitiation>1</DaysAfterInitiation></AbortIncompleteMultipartUpload></Rule>` +
		`<Rule><ID>apagada</ID><Filter><Prefix></Prefix></Filter><Status>Disabled</Status><Expiration><Days>1</Days></Expiration></Rule></LifecycleConfiguration>`
	expect(t, admin.do("PUT", "/logs?lifecycle", []byte(cfg), nil), 200, "PutBucketLifecycleConfiguration")
	if b := expect(t, admin.do("GET", "/logs?lifecycle", nil, nil), 200, "GetBucketLifecycleConfiguration"); !bytes.Contains(b, []byte("<Prefix>tmp/</Prefix>")) || !bytes.Contains(b, []byte("<Status>Disabled</Status>")) {
		t.Fatalf("lifecycle = %s", b)
	}
	expect(t, admin.do("PUT", "/logs?lifecycle", []byte(`<LifecycleConfiguration><Rule><ID>x</ID><Status>Enabled</Status><Transition><Days>1</Days><StorageClass>STANDARD_IA</StorageClass></Transition></Rule></LifecycleConfiguration>`), nil), 501, "transiciones")

	// Hoy no caduca nada.
	if n, err := e.s.ApplyLifecycle(ctx); err != nil || n != 0 {
		t.Fatalf("hoy: n=%d err=%v", n, err)
	}
	real := e.s.now
	e.s.now = func() time.Time { return real().Add(49 * time.Hour) }
	n, err := e.s.ApplyLifecycle(ctx)
	e.s.now = real
	if err != nil || n != 3 {
		t.Fatalf("pasados dos días: n=%d err=%v (esperaba 2 objetos y 1 subida)", n, err)
	}
	if b := read(t, admin.do("GET", "/logs?list-type=2", nil, nil)); !bytes.Contains(b, []byte("<Key>keep/c.log</Key>")) || bytes.Contains(b, []byte("tmp/")) {
		t.Fatalf("tras caducar = %s", b)
	}
	expect(t, admin.do("GET", "/logs/tmp/grande.bin?uploadId="+init.UploadId, nil, nil), 404, "subida cancelada")
	// Caducar es borrar: va a la papelera.
	if trash, err := e.v.ListTrash(ctx); err != nil || len(trash) != 2 {
		t.Fatalf("papelera = %d %v", len(trash), err)
	}
	expect(t, admin.do("DELETE", "/logs?lifecycle", nil, nil), 204, "DeleteBucketLifecycle")
	expect(t, admin.do("GET", "/logs?lifecycle", nil, nil), 404, "sin reglas")
}

func TestConfigureValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	b, err := e.s.CreateBucket(ctx, "valida", 0)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]BucketConfig{
		"CORS sin origen":  {CORS: []CORSRule{{AllowedMethods: []string{"GET"}}}},
		"dos comodines":    {CORS: []CORSRule{{AllowedOrigins: []string{"*.a.*"}, AllowedMethods: []string{"GET"}}}},
		"regla sin acción": {Lifecycle: []LifecycleRule{{ID: "x", Enabled: true}}},
		"IDs repetidos":    {Lifecycle: []LifecycleRule{{ID: "x", ExpireDays: 1}, {ID: "x", AbortDays: 1}}},
		"regla sin ID":     {Lifecycle: []LifecycleRule{{ExpireDays: 1}}},
	} {
		if _, err := e.s.Configure(ctx, b.ID, c); err == nil {
			t.Errorf("%s: esperaba error", name)
		}
	}
	got, err := e.s.Configure(ctx, b.ID, BucketConfig{Public: true, CORS: []CORSRule{{AllowedOrigins: []string{"*"}, AllowedMethods: []string{"GET"}}},
		Lifecycle: []LifecycleRule{{ID: "x", Enabled: true, ExpireDays: 7}}})
	if err != nil || !got.Public || len(got.CORS) != 1 || len(got.Lifecycle) != 1 {
		t.Fatalf("Configure = %+v %v", got, err)
	}
	if got, _ = e.s.Configure(ctx, b.ID, BucketConfig{}); got.Public || len(got.CORS) != 0 || got.Lifecycle == nil {
		t.Fatalf("vaciar = %+v", got)
	}
}

func TestSubresources(t *testing.T) {
	e := newEnv(t)
	admin := e.client(t, PermAdminRW)
	expect(t, admin.do("PUT", "/sub", nil, nil), 200, "crear bucket")
	expect(t, admin.do("PUT", "/sub/a.txt", []byte("a"), nil), 200, "PUT")
	if b := expect(t, admin.do("GET", "/sub/a.txt?tagging", nil, nil), 200, "GetObjectTagging"); !bytes.Contains(b, []byte("<TagSet></TagSet>")) {
		t.Fatalf("tagging = %s", b)
	}
	expect(t, admin.do("GET", "/sub/nada.txt?tagging", nil, nil), 404, "tagging de inexistente")
	expect(t, admin.do("GET", "/sub/a.txt?acl", nil, nil), 501, "acl de objeto")
	expect(t, admin.do("GET", "/sub?tagging", nil, nil), 404, "GetBucketTagging")
	expect(t, admin.do("GET", "/sub?website", nil, nil), 501, "website")
}

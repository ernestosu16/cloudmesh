package http

import (
	"io/fs"
	"mime"
	"net/http"
	"strings"

	"github.com/hirasolutions/cloudmesh/web"
)

// spaCSP solo deja cargar código del propio origen: un fallo que cuele HTML
// en la página no puede ejecutar scripts ni mandar datos fuera. Los estilos en
// línea los usan los componentes (posición de menús y diálogos).
const spaCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; media-src 'self' blob:; font-src 'self' data:; connect-src 'self'; " +
	"frame-src 'self'; frame-ancestors 'self'; object-src 'none'; base-uri 'self'; form-action 'self'"

// spaHandler sirve web/dist. Rutas sin archivo devuelven index.html para que
// el router del frontend resuelva la URL. Si no hay build, responde 503.
func spaHandler() http.Handler {
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		panic(err)
	}
	if _, err := fs.Stat(dist, "index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "frontend no compilado: ejecuta `pnpm build` en web/", http.StatusServiceUnavailable)
		})
	}
	files := http.FileServerFS(dist)
	// El manifiesto de la webapp no está en la tabla de tipos de Go.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", spaCSP)
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(dist, name); err != nil && strings.HasPrefix(name, "assets/") {
			// Un trozo de una build anterior: 404 y no index.html, para que la
			// importación falle limpia y la página ofrezca actualizar.
			http.NotFound(w, r)
			return
		} else if err != nil {
			// Ruta del SPA: entregar index.html sin cachear.
			w.Header().Set("Cache-Control", "no-cache")
			r.URL.Path = "/"
		} else if strings.HasPrefix(name, "assets/") {
			// Assets con hash en el nombre: cache larga.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else if name == "version.json" {
			// Lo consulta la página abierta para saber si hay una build nueva.
			w.Header().Set("Cache-Control", "no-store")
		} else if name == "sw.js" || name == "manifest.webmanifest" {
			// Sin nombre con hash: el navegador debe ver cada versión nueva.
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

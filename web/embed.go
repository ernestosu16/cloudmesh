// Package web embebe el frontend compilado (web/dist) en el binario.
package web

import "embed"

// Dist contiene el resultado de `pnpm build`. Vacío (solo .gitkeep) si no se ha compilado.
//
//go:embed all:dist
var Dist embed.FS

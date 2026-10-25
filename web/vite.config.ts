import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { tanstackRouter } from '@tanstack/router-plugin/vite'
import { fileURLToPath, URL } from 'node:url'
import process from 'node:process'

// Identidad de la build para el aviso de versión nueva (features/update): un
// <meta> en index.html, que se sirve sin caché, y dist/version.json, que la
// página consulta. En el HTML y no en el bundle: así no cambia el hash de todos
// los assets en cada build. VITE_BUILD_ID la fija desde fuera si hace falta.
const buildId = process.env.VITE_BUILD_ID || Date.now().toString(36)
const buildInfo: Plugin = {
  name: 'build-info',
  apply: 'build',
  transformIndexHtml: () => [{ tag: 'meta', attrs: { name: 'app-build-id', content: buildId }, injectTo: 'head' }],
  generateBundle() {
    this.emitFile({
      type: 'asset',
      fileName: 'version.json',
      source: JSON.stringify({ build_id: buildId, built_at: new Date().toISOString() }),
    })
  },
}

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    // El plugin del router debe ir antes de react para generar routeTree.gen.ts.
    tanstackRouter({ target: 'react', autoCodeSplitting: true }),
    react(),
    tailwindcss(),
    buildInfo,
  ],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  server: {
    port: 5173,
    // En desarrollo la API corre en Go; en Docker el host es el servicio `api`.
    // En producción Go sirve API y frontend desde el mismo origen.
    proxy: { '/api': process.env.VITE_API_PROXY ?? 'http://localhost:8080' },
    // Dentro de Docker los eventos de fichero no siempre llegan: sondeo.
    watch: process.env.VITE_POLL ? { usePolling: true, interval: 300 } : undefined,
  },
  build: { outDir: 'dist', emptyOutDir: true },
})

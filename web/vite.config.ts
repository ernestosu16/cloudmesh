import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { tanstackRouter } from '@tanstack/router-plugin/vite'
import { fileURLToPath, URL } from 'node:url'
import process from 'node:process'

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    // El plugin del router debe ir antes de react para generar routeTree.gen.ts.
    tanstackRouter({ target: 'react', autoCodeSplitting: true }),
    react(),
    tailwindcss(),
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

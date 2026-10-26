// Aviso de versión nueva. Contrato con el plugin build-info de vite.config.ts:
// el <meta> dice qué build corre esta página y /version.json cuál sirve ahora el servidor.

/** Cada cuánto se mira, con la pestaña visible. */
export const CHECK_INTERVAL_MS = 5 * 60_000
/** Foco y visibilidad llegan juntos: como mucho una consulta por minuto. */
export const MIN_CHECK_GAP_MS = 60_000
const CHECK_TIMEOUT_MS = 8_000

export interface BuildInfo {
  build_id: string
  built_at: string
}

/** Build de esta página; null en desarrollo (Vite no pone el <meta>). */
export function currentBuildId(): string | null {
  return document.querySelector<HTMLMetaElement>('meta[name="app-build-id"]')?.content.trim() || null
}

/**
 * Versión que se enseña en el menú: fecha de la build (UTC) y su id, p. ej.
 * «v2026.10.25 · mvoh5lln»; «dev» en desarrollo.
 */
export function versionLabel(): string {
  const id = currentBuildId()
  if (!id) return 'dev'
  const at = document.querySelector<HTMLMetaElement>('meta[name="app-build-time"]')?.content ?? ''
  const day = /^\d{4}-\d{2}-\d{2}/.test(at) ? `v${at.slice(0, 10).replaceAll('-', '.')} · ` : ''
  return `${day}${id}`
}

/**
 * La build que publica el servidor, o null si no se sabe (sin red, respuesta
 * rara). Nunca lanza: «no se sabe» no es «hay versión nueva».
 */
export async function fetchBuildInfo(): Promise<BuildInfo | null> {
  try {
    const res = await fetch(`/version.json?t=${Date.now()}`, {
      cache: 'no-store',
      headers: { Accept: 'application/json' },
      signal: AbortSignal.timeout(CHECK_TIMEOUT_MS),
    })
    // Una ruta sin archivo devuelve index.html con 200: mirar también el tipo.
    if (!res.ok || !(res.headers.get('content-type') ?? '').includes('json')) return null
    const data: unknown = await res.json()
    if (typeof data !== 'object' || data === null) return null
    const info = data as Partial<BuildInfo>
    return typeof info.build_id === 'string' && info.build_id ? { build_id: info.build_id, built_at: info.built_at ?? '' } : null
  } catch {
    return null
  }
}

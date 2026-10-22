import { getLang, t } from '@/lib/i18n'

const units = ['B', 'KB', 'MB', 'GB', 'TB']

/** Bytes a texto corto: 1.5 GB. */
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B'
  const i = Math.min(units.length - 1, Math.floor(Math.log(n) / Math.log(1024)))
  const v = n / 1024 ** i
  return `${v < 10 && i > 0 ? v.toFixed(1) : Math.round(v)} ${units[i]}`
}

/** Porcentaje entero acotado a [0, 100]. */
export function percent(used: number, total: number): number {
  if (total <= 0) return 0
  return Math.min(100, Math.round((used / total) * 100))
}

/** Fecha relativa corta en el idioma activo a partir de segundos unix. */
export function relativeTime(unixSeconds: number): string {
  if (!unixSeconds) return t('nunca')
  const diff = Math.round((unixSeconds * 1000 - Date.now()) / 1000)
  const rtf = new Intl.RelativeTimeFormat(getLang(), { numeric: 'auto' })
  const abs = Math.abs(diff)
  if (abs < 60) return rtf.format(diff, 'second')
  if (abs < 3600) return rtf.format(Math.round(diff / 60), 'minute')
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), 'hour')
  return rtf.format(Math.round(diff / 86400), 'day')
}

/** Dos iniciales de un nombre o un email: «Ana Pérez» → AP, ernesto.suarez@x → ES. */
export function initials(text: string): string {
  const parts = text.split('@')[0].split(/[\s._\-+]+/).filter(Boolean)
  const s = parts.length > 1 ? parts[0][0] + parts[1][0] : (parts[0] ?? '?').slice(0, 2)
  return s.toUpperCase()
}

/** Fecha corta en el idioma activo: 28 oct 2026. */
export function shortDate(unixSeconds: number): string {
  return new Date(unixSeconds * 1000).toLocaleDateString(getLang(), { day: 'numeric', month: 'short', year: 'numeric' })
}

/** Nombre a mostrar de un usuario: su nombre o, si no tiene, el email. */
export const displayName = (u: { name: string; email: string }) => u.name || u.email

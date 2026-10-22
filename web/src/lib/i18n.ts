import { useSyncExternalStore } from 'react'
import { en } from './i18n.en'

/**
 * Traducción de la interfaz sin dependencias. El texto en español es la clave:
 * `t('Subir archivos')` lo busca en el diccionario del idioma activo y, si falta,
 * devuelve el español. `{nombre}` se sustituye con `vars`.
 */
export type Lang = 'es' | 'en'

export const languages: { value: Lang; label: string }[] = [
  { value: 'es', label: 'Español' },
  { value: 'en', label: 'English' },
]

const dictionaries: Record<Lang, Record<string, string> | null> = { es: null, en }
const storageKey = 'cloudmesh.lang'
const listeners = new Set<() => void>()

function initialLang(): Lang {
  try {
    const saved = localStorage.getItem(storageKey)
    if (saved === 'es' || saved === 'en') return saved
  } catch {
    // sin localStorage (modo privado): se usa el del navegador
  }
  return navigator.language.toLowerCase().startsWith('es') ? 'es' : 'en'
}

let current: Lang = initialLang()
document.documentElement.lang = current

/** Idioma activo, fuera de React (formatos, mensajes de la cola de subidas). */
export const getLang = (): Lang => current

export function setLang(lang: Lang) {
  if (lang === current) return
  current = lang
  document.documentElement.lang = lang
  try {
    localStorage.setItem(storageKey, lang)
  } catch {
    // se pierde al recargar, sin más
  }
  listeners.forEach((l) => l())
}

const subscribe = (l: () => void) => {
  listeners.add(l)
  return () => listeners.delete(l)
}

/** Idioma activo como estado de React: re-renderiza al cambiarlo. */
export const useLang = () => useSyncExternalStore(subscribe, getLang)

/** Marca un texto para traducir más tarde con `t` (constantes de módulo, como el menú). */
export const msg = (text: string) => text

export function t(text: string, vars?: Record<string, string | number>): string {
  const out = dictionaries[current]?.[text] ?? text
  if (!vars) return out
  return out.replace(/\{(\w+)\}/g, (m, k: string) => (k in vars ? String(vars[k]) : m))
}

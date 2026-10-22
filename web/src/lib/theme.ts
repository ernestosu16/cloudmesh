import { useSyncExternalStore } from 'react'
import { msg } from './i18n'

/**
 * Tema claro u oscuro. `system` sigue al sistema operativo. La clase `dark` en
 * `<html>` activa los colores de `.dark` en `index.css`; `index.html` la pone
 * antes de pintar para que no parpadee al cargar.
 */
export type Theme = 'system' | 'light' | 'dark'

export const themes: { value: Theme; label: string }[] = [
  { value: 'system', label: msg('Sistema') },
  { value: 'light', label: msg('Claro') },
  { value: 'dark', label: msg('Oscuro') },
]

// Misma clave que el script de index.html.
const storageKey = 'cloudmesh.theme'
const listeners = new Set<() => void>()
const media = window.matchMedia('(prefers-color-scheme: dark)')

function initialTheme(): Theme {
  try {
    const saved = localStorage.getItem(storageKey)
    if (saved === 'light' || saved === 'dark') return saved
  } catch {
    // sin localStorage (modo privado): se sigue al sistema
  }
  return 'system'
}

let current: Theme = initialTheme()

function apply() {
  const dark = current === 'dark' || (current === 'system' && media.matches)
  document.documentElement.classList.toggle('dark', dark)
  // Barra del navegador y de la webapp instalada con el color de la cabecera.
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', dark ? '#111821' : '#ffffff')
}

apply()
media.addEventListener('change', apply)

export const getTheme = (): Theme => current

export function setTheme(theme: Theme) {
  if (theme === current) return
  current = theme
  try {
    if (theme === 'system') localStorage.removeItem(storageKey)
    else localStorage.setItem(storageKey, theme)
  } catch {
    // se pierde al recargar, sin más
  }
  apply()
  listeners.forEach((l) => l())
}

const subscribe = (l: () => void) => {
  listeners.add(l)
  return () => listeners.delete(l)
}

/** Tema elegido como estado de React. */
export const useTheme = () => useSyncExternalStore(subscribe, getTheme)

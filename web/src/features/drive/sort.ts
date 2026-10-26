import { useState } from 'react'
import type { DriveFile, Folder, Share } from '@/lib/api'
import { getLang } from '@/lib/i18n'

/** Columnas del listado del disco que se pueden ordenar. */
export type SortKey = 'name' | 'size' | 'date' | 'access'
/** null: el orden del servidor (por nombre, carpetas primero). */
export type Sort = { key: SortKey; dir: 'asc' | 'desc' } | null

/** Cada clic en una columna: ascendente → descendente → sin orden. */
export function nextSort(cur: Sort, key: SortKey): Sort {
  if (cur?.key !== key) return { key, dir: 'asc' }
  return cur.dir === 'asc' ? { key, dir: 'desc' } : null
}

const STORAGE_KEY = 'cloudmesh.drive.sort'
const keys: SortKey[] = ['name', 'size', 'date', 'access']

function load(): Sort {
  try {
    const v = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? 'null') as Sort
    return v && keys.includes(v.key) && (v.dir === 'asc' || v.dir === 'desc') ? v : null
  } catch {
    return null
  }
}

/** Orden del listado; se recuerda en este navegador entre carpetas y visitas. */
export function useDriveSort(): [Sort, (key: SortKey) => void] {
  const [sort, setSort] = useState<Sort>(load)
  const toggle = (key: SortKey) => {
    const next = nextSort(sort, key)
    setSort(next)
    try {
      if (next) localStorage.setItem(STORAGE_KEY, JSON.stringify(next))
      else localStorage.removeItem(STORAGE_KEY)
    } catch {
      // Sin almacenamiento el orden solo dura la visita.
    }
  }
  return [sort, toggle]
}

type ShareOf = (kind: Share['kind'], id: number) => Share | undefined

// Orden de la columna Acceso: lo que pide atención primero, como se ve en la etiqueta.
const accessRank = { lost: 0, degraded: 1, restricted: 2, link: 3, private: 4 }

function folderAccess(f: Folder, share: ShareOf) {
  return f.restricted ? accessRank.restricted : share('folder', f.id) ? accessRank.link : accessRank.private
}

function fileAccess(f: DriveFile, share: ShareOf) {
  if (f.status === 'lost') return accessRank.lost
  if (f.status === 'degraded') return accessRank.degraded
  return share('file', f.id) ? accessRank.link : accessRank.private
}

const byName = (a: string, b: string) => a.localeCompare(b, getLang(), { numeric: true, sensitivity: 'base' })

function sorted<T extends { name: string }>(items: T[], sort: Sort, value: (x: T) => number): T[] {
  if (!sort) return items
  const sign = sort.dir === 'asc' ? 1 : -1
  return [...items].sort((a, b) => {
    const d = sort.key === 'name' ? byName(a.name, b.name) : value(a) - value(b)
    // Empates (mismo tamaño, misma etiqueta): por nombre, siempre de la A a la Z.
    return d !== 0 ? sign * d : byName(a.name, b.name)
  })
}

/**
 * Ordena carpetas y archivos por la columna elegida. Las carpetas siguen
 * delante, como en cualquier explorador; cada grupo se ordena por su cuenta.
 */
export function sortListing(folders: Folder[], files: DriveFile[], sort: Sort, share: ShareOf) {
  const folderValue = (f: Folder) =>
    sort?.key === 'size' ? (f.size ?? 0) : sort?.key === 'date' ? f.created_at : folderAccess(f, share)
  const fileValue = (f: DriveFile) => (sort?.key === 'size' ? f.size : sort?.key === 'date' ? f.mtime : fileAccess(f, share))
  return { folders: sorted(folders, sort, folderValue), files: sorted(files, sort, fileValue) }
}

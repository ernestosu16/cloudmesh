import { useSyncExternalStore } from 'react'
import { ApiError, createFolder, uploadFile } from '@/lib/api'
import { t } from '@/lib/i18n'

export type UploadStatus = 'queued' | 'uploading' | 'done' | 'error' | 'cancelled'

export interface UploadItem {
  id: number
  batchId: number
  name: string
  /** Carpetas bajo el destino del lote, sin el nombre del archivo: ['Fotos', '2024']. */
  dirs: string[]
  size: number
  loaded: number
  status: UploadStatus
  error?: string
}

/** Lo que se sube junto: un archivo suelto o un directorio entero. */
export interface UploadBatch {
  id: number
  kind: 'file' | 'folder'
  name: string
  folderId: number
  items: UploadItem[]
  size: number
  loaded: number
  count: Record<UploadStatus, number>
  /** Error al crear alguna carpeta vacía del directorio. */
  error?: string
}

export interface UploadSnapshot {
  batches: UploadBatch[]
  /** Archivos en cola o subiendo. */
  pending: number
  files: number
  done: number
  failed: number
  size: number
  loaded: number
  /** Bytes por segundo en los últimos segundos; 0 si no se sube nada. */
  rate: number
}

/** Un archivo elegido con su ruta relativa al destino. */
export interface PickedFile {
  file: File
  dirs: string[]
}

/** Cola de subidas global: sobrevive a cambios de ruta. Máximo 3 archivos en paralelo. */
const MAX_PARALLEL = 3
/** Basura de sistema que no merece la pena subir al arrastrar carpetas. */
const IGNORED = new Set(['.DS_Store', 'Thumbs.db', 'desktop.ini'])

interface BatchMeta {
  id: number
  kind: 'file' | 'folder'
  name: string
  folderId: number
  ids: number[]
  error?: string
}

const items = new Map<number, UploadItem>()
const batches = new Map<number, BatchMeta>()
const files = new Map<number, File>()
const controllers = new Map<number, AbortController>()
const listeners = new Set<() => void>()
let nextId = 1
let onDone: (() => void) | null = null

// ---- Carpetas --------------------------------------------------------------

/** Carpetas ya resueltas en esta tanda; se vacía cuando la cola queda inactiva. */
const folderCache = new Map<string, Promise<number>>()

/** Id de la carpeta dirs bajo base, creándola (y sus padres) si no existe. */
function resolveFolder(base: number, dirs: string[]): Promise<number> {
  if (dirs.length === 0) return Promise.resolve(base)
  const key = `${base}:${dirs.join('/')}`
  let p = folderCache.get(key)
  if (!p) {
    p = resolveFolder(base, dirs.slice(0, -1))
      .then((parent) => createFolder(parent, dirs[dirs.length - 1], true))
      .then((f) => {
        notifyDrive()
        return f.id
      })
    p.catch(() => folderCache.delete(key))
    folderCache.set(key, p)
  }
  return p
}

// ---- Velocidad -------------------------------------------------------------

let transferred = 0
const samples: { t: number; bytes: number }[] = []

function currentRate(active: boolean): number {
  const t = performance.now()
  samples.push({ t, bytes: transferred })
  while (samples.length > 1 && t - samples[0].t > 6000) samples.shift()
  if (!active || samples.length < 2) return 0
  const first = samples[0]
  const dt = (t - first.t) / 1000
  return dt < 0.5 ? 0 : (transferred - first.bytes) / dt
}

// ---- Notificaciones --------------------------------------------------------

let emitTimer: ReturnType<typeof setTimeout> | null = null
let driveTimer: ReturnType<typeof setTimeout> | null = null
let snapshot: UploadSnapshot = build()

/** Agrupa los cambios (el progreso llega muchas veces por segundo). */
function emit(now = false) {
  if (now) {
    if (emitTimer) clearTimeout(emitTimer)
    emitTimer = null
    snapshot = build()
    listeners.forEach((l) => l())
    return
  }
  emitTimer ??= setTimeout(() => emit(true), 150)
}

/** Avisa al drive como mucho una vez por segundo aunque terminen cien archivos. */
function notifyDrive() {
  driveTimer ??= setTimeout(() => {
    driveTimer = null
    onDone?.()
  }, 1000)
}

function build(): UploadSnapshot {
  const out: UploadSnapshot = { batches: [], pending: 0, files: 0, done: 0, failed: 0, size: 0, loaded: 0, rate: 0 }
  for (const meta of batches.values()) {
    const b: UploadBatch = {
      id: meta.id,
      kind: meta.kind,
      name: meta.name,
      folderId: meta.folderId,
      items: [],
      size: 0,
      loaded: 0,
      count: { queued: 0, uploading: 0, done: 0, error: 0, cancelled: 0 },
      error: meta.error,
    }
    for (const id of meta.ids) {
      const it = items.get(id)!
      b.items.push(it)
      b.count[it.status]++
      if (it.status === 'cancelled') continue
      b.size += it.size
      b.loaded += it.status === 'done' ? it.size : it.loaded
    }
    out.batches.push(b)
    out.pending += b.count.queued + b.count.uploading
    out.done += b.count.done
    out.failed += b.count.error
    out.files += b.items.length - b.count.cancelled
    out.size += b.size
    out.loaded += b.loaded
  }
  out.rate = currentRate(out.pending > 0)
  return out
}

// ---- Cola ------------------------------------------------------------------

function patch(id: number, p: Partial<UploadItem>, now = false) {
  const it = items.get(id)
  if (!it) return
  items.set(id, { ...it, ...p })
  emit(now)
}

function pump() {
  let active = 0
  for (const it of items.values()) if (it.status === 'uploading') active++
  for (const it of items.values()) {
    if (active >= MAX_PARALLEL) break
    if (it.status !== 'queued') continue
    active++
    void start(it)
  }
  if (active === 0) folderCache.clear()
}

async function start(item: UploadItem) {
  const file = files.get(item.id)
  const batch = batches.get(item.batchId)
  if (!file || !batch) return
  const controller = new AbortController()
  controllers.set(item.id, controller)
  patch(item.id, { status: 'uploading', loaded: 0, error: undefined }, true)
  let last = 0
  try {
    const folderId = await resolveFolder(batch.folderId, item.dirs)
    if (controller.signal.aborted) throw new DOMException('Cancelada', 'AbortError')
    await uploadFile(
      folderId,
      file,
      (loaded) => {
        transferred += Math.max(0, loaded - last)
        last = loaded
        patch(item.id, { loaded })
      },
      controller.signal,
    )
    files.delete(item.id)
    patch(item.id, { status: 'done', loaded: item.size }, true)
    notifyDrive()
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') {
      patch(item.id, { status: 'cancelled', loaded: 0 }, true)
    } else {
      patch(item.id, { status: 'error', loaded: 0, error: errorText(err) }, true)
    }
  } finally {
    controllers.delete(item.id)
    pump()
  }
}

function errorText(err: unknown): string {
  if (err instanceof ApiError) return err.message
  return t('No se pudo conectar con el servidor')
}

function addBatch(kind: BatchMeta['kind'], name: string, folderId: number, picked: PickedFile[]): BatchMeta {
  const meta: BatchMeta = { id: nextId++, kind, name, folderId, ids: [] }
  for (const { file, dirs } of picked) {
    const id = nextId++
    files.set(id, file)
    items.set(id, { id, batchId: meta.id, name: file.name, dirs, size: file.size, loaded: 0, status: 'queued' })
    meta.ids.push(id)
  }
  batches.set(meta.id, meta)
  return meta
}

function cancelItem(id: number) {
  const it = items.get(id)
  if (it?.status === 'uploading') controllers.get(id)?.abort()
  else if (it?.status === 'queued') patch(id, { status: 'cancelled' })
}

function retryItem(id: number) {
  const it = items.get(id)
  if ((it?.status === 'error' || it?.status === 'cancelled') && files.has(id)) {
    patch(id, { status: 'queued', loaded: 0, error: undefined })
  }
}

function removeBatch(meta: BatchMeta) {
  for (const id of meta.ids) {
    items.delete(id)
    files.delete(id)
  }
  batches.delete(meta.id)
}

const finished = (meta: BatchMeta) =>
  meta.ids.every((id) => {
    const s = items.get(id)!.status
    return s !== 'queued' && s !== 'uploading'
  })

export const uploads = {
  /**
   * Encola archivos sueltos o los de un <input webkitdirectory>: si traen
   * webkitRelativePath se agrupan por su directorio de primer nivel.
   */
  enqueue(folderId: number, list: FileList | File[]) {
    uploads.enqueuePicked(
      folderId,
      Array.from(list).map((file) => {
        const parts = (file.webkitRelativePath || '').split('/').filter(Boolean)
        return { file, dirs: parts.slice(0, -1) }
      }),
    )
  },

  /** Encola archivos con su ruta; emptyDirs son carpetas sin archivos que también se crean. */
  enqueuePicked(folderId: number, picked: PickedFile[], emptyDirs: string[][] = []) {
    const groups = new Map<string, PickedFile[]>()
    for (const p of picked) {
      if (IGNORED.has(p.file.name)) continue
      if (p.dirs.length === 0) {
        addBatch('file', p.file.name, folderId, [p])
        continue
      }
      const g = groups.get(p.dirs[0]) ?? []
      g.push(p)
      groups.set(p.dirs[0], g)
    }
    for (const [name, list] of groups) addBatch('folder', name, folderId, list)

    // Carpetas vacías: se crean aparte; las que tienen archivos ya las crea la subida.
    for (const dirs of emptyDirs) {
      const meta =
        [...batches.values()].reverse().find((b) => b.kind === 'folder' && b.name === dirs[0] && b.folderId === folderId) ??
        addBatch('folder', dirs[0], folderId, [])
      resolveFolder(folderId, dirs).catch((err: unknown) => {
        meta.error = t('No se pudo crear «{ruta}»: {error}', { ruta: dirs.join('/'), error: errorText(err) })
        emit()
      })
    }
    emit(true)
    pump()
  },

  cancel: (id: number) => cancelItem(id),
  retry(id: number) {
    retryItem(id)
    pump()
  },
  cancelBatch(batchId: number) {
    batches.get(batchId)?.ids.forEach(cancelItem)
  },
  retryBatch(batchId: number) {
    batches.get(batchId)?.ids.forEach(retryItem)
    pump()
  },
  /** Quita un lote de la lista; si sigue activo, lo cancela antes. */
  dismissBatch(batchId: number) {
    const meta = batches.get(batchId)
    if (!meta) return
    meta.ids.forEach(cancelItem)
    removeBatch(meta)
    emit(true)
  },
  cancelAll() {
    for (const id of items.keys()) cancelItem(id)
  },
  retryAll() {
    for (const id of items.keys()) retryItem(id)
    pump()
  },
  /** Quita los lotes terminados del todo, bien o mal. */
  clearFinished() {
    for (const meta of [...batches.values()]) if (finished(meta)) removeBatch(meta)
    emit(true)
  },
  setOnDone(fn: (() => void) | null) {
    onDone = fn
  },
}

// Cerrar la pestaña corta las subidas: el navegador pide confirmación.
if (typeof window !== 'undefined') {
  window.addEventListener('beforeunload', (e) => {
    if (snapshot.pending > 0) e.preventDefault()
  })
}

export function useUploads(): UploadSnapshot {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l)
      return () => listeners.delete(l)
    },
    () => snapshot,
  )
}

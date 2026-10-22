import type { PickedFile } from './store'

export interface Dropped {
  files: PickedFile[]
  /** Carpetas sin ningún archivo dentro (a cualquier profundidad). */
  emptyDirs: string[][]
}

/** ¿El arrastre trae archivos del sistema? Evita reaccionar a texto o enlaces arrastrados. */
export function hasFiles(dt: DataTransfer | null): boolean {
  return !!dt && Array.from(dt.types).includes('Files')
}

/**
 * Lee lo soltado, recorriendo directorios enteros. Debe llamarse dentro del
 * propio evento drop: las entradas solo se pueden pedir de forma síncrona.
 */
export function collectDrop(dt: DataTransfer): Promise<Dropped> {
  const entries: FileSystemEntry[] = []
  const loose: File[] = []
  for (const item of Array.from(dt.items)) {
    if (item.kind !== 'file') continue
    const entry = item.webkitGetAsEntry?.()
    if (entry) entries.push(entry)
    else {
      const f = item.getAsFile()
      if (f) loose.push(f)
    }
  }
  // Navegadores sin entradas: solo archivos sueltos.
  if (entries.length === 0 && loose.length === 0) loose.push(...Array.from(dt.files))

  return (async () => {
    const out: Dropped = { files: loose.map((file) => ({ file, dirs: [] })), emptyDirs: [] }
    for (const entry of entries) await walk(entry, [], out)
    return out
  })()
}

async function walk(entry: FileSystemEntry, dirs: string[], out: Dropped): Promise<number> {
  if (entry.isFile) {
    const file = await new Promise<File>((resolve, reject) => (entry as FileSystemFileEntry).file(resolve, reject))
    out.files.push({ file, dirs })
    return 1
  }
  if (!entry.isDirectory) return 0
  const here = [...dirs, entry.name]
  let n = 0
  for (const child of await readAll(entry as FileSystemDirectoryEntry)) n += await walk(child, here, out)
  if (n === 0) out.emptyDirs.push(here)
  return n
}

/** readEntries devuelve tandas (100 en Chrome): hay que pedir hasta que llegue vacía. */
async function readAll(dir: FileSystemDirectoryEntry): Promise<FileSystemEntry[]> {
  const reader = dir.createReader()
  const all: FileSystemEntry[] = []
  for (;;) {
    const batch = await new Promise<FileSystemEntry[]>((resolve, reject) => reader.readEntries(resolve, reject))
    if (batch.length === 0) return all
    all.push(...batch)
  }
}

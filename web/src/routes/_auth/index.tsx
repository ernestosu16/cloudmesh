import { useMemo, useRef, useState, type DragEvent } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useMutation, useQuery, useSuspenseQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { FolderPlus, FolderUp, Plus, Search, Trash2, Upload, X } from 'lucide-react'
import {
  ApiError,
  createFolder,
  deleteFile,
  deleteFolder,
  folderQuery,
  searchQuery,
  sharesQuery,
  updateFile,
  updateFolder,
  type DriveFile,
  type Folder,
  type Share,
} from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Breadcrumbs } from '@/features/drive/Breadcrumbs'
import { FileRow, FolderRow, RowsHeader, SelectBox, type FileActions, type FolderActions } from '@/features/drive/Rows'
import { NameDialog } from '@/features/drive/NameDialog'
import { DetailsDialog, MoveDialog, PreviewDialog, useInvalidateDrive } from '@/features/drive/FileDialogs'
import { uploads } from '@/features/uploads/store'
import { collectDrop, hasFiles } from '@/features/uploads/collect'
import { ShareDialog, type ShareTarget } from '@/features/shares/ShareEditor'
import { FolderAccessDialog } from '@/features/drive/FolderAccessDialog'
import { Chip } from '@/features/shell/Page'
import { can } from '@/features/auth/roles'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { msg, t } from '@/lib/i18n'

/** React no tipa webkitdirectory; con él, el selector elige carpetas enteras. */
const directoryProps = { webkitdirectory: '', directory: '' } as Record<string, string>

export const Route = createFileRoute('/_auth/')({
  validateSearch: z.object({
    folder: z.number().int().nonnegative().optional().catch(undefined),
    q: z.string().optional().catch(undefined),
  }),
  loaderDeps: ({ search }) => ({ folder: search.folder ?? 0 }),
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(folderQuery(deps.folder)),
  component: DrivePage,
})

type Target = { kind: 'folder'; item: Folder } | { kind: 'file'; item: DriveFile }

type Filter = 'all' | 'folders' | 'docs' | 'images' | 'videos'

const filters: { value: Filter; label: string }[] = [
  { value: 'all', label: msg('Todo') },
  { value: 'folders', label: msg('Carpetas') },
  { value: 'docs', label: msg('Documentos') },
  { value: 'images', label: msg('Imágenes') },
  { value: 'videos', label: msg('Vídeos') },
]

const fileMatches = (f: DriveFile, filter: Filter) => {
  switch (filter) {
    case 'all':
      return true
    case 'folders':
      return false
    case 'images':
      return f.mime.startsWith('image/')
    case 'videos':
      return f.mime.startsWith('video/')
    case 'docs':
      return !/^(image|video|audio)\//.test(f.mime)
  }
}

/** Índice de enlaces por elemento compartido. */
function useShareIndex(enabled: boolean) {
  const { data = [] } = useQuery({ ...sharesQuery, enabled })
  return useMemo(() => {
    const byKey = new Map<string, Share>()
    for (const s of data) byKey.set(`${s.kind}:${s.target_id}`, s)
    return { all: data, get: (kind: Share['kind'], id: number) => byKey.get(`${kind}:${id}`) }
  }, [data])
}

function DrivePage() {
  const { user } = Route.useRouteContext()
  const { folder: folderId = 0, q = '' } = Route.useSearch()
  const navigate = useNavigate({ from: '/' })
  const { data } = useSuspenseQuery(folderQuery(folderId))
  // Editar pide el rol y que la carpeta abierta no sea de solo lectura para este usuario.
  const editor = can(user, 'editor') && data.writable
  const admin = can(user, 'admin')
  const [accessFolder, setAccessFolder] = useState<Folder | null>(null)
  const search = useQuery(searchQuery(q))
  const shares = useShareIndex(can(user, 'editor'))
  const invalidate = useInvalidateDrive()
  const fileInput = useRef<HTMLInputElement>(null)
  const dirInput = useRef<HTMLInputElement>(null)
  const [dragging, setDragging] = useState(false)
  // Carpeta de la lista bajo el cursor; null = la carpeta abierta.
  const [dropFolder, setDropFolder] = useState<number | null>(null)
  const dragDepth = useRef(0)
  const [filter, setFilter] = useState<Filter>('all')

  const [creating, setCreating] = useState(false)
  const [renaming, setRenaming] = useState<Target | null>(null)
  const [moving, setMoving] = useState<Target | null>(null)
  const [deleting, setDeleting] = useState<Target | null>(null)
  const [preview, setPreview] = useState<DriveFile | null>(null)
  const [details, setDetails] = useState<DriveFile | null>(null)
  const [sharing, setSharing] = useState<ShareTarget | null>(null)
  // Selección múltiple de la carpeta abierta (claves `d<id>` / `f<id>`); se descarta al cambiar de carpeta.
  const [selection, setSelection] = useState<{ folder: number; keys: Set<string> }>({ folder: folderId, keys: new Set() })
  const [bulkDeleting, setBulkDeleting] = useState(false)

  const create = useMutation({
    mutationFn: (name: string) => createFolder(folderId, name),
    onSuccess: () => void invalidate().then(() => setCreating(false)),
  })
  const rename = useMutation({
    mutationFn: async ({ target, name }: { target: Target; name: string }): Promise<DriveFile | Folder> =>
      target.kind === 'folder' ? updateFolder(target.item.id, { name }) : updateFile(target.item.id, { name }),
    onSuccess: () => void invalidate().then(() => setRenaming(null)),
  })
  const move = useMutation({
    mutationFn: async ({ target, to }: { target: Target; to: number }): Promise<DriveFile | Folder> =>
      target.kind === 'folder' ? updateFolder(target.item.id, { parent_id: to }) : updateFile(target.item.id, { folder_id: to }),
    onSuccess: () => void invalidate().then(() => setMoving(null)),
  })
  const remove = useMutation({
    mutationFn: (target: Target) => (target.kind === 'folder' ? deleteFolder(target.item.id) : deleteFile(target.item.id)),
    onSuccess: () => void invalidate().then(() => setDeleting(null)),
  })

  const removeMany = useMutation({
    mutationFn: async (targets: Target[]) => {
      const results = await Promise.allSettled(
        targets.map((x) => (x.kind === 'folder' ? deleteFolder(x.item.id) : deleteFile(x.item.id))),
      )
      const failed = results.filter((r) => r.status === 'rejected')
      if (failed.length > 0) {
        const reason = failed[0].reason instanceof ApiError ? failed[0].reason.message : t('Error de red')
        throw new Error(t('No se pudieron eliminar {n} de {total} elementos: {motivo}', { n: failed.length, total: targets.length, motivo: reason }))
      }
    },
    onSettled: () => void invalidate(),
    onSuccess: () => {
      setBulkDeleting(false)
      setSelection({ folder: folderId, keys: new Set() })
    },
  })

  const setQuery = (value: string) =>
    void navigate({ search: (prev) => ({ ...prev, q: value || undefined }), replace: true })

  const rowFolder = (e: DragEvent): number | null => {
    if (searching) return null
    const id = (e.target as HTMLElement).closest<HTMLElement>('[data-drop-folder]')?.dataset.dropFolder
    return id ? Number(id) : null
  }
  const endDrag = () => {
    dragDepth.current = 0
    setDragging(false)
    setDropFolder(null)
  }
  const onDragEnter = (e: DragEvent) => {
    if (!editor || !hasFiles(e.dataTransfer)) return
    dragDepth.current++
    setDragging(true)
  }
  const onDragOver = (e: DragEvent) => {
    if (!editor || !hasFiles(e.dataTransfer)) return
    e.preventDefault()
    e.dataTransfer.dropEffect = 'copy'
    const target = rowFolder(e)
    if (target !== dropFolder) setDropFolder(target)
  }
  const onDragLeave = () => {
    if (dragDepth.current > 0 && --dragDepth.current === 0) endDrag()
  }
  const onDrop = (e: DragEvent) => {
    if (!editor || !hasFiles(e.dataTransfer)) return
    e.preventDefault()
    const target = rowFolder(e) ?? folderId
    endDrag()
    void collectDrop(e.dataTransfer).then(({ files, emptyDirs }) => uploads.enqueuePicked(target, files, emptyDirs))
  }
  const dropName = (dropFolder !== null && data.folders.find((f) => f.id === dropFolder)?.name) || null

  const fileActions: FileActions = {
    onPreview: setPreview,
    onDetails: setDetails,
    edit: editor
      ? {
          onRename: (item) => setRenaming({ kind: 'file', item }),
          onMove: (item) => setMoving({ kind: 'file', item }),
          onDelete: (item) => setDeleting({ kind: 'file', item }),
          onShare: (item) => setSharing({ kind: 'file', id: item.id, name: item.name }),
        }
      : undefined,
  }
  const folderActions: FolderActions = {
    onAccess: admin ? setAccessFolder : undefined,
    edit: editor
      ? {
          onRename: (item) => setRenaming({ kind: 'folder', item }),
          onMove: (item) => setMoving({ kind: 'folder', item }),
          onDelete: (item) => setDeleting({ kind: 'folder', item }),
          onShare: (item) => setSharing({ kind: 'folder', id: item.id, name: item.name }),
        }
      : undefined,
  }

  const searching = q.trim().length > 0
  const folders = filter === 'all' || filter === 'folders' ? data.folders : []
  const files = data.files.filter((f) => fileMatches(f, filter))
  const empty = folders.length === 0 && files.length === 0
  const here = data.folder.id === 0 ? t('Mi espacio') : data.folder.name

  // Solo cuenta lo visible: cambiar el filtro no borra a escondidas lo que ya no se ve.
  const selectable = editor && !searching
  const keys = selection.folder === folderId ? selection.keys : new Set<string>()
  const visible: Target[] = [
    ...folders.map((item) => ({ kind: 'folder' as const, item })),
    ...files.map((item) => ({ kind: 'file' as const, item })),
  ]
  const keyOf = (x: Target) => `${x.kind === 'folder' ? 'd' : 'f'}${x.item.id}`
  const picked = selectable ? visible.filter((x) => keys.has(keyOf(x))) : []
  const toggle = (x: Target, on: boolean) => {
    const next = new Set(keys)
    if (on) next.add(keyOf(x))
    else next.delete(keyOf(x))
    setSelection({ folder: folderId, keys: next })
  }
  const toggleAll = (on: boolean) => setSelection({ folder: folderId, keys: new Set(on ? visible.map(keyOf) : []) })
  const selectBox = (x: Target) =>
    selectable && (
      <SelectBox checked={keys.has(keyOf(x))} label={t('Seleccionar {nombre}', { nombre: x.item.name })} onChange={(on) => toggle(x, on)} />
    )
  const pickedFolders = picked.filter((x) => x.kind === 'folder').length
  const selectAll = selectable && (
    <SelectBox
      checked={picked.length === visible.length}
      indeterminate={picked.length > 0 && picked.length < visible.length}
      label={t('Seleccionar todo')}
      onChange={toggleAll}
    />
  )

  return (
    <section
      className="grid grid-cols-1 gap-6"
      onDragEnter={onDragEnter}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
    >
      {dragging && (
        <div className="pointer-events-none fixed inset-0 z-30 flex items-end justify-center bg-primary/5 p-6 pb-[calc(6rem+env(safe-area-inset-bottom))] ring-4 ring-primary/60 ring-inset lg:pb-10">
          <p className="flex items-center gap-2 rounded-xl bg-primary px-4 py-2.5 text-sm font-medium text-primary-foreground shadow-lg">
            <Upload className="size-4" aria-hidden />
            {t('Suelta para subir a')} <strong className="max-w-[50vw] truncate">{dropName ?? here}</strong>
          </p>
        </div>
      )}
      <div className="flex flex-wrap items-center gap-3">
        <div className="relative min-w-0 flex-[1_1_18rem]">
          <Search className="pointer-events-none absolute top-1/2 left-3.5 size-[18px] -translate-y-1/2 text-muted-foreground" />
          <Input
            type="search"
            placeholder={t('Buscar archivos y carpetas…')}
            aria-label={t('Buscar')}
            className="rounded-xl border-border pr-10 pl-10 text-base md:text-[15px]"
            value={q}
            onChange={(e) => setQuery(e.target.value)}
          />
          {searching && (
            <button
              type="button"
              aria-label={t('Limpiar búsqueda')}
              className="absolute top-1/2 right-1 flex size-9 -translate-y-1/2 items-center justify-center text-muted-foreground hover:text-foreground"
              onClick={() => setQuery('')}
            >
              <X className="size-4" />
            </button>
          )}
        </div>
        {editor && (
          <>
            <Button variant="outline" size="lg" onClick={() => setCreating(true)} className="rounded-xl">
              <FolderPlus /> {t('Nueva carpeta')}
            </Button>
            <Button variant="outline" size="lg" onClick={() => dirInput.current?.click()} className="hidden rounded-xl sm:inline-flex">
              <FolderUp /> {t('Subir carpeta')}
            </Button>
          </>
        )}
        <input
          ref={fileInput}
          type="file"
          multiple
          className="hidden"
          onChange={(e) => {
            if (e.target.files?.length) uploads.enqueue(folderId, e.target.files)
            e.target.value = ''
          }}
        />
        <input
          ref={dirInput}
          type="file"
          multiple
          {...directoryProps}
          className="hidden"
          onChange={(e) => {
            if (e.target.files?.length) uploads.enqueue(folderId, e.target.files)
            e.target.value = ''
          }}
        />
      </div>

      <div className="flex flex-wrap items-center justify-between gap-3">
        {searching ? (
          <p className="text-[15px] font-semibold">{t('Resultados para «{q}»', { q })}</p>
        ) : (
          <Breadcrumbs breadcrumbs={data.breadcrumbs} current={data.folder} />
        )}
        <div role="group" aria-label={t('Filtrar por tipo')} className="flex w-full min-w-0 gap-1.5 overflow-x-auto [scrollbar-width:none] sm:w-auto sm:flex-wrap">
          {filters.map((f) => (
            <Chip key={f.value} active={filter === f.value} onClick={() => setFilter(f.value)}>
              {t(f.label)}
            </Chip>
          ))}
        </div>
      </div>

      <div>
        {searching ? (
          <SearchResults q={q} result={search.data} pending={search.isPending} filter={filter} shares={shares} fileActions={fileActions} folderActions={folderActions} />
        ) : empty ? (
          <div className="rounded-2xl border bg-card p-10 text-center text-sm text-muted-foreground">
            {filter === 'all' ? t('Carpeta vacía.') : t('Nada de este tipo en esta carpeta.')}
          </div>
        ) : (
          <div className="overflow-hidden rounded-2xl border bg-card">
            {/* Con selección, la barra ocupa el sitio de la cabecera: las filas no se desplazan bajo el dedo. */}
            {picked.length > 0 ? (
              <div className="flex h-11 items-center gap-3 border-b bg-primary/5 px-4 md:px-5">
                {selectAll}
                <span className="min-w-0 flex-1 truncate text-sm font-medium">
                  {picked.length === 1 ? t('1 seleccionado') : t('{n} seleccionados', { n: picked.length })}
                </span>
                <Button variant="ghost" size="sm" onClick={() => toggleAll(false)}>
                  {t('Cancelar')}
                </Button>
                <Button
                  variant="destructive"
                  size="sm"
                  onClick={() => {
                    removeMany.reset()
                    setBulkDeleting(true)
                  }}
                >
                  <Trash2 /> {t('Eliminar')}
                </Button>
              </div>
            ) : (
              <RowsHeader selectAll={selectAll} />
            )}
            <ul className="divide-y divide-secondary">
              {folders.map((f) => (
                <FolderRow
                  key={`d${f.id}`}
                  folder={f}
                  share={shares.get('folder', f.id)}
                  actions={folderActions}
                  dropTarget={dragging && dropFolder === f.id}
                  select={selectBox({ kind: 'folder', item: f })}
                />
              ))}
              {files.map((f) => (
                <FileRow key={`f${f.id}`} file={f} share={shares.get('file', f.id)} actions={fileActions} select={selectBox({ kind: 'file', item: f })} />
              ))}
            </ul>
          </div>
        )}
      </div>

      {editor && !searching && (
        <div className="hidden flex-wrap items-center justify-center gap-2.5 rounded-2xl border-2 border-dashed border-input p-7 text-center text-[15px] text-secondary-foreground lg:flex">
          <Upload className="size-5" aria-hidden />
          <span>
            {t('Arrastra archivos o carpetas aquí para subirlos a')} <strong className="text-foreground">{here}</strong>
            {t(', o elige')}{' '}
            <button type="button" className="font-medium text-primary hover:underline" onClick={() => fileInput.current?.click()}>
              {t('archivos')}
            </button>{' '}
            {t('o una')}{' '}
            <button type="button" className="font-medium text-primary hover:underline" onClick={() => dirInput.current?.click()}>
              {t('carpeta')}
            </button>
            .
          </span>
        </div>
      )}

      {editor && (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              size="icon-lg"
              aria-label={t('Subir o crear')}
              className="fixed right-5 bottom-[calc(5.5rem+env(safe-area-inset-bottom))] z-20 size-14 rounded-2xl shadow-lg shadow-primary/40 lg:hidden"
            >
              <Plus className="size-6" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" side="top">
            <DropdownMenuItem onSelect={() => fileInput.current?.click()}>
              <Upload className="size-4" /> {t('Subir archivos')}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => dirInput.current?.click()}>
              <FolderUp className="size-4" /> {t('Subir carpeta')}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => setCreating(true)}>
              <FolderPlus className="size-4" /> {t('Nueva carpeta')}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      )}

      <NameDialog
        open={creating}
        title={t('Nueva carpeta')}
        submitLabel={t('Crear')}
        error={create.error}
        pending={create.isPending}
        onSubmit={(name) => create.mutate(name)}
        onClose={() => setCreating(false)}
      />
      <NameDialog
        open={renaming !== null}
        title={renaming?.kind === 'file' ? t('Renombrar archivo') : t('Renombrar carpeta')}
        initial={renaming?.item.name}
        submitLabel={t('Guardar')}
        error={rename.error}
        pending={rename.isPending}
        onSubmit={(name) => renaming && rename.mutate({ target: renaming, name })}
        onClose={() => setRenaming(null)}
      />
      <MoveDialog
        open={moving !== null}
        title={t('Mover «{nombre}»', { nombre: moving?.item.name ?? '' })}
        excludeFolderId={moving?.kind === 'folder' ? moving.item.id : undefined}
        pending={move.isPending}
        error={move.error}
        onPick={(to) => moving && move.mutate({ target: moving, to })}
        onClose={() => setMoving(null)}
      />
      <PreviewDialog file={preview} onClose={() => setPreview(null)} />
      <DetailsDialog file={details} onClose={() => setDetails(null)} />
      <ShareDialog target={sharing} shares={shares.all} onClose={() => setSharing(null)} />
      <FolderAccessDialog folder={accessFolder} onClose={() => setAccessFolder(null)} />
      <AlertDialog open={bulkDeleting} onOpenChange={(o) => !o && !removeMany.isPending && setBulkDeleting(false)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {picked.length === 1 ? t('¿Eliminar 1 elemento?') : t('¿Eliminar {n} elementos?', { n: picked.length })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t('Los archivos van a la papelera y podrás restaurarlos durante 30 días; sus enlaces dejan de funcionar.')}
              {pickedFolders > 0 && ` ${t('Las carpetas se eliminan con todo su contenido.')}`}
            </AlertDialogDescription>
          </AlertDialogHeader>
          {removeMany.error ? (
            <p role="alert" className="text-sm text-destructive">
              {removeMany.error.message}
            </p>
          ) : null}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={removeMany.isPending}>{t('Cancelar')}</AlertDialogCancel>
            <AlertDialogAction
              disabled={removeMany.isPending}
              onClick={(e) => {
                e.preventDefault()
                removeMany.mutate(picked)
              }}
            >
              {t('Eliminar')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <AlertDialog open={deleting !== null} onOpenChange={(o) => !o && setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {deleting?.kind === 'file'
                ? t('¿Mover «{nombre}» a la papelera?', { nombre: deleting.item.name })
                : t('¿Eliminar «{nombre}»?', { nombre: deleting?.item.name ?? '' })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {deleting?.kind === 'file'
                ? t('Podrás restaurarlo durante 30 días desde la Papelera. Mientras tanto sus enlaces dejan de funcionar.')
                : t('Se elimina con todas sus subcarpetas. Sus archivos van a la papelera y podrás restaurarlos durante 30 días (a la raíz, porque la carpeta ya no existirá).')}
            </AlertDialogDescription>
          </AlertDialogHeader>
          {remove.error ? (
            <p role="alert" className="text-sm text-destructive">
              {remove.error instanceof ApiError ? remove.error.message : t('Error de red')}
            </p>
          ) : null}
          <AlertDialogFooter>
            <AlertDialogCancel>{t('Cancelar')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault()
                if (deleting) remove.mutate(deleting)
              }}
            >
              {deleting?.kind === 'file' ? t('Mover a la papelera') : t('Eliminar')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}

function SearchResults({
  q,
  result,
  pending,
  filter,
  shares,
  fileActions,
  folderActions,
}: {
  q: string
  result?: { folders: Folder[]; files: DriveFile[] }
  pending: boolean
  filter: Filter
  shares: ReturnType<typeof useShareIndex>
  fileActions: FileActions
  folderActions: FolderActions
}) {
  if (pending || !result) return <p className="text-sm text-muted-foreground">{t('Buscando…')}</p>
  const folders = filter === 'all' || filter === 'folders' ? result.folders : []
  const files = result.files.filter((f) => fileMatches(f, filter))
  const n = folders.length + files.length
  if (n === 0) return <p className="text-sm text-muted-foreground">{t('Nada coincide con «{q}».', { q })}</p>
  return (
    <div className="grid gap-2">
      <p className="text-sm text-muted-foreground">
        {n === 1 ? t('1 resultado') : t('{n} resultados', { n })}
      </p>
      <div className="overflow-hidden rounded-2xl border bg-card">
        <RowsHeader />
        <ul className="divide-y divide-secondary">
          {folders.map((f) => (
            <FolderRow key={`d${f.id}`} folder={f} share={shares.get('folder', f.id)} actions={folderActions} />
          ))}
          {files.map((f) => (
            <FileRow key={`f${f.id}`} file={f} share={shares.get('file', f.id)} showPath actions={fileActions} />
          ))}
        </ul>
      </div>
    </div>
  )
}

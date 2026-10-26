import type { ReactNode } from 'react'
import { Link } from '@tanstack/react-router'
import { ArrowDown, ArrowUp, ArrowUpDown, Download, Eye, FolderInput, Info, Link2, Lock, MoreVertical, Pencil, Trash2, UserCog } from 'lucide-react'
import { fileContentUrl, type DriveFile, type Folder, type Share } from '@/lib/api'
import { formatBytes, relativeTime } from '@/lib/format'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Tag } from '@/features/shell/Page'
import { FileTypeIcon, FolderIcon } from './icons'
import { msg, t } from '@/lib/i18n'
import type { Sort, SortKey } from './sort'

/** Casilla de selección múltiple; la etiqueta amplía la zona táctil. */
export function SelectBox({
  checked,
  indeterminate,
  label,
  onChange,
}: {
  checked: boolean
  indeterminate?: boolean
  label: string
  onChange: (checked: boolean) => void
}) {
  return (
    <label className="-m-2 flex shrink-0 cursor-pointer items-center p-2">
      <input
        type="checkbox"
        aria-label={label}
        className="size-4 cursor-pointer accent-primary"
        checked={checked}
        ref={(el) => {
          if (el) el.indeterminate = !!indeterminate
        }}
        onChange={(e) => onChange(e.target.checked)}
      />
    </label>
  )
}

const columns: { key: SortKey; label: string; width: string }[] = [
  { key: 'name', label: msg('Nombre'), width: 'min-w-0 flex-1' },
  { key: 'size', label: msg('Tamaño'), width: 'w-24' },
  { key: 'date', label: msg('Modificado'), width: 'w-32' },
  { key: 'access', label: msg('Acceso'), width: 'w-28' },
]

function SortIcon({ dir }: { dir?: 'asc' | 'desc' }) {
  if (dir === 'asc') return <ArrowUp className="size-3.5 shrink-0" aria-hidden />
  if (dir === 'desc') return <ArrowDown className="size-3.5 shrink-0" aria-hidden />
  return <ArrowUpDown className="size-3.5 shrink-0 opacity-40" aria-hidden />
}

const sortState = (dir?: 'asc' | 'desc') => (dir === 'asc' ? t('ascendente') : dir === 'desc' ? t('descendente') : t('sin ordenar'))

/**
 * Cabecera de columnas; solo en pantallas medianas, donde las filas son tabla.
 * Con selectAll, la casilla de seleccionar todo se ve también en móvil. Con
 * onSort, cada columna ordena al pulsarla (ascendente → descendente → sin
 * orden); en móvil, donde no hay columnas, lo hace un menú «Ordenar».
 */
export function RowsHeader({ selectAll, sort, onSort }: { selectAll?: ReactNode; sort?: Sort; onSort?: (key: SortKey) => void }) {
  const dirOf = (key: SortKey) => (sort?.key === key ? sort.dir : undefined)
  return (
    <div
      className={`h-11 items-center gap-3 border-b px-4 text-xs font-semibold tracking-wider text-muted-foreground uppercase md:flex md:px-5 ${selectAll || onSort ? 'flex' : 'hidden'}`}
    >
      {selectAll}
      <span className="flex-1 md:hidden">{selectAll && t('Seleccionar todo')}</span>
      {onSort && (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="sm" className="-mr-2 text-xs text-muted-foreground normal-case md:hidden">
              {sort ? t(columns.find((c) => c.key === sort.key)!.label) : t('Ordenar')}
              <SortIcon dir={sort?.dir} />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            {columns.map((c) => (
              // preventDefault: el menú sigue abierto para pulsar otra vez y cambiar el sentido.
              <DropdownMenuItem key={c.key} onSelect={(e) => (e.preventDefault(), onSort(c.key))}>
                <span className="flex-1">{t(c.label)}</span>
                <SortIcon dir={dirOf(c.key)} />
                <span className="sr-only">{sortState(dirOf(c.key))}</span>
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      )}
      {columns.map((c) =>
        onSort ? (
          <button
            key={c.key}
            type="button"
            onClick={() => onSort(c.key)}
            title={t('Ordenar por {columna}', { columna: t(c.label) })}
            className={`hidden items-center gap-1 text-left tracking-wider uppercase hover:text-foreground md:flex ${c.width} ${dirOf(c.key) ? 'text-foreground' : ''}`}
          >
            <span className="truncate">{t(c.label)}</span>
            <SortIcon dir={dirOf(c.key)} />
            <span className="sr-only">{sortState(dirOf(c.key))}</span>
          </button>
        ) : (
          <span key={c.key} aria-hidden className={`hidden md:block ${c.width}`}>
            {t(c.label)}
          </span>
        ),
      )}
      <span className="hidden w-[88px] md:block" />
    </div>
  )
}

function Row({
  select,
  icon,
  name,
  meta,
  size,
  date,
  access,
  actions,
  dropFolder,
  dropTarget,
}: {
  select?: ReactNode
  icon: ReactNode
  name: ReactNode
  meta: string
  size: string
  date: string
  access: ReactNode
  actions: ReactNode
  /** Carpeta que recibe lo que se suelta sobre la fila (lo resuelve la página). */
  dropFolder?: number
  dropTarget?: boolean
}) {
  return (
    <li
      data-drop-folder={dropFolder}
      className={`flex items-center gap-3 px-4 py-2.5 hover:bg-background focus-within:bg-background md:px-5 ${dropTarget ? 'bg-primary/10 ring-2 ring-primary ring-inset' : ''}`}
    >
      {select}
      <span className="flex size-10 shrink-0 items-center justify-center rounded-lg border bg-card text-muted-foreground md:size-auto md:border-0 md:bg-transparent">
        {icon}
      </span>
      <div className="min-w-0 flex-1">
        {name}
        <p className="truncate text-xs text-muted-foreground md:hidden">{meta}</p>
      </div>
      <span className="hidden w-24 shrink-0 font-mono text-[13px] text-secondary-foreground md:block">{size}</span>
      <span className="hidden w-32 shrink-0 truncate text-secondary-foreground md:block">{date}</span>
      <span className="hidden w-28 shrink-0 md:block">{access}</span>
      <span className="flex shrink-0 items-center md:w-[88px] md:justify-end">{actions}</span>
    </li>
  )
}

function AccessTag({ share, status }: { share?: Share; status?: DriveFile['status'] }) {
  if (status === 'lost') return <Tag tone="danger">{t('Perdido')}</Tag>
  if (status === 'degraded') return <Tag tone="warning">{t('Degradado')}</Tag>
  return share ? <Tag tone="accent">{t('Enlace')}</Tag> : <Tag>{t('Privado')}</Tag>
}

function ShareButton({ name, onShare }: { name: string; onShare: () => void }) {
  return (
    <Button variant="ghost" size="icon" aria-label={t('Compartir {nombre}', { nombre: name })} onClick={onShare} className="hidden md:inline-flex">
      <Link2 />
    </Button>
  )
}

/** Sin edit (rol lector) solo se navega: nada de compartir, renombrar, mover ni borrar. */
export interface FolderActions {
  edit?: {
    onRename: (f: Folder) => void
    onMove: (f: Folder) => void
    onDelete: (f: Folder) => void
    onShare: (f: Folder) => void
  }
  /** Solo administradores: quién ve la carpeta. */
  onAccess?: (f: Folder) => void
}

export function FolderRow({
  folder,
  share,
  actions,
  dropTarget,
  select,
}: {
  folder: Folder
  share?: Share
  actions: FolderActions
  dropTarget?: boolean
  select?: ReactNode
}) {
  const { edit } = actions
  const date = relativeTime(folder.created_at)
  const size = folder.size === undefined ? undefined : formatBytes(folder.size)
  return (
    <Row
      select={select}
      icon={<FolderIcon className="size-5" aria-hidden />}
      dropFolder={actions.edit ? folder.id : undefined}
      dropTarget={dropTarget}
      name={
        <Link to="/" search={{ folder: folder.id }} className="flex min-w-0 items-center gap-1.5 font-medium hover:underline">
          <span className="truncate">{folder.name}</span>
          {folder.restricted && <Lock className="size-3.5 shrink-0 text-warning" aria-label={t('Carpeta restringida')} />}
        </Link>
      }
      meta={`${t('Carpeta')}${size ? ` · ${size}` : ''} · ${date}${share ? ` · ${t('enlace')}` : ''}`}
      size={size ?? '—'}
      date={date}
      access={folder.restricted ? <Tag tone="warning">{t('Restringida')}</Tag> : <AccessTag share={share} />}
      actions={
        (edit || actions.onAccess) && (
          <>
            {edit && <ShareButton name={folder.name} onShare={() => edit.onShare(folder)} />}
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon" aria-label={t('Más acciones para {nombre}', { nombre: folder.name })}>
                  <MoreVertical />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                {actions.onAccess && (
                  <DropdownMenuItem onSelect={() => actions.onAccess?.(folder)}>
                    <UserCog className="size-4" /> {t('Acceso')}
                  </DropdownMenuItem>
                )}
                {edit && (
                  <>
                    <DropdownMenuItem onSelect={() => edit.onShare(folder)}>
                      <Link2 className="size-4" /> {t('Compartir')}
                    </DropdownMenuItem>
                    <DropdownMenuItem onSelect={() => edit.onRename(folder)}>
                      <Pencil className="size-4" /> {t('Renombrar')}
                    </DropdownMenuItem>
                    <DropdownMenuItem onSelect={() => edit.onMove(folder)}>
                      <FolderInput className="size-4" /> {t('Mover')}
                    </DropdownMenuItem>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem variant="destructive" onSelect={() => edit.onDelete(folder)}>
                      <Trash2 className="size-4" /> {t('Eliminar')}
                    </DropdownMenuItem>
                  </>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          </>
        )
      }
    />
  )
}

export interface FileActions {
  onPreview: (f: DriveFile) => void
  onDetails: (f: DriveFile) => void
  /** Ausente para lectores: ver y descargar sí, cambiar no. */
  edit?: {
    onRename: (f: DriveFile) => void
    onMove: (f: DriveFile) => void
    onDelete: (f: DriveFile) => void
    onShare: (f: DriveFile) => void
  }
}

export function FileRow({
  file,
  share,
  showPath,
  actions,
  select,
}: {
  file: DriveFile
  share?: Share
  showPath?: boolean
  actions: FileActions
  select?: ReactNode
}) {
  const { edit } = actions
  const date = relativeTime(file.mtime)
  const raid = file.layout === 'rs' ? `RAID ${file.data_shards}+${file.parity_shards}` : t('entero')
  return (
    <Row
      select={select}
      icon={<FileTypeIcon mime={file.mime} className="size-5" />}
      name={
        <>
          <button
            type="button"
            title={raid}
            className="block max-w-full truncate text-left font-medium hover:underline"
            onClick={() => actions.onPreview(file)}
          >
            {file.name}
          </button>
          {showPath && (
            <Link to="/" search={{ folder: file.folder_id }} className="text-xs text-muted-foreground underline-offset-2 hover:underline">
              {t('Ir a la carpeta')}
            </Link>
          )}
        </>
      }
      meta={`${formatBytes(file.size)} · ${date}${file.status !== 'ready' ? ` · ${file.status === 'lost' ? t('perdido') : t('degradado')}` : ''}`}
      size={formatBytes(file.size)}
      date={date}
      access={<AccessTag share={share} status={file.status} />}
      actions={
        <>
          {edit && <ShareButton name={file.name} onShare={() => edit.onShare(file)} />}
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon" aria-label={t('Más acciones para {nombre}', { nombre: file.name })}>
                <MoreVertical />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onSelect={() => actions.onPreview(file)}>
                <Eye className="size-4" /> {t('Vista previa')}
              </DropdownMenuItem>
              <DropdownMenuItem asChild>
                <a href={fileContentUrl(file.id, true)} download={file.name}>
                  <Download className="size-4" /> {t('Descargar')}
                </a>
              </DropdownMenuItem>
              {edit && (
                <DropdownMenuItem onSelect={() => edit.onShare(file)}>
                  <Link2 className="size-4" /> {t('Compartir')}
                </DropdownMenuItem>
              )}
              <DropdownMenuItem onSelect={() => actions.onDetails(file)}>
                <Info className="size-4" /> {t('Detalles')}
              </DropdownMenuItem>
              {edit && (
                <>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem onSelect={() => edit.onRename(file)}>
                    <Pencil className="size-4" /> {t('Renombrar')}
                  </DropdownMenuItem>
                  <DropdownMenuItem onSelect={() => edit.onMove(file)}>
                    <FolderInput className="size-4" /> {t('Mover')}
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem variant="destructive" onSelect={() => edit.onDelete(file)}>
                    <Trash2 className="size-4" /> {t('Mover a la papelera')}
                  </DropdownMenuItem>
                </>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        </>
      }
    />
  )
}

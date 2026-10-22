import { useState } from 'react'
import { useQuery, useQueryClient, useSuspenseQuery } from '@tanstack/react-query'
import { ChevronRight, Folder as FolderIcon, Home } from 'lucide-react'
import { fileContentUrl, fileDetailQuery, folderQuery, type DriveFile, type Folder } from '@/lib/api'
import { formatBytes } from '@/lib/format'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { t } from '@/lib/i18n'

/**
 * Vista previa por tipo: imagen, vídeo, audio, PDF, texto; el resto ofrece descarga.
 * contentUrl cambia el origen del contenido (el enlace público usa el suyo).
 */
export function PreviewDialog({
  file,
  onClose,
  contentUrl = fileContentUrl,
}: {
  file: DriveFile | null
  onClose: () => void
  contentUrl?: (id: number, download?: boolean) => string
}) {
  const url = file ? contentUrl(file.id) : ''
  const mime = file?.mime ?? ''
  return (
    <Dialog open={file !== null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle className="truncate pr-6">{file?.name}</DialogTitle>
          <DialogDescription>
            {file && `${formatBytes(file.size)} · ${mime || t('tipo desconocido')}`}
          </DialogDescription>
        </DialogHeader>
        {file && (
          <div className="flex max-h-[65dvh] items-center justify-center overflow-auto rounded-md bg-muted/40">
            {mime.startsWith('image/') ? (
              <img src={url} alt={file.name} className="max-h-[65dvh] max-w-full object-contain" />
            ) : mime.startsWith('video/') ? (
              <video src={url} controls className="max-h-[65dvh] w-full" />
            ) : mime.startsWith('audio/') ? (
              <audio src={url} controls className="w-full p-6" />
            ) : mime === 'application/pdf' || mime.startsWith('text/') ? (
              <iframe src={url} title={file.name} className="h-[65dvh] w-full bg-white" />
            ) : (
              <p className="p-8 text-sm text-muted-foreground">{t('Sin vista previa para este tipo.')}</p>
            )}
          </div>
        )}
        <DialogFooter>
          <Button asChild variant="outline">
            <a href={file ? contentUrl(file.id, true) : '#'} download={file?.name}>
              {t('Descargar')}
            </a>
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** Detalle: layout, redundancia y bloques por cuenta. */
export function DetailsDialog({ file, onClose }: { file: DriveFile | null; onClose: () => void }) {
  return (
    <Dialog open={file !== null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="truncate pr-6">{file?.name}</DialogTitle>
          <DialogDescription>{file && formatBytes(file.size)}</DialogDescription>
        </DialogHeader>
        {file && <Details id={file.id} />}
      </DialogContent>
    </Dialog>
  )
}

function Details({ id }: { id: number }) {
  const { data, isPending } = useQuery(fileDetailQuery(id))
  if (isPending || !data) return <p className="text-sm text-muted-foreground">{t('Cargando…')}</p>
  const { file, chunks } = data
  const byAccount = new Map<string, { data: number; parity: number; status: string }>()
  for (const c of chunks) {
    const cur = byAccount.get(c.account_label) ?? { data: 0, parity: 0, status: c.account_status }
    cur[c.kind]++
    byAccount.set(c.account_label, cur)
  }
  const stripes = new Set(chunks.map((c) => c.stripe)).size
  return (
    <div className="grid gap-3 text-sm">
      <div className="flex flex-wrap gap-2">
        <Badge variant="outline">{file.layout === 'rs' ? `RAID ${file.data_shards}+${file.parity_shards}` : t('Entero en una cuenta')}</Badge>
        <Badge variant={file.status === 'ready' ? 'secondary' : 'destructive'}>{file.status}</Badge>
        {file.layout === 'rs' && <Badge variant="outline">{stripes === 1 ? t('1 franja') : t('{n} franjas', { n: stripes })}</Badge>}
      </div>
      {file.layout === 'rs' && (
        <p className="text-muted-foreground">
          {file.parity_shards === 1
            ? t('Tolera la pérdida de 1 cuenta sin perder datos.')
            : t('Tolera la pérdida de {n} cuentas sin perder datos.', { n: file.parity_shards })}
        </p>
      )}
      <ul className="divide-y rounded-md border">
        {[...byAccount.entries()].map(([label, v]) => (
          <li key={label} className="flex items-center justify-between gap-2 px-3 py-2">
            <span className="min-w-0 truncate">{label}</span>
            <span className="shrink-0 text-xs text-muted-foreground">
              {t('{n} datos', { n: v.data })}
              {v.parity > 0 ? ` · ${t('{n} paridad', { n: v.parity })}` : ''}
              {v.status !== 'active' ? ` · ${t('sin conexión')}` : ''}
            </span>
          </li>
        ))}
      </ul>
    </div>
  )
}

/** Elegir carpeta destino navegando el árbol. */
export function MoveDialog({
  open,
  title,
  description = t('Elige la carpeta destino.'),
  actionLabel = t('Mover aquí'),
  excludeFolderId,
  onPick,
  onClose,
  pending,
  error,
}: {
  open: boolean
  title: string
  description?: string
  actionLabel?: string
  excludeFolderId?: number
  onPick: (folderId: number) => void
  onClose: () => void
  pending?: boolean
  error?: unknown
}) {
  const [current, setCurrent] = useState(0)
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        {open && <FolderPicker current={current} onNavigate={setCurrent} exclude={excludeFolderId} />}
        {error ? <p role="alert" className="text-sm text-destructive">{String((error as Error).message ?? error)}</p> : null}
        <DialogFooter className="gap-2 sm:gap-0">
          <Button type="button" variant="outline" onClick={onClose}>
            {t('Cancelar')}
          </Button>
          <Button type="button" disabled={pending} onClick={() => onPick(current)}>
            {actionLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function FolderPicker({ current, onNavigate, exclude }: { current: number; onNavigate: (id: number) => void; exclude?: number }) {
  const { data } = useSuspenseQuery(folderQuery(current))
  const crumbs: Folder[] = [...data.breadcrumbs, data.folder].filter((f) => f.id !== 0)
  return (
    <div className="grid gap-2">
      <nav className="flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
        <button type="button" className="flex items-center gap-1 hover:text-foreground" onClick={() => onNavigate(0)}>
          <Home className="size-3.5" /> {t('Drive')}
        </button>
        {crumbs.map((f) => (
          <span key={f.id} className="flex items-center gap-1">
            <ChevronRight className="size-3" />
            <button type="button" className="hover:text-foreground" onClick={() => onNavigate(f.id)}>
              {f.name}
            </button>
          </span>
        ))}
      </nav>
      <ul className="max-h-60 divide-y overflow-y-auto rounded-md border">
        {data.folders.length === 0 && <li className="px-3 py-3 text-sm text-muted-foreground">{t('Sin subcarpetas')}</li>}
        {data.folders.map((f) => (
          <li key={f.id}>
            <button
              type="button"
              disabled={f.id === exclude}
              className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm hover:bg-accent/60 disabled:opacity-40"
              onClick={() => onNavigate(f.id)}
            >
              <FolderIcon className="size-4 text-amber-500" /> <span className="truncate">{f.name}</span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  )
}

export function useInvalidateDrive() {
  const queryClient = useQueryClient()
  return () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ['folder'] }),
      queryClient.invalidateQueries({ queryKey: ['search'] }),
      queryClient.invalidateQueries({ queryKey: ['quota'] }),
      queryClient.invalidateQueries({ queryKey: ['trash'] }),
    ])
}

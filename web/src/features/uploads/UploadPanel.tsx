import { useEffect, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { CheckCircle2, ChevronDown, ChevronRight, ChevronUp, CircleX, File as FileIcon, Folder, RotateCw, X } from 'lucide-react'
import { formatBytes, percent } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { uploads, useUploads, type UploadBatch, type UploadItem } from './store'
import { getLang, t } from '@/lib/i18n'

/** Panel fijo abajo a la derecha con la cola de subidas. Ancho fluido en móvil. */
export function UploadPanel() {
  const s = useUploads()
  const [collapsed, setCollapsed] = useState(false)
  const queryClient = useQueryClient()

  useEffect(() => {
    uploads.setOnDone(() => {
      void queryClient.invalidateQueries({ queryKey: ['folder'] })
      void queryClient.invalidateQueries({ queryKey: ['quota'] })
      void queryClient.invalidateQueries({ queryKey: ['health'] })
      void queryClient.invalidateQueries({ queryKey: ['auth', 'me'] })
    })
    return () => uploads.setOnDone(null)
  }, [queryClient])

  if (s.batches.length === 0) return null
  const active = s.pending > 0

  return (
    <aside
      aria-label={t('Subidas')}
      className="fixed inset-x-2 bottom-[calc(4.5rem+env(safe-area-inset-bottom))] z-40 overflow-hidden rounded-xl border bg-card shadow-lg sm:inset-x-auto sm:right-4 sm:w-[26rem] lg:bottom-4"
    >
      <header className="grid gap-2 border-b px-3 py-2">
        <div className="flex items-center justify-between gap-2">
          <div className="min-w-0 flex-1">
            <p className="truncate text-sm font-medium">
              {active
                ? t('Subidos {n} de {total}', { n: s.done.toLocaleString(getLang()), total: plural(s.files, t('archivo'), t('archivos')) })
                : s.failed > 0
                  ? plural(s.failed, t('subida fallida'), t('subidas fallidas'))
                  : s.done > 0
                    ? plural(s.done, t('subida completada'), t('subidas completadas'))
                    : t('Subidas canceladas')}
            </p>
            <p className="truncate text-xs text-muted-foreground" aria-live="polite">
              {active
                ? [t('{hecho} de {total}', { hecho: formatBytes(s.loaded), total: formatBytes(s.size) }), s.rate > 0 && `${formatBytes(s.rate)}/s`, eta(s.size - s.loaded, s.rate)]
                    .filter(Boolean)
                    .join(' · ')
                : s.done > 0
                  ? t('{tamaño} subidos', { tamaño: formatBytes(s.loaded) })
                  : t('Nada se ha subido')}
            </p>
          </div>
          <div className="flex shrink-0 items-center gap-1">
            {s.failed > 0 && (
              <Button variant="ghost" size="icon" aria-label={t('Reintentar las fallidas')} title={t('Reintentar las fallidas')} onClick={() => uploads.retryAll()}>
                <RotateCw className="size-4" />
              </Button>
            )}
            <Button variant="ghost" size="icon" aria-label={collapsed ? t('Expandir') : t('Contraer')} onClick={() => setCollapsed((c) => !c)}>
              {collapsed ? <ChevronUp className="size-4" /> : <ChevronDown className="size-4" />}
            </Button>
            {active ? (
              <Button variant="ghost" size="sm" className="px-2 text-xs" onClick={() => uploads.cancelAll()}>
                {t('Cancelar')}
              </Button>
            ) : (
              <Button variant="ghost" size="icon" aria-label={t('Cerrar')} onClick={() => uploads.clearFinished()}>
                <X className="size-4" />
              </Button>
            )}
          </div>
        </div>
        {active && <Progress value={percent(s.loaded, s.size)} aria-label={t('Progreso total')} />}
      </header>
      {!collapsed && (
        <ul className="max-h-[min(20rem,45vh)] divide-y overflow-y-auto">
          {/* Lo más reciente arriba: es lo que se acaba de soltar. */}
          {[...s.batches].reverse().map((b) => (
            <BatchRow key={b.id} batch={b} />
          ))}
        </ul>
      )}
    </aside>
  )
}

function BatchRow({ batch: b }: { batch: UploadBatch }) {
  const [open, setOpen] = useState(false)
  const pending = b.count.queued + b.count.uploading
  const failed = b.count.error
  const total = b.items.length - b.count.cancelled
  const allCancelled = b.items.length > 0 && b.count.cancelled === b.items.length
  const single = b.kind === 'file' ? b.items[0] : undefined
  // Dentro de una carpeta solo interesa lo que está en marcha o ha fallado.
  const notable = b.items.filter((i) => i.status === 'uploading' || i.status === 'error')
  const Icon = b.kind === 'folder' ? Folder : FileIcon

  return (
    <li className="grid gap-1.5 px-3 py-2 text-sm">
      <div className="flex items-center gap-2">
        {b.kind === 'folder' && notable.length > 0 ? (
          <button
            type="button"
            aria-label={open ? t('Ocultar detalle') : t('Ver detalle')}
            aria-expanded={open}
            className="-ml-1 flex size-6 shrink-0 items-center justify-center rounded text-muted-foreground hover:text-foreground"
            onClick={() => setOpen((o) => !o)}
          >
            <ChevronRight className={`size-4 transition-transform ${open ? 'rotate-90' : ''}`} />
          </button>
        ) : null}
        <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <span className="min-w-0 flex-1 truncate" title={b.name}>
          {b.name}
        </span>
        {pending === 0 && failed === 0 && !allCancelled && !b.error && (
          <CheckCircle2 className="size-4 shrink-0 text-success" aria-label={t('Completada')} />
        )}
        {pending === 0 && (failed > 0 || b.error) && <CircleX className="size-4 shrink-0 text-destructive" aria-label={t('Con errores')} />}
        {pending > 0 ? (
          <button type="button" className="shrink-0 text-xs text-muted-foreground hover:text-foreground" onClick={() => uploads.cancelBatch(b.id)}>
            {t('Cancelar')}
          </button>
        ) : failed > 0 || b.count.cancelled > 0 ? (
          <button type="button" className="shrink-0 text-xs text-muted-foreground hover:text-foreground" onClick={() => uploads.retryBatch(b.id)}>
            {t('Reintentar')}
          </button>
        ) : null}
        {pending === 0 && (
          <button
            type="button"
            aria-label={t('Quitar de la lista')}
            className="flex size-6 shrink-0 items-center justify-center rounded text-muted-foreground hover:text-foreground"
            onClick={() => uploads.dismissBatch(b.id)}
          >
            <X className="size-3.5" />
          </button>
        )}
      </div>
      {pending > 0 && <Progress value={percent(b.loaded, b.size)} />}
      <p className="truncate text-xs text-muted-foreground">
        {single ? itemText(single) : folderText(b, total, failed, allCancelled)}
      </p>
      {b.error && <p className="text-xs text-destructive">{b.error}</p>}
      {open && notable.length > 0 && (
        <ul className="grid gap-1.5 border-l pl-3">
          {notable.slice(0, 100).map((it) => (
            <li key={it.id} className="grid gap-1">
              <div className="flex items-center gap-2 text-xs">
                <span className="min-w-0 flex-1 truncate" title={[...it.dirs, it.name].join('/')}>
                  {[...it.dirs.slice(1), it.name].join('/')}
                </span>
                {it.status === 'error' ? (
                  <button type="button" className="shrink-0 text-muted-foreground hover:text-foreground" onClick={() => uploads.retry(it.id)}>
                    {t('Reintentar')}
                  </button>
                ) : (
                  <button type="button" className="shrink-0 text-muted-foreground hover:text-foreground" onClick={() => uploads.cancel(it.id)}>
                    {t('Cancelar')}
                  </button>
                )}
              </div>
              {it.status === 'uploading' ? (
                <Progress value={percent(it.loaded, it.size)} className="h-1" />
              ) : (
                <p className="text-xs text-destructive">{it.error}</p>
              )}
            </li>
          ))}
          {notable.length > 100 && <li className="text-xs text-muted-foreground">{t('y {n} más…', { n: notable.length - 100 })}</li>}
        </ul>
      )}
    </li>
  )
}

function itemText(it: UploadItem): string {
  switch (it.status) {
    case 'error':
      return it.error ?? t('Error')
    case 'cancelled':
      return t('Cancelada')
    case 'queued':
      return `${t('En cola')} · ${formatBytes(it.size)}`
    case 'done':
      return formatBytes(it.size)
    case 'uploading':
      return t('{hecho} de {total}', { hecho: formatBytes(it.loaded), total: formatBytes(it.size) })
  }
}

function folderText(b: UploadBatch, total: number, failed: number, allCancelled: boolean): string {
  if (b.items.length === 0) return b.error ? t('Carpeta vacía') : t('Carpeta vacía creada')
  if (allCancelled) return t('Cancelada')
  const parts = [
    t('{hecho} de {total}', { hecho: b.count.done, total: plural(total, t('archivo'), t('archivos')) }),
    t('{hecho} de {total}', { hecho: formatBytes(b.loaded), total: formatBytes(b.size) }),
  ]
  if (failed > 0) parts.push(plural(failed, t('error'), t('errores')))
  return parts.join(' · ')
}

function plural(n: number, one: string, many: string): string {
  return `${n.toLocaleString(getLang())} ${n === 1 ? one : many}`
}

/** Tiempo restante aproximado: «~3 min». */
function eta(bytes: number, rate: number): string | false {
  if (rate <= 0 || bytes <= 0) return false
  const s = bytes / rate
  if (s < 60) return t('menos de 1 min')
  if (s < 3600) return `~${Math.round(s / 60)} min`
  return `~${Math.floor(s / 3600)} h ${Math.round((s % 3600) / 60)} min`
}

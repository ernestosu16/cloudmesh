import { useId, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Check, Copy, ExternalLink, FileIcon, Folder } from 'lucide-react'
import { createShare, deleteShare, shareUrl, updateShare, type Share, type ShareKind, type ShareOptions } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { ErrorText } from '@/features/shell/Page'
import { t } from '@/lib/i18n'

export interface ShareTarget {
  kind: ShareKind
  id: number
  name: string
}

/**
 * Crear o editar el enlace de un archivo o carpeta. Como en Mega, es de solo
 * lectura y puede llevar contraseña. Sin share muestra el formulario de alta.
 * El estado se toma de share al montar: quien lo usa le da un key por enlace.
 */
export function ShareEditor({ target, share, onDeleted }: { target: ShareTarget; share?: Share; onDeleted?: () => void }) {
  const queryClient = useQueryClient()
  const ids = useId()
  const [pass, setPass] = useState(share?.has_password ?? false)
  const [password, setPassword] = useState('')
  const [copied, setCopied] = useState(false)

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['shares'] })
  const options = (): ShareOptions => ({
    // Contraseña activa sin escribir otra = conservar la actual.
    password: pass ? (password || (share?.has_password ? undefined : '')) : '',
  })
  const save = useMutation({
    mutationFn: () => (share ? updateShare(share.id, options()) : createShare(target.kind, target.id, options())),
    onSuccess: () => {
      setPassword('')
      return invalidate()
    },
  })
  const remove = useMutation({
    // Sin id no hay nada que borrar: nunca se pide DELETE /shares/undefined.
    mutationFn: () => (share?.id ? deleteShare(share.id) : Promise.resolve()),
    onSuccess: () => invalidate().then(() => onDeleted?.()),
  })

  const url = share ? shareUrl(share.token) : ''
  const copy = async () => {
    await navigator.clipboard.writeText(url)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }
  const missingPassword = pass && !password && !share?.has_password

  const rows = [
    {
      key: 'pass',
      label: t('Contraseña'),
      hint: share?.has_password ? t('Pedir contraseña antes de mostrar el contenido') : t('Quien abra el enlace tendrá que escribirla'),
      on: pass,
      set: setPass,
      field: (
        <Input
          type="password"
          autoComplete="new-password"
          aria-label={t('Contraseña del enlace')}
          placeholder={share?.has_password ? t('Sin cambios (escribe para reemplazarla)') : t('Mínimo 4 caracteres')}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
      ),
    },
  ]

  return (
    <div className="grid grid-cols-1 gap-5">
      <div className="flex items-center gap-3">
        <span className="flex size-11 shrink-0 items-center justify-center rounded-xl bg-secondary text-secondary-foreground">
          {target.kind === 'folder' ? <Folder className="size-5" /> : <FileIcon className="size-5" />}
        </span>
        <div className="min-w-0">
          <h2 className="truncate pr-6 text-xl font-bold">Compartir «{target.name}»</h2>
          <p className="text-[13px] text-muted-foreground">{target.kind === 'folder' ? t('Carpeta con todo su contenido') : t('Archivo')}</p>
        </div>
      </div>

      {share && (
        <div className="grid grid-cols-1 gap-2">
          <span className="text-[13px] font-semibold">{t('Enlace')}</span>
          <div className="flex items-center gap-2 rounded-xl border border-input py-1 pr-1 pl-3.5">
            <span className="min-w-0 flex-1 truncate font-mono text-[13px] text-secondary-foreground">{url}</span>
            <Button onClick={() => void copy()}>
              {copied ? <Check /> : <Copy />} {copied ? t('Copiado') : t('Copiar')}
            </Button>
          </div>
        </div>
      )}

      <div className="grid grid-cols-1">
        {rows.map((r) => (
          <div key={r.key} className="grid grid-cols-1 gap-2.5 border-t py-3">
            <div className="flex items-center gap-3.5">
              <label htmlFor={`${ids}-${r.key}`} className="grid min-w-0 flex-1 gap-0.5">
                <span className="text-sm font-semibold">{r.label}</span>
                <span className="text-[13px] text-muted-foreground">{r.hint}</span>
              </label>
              <Switch id={`${ids}-${r.key}`} checked={r.on} onCheckedChange={r.set} />
            </div>
            {r.on && r.field}
          </div>
        ))}
      </div>

      <ErrorText error={save.error ?? remove.error} />
      <div className="flex flex-wrap justify-end gap-2.5 border-t pt-4">
        {share && (
          <>
            <Button variant="ghost" className="text-destructive hover:text-destructive" disabled={remove.isPending} onClick={() => remove.mutate()}>
              {t('Desactivar enlace')}
            </Button>
            <Button asChild variant="outline">
              <a href={url} target="_blank" rel="noopener">
                <ExternalLink /> {t('Vista previa')}
              </a>
            </Button>
          </>
        )}
        <Button disabled={save.isPending || missingPassword} onClick={() => save.mutate()}>
          {share ? (save.isSuccess && !save.isPending ? t('Guardado') : t('Guardar')) : t('Crear enlace')}
        </Button>
      </div>
    </div>
  )
}

/** Diálogo con el editor; lo abre la acción «Compartir» del drive. */
export function ShareDialog({ target, shares, onClose }: { target: ShareTarget | null; shares: Share[]; onClose: () => void }) {
  const share = target ? shares.find((s) => s.kind === target.kind && s.target_id === target.id) : undefined
  return (
    <Dialog open={target !== null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
        <DialogHeader className="sr-only">
          <DialogTitle>{t('Compartir')}</DialogTitle>
          <DialogDescription>{t('Enlace público de solo lectura, con contraseña opcional.')}</DialogDescription>
        </DialogHeader>
        {target && <ShareEditor key={share?.id ?? 'nuevo'} target={target} share={share} onDeleted={onClose} />}
      </DialogContent>
    </Dialog>
  )
}

import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Lock } from 'lucide-react'
import { folderAccessQuery, setFolderAccess, usersQuery, type AccessLevel, type Folder, type FolderAccess } from '@/lib/api'
import { displayName } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { roleLabels } from '@/features/auth/roles'
import { ErrorText } from '@/features/shell/Page'
import { t } from '@/lib/i18n'

type Choice = 'none' | AccessLevel

/** Restringir una carpeta (y lo que cuelga de ella) a ciertos usuarios. Solo administradores. */
export function FolderAccessDialog({ folder, onClose }: { folder: Folder | null; onClose: () => void }) {
  return (
    <Dialog open={folder !== null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="break-all">{t('Acceso a «{nombre}»', { nombre: folder?.name ?? '' })}</DialogTitle>
          <DialogDescription>
            {t('Por defecto todos ven el disco. Una carpeta restringida, con todo lo de dentro, solo la ven los administradores y quien elijas.')}
          </DialogDescription>
        </DialogHeader>
        {folder && <AccessForm key={folder.id} folder={folder} onClose={onClose} />}
      </DialogContent>
    </Dialog>
  )
}

function AccessForm({ folder, onClose }: { folder: Folder; onClose: () => void }) {
  const current = useQuery(folderAccessQuery(folder.id))
  const users = useQuery(usersQuery)
  if (current.isPending || users.isPending) return <p className="text-sm text-muted-foreground">{t('Cargando…')}</p>
  if (current.error || users.error || !current.data || !users.data) return <ErrorText error={current.error ?? users.error} />
  return <AccessEditor folder={folder} initial={current.data} users={users.data} onClose={onClose} />
}

function AccessEditor({
  folder,
  initial,
  users,
  onClose,
}: {
  folder: Folder
  initial: FolderAccess
  users: { id: number; email: string; name: string; role: 'admin' | 'editor' | 'reader' }[]
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const [restricted, setRestricted] = useState(initial.restricted)
  const [levels, setLevels] = useState<Record<number, Choice>>(() =>
    Object.fromEntries(initial.grants.map((g) => [g.user_id, g.level])),
  )
  const save = useMutation({
    mutationFn: () =>
      setFolderAccess(folder.id, {
        restricted,
        grants: Object.entries(levels)
          .filter(([, l]) => l !== 'none')
          .map(([id, level]) => ({ user_id: Number(id), level: level as AccessLevel })),
      }),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['folder'] }),
        queryClient.invalidateQueries({ queryKey: ['folder-access', folder.id] }),
      ])
      onClose()
    },
  })
  const others = users.filter((u) => u.role !== 'admin')

  return (
    <div className="grid grid-cols-1 gap-4">
      <div className="flex items-start justify-between gap-4 rounded-xl border p-3.5">
        <span className="grid gap-0.5">
          <Label htmlFor="folder-restricted" className="flex items-center gap-1.5 text-base font-semibold">
            <Lock className="size-4" aria-hidden /> {t('Restringir')}
          </Label>
          <span className="text-[13px] text-muted-foreground">
            {t('Los enlaces públicos de carpetas superiores tampoco mostrarán esta. Las llaves S3 no se ven afectadas.')}
          </span>
        </span>
        <Switch id="folder-restricted" checked={restricted} onCheckedChange={setRestricted} className="mt-1" />
      </div>

      {restricted && (
        <div className="grid grid-cols-1 gap-2">
          <span className="text-sm font-semibold">{t('Quién entra')}</span>
          {others.length === 0 && <p className="text-[13px] text-muted-foreground">{t('No hay más usuarios que administradores.')}</p>}
          <ul className="grid grid-cols-1 divide-y rounded-xl border">
            {others.map((u) => (
              <li key={u.id} className="flex flex-wrap items-center justify-between gap-2 px-3.5 py-2.5">
                <span className="grid min-w-0 flex-[1_1_10rem]">
                  <span className="truncate text-sm font-medium">{displayName(u)}</span>
                  <span className="truncate text-xs text-muted-foreground">
                    {u.name ? `${u.email} · ` : ''}
                    {roleLabels[u.role]}
                  </span>
                </span>
                <Select value={levels[u.id] ?? 'none'} onValueChange={(v) => setLevels((cur) => ({ ...cur, [u.id]: v as Choice }))}>
                  <SelectTrigger className="w-full min-w-36 sm:w-40" aria-label={t('Acceso de {email}', { email: u.email })}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">{t('Sin acceso')}</SelectItem>
                    <SelectItem value="read">{t('Solo ver')}</SelectItem>
                    <SelectItem value="write" disabled={u.role === 'reader'}>
                      {t('Ver y editar')}
                    </SelectItem>
                  </SelectContent>
                </Select>
              </li>
            ))}
          </ul>
          <p className="text-xs text-muted-foreground">
            {t('Los administradores siempre entran. Un lector nunca edita, aunque se le dé acceso de edición a una carpeta.')}
          </p>
        </div>
      )}

      <ErrorText error={save.error} />
      <div className="flex justify-end gap-2.5">
        <Button type="button" variant="outline" onClick={onClose}>
          {t('Cancelar')}
        </Button>
        <Button onClick={() => save.mutate()} disabled={save.isPending}>
          {t('Guardar')}
        </Button>
      </div>
    </div>
  )
}

import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useMutation, useQueryClient, useSuspenseQuery } from '@tanstack/react-query'
import { RotateCcw, Trash2 } from 'lucide-react'
import { emptyTrash, meQuery, purgeTrash, restoreTrash, trashQuery, type TrashItem } from '@/lib/api'
import { formatBytes, relativeTime } from '@/lib/format'
import { Button } from '@/components/ui/button'
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
import { FileTypeIcon } from '@/features/drive/icons'
import { ErrorText, PageHeader, Panel } from '@/features/shell/Page'
import { can, requireRole } from '@/features/auth/roles'
import { t } from '@/lib/i18n'

export const Route = createFileRoute('/_auth/trash')({
  beforeLoad: requireRole('editor'),
  loader: ({ context }) => context.queryClient.ensureQueryData(trashQuery),
  component: TrashPage,
})

/** Destino de un archivo al restaurarlo, tal como lo verá el usuario. */
const origin = (it: TrashItem) => (!it.folder_exists ? t('Mi espacio (su carpeta ya no existe)') : it.folder_name || t('Mi espacio'))

function TrashPage() {
  const { user } = Route.useRouteContext()
  const { data: items } = useSuspenseQuery(trashQuery)
  const queryClient = useQueryClient()
  // null: vaciar todo; un elemento: borrar solo ese.
  const [confirm, setConfirm] = useState<TrashItem | null | undefined>(undefined)
  const [notice, setNotice] = useState<string | null>(null)

  const refresh = () =>
    Promise.all(
      [['trash'], ['folder'], ['search'], ['quota'], ['shares'], meQuery.queryKey].map((queryKey) => queryClient.invalidateQueries({ queryKey })),
    )
  const restore = useMutation({
    mutationFn: (it: TrashItem) => restoreTrash(it.file.id),
    onSuccess: (_, it) => {
      setNotice(t('«{nombre}» ha vuelto a {destino}.', { nombre: it.file.name, destino: origin(it) }))
      return refresh()
    },
  })
  const purge = useMutation({
    mutationFn: (it: TrashItem | null) => (it ? purgeTrash(it.file.id).then(() => 1) : emptyTrash().then((r) => r.purged)),
    onSuccess: (n) => {
      setNotice(n === 1 ? t('Archivo borrado definitivamente.') : t('{n} archivos borrados definitivamente.', { n }))
      setConfirm(undefined)
      return refresh()
    },
  })
  const total = items.reduce((a, it) => a + it.file.size, 0)

  return (
    <section className="grid grid-cols-1 gap-6">
      <PageHeader
        title={t('Papelera')}
        description={t('Lo que borras pasa aquí 30 días antes de desaparecer. Mientras tanto sigue ocupando espacio en las cuentas.')}
      >
        {items.length > 0 && can(user, 'admin') && (
          <Button variant="outline" size="lg" className="text-destructive hover:text-destructive" onClick={() => setConfirm(null)}>
            <Trash2 /> {t('Vaciar papelera')}
          </Button>
        )}
      </PageHeader>

      {notice && <p className="rounded-xl bg-success-soft px-4 py-3 text-sm wrap-anywhere text-success">{notice}</p>}
      <ErrorText error={restore.error} />

      {items.length === 0 ? (
        <Panel className="text-center text-sm text-muted-foreground">
          {t('La papelera está vacía.')}{' '}
          <Link to="/" search={{}} className="font-semibold text-primary hover:underline">
            {t('Volver a Mi espacio')}
          </Link>
        </Panel>
      ) : (
        <div className="overflow-hidden rounded-2xl border bg-card">
          <p className="border-b px-4 py-3 text-[13px] text-muted-foreground md:px-5">
            {items.length === 1 ? t('1 archivo') : t('{n} archivos', { n: items.length })} · {formatBytes(total)}
          </p>
          <ul className="divide-y divide-secondary">
            {items.map((it) => (
              <li key={it.file.id} className="flex flex-wrap items-center gap-3 px-4 py-3 md:px-5">
                <span className="flex size-10 shrink-0 items-center justify-center rounded-lg border bg-card text-muted-foreground">
                  <FileTypeIcon mime={it.file.mime} className="size-5" />
                </span>
                <span className="grid min-w-0 flex-[1_1_12rem] gap-0.5">
                  <span className="truncate font-medium">{it.file.name}</span>
                  <span className="text-xs text-muted-foreground">
                    {formatBytes(it.file.size)} · {t('de {origen}', { origen: origin(it) })} · {t('se borra {cuando}', { cuando: relativeTime(it.purge_at) })}
                  </span>
                </span>
                <span className="ml-auto flex shrink-0 items-center gap-1">
                  <Button variant="outline" disabled={restore.isPending} onClick={() => restore.mutate(it)}>
                    <RotateCcw /> {t('Restaurar')}
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="text-destructive hover:text-destructive"
                    aria-label={t('Borrar definitivamente {nombre}', { nombre: it.file.name })}
                    onClick={() => setConfirm(it)}
                  >
                    <Trash2 />
                  </Button>
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}

      <AlertDialog open={confirm !== undefined} onOpenChange={(o) => !o && setConfirm(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{confirm ? t('¿Borrar «{nombre}» definitivamente?', { nombre: confirm.file.name }) : t('¿Vaciar la papelera?')}</AlertDialogTitle>
            <AlertDialogDescription>
              {confirm
                ? t('Se destruyen sus pedazos en todas las cuentas y sus enlaces se eliminan. No se puede deshacer.')
                : items.length === 1
                  ? t('Se destruye 1 archivo con sus pedazos y enlaces. No se puede deshacer.')
                  : t('Se destruyen {n} archivos con sus pedazos y enlaces. No se puede deshacer.', { n: items.length })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <ErrorText error={purge.error} />
          <AlertDialogFooter>
            <AlertDialogCancel>{t('Cancelar')}</AlertDialogCancel>
            <AlertDialogAction
              disabled={purge.isPending}
              onClick={(e) => {
                e.preventDefault()
                purge.mutate(confirm ?? null)
              }}
            >
              {t('Borrar definitivamente')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}

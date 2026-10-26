import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useMutation, useQueryClient, useSuspenseQuery } from '@tanstack/react-query'
import { Boxes, FolderOpen, KeyRound, Pencil, Plus, RefreshCw, Settings2, Trash2, TriangleAlert } from 'lucide-react'
import { bucketsQuery, deleteS3Key, regenerateS3Key, s3InfoQuery, s3KeysQuery, unlinkBucket, type Bucket, type NewS3Key, type S3Key } from '@/lib/api'
import { relativeTime, shortDate } from '@/lib/format'
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
import { CopyField, CreateBucketDialog, CreateKeyDialog, KeySecret, permissionLabel } from '@/features/s3/S3Dialogs'
import { Dialog, DialogContent } from '@/components/ui/dialog'
import { BucketSettingsDialog, publicUrl } from '@/features/s3/BucketSettings'
import { ErrorText, PageHeader, Panel, Tag } from '@/features/shell/Page'
import { requireRole } from '@/features/auth/roles'
import { t } from '@/lib/i18n'

export const Route = createFileRoute('/_auth/s3')({
  beforeLoad: requireRole('admin'),
  loader: ({ context }) =>
    Promise.all([
      context.queryClient.ensureQueryData(s3InfoQuery),
      context.queryClient.ensureQueryData(bucketsQuery),
      context.queryClient.ensureQueryData(s3KeysQuery),
    ]),
  component: S3Page,
})

function S3Page() {
  const { data: info } = useSuspenseQuery(s3InfoQuery)
  const { data: buckets } = useSuspenseQuery(bucketsQuery)
  const { data: keys } = useSuspenseQuery(s3KeysQuery)
  const queryClient = useQueryClient()
  const [creatingBucket, setCreatingBucket] = useState(false)
  const [creatingKey, setCreatingKey] = useState(false)
  const [unlinking, setUnlinking] = useState<Bucket | null>(null)
  const [configuring, setConfiguring] = useState<Bucket | null>(null)
  const [revoking, setRevoking] = useState<S3Key | null>(null)
  const [regenerating, setRegenerating] = useState<S3Key | null>(null)
  const [editingKey, setEditingKey] = useState<S3Key | null>(null)
  const [regenerated, setRegenerated] = useState<NewS3Key | null>(null)

  // Sin CLOUDMESH_S3_URL se supone el mismo host en el puerto 9000.
  const endpoint = info.endpoint || `${window.location.protocol}//${window.location.hostname}:9000`
  const refresh = () => queryClient.invalidateQueries({ queryKey: ['s3'] })
  const unlink = useMutation({ mutationFn: (b: Bucket) => unlinkBucket(b.id), onSuccess: () => refresh().then(() => setUnlinking(null)) })
  const regenerate = useMutation({
    mutationFn: (k: S3Key) => regenerateS3Key(k.id),
    onSuccess: (k) => {
      setRegenerating(null)
      setRegenerated(k)
      void refresh()
    },
  })
  const revoke = useMutation({ mutationFn: (k: S3Key) => deleteS3Key(k.id), onSuccess: () => refresh().then(() => setRevoking(null)) })

  return (
    <section className="grid grid-cols-1 gap-6">
      <PageHeader title={t('Buckets S3')} description={t('Publica carpetas del disco con una API compatible con S3, como Cloudflare R2.')}>
        <Button size="lg" onClick={() => setCreatingBucket(true)}>
          <Plus /> {t('Crear bucket')}
        </Button>
      </PageHeader>

      {!info.enabled && (
        <p className="rounded-xl bg-warning-soft px-4 py-3 text-sm text-warning">
          {t('La API S3 está desactivada en este servidor (CLOUDMESH_S3_ADDR=off). Puedes preparar buckets y llaves, pero no responderá.')}
        </p>
      )}

      <div className="grid grid-cols-1 gap-4 rounded-2xl border bg-card p-5 sm:grid-cols-[2fr_1fr_1fr] sm:p-6">
        <div className="grid min-w-0 gap-1.5">
          <span className="text-xs tracking-wider text-muted-foreground uppercase">{t('Endpoint')}</span>
          <span className="font-mono text-[15px] break-all">{endpoint}</span>
        </div>
        <div className="grid gap-1.5">
          <span className="text-xs tracking-wider text-muted-foreground uppercase">{t('Región')}</span>
          <span className="font-mono text-[15px]">{info.region}</span>
        </div>
        <div className="grid gap-1.5">
          <span className="text-xs tracking-wider text-muted-foreground uppercase">{t('Estado')}</span>
          <span className="flex items-center gap-2 text-[15px]">
            <span className={info.enabled ? 'size-2 rounded-full bg-success' : 'size-2 rounded-full bg-warning'} />
            {info.enabled ? t('Operativo') : t('Desactivado')}
          </span>
        </div>
        <p className="border-t pt-4 text-[13px] text-muted-foreground sm:col-span-3">
          {t('Firma AWS SigV4 y URLs prefirmadas. Rutas de estilo «path»:')}{' '}
          <code className="font-mono break-all text-foreground">
            {endpoint}/&lt;bucket&gt;/&lt;{t('clave')}&gt;
          </code>
          {info.domain ? (
            <>
              {' '}
              {t('y de estilo virtual-host:')}{' '}
              <code className="font-mono break-all text-foreground">
                {publicUrl(endpoint, info.domain, '<bucket>')}&lt;{t('clave')}&gt;
              </code>
            </>
          ) : null}
          .
        </p>
      </div>

      <section className="grid grid-cols-1 gap-3.5">
        <h2 className="text-xl font-bold">{t('Buckets')}</h2>
        {buckets.length === 0 ? (
          <Panel className="grid justify-items-center gap-3 py-10 text-center">
            <span className="flex size-12 items-center justify-center rounded-xl bg-accent text-primary">
              <Boxes className="size-6" aria-hidden />
            </span>
            <p className="max-w-md text-secondary-foreground">
              {t('Aún no hay buckets. Crea uno sobre una carpeta nueva o sobre una que ya tengas en Mi espacio.')}
            </p>
          </Panel>
        ) : (
          <ul className="grid grid-cols-[repeat(auto-fill,minmax(min(260px,100%),1fr))] gap-4">
            {buckets.map((b) => (
              <li key={b.id} className="grid min-w-0 gap-3 rounded-2xl border bg-card p-4 *:min-w-0">
                <div className="flex items-center justify-between gap-1">
                  <span className="min-w-0 flex-1 truncate font-mono text-base font-medium">{b.name}</span>
                  <Button variant="ghost" size="icon" aria-label={t('Configurar el bucket {nombre}', { nombre: b.name })} onClick={() => setConfiguring(b)}>
                    <Settings2 />
                  </Button>
                  <Button variant="ghost" size="icon" aria-label={t('Quitar el bucket {nombre}', { nombre: b.name })} onClick={() => setUnlinking(b)}>
                    <Trash2 />
                  </Button>
                </div>
                <div className="flex flex-wrap gap-1.5">
                  {b.public ? <Tag tone="warning">{t('Público')}</Tag> : <Tag>{t('Privado')}</Tag>}
                  {b.cors.length > 0 && <Tag tone="accent">CORS · {b.cors.length}</Tag>}
                  {b.lifecycle.some((r) => r.enabled) && <Tag tone="accent">{t('Ciclo de vida')} · {b.lifecycle.filter((r) => r.enabled).length}</Tag>}
                </div>
                <Link
                  to="/"
                  search={{ folder: b.folder_id }}
                  className="flex min-w-0 items-center gap-2 text-[13px] text-secondary-foreground hover:text-foreground hover:underline"
                >
                  <FolderOpen className="size-4 shrink-0" aria-hidden />
                  <span className="min-w-0 truncate font-mono">{b.folder_path}</span>
                </Link>
                <span className="text-xs text-muted-foreground">{t('Creado el {fecha}', { fecha: shortDate(b.created_at) })}</span>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="grid grid-cols-1 gap-3.5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h2 className="text-xl font-bold">{t('Llaves de acceso')}</h2>
          <Button variant="outline" onClick={() => setCreatingKey(true)}>
            <KeyRound /> {t('Crear llave')}
          </Button>
        </div>
        {keys.length === 0 ? (
          <Panel className="text-sm text-muted-foreground">{t('Sin llaves. Crea una para conectar aws-cli, rclone o restic.')}</Panel>
        ) : (
          <>
            <ul className="grid gap-2.5 md:hidden">
              {keys.map((k) => (
                <li key={k.id} className="grid gap-2 rounded-2xl border bg-card p-4">
                  <span className="grid min-w-0">
                    <span className="truncate font-semibold">{k.name}</span>
                    <span className="truncate font-mono text-xs text-muted-foreground">{k.access_key_id}</span>
                  </span>
                  <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                    <Tag>{permissionLabel(k.permission)}</Tag>
                    <span className="min-w-0 font-mono break-all">{k.buckets.length ? k.buckets.join(', ') : t('todos los buckets')}</span>
                    <span>· {k.last_used_at ? t('usada {cuando}', { cuando: relativeTime(k.last_used_at) }) : t('sin usar')}</span>
                  </div>
                  {/* Acciones en su fila: al lado del nombre lo dejaban sin sitio. */}
                  <div className="-mx-2 -mb-1 flex flex-wrap gap-1 border-t pt-2">
                    <Button variant="ghost" size="sm" onClick={() => setEditingKey(k)}>
                      <Pencil /> {t('Editar')}
                    </Button>
                    <Button variant="ghost" size="sm" onClick={() => setRegenerating(k)}>
                      <RefreshCw /> {t('Regenerar')}
                    </Button>
                    <Button variant="ghost" size="sm" className="ml-auto text-destructive hover:text-destructive" onClick={() => setRevoking(k)}>
                      {t('Revocar')}
                    </Button>
                  </div>
                </li>
              ))}
            </ul>
            <div className="hidden overflow-x-auto rounded-2xl border bg-card md:block">
              <table className="w-full min-w-[720px] border-collapse text-sm">
                <thead>
                  <tr className="text-left text-xs tracking-wider text-muted-foreground uppercase">
                    <th className="border-b px-5 py-3.5 font-semibold">{t('Llave')}</th>
                    <th className="border-b px-3 py-3.5 font-semibold">{t('Permisos')}</th>
                    <th className="border-b px-3 py-3.5 font-semibold">{t('Buckets')}</th>
                    <th className="border-b px-3 py-3.5 font-semibold">{t('Último uso')}</th>
                    <th className="border-b px-5 py-3.5">
                      <span className="sr-only">{t('Acciones')}</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {keys.map((k) => (
                    <tr key={k.id} className="border-b border-secondary last:border-0">
                      <td className="px-5 py-3">
                        <span className="grid gap-0.5">
                          <span className="font-semibold">{k.name}</span>
                          <span className="font-mono text-xs text-muted-foreground">{k.access_key_id}</span>
                        </span>
                      </td>
                      <td className="px-3 py-3">
                        <Tag>{permissionLabel(k.permission)}</Tag>
                      </td>
                      <td className="px-3 py-3 font-mono text-[13px] text-secondary-foreground">{k.buckets.length ? k.buckets.join(', ') : t('Todos')}</td>
                      <td className="px-3 py-3 text-secondary-foreground">{k.last_used_at ? relativeTime(k.last_used_at) : t('Nunca')}</td>
                      <td className="px-5 py-2 text-right whitespace-nowrap">
                        <Button variant="ghost" size="sm" onClick={() => setEditingKey(k)}>
                          <Pencil /> {t('Editar')}
                        </Button>
                        <Button variant="ghost" size="sm" onClick={() => setRegenerating(k)}>
                          <RefreshCw /> {t('Regenerar')}
                        </Button>
                        <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={() => setRevoking(k)}>
                          {t('Revocar')}
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        )}
        <CopyField label={t('Endpoint para tus clientes')} value={endpoint} />
      </section>

      <CreateBucketDialog open={creatingBucket} onClose={() => setCreatingBucket(false)} />
      <BucketSettingsDialog bucket={configuring} endpoint={endpoint} domain={info.domain} onClose={() => setConfiguring(null)} />
      <CreateKeyDialog open={creatingKey} onClose={() => setCreatingKey(false)} buckets={buckets} endpoint={endpoint} region={info.region} />
      {editingKey && (
        <CreateKeyDialog open editing={editingKey} onClose={() => setEditingKey(null)} buckets={buckets} endpoint={endpoint} region={info.region} />
      )}

      <AlertDialog open={unlinking !== null} onOpenChange={(o) => !o && setUnlinking(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('¿Quitar el bucket {nombre}?', { nombre: unlinking?.name ?? '' })}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('Deja de estar disponible por S3. La carpeta {ruta} y su contenido siguen en Mi espacio.', { ruta: unlinking?.folder_path ?? '' })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <ErrorText error={unlink.error} />
          <AlertDialogFooter>
            <AlertDialogCancel>{t('Cancelar')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault()
                if (unlinking) unlink.mutate(unlinking)
              }}
            >
              {t('Quitar')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <AlertDialog open={regenerating !== null} onOpenChange={(o) => {
          if (o) return
          setRegenerating(null)
          regenerate.reset()
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('¿Regenerar el secreto de «{nombre}»?', { nombre: regenerating?.name ?? '' })}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('Se crea una llave secreta nueva y la actual deja de valer al momento. Todas las conexiones que la usan (aws-cli, rclone, restic, backups, apps) dejarán de funcionar hasta que les pongas la nueva.')}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <p className="flex gap-2 rounded-xl bg-warning-soft px-3.5 py-2.5 text-[13px] text-warning">
            <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
            {t('El Access Key ID, los permisos y los buckets no cambian. No se puede deshacer.')}
          </p>
          <ErrorText error={regenerate.error} />
          <AlertDialogFooter>
            <AlertDialogCancel>{t('Cancelar')}</AlertDialogCancel>
            <AlertDialogAction
              disabled={regenerate.isPending}
              onClick={(e) => {
                e.preventDefault()
                if (regenerating) regenerate.mutate(regenerating)
              }}
            >
              {t('Regenerar secreto')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <Dialog open={regenerated !== null} onOpenChange={(o) => !o && setRegenerated(null)}>
        <DialogContent className="sm:max-w-lg">
          {regenerated && (
            <KeySecret title={t('Secreto regenerado')} created={regenerated} endpoint={endpoint} region={info.region} onDone={() => setRegenerated(null)} />
          )}
        </DialogContent>
      </Dialog>
      <AlertDialog open={revoking !== null} onOpenChange={(o) => !o && setRevoking(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('¿Revocar la llave «{nombre}»?', { nombre: revoking?.name ?? '' })}</AlertDialogTitle>
            <AlertDialogDescription>{t('Los clientes que la usen dejarán de poder entrar al momento. No se puede deshacer.')}</AlertDialogDescription>
          </AlertDialogHeader>
          <ErrorText error={revoke.error} />
          <AlertDialogFooter>
            <AlertDialogCancel>{t('Cancelar')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault()
                if (revoking) revoke.mutate(revoking)
              }}
            >
              {t('Revocar')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}

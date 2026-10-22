import { useState, type FormEvent } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { cn } from 'cn'
import { Check, Copy, FolderOpen, TriangleAlert } from 'lucide-react'
import { createBucket, createS3Key, folderQuery, type Bucket, type NewS3Key, type S3Permission } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { MoveDialog } from '@/features/drive/FileDialogs'
import { ErrorText } from '@/features/shell/Page'
import { msg, t } from '@/lib/i18n'

/** Campo de solo lectura con botón de copiar. */
export function CopyField({ label, value, secret }: { label: string; value: string; secret?: boolean }) {
  const [copied, setCopied] = useState(false)
  return (
    <div className="grid gap-1.5">
      <span className="text-[13px] font-semibold">{label}</span>
      <div className={cn('flex items-center gap-2 rounded-xl border py-1 pr-1 pl-3', secret ? 'border-warning/40 bg-warning-soft' : 'bg-background')}>
        <code className="min-w-0 flex-1 font-mono text-[13px] break-all">{value}</code>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={t('Copiar {nombre}', { nombre: label })}
          onClick={() =>
            void navigator.clipboard.writeText(value).then(() => {
              setCopied(true)
              setTimeout(() => setCopied(false), 2000)
            })
          }
        >
          {copied ? <Check /> : <Copy />}
        </Button>
      </div>
    </div>
  )
}

const bucketNameOk = (n: string) => /^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$/.test(n) && !n.includes('--')

export function CreateBucketDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [folder, setFolder] = useState<{ id: number; label: string } | null>(null)
  const [picking, setPicking] = useState(false)
  const create = useMutation({
    mutationFn: () => createBucket(name, folder?.id ?? 0),
    onSuccess: () => {
      setName('')
      setFolder(null)
      void queryClient.invalidateQueries({ queryKey: ['s3'] })
      void queryClient.invalidateQueries({ queryKey: ['folder'] })
      onClose()
    },
  })
  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    create.mutate()
  }
  return (
    <>
      <Dialog open={open && !picking} onOpenChange={(o) => !o && onClose()}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t('Crear bucket')}</DialogTitle>
            <DialogDescription>{t('Un bucket es una carpeta del disco publicada por la API S3.')}</DialogDescription>
          </DialogHeader>
          <form onSubmit={onSubmit} className="grid grid-cols-1 gap-4">
            <div className="grid gap-1.5">
              <Label htmlFor="bucket-name">{t('Nombre')}</Label>
              <Input
                id="bucket-name"
                autoComplete="off"
                autoCapitalize="none"
                spellCheck={false}
                placeholder={t('respaldos')}
                value={name}
                onChange={(e) => setName(e.target.value.toLowerCase())}
                aria-invalid={name !== '' && !bucketNameOk(name)}
              />
              <p className={cn('text-xs', name !== '' && !bucketNameOk(name) ? 'text-destructive' : 'text-muted-foreground')}>
                {t('3 a 63 caracteres: minúsculas, números y guiones; sin guion al principio ni al final.')}
              </p>
            </div>
            <div className="grid gap-1.5">
              <span className="text-sm font-semibold">{t('Carpeta')}</span>
              <div className="flex flex-wrap items-center gap-2 rounded-xl border bg-background p-3">
                <FolderOpen className="size-4 text-muted-foreground" aria-hidden />
                <span className="min-w-0 flex-1 truncate text-sm">
                  {folder ? folder.label : `${t('Nueva:')} /Buckets/${name || `<${t('nombre')}>`}`}
                </span>
                <Button type="button" variant="outline" size="sm" onClick={() => setPicking(true)}>
                  {folder ? t('Cambiar') : t('Usar una existente')}
                </Button>
                {folder && (
                  <Button type="button" variant="ghost" size="sm" onClick={() => setFolder(null)}>
                    {t('Nueva')}
                  </Button>
                )}
              </div>
              <p className="text-xs text-muted-foreground">{t('Lo que subas por S3 aparece en esa carpeta de Mi espacio, y lo que ya tenga se lee por S3.')}</p>
            </div>
            <ErrorText error={create.error} />
            <div className="flex justify-end gap-2.5">
              <Button type="button" variant="outline" onClick={onClose}>
                {t('Cancelar')}
              </Button>
              <Button type="submit" disabled={!bucketNameOk(name) || create.isPending}>
                {t('Crear bucket')}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
      <MoveDialog
        open={open && picking}
        title={t('Carpeta del bucket')}
        description={t('Elige la carpeta que publicará el bucket. La raíz no vale.')}
        actionLabel={t('Usar esta carpeta')}
        onClose={() => setPicking(false)}
        onPick={(id) => {
          setPicking(false)
          if (id === 0) return
          void queryClient.fetchQuery(folderQuery(id)).then((l) =>
            setFolder({ id, label: '/' + [...l.breadcrumbs, l.folder].map((f) => f.name).join('/') }),
          )
        }}
      />
    </>
  )
}

const permissions: { value: S3Permission; label: string; hint: string }[] = [
  { value: 'admin_rw', label: msg('Admin · lectura y escritura'), hint: msg('Crear y borrar buckets, leer y escribir objetos en todos.') },
  { value: 'admin_ro', label: msg('Admin · solo lectura'), hint: msg('Listar buckets y leer objetos de todos.') },
  { value: 'object_rw', label: msg('Objetos · lectura y escritura'), hint: msg('Leer, escribir y borrar objetos. Para backups (restic, rclone).') },
  { value: 'object_ro', label: msg('Objetos · solo lectura'), hint: msg('Leer y descargar objetos.') },
]

export function permissionLabel(p: S3Permission) {
  const label = permissions.find((x) => x.value === p)?.label
  return label ? t(label) : p
}

/** Credenciales recién generadas: el secreto solo se ve aquí. */
export function KeySecret({
  title,
  created,
  endpoint,
  region,
  onDone,
}: {
  title: string
  created: NewS3Key
  endpoint: string
  region: string
  onDone: () => void
}) {
  return (
    <div className="grid grid-cols-1 gap-4">
      <DialogHeader>
        <DialogTitle>{title}</DialogTitle>
        <DialogDescription>{created.name}</DialogDescription>
      </DialogHeader>
      <p className="flex gap-2 rounded-xl bg-warning-soft px-3.5 py-2.5 text-[13px] text-warning">
        <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
        {t('Copia la llave secreta ahora: no se vuelve a mostrar. Si la pierdes, regenérala o revoca la llave y crea otra.')}
      </p>
      <CopyField label="Access Key ID" value={created.access_key_id} />
      <CopyField label="Secret Access Key" value={created.secret_access_key} secret />
      <CopyField label={t('Endpoint')} value={endpoint} />
      <details className="text-sm">
        <summary className="cursor-pointer font-semibold">{t('Ejemplos de configuración')}</summary>
        <div className="mt-2 grid gap-2">
          <span className="text-[13px] text-muted-foreground">{'aws-cli'}</span>
          <pre className="overflow-x-auto rounded-xl bg-ink p-3 font-mono text-xs text-ink-foreground">{`aws configure set aws_access_key_id ${created.access_key_id}
aws configure set aws_secret_access_key <secret>
aws configure set region ${region}
aws --endpoint-url ${endpoint} s3 ls`}</pre>
          <span className="text-[13px] text-muted-foreground">rclone</span>
          <pre className="overflow-x-auto rounded-xl bg-ink p-3 font-mono text-xs text-ink-foreground">{`[cloudmesh]
type = s3
provider = Other
access_key_id = ${created.access_key_id}
secret_access_key = <secret>
endpoint = ${endpoint}
region = ${region}
force_path_style = true`}</pre>
        </div>
      </details>
      <div className="flex justify-end">
        <Button onClick={onDone}>{t('Listo')}</Button>
      </div>
    </div>
  )
}

export function CreateKeyDialog({
  open,
  onClose,
  buckets,
  endpoint,
  region,
}: {
  open: boolean
  onClose: () => void
  buckets: Bucket[]
  endpoint: string
  region: string
}) {
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [perm, setPerm] = useState<S3Permission>('object_rw')
  const [scoped, setScoped] = useState(false)
  const [picked, setPicked] = useState<number[]>([])
  const [created, setCreated] = useState<NewS3Key | null>(null)
  const create = useMutation({
    mutationFn: () => createS3Key(name, perm, perm.startsWith('object') && scoped ? picked : []),
    onSuccess: (k) => {
      setCreated(k)
      void queryClient.invalidateQueries({ queryKey: ['s3', 'keys'] })
    },
  })
  const close = () => {
    setName('')
    setPerm('object_rw')
    setScoped(false)
    setPicked([])
    setCreated(null)
    create.reset()
    onClose()
  }
  const objectPerm = perm.startsWith('object')
  const valid = name.trim() !== '' && (!objectPerm || !scoped || picked.length > 0)
  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    create.mutate()
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && close()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
        {created ? (
          <KeySecret title={t('Llave creada')} created={created} endpoint={endpoint} region={region} onDone={close} />
        ) : (
          <form onSubmit={onSubmit} className="grid grid-cols-1 gap-4">
            <DialogHeader>
              <DialogTitle>{t('Crear llave de acceso')}</DialogTitle>
              <DialogDescription>{t('Para aws-cli, rclone, restic o cualquier SDK de S3.')}</DialogDescription>
            </DialogHeader>
            <div className="grid gap-1.5">
              <Label htmlFor="key-name">{t('Nombre')}</Label>
              <Input id="key-name" maxLength={80} placeholder={t('restic — servidor casa')} value={name} onChange={(e) => setName(e.target.value)} />
            </div>
            <div role="radiogroup" aria-label={t('Permisos')} className="grid gap-2">
              <span className="text-sm font-semibold">{t('Permisos')}</span>
              {permissions.map((p) => (
                <button
                  key={p.value}
                  type="button"
                  role="radio"
                  aria-checked={perm === p.value}
                  onClick={() => setPerm(p.value)}
                  className={cn('grid gap-0.5 rounded-xl p-3 text-left', perm === p.value ? 'bg-accent/60 ring-2 ring-primary' : 'border hover:bg-background')}
                >
                  <span className="text-sm font-semibold">{t(p.label)}</span>
                  <span className="text-xs text-muted-foreground">{t(p.hint)}</span>
                </button>
              ))}
            </div>
            {objectPerm && (
              <fieldset className="grid gap-2">
                <legend className="mb-1 text-sm font-semibold">{t('Buckets')}</legend>
                <label className="flex items-center gap-2 text-sm">
                  <input type="radio" name="scope" checked={!scoped} onChange={() => setScoped(false)} className="size-4 accent-primary" />
                  {t('Todos los buckets')}
                </label>
                <label className="flex items-center gap-2 text-sm">
                  <input type="radio" name="scope" checked={scoped} onChange={() => setScoped(true)} className="size-4 accent-primary" />
                  {t('Solo algunos')}
                </label>
                {scoped && (
                  <div className="grid gap-1.5 rounded-xl border p-3">
                    {buckets.length === 0 && <span className="text-[13px] text-muted-foreground">{t('Aún no hay buckets.')}</span>}
                    {buckets.map((b) => (
                      <label key={b.id} className="flex items-center gap-2 font-mono text-[13px]">
                        <input
                          type="checkbox"
                          className="size-4 accent-primary"
                          checked={picked.includes(b.id)}
                          onChange={(e) => setPicked((cur) => (e.target.checked ? [...cur, b.id] : cur.filter((id) => id !== b.id)))}
                        />
                        {b.name}
                      </label>
                    ))}
                  </div>
                )}
              </fieldset>
            )}
            <ErrorText error={create.error} />
            <div className="flex justify-end gap-2.5">
              <Button type="button" variant="outline" onClick={close}>
                {t('Cancelar')}
              </Button>
              <Button type="submit" disabled={!valid || create.isPending}>
                {t('Crear llave')}
              </Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

import { useState, type FormEvent } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Plus, Trash2 } from 'lucide-react'
import { configureBucket, type Bucket, type CORSRule, type LifecycleRule } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { CopyField } from '@/features/s3/S3Dialogs'
import { ErrorText } from '@/features/shell/Page'
import { t } from '@/lib/i18n'

/**
 * URL pública de un bucket: con dominio, estilo virtual-host como el dominio
 * público de R2; sin él, estilo de ruta sobre el endpoint.
 */
export function publicUrl(endpoint: string, domain: string, bucket: string) {
  try {
    const u = new URL(endpoint)
    if (domain && u.hostname === domain) return `${u.protocol}//${bucket}.${u.host}/`
  } catch {
    // endpoint sin esquema: se usa tal cual
  }
  return `${endpoint}/${bucket}/`
}

const corsExample: CORSRule[] = [
  {
    AllowedOrigins: ['https://app.ejemplo.com'],
    AllowedMethods: ['GET', 'PUT'],
    AllowedHeaders: ['*'],
    ExposeHeaders: ['ETag'],
    MaxAgeSeconds: 3600,
  },
]

/** CORS en texto: vacío es sin reglas; si no es una lista JSON, devuelve el error. */
function parseCORS(text: string): { rules: CORSRule[]; error?: string } {
  if (text.trim() === '') return { rules: [] }
  try {
    const v: unknown = JSON.parse(text)
    if (!Array.isArray(v)) return { rules: [], error: t('Debe ser una lista JSON de reglas: [ { … } ]') }
    return { rules: v as CORSRule[] }
  } catch (e) {
    return { rules: [], error: t('JSON no válido: {error}', { error: (e as Error).message }) }
  }
}

const newRule = (n: number): LifecycleRule => ({ id: `regla-${n}`, enabled: true, prefix: '', expire_days: 30, abort_days: 7 })

export function BucketSettingsDialog({
  bucket,
  endpoint,
  domain,
  onClose,
}: {
  bucket: Bucket | null
  endpoint: string
  domain: string
  onClose: () => void
}) {
  return (
    <Dialog open={bucket !== null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-2xl">
        {/* key: el formulario arranca con la configuración del bucket elegido. */}
        {bucket && <BucketSettingsForm key={bucket.id} bucket={bucket} endpoint={endpoint} domain={domain} onClose={onClose} />}
      </DialogContent>
    </Dialog>
  )
}

function BucketSettingsForm({ bucket, endpoint, domain, onClose }: { bucket: Bucket; endpoint: string; domain: string; onClose: () => void }) {
  const queryClient = useQueryClient()
  const [isPublic, setPublic] = useState(bucket.public)
  const [cors, setCors] = useState(bucket.cors.length ? JSON.stringify(bucket.cors, null, 2) : '')
  const [rules, setRules] = useState<LifecycleRule[]>(bucket.lifecycle)
  const parsed = parseCORS(cors)
  const save = useMutation({
    mutationFn: () => configureBucket(bucket.id, { public: isPublic, cors: parsed.rules, lifecycle: rules }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['s3', 'buckets'] })
      onClose()
    },
  })
  const setRule = (i: number, patch: Partial<LifecycleRule>) => setRules((cur) => cur.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (!parsed.error) save.mutate()
  }

  return (
    <form onSubmit={onSubmit} className="grid grid-cols-1 gap-6">
      <DialogHeader>
        <DialogTitle>
          {t('Configurar')} <span className="font-mono">{bucket.name}</span>
        </DialogTitle>
        <DialogDescription>{t('Como en R2: acceso público, CORS y ciclo de vida. También se cambian por la API S3 con una llave de administración.')}</DialogDescription>
      </DialogHeader>

      <section className="grid grid-cols-1 gap-3">
        <div className="flex items-start justify-between gap-4">
          <span className="grid gap-0.5">
            <Label htmlFor="bucket-public" className="text-base font-semibold">
              {t('Acceso público')}
            </Label>
            <span className="text-[13px] text-muted-foreground">
              {t('Cualquiera con la URL lee los objetos sin firma. Listar, subir y borrar siguen pidiendo una llave.')}
            </span>
          </span>
          <Switch id="bucket-public" checked={isPublic} onCheckedChange={setPublic} className="mt-1" />
        </div>
        {isPublic && <CopyField label={t('URL pública')} value={publicUrl(endpoint, domain, bucket.name) + `<${t('clave')}>`} />}
      </section>

      <section className="grid grid-cols-1 gap-2">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <Label htmlFor="bucket-cors" className="text-base font-semibold">
            {t('Reglas CORS')}
          </Label>
          {cors.trim() === '' && (
            <Button type="button" variant="ghost" size="sm" onClick={() => setCors(JSON.stringify(corsExample, null, 2))}>
              {t('Usar un ejemplo')}
            </Button>
          )}
        </div>
        <span className="text-[13px] text-muted-foreground">
          {t('JSON en el formato del panel de R2. Hacen falta para subir o leer desde un navegador; añade')}{' '}
          <code className="font-mono">ETag</code> {t('a ExposeHeaders si usas multipart. Vacío: sin CORS.')}
        </span>
        <textarea
          id="bucket-cors"
          spellCheck={false}
          rows={8}
          value={cors}
          onChange={(e) => setCors(e.target.value)}
          aria-invalid={parsed.error !== undefined}
          placeholder="[]"
          className="w-full min-w-0 rounded-lg border border-input bg-card p-3 font-mono text-base outline-none md:text-[13px] focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 aria-invalid:border-destructive"
        />
        {parsed.error && <p className="text-xs text-destructive">{parsed.error}</p>}
      </section>

      <section className="grid grid-cols-1 gap-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span className="text-base font-semibold">{t('Ciclo de vida')}</span>
          <Button type="button" variant="outline" size="sm" onClick={() => setRules((cur) => [...cur, newRule(cur.length + 1)])}>
            <Plus /> {t('Añadir regla')}
          </Button>
        </div>
        <span className="text-[13px] text-muted-foreground">
          {t('El mantenimiento manda a la papelera los objetos del prefijo pasados N días desde su subida y cancela las subidas multipart a medias. 0 desactiva cada parte.')}
        </span>
        {rules.length === 0 && <p className="rounded-xl border border-dashed p-4 text-center text-sm text-muted-foreground">{t('Sin reglas.')}</p>}
        <ul className="grid grid-cols-1 gap-3">
          {rules.map((r, i) => (
            <li key={i} className="grid grid-cols-2 gap-3 rounded-xl border p-3 sm:grid-cols-[1fr_1fr_7rem_7rem]">
              <div className="col-span-2 flex items-center justify-between gap-2 sm:col-span-4">
                <label className="flex items-center gap-2 text-sm font-semibold">
                  <Switch checked={r.enabled} onCheckedChange={(enabled) => setRule(i, { enabled })} aria-label={t('Activar la regla {id}', { id: r.id })} />
                  {r.enabled ? t('Activa') : t('Desactivada')}
                </label>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="text-destructive hover:text-destructive"
                  aria-label={t('Quitar la regla {id}', { id: r.id })}
                  onClick={() => setRules((cur) => cur.filter((_, j) => j !== i))}
                >
                  <Trash2 />
                </Button>
              </div>
              <div className="grid min-w-0 gap-1">
                <Label htmlFor={`lc-id-${i}`} className="text-xs">
                  ID
                </Label>
                <Input id={`lc-id-${i}`} value={r.id} maxLength={255} onChange={(e) => setRule(i, { id: e.target.value })} />
              </div>
              <div className="grid min-w-0 gap-1">
                <Label htmlFor={`lc-prefix-${i}`} className="text-xs">
                  {t('Prefijo')}
                </Label>
                <Input
                  id={`lc-prefix-${i}`}
                  className="font-mono"
                  placeholder={t('todo el bucket')}
                  value={r.prefix}
                  onChange={(e) => setRule(i, { prefix: e.target.value })}
                />
              </div>
              <div className="grid min-w-0 gap-1">
                <Label htmlFor={`lc-exp-${i}`} className="text-xs">
                  {t('Borrar tras (días)')}
                </Label>
                <Input
                  id={`lc-exp-${i}`}
                  type="number"
                  min={0}
                  inputMode="numeric"
                  value={r.expire_days}
                  onChange={(e) => setRule(i, { expire_days: Math.max(0, Math.trunc(Number(e.target.value) || 0)) })}
                />
              </div>
              <div className="grid min-w-0 gap-1">
                <Label htmlFor={`lc-abort-${i}`} className="text-xs">
                  {t('Cancelar subidas (días)')}
                </Label>
                <Input
                  id={`lc-abort-${i}`}
                  type="number"
                  min={0}
                  inputMode="numeric"
                  value={r.abort_days}
                  onChange={(e) => setRule(i, { abort_days: Math.max(0, Math.trunc(Number(e.target.value) || 0)) })}
                />
              </div>
            </li>
          ))}
        </ul>
      </section>

      <ErrorText error={save.error} />
      <div className="flex justify-end gap-2.5">
        <Button type="button" variant="outline" onClick={onClose}>
          {t('Cancelar')}
        </Button>
        <Button type="submit" disabled={save.isPending || parsed.error !== undefined}>
          {t('Guardar')}
        </Button>
      </div>
    </form>
  )
}

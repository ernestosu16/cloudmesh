import { useEffect, useMemo, useState } from 'react'
import { createFileRoute, Link, useLocation, useNavigate } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient, useSuspenseQuery } from '@tanstack/react-query'
import { cn } from 'cn'
import {
  CircleCheck,
  CircleHelp,
  Cloud,
  Database,
  Download,
  FlaskConical,
  HardDrive,
  MoreVertical,
  Package,
  Plus,
  RefreshCw,
  Settings2,
  SlidersHorizontal,
  Trash2,
  type LucideIcon,
} from 'lucide-react'
import {
  deleteAccount,
  formatPool,
  healthQuery,
  manifestExportUrl,
  providersQuery,
  quotaQuery,
  runRebuildAll,
  runRebuildFile,
  runScrub,
  settingsQuery,
  syncAccount,
  updateSettings,
  writeManifest,
  type Account,
  type Settings,
} from '@/lib/api'
import { formatBytes, percent, relativeTime } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
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
import { ReconnectDialog } from '@/features/accounts/CredentialsDialog'
import { ConnectForm } from '@/features/accounts/ConnectForm'
import { contributionLabel } from '@/features/accounts/Contribution'
import { SettingsDialog } from '@/features/accounts/SettingsDialog'
import { usableSpace } from '@/features/accounts/usable'
import { accountCodes, accountColors } from '@/features/accounts/codes'
import { ErrorText, PageHeader, Panel, Tag } from '@/features/shell/Page'
import { requireRole } from '@/features/auth/roles'
import { getLang, msg, t } from '@/lib/i18n'

export const Route = createFileRoute('/_auth/storage')({
  beforeLoad: requireRole('admin'),
  loader: ({ context }) =>
    Promise.all([
      context.queryClient.ensureQueryData(quotaQuery),
      context.queryClient.ensureQueryData(settingsQuery),
      context.queryClient.ensureQueryData(healthQuery),
    ]),
  component: StoragePage,
})

interface Scheme {
  id: string
  label: string
  raid: string
  k: number
  m: number
}

/** Esquemas según cuántas cuentas reciben subidas. La paridad crece con ellas hasta 6+1 y 10+2. */
function schemesFor(n: number): Scheme[] {
  return [
    { id: 'none', label: t('Sin protección'), raid: 'JBOD', k: 1, m: 0 },
    { id: 'x2', label: t('Copia doble'), raid: 'RAID 1', k: 1, m: 1 },
    { id: 'x3', label: t('Copia triple'), raid: 'RAID 1 ×3', k: 1, m: 2 },
    { id: 'p1', label: t('Paridad simple'), raid: 'RAID 5', k: Math.min(6, Math.max(2, n - 1)), m: 1 },
    { id: 'p2', label: t('Paridad doble'), raid: 'RAID 6', k: Math.min(10, Math.max(2, n - 2)), m: 2 },
  ]
}

const falls = (m: number) => (m === 1 ? t('1 caída') : t('{n} caídas', { n: m }))

function StoragePage() {
  const { data: quota } = useSuspenseQuery(quotaQuery)
  const { data: settings } = useSuspenseQuery(settingsQuery)
  const { data: health } = useSuspenseQuery(healthQuery)
  const [configOpen, setConfigOpen] = useState(false)
  const [helpOpen, setHelpOpen] = useState(false)
  // /storage#add (enlace de Perfil) abre el alta; se quita el hash para que
  // el enlace vuelva a funcionar tras cerrarla.
  const hash = useLocation({ select: (l) => l.hash })
  const navigate = useNavigate()
  const [adding, setAdding] = useState(hash === 'add')
  useEffect(() => {
    if (hash === 'add') void navigate({ to: '.', hash: '', replace: true })
  }, [hash, navigate])

  const pool = quota.accounts.filter((a) => a.enabled && a.status === 'active')
  const codes = accountCodes(quota.accounts)
  const k = settings.data_shards
  const m = settings.parity_shards
  const striped = k + m > 1 && pool.length >= k + m
  const usable = usableSpace(quota, settings)
  const current = schemesFor(pool.length).find((s) => s.k === k && s.m === m)

  return (
    <section className="grid grid-cols-1 gap-6">
      <PageHeader
        title={t('Almacenamiento')}
        description={t('Cada cuenta que conectas suma su espacio al disco. Quien usa CloudMesh solo ve un único disco.')}
      />

      <Panel aria-labelledby="pool-title" className="grid grid-cols-1 gap-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h2 id="pool-title" className="text-2xl font-bold">
            {formatBytes(usable.used)}{' '}
            <span className="text-base font-medium text-muted-foreground">{t('usados de {total} útiles', { total: formatBytes(usable.total) })}</span>
          </h2>
          <Button
            variant={configOpen ? 'default' : 'outline'}
            className={cn(configOpen && 'bg-foreground text-background hover:bg-foreground/90')}
            aria-expanded={configOpen}
            aria-controls="pool-config"
            onClick={() => setConfigOpen((o) => !o)}
          >
            <SlidersHorizontal /> {configOpen ? t('Cerrar') : t('Configurar')}
          </Button>
        </div>

        <div className="flex h-3 overflow-hidden rounded-full bg-secondary" aria-hidden>
          {quota.accounts.map((a, i) => (
            <span key={a.id} className={accountColors[i % accountColors.length]} style={{ width: `${percent(a.occupied, quota.total)}%` }} />
          ))}
        </div>

        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-[13px] text-secondary-foreground">
            {quota.accounts.map((a, i) => (
              <span key={a.id} className="flex items-center gap-1.5">
                <span className={cn('size-2.5 rounded-sm', accountColors[i % accountColors.length])} />
                <span className="font-mono text-xs">{codes.get(a.id)}</span>
                <span className="max-w-32 truncate">{a.label}</span>
              </span>
            ))}
            <span>
              ·{' '}
              {quota.accounts.length === 1
                ? t('{total} brutos en 1 cuenta', { total: formatBytes(quota.total) })
                : t('{total} brutos en {n} cuentas', { total: formatBytes(quota.total), n: quota.accounts.length })}
            </span>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="ghost" size="icon-sm" aria-label={t('Ayuda: cuántas cuentas necesita cada protección')} onClick={() => setHelpOpen(true)}>
              <CircleHelp />
            </Button>
            <Tag tone="accent">
              {current ? `${current.label} (≈ ${current.raid})` : t('Personalizado {k}+{m}', { k, m })} · {t('tolera {caidas}', { caidas: falls(m) })}
            </Tag>
            {health.missing_chunks > 0 ? (
              <Tag tone="warning">{t('{n} pedazos en reparación', { n: health.missing_chunks })}</Tag>
            ) : (
              <Tag tone="success">{t('Sin pedazos perdidos')}</Tag>
            )}
            <Tag>{health.manifest_written_at ? t('Índice respaldado {cuando}', { cuando: relativeTime(health.manifest_written_at) }) : t('Índice sin respaldar')}</Tag>
            {health.plain_chunks > 0 ? (
              <Tag tone="accent" title={t('El mantenimiento cifra poco a poco los bloques subidos antes del cifrado, para no agotar la transferencia de las cuentas.')}>
                {t('Cifrando · {n} bloques pendientes', { n: health.plain_chunks.toLocaleString(getLang()) })}
              </Tag>
            ) : (
              <Tag tone="success" title={t('Cada bloque sale cifrado con AES-256-GCM y una clave por archivo: los proveedores solo ven datos cifrados.')}>
                {t('Bloques cifrados')}
              </Tag>
            )}
            {health.whole_files > 0 && (
              <Tag title={t('Archivos que ya estaban en la carpeta cloudmesh del proveedor y que el sync añadió: siguen visibles allí y no se cifran.')}>
                {health.whole_files === 1
                  ? t('1 archivo importado sin cifrar')
                  : t('{n} archivos importados sin cifrar', { n: health.whole_files.toLocaleString(getLang()) })}
              </Tag>
            )}
          </div>
        </div>

        {!striped && quota.accounts.length > 0 && (
          <p className="rounded-xl bg-warning-soft px-4 py-3 text-[13px] text-warning">
            {k + m > 1
              ? pool.length === 1
                ? t('Con 1 cuenta activa no se puede repartir {k}+{m}: los archivos nuevos se guardan en bloques cifrados sin paridad (1+0).', { k, m })
                : t('Con {n} cuentas activas no se puede repartir {k}+{m}: los archivos nuevos se guardan en bloques cifrados sin paridad (1+0).', { n: pool.length, k, m })
              : t('Sin protección: los bloques de cada archivo no tienen paridad; perder una cuenta pierde lo que guarda.')}
          </p>
        )}

        {configOpen && (
          <PoolConfig id="pool-config" settings={settings} pool={pool} codes={codes} onHelp={() => setHelpOpen(true)} onAdd={() => setAdding(true)} />
        )}
      </Panel>

      <Accounts accounts={quota.accounts} codes={codes} onAdd={() => setAdding(true)} />

      <AddAccountDialog open={adding} onClose={() => setAdding(false)} />

      <HelpDialog open={helpOpen} onClose={() => setHelpOpen(false)} accounts={pool.length} />
    </section>
  )
}

function PoolConfig({
  id,
  settings,
  pool,
  codes,
  onHelp,
  onAdd,
}: {
  id: string
  settings: Settings
  pool: Account[]
  codes: Map<number, string>
  onHelp: () => void
  onAdd: () => void
}) {
  const queryClient = useQueryClient()
  const { data: health } = useSuspenseQuery(healthQuery)
  const schemes = useMemo(() => schemesFor(pool.length), [pool.length])
  const [k, setK] = useState(settings.data_shards)
  const [m, setM] = useState(settings.parity_shards)
  const [chunkMiB, setChunkMiB] = useState(Math.round(settings.chunk_size / (1 << 20)))
  const [failed, setFailed] = useState<Set<number>>(new Set())
  const [note, setNote] = useState<string | null>(null)
  const pick = (nk: number, nm: number) => {
    setK(nk)
    setM(nm)
    setFailed(new Set())
  }

  const refresh = () =>
    Promise.all(['health', 'quota', 'folder'].map((key) => queryClient.invalidateQueries({ queryKey: [key] })))
  const save = useMutation({
    mutationFn: () => updateSettings({ ...settings, data_shards: k, parity_shards: m, chunk_size: chunkMiB * (1 << 20) }),
    onSuccess: (s) => {
      queryClient.setQueryData(settingsQuery.queryKey, s)
      setNote(t('Ajustes guardados. Solo afectan a las subidas nuevas.'))
    },
  })
  const repair = useMutation({
    mutationFn: async () => {
      const scrub = await runScrub()
      const rebuild = await runRebuildAll()
      return { scrub, rebuild }
    },
    onSuccess: ({ scrub, rebuild }) => {
      const sum = <T,>(xs: T[], f: (x: T) => number) => xs.reduce((n, x) => n + f(x), 0)
      setNote(
        t('{comprobados} pedazos comprobados, {perdidos} perdidos, {regenerados} regenerados, {pendientes} pendientes.', {
          comprobados: sum(scrub, (r) => r.checked),
          perdidos: sum(scrub, (r) => r.missing),
          regenerados: sum(rebuild, (r) => r.rebuilt),
          pendientes: sum(rebuild, (r) => r.pending),
        }),
      )
      return refresh()
    },
  })
  const backup = useMutation({
    mutationFn: writeManifest,
    onSuccess: (r) => {
      setNote(r.accounts === 1 ? t('Índice cifrado guardado en 1 cuenta.') : t('Índice cifrado guardado en {n} cuentas.', { n: r.accounts }))
      return refresh()
    },
  })
  const busy = save.isPending || repair.isPending || backup.isPending

  const need = k + m
  const usefulPct = Math.round((k / (k + m)) * 100)
  const kind = k === 1 ? 'copy' : 'parity'
  const pieces = Array.from({ length: need }, (_, i) => {
    const parity = i >= k
    const label = kind === 'copy' ? (m === 0 ? t('Archivo') : parity ? t('Copia {n}', { n: i - k + 1 }) : t('Original')) : parity ? `P${i - k + 1}` : `D${i + 1}`
    const account = pool[i]
    return { i, label, account, down: !account || failed.has(i) }
  })
  const lost = pieces.filter((p) => p.down).length
  const verdict =
    need > pool.length
      ? { text: t('No disponible: faltan cuentas para repartir todos los pedazos.'), cls: 'bg-secondary text-secondary-foreground' }
      : lost === 0
        ? { text: t('Todo en línea. El archivo está completo.'), cls: 'bg-success-soft text-success' }
        : m === 0
          ? { text: t('Archivo perdido. Sin protección se pierden los archivos que vivían en esa cuenta.'), cls: 'bg-destructive/10 text-destructive' }
          : lost <= m
            ? { text: t('Faltan {n} pedazo(s): se reconstruye con la paridad y se repara solo.', { n: lost }), cls: 'bg-accent text-accent-foreground' }
            : { text: t('Faltan {n} pedazo(s) y la paridad cubre {m}: archivo perdido.', { n: lost, m }), cls: 'bg-destructive/10 text-destructive' }
  const totalChunks = health.accounts.reduce((n, a) => n + a.chunks, 0)
  const degraded = health.files.degraded + health.files.lost

  return (
    <div id={id} className="grid grid-cols-1 gap-5 border-t pt-5">
      <div className="grid grid-cols-1 gap-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span className="text-sm font-semibold">{t('Redundancia')}</span>
          <Button variant="link" size="sm" className="h-auto p-0" onClick={onHelp}>
            <CircleHelp /> {t('¿Cuántas cuentas necesito?')}
          </Button>
        </div>
        <div role="radiogroup" aria-label={t('Esquema de redundancia')} className="grid grid-cols-[repeat(auto-fit,minmax(140px,1fr))] gap-2.5">
          {schemes.map((s) => {
            const on = s.k === k && s.m === m
            const extra = s.k + s.m > pool.length ? ` · ${t('{n} cuentas', { n: s.k + s.m })}` : ''
            return (
              <button
                key={s.id}
                type="button"
                role="radio"
                aria-checked={on}
                onClick={() => pick(s.k, s.m)}
                className={cn(
                  'grid gap-1 rounded-xl p-3.5 text-left',
                  on ? 'bg-accent/60 ring-2 ring-primary' : 'border bg-card hover:bg-background',
                )}
              >
                <span className="text-sm font-semibold">{s.label}</span>
                <span className="text-xs text-muted-foreground">
                  ≈ {s.raid} · {s.m === 0 ? t('no tolera caídas') : t('tolera {caidas}', { caidas: s.m })} ·{' '}
                  {t('{n}% útil', { n: Math.round((s.k / (s.k + s.m)) * 100) })}
                  {extra}
                </span>
              </button>
            )
          })}
        </div>
        {need > pool.length && (
          <p className="flex flex-wrap items-center gap-2 rounded-xl bg-warning-soft px-4 py-3 text-[13px] text-warning">
            {t('Necesita {n} cuentas activas y tienes {tienes}. Añade {faltan} más; mientras tanto los archivos se guardan sin paridad (1+0).', {
              n: need,
              tienes: pool.length,
              faltan: need - pool.length,
            })}
            <button type="button" onClick={onAdd} className="font-semibold underline">
              {t('Añadir cuenta')}
            </button>
          </p>
        )}
        <div className="flex flex-wrap gap-x-6 gap-y-1 text-sm text-secondary-foreground">
          <span>
            {t('Esquema:')}{' '}
            <strong className="text-foreground">
              {k}+{m}
            </strong>
          </span>
          <span>
            {t('Útil:')} <strong className="text-foreground">{usefulPct}%</strong>
          </span>
          <span>
            {t('Puedes perder:')} <strong className="text-foreground">{falls(m)}</strong>
          </span>
          <span>
            {t('Extra por redundancia:')} <strong className="text-foreground">{m === 0 ? '0%' : `+${Math.round((m / k) * 100)}%`}</strong>
          </span>
        </div>
        <details className="text-sm">
          <summary className="cursor-pointer font-semibold text-secondary-foreground">{t('Avanzado')}</summary>
          <div className="mt-3 flex flex-wrap gap-3">
            <div className="grid grid-cols-1 w-28 gap-1.5">
              <Label htmlFor="cfg-k">{t('Datos (k)')}</Label>
              <Input id="cfg-k" type="number" min={1} max={16} value={k} onChange={(e) => pick(Math.max(1, Number(e.target.value) || 1), m)} />
            </div>
            <div className="grid grid-cols-1 w-28 gap-1.5">
              <Label htmlFor="cfg-m">{t('Paridad (m)')}</Label>
              <Input id="cfg-m" type="number" min={0} max={8} value={m} onChange={(e) => pick(k, Math.max(0, Number(e.target.value) || 0))} />
            </div>
            <div className="grid grid-cols-1 w-36 gap-1.5">
              <Label htmlFor="cfg-chunk">{t('Bloque (MiB)')}</Label>
              <Input id="cfg-chunk" type="number" min={1} max={256} value={chunkMiB} onChange={(e) => setChunkMiB(Math.max(1, Number(e.target.value) || 1))} />
            </div>
            <p className="basis-full text-[13px] text-muted-foreground">
              {t('Memoria por subida en curso: {n} MiB (una franja).', { n: k * chunkMiB })}
            </p>
          </div>
        </details>
      </div>

      <div className="grid grid-cols-1 gap-3 rounded-xl bg-background p-4">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <span className="text-sm font-semibold">{t('Simulación: una franja de un archivo')}</span>
          <span className="text-xs text-muted-foreground">{t('Pulsa un pedazo para que su cuenta se caiga')}</span>
        </div>
        <div className="grid grid-cols-[repeat(auto-fill,minmax(130px,1fr))] gap-2.5">
          {pieces.map((p) => {
            const down = p.down
            return (
              <button
                key={p.i}
                type="button"
                disabled={!p.account}
                aria-pressed={!!p.account && failed.has(p.i)}
                onClick={() =>
                  setFailed((f) => {
                    const next = new Set(f)
                    if (next.has(p.i)) next.delete(p.i)
                    else next.add(p.i)
                    return next
                  })
                }
                className={cn(
                  'grid gap-1 rounded-xl p-3 text-left',
                  down ? 'border border-dashed border-destructive bg-destructive/5 text-destructive' : 'border bg-card',
                  !p.account && 'border-muted-foreground text-muted-foreground',
                )}
              >
                <span className={cn('flex items-center justify-between gap-2 font-semibold', down && p.account && 'line-through')}>
                  {p.label}
                  <span className="font-mono text-xs font-normal">{p.account ? codes.get(p.account.id) : '—'}</span>
                </span>
                <span className="truncate text-xs">{!p.account ? t('Falta cuenta') : down ? `${t('Caída')} · ${p.account.label}` : p.account.label}</span>
              </button>
            )
          })}
        </div>
        <p role="status" className={cn('rounded-lg px-3.5 py-2.5 text-[13px] font-medium', verdict.cls)}>
          {verdict.text}
        </p>
      </div>

      {note && <p className="rounded-lg bg-background px-3.5 py-2.5 text-sm">{note}</p>}
      <ErrorText error={save.error ?? repair.error ?? backup.error} />
      <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-4">
        <span className="text-[13px] text-muted-foreground">
          {t('{n} pedazos', { n: totalChunks.toLocaleString(getLang()) })} ·{' '}
          {degraded === 1 ? t('1 archivo degradado') : t('{n} archivos degradados', { n: degraded })} ·{' '}
          {health.manifest_written_at
            ? t('índice respaldado {cuando}', { cuando: relativeTime(health.manifest_written_at) })
            : t('índice sin respaldar')}
        </span>
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" disabled={busy} onClick={() => backup.mutate()}>
            {t('Respaldar índice')}
          </Button>
          <Button asChild variant="outline">
            <a href={manifestExportUrl} download>
              <Download /> {t('Exportar índice')}
            </a>
          </Button>
          <Button variant="outline" disabled={busy} onClick={() => repair.mutate()}>
            <RefreshCw className={repair.isPending ? 'animate-spin' : undefined} /> {t('Verificar y reparar')}
          </Button>
          <Button disabled={busy} onClick={() => save.mutate()}>
            {t('Guardar')}
          </Button>
        </div>
      </div>
      <p className="text-[13px] text-muted-foreground">
        {t('El índice exportado va cifrado con la clave maestra. En una instancia vacía se restaura con')}{' '}
        <code className="rounded bg-secondary px-1 font-mono">cloudmesh restore --file {t('ARCHIVO')}</code>.
      </p>
      <DegradedFiles />
      <FormatPool />
    </div>
  )
}

const formatWord = msg('FORMATEAR')

/** Borra todo el contenido del pool y deja el índice de cero, tras confirmar. */
function FormatPool() {
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const [typed, setTyped] = useState('')
  const [done, setDone] = useState<number | null>(null)
  const word = t(formatWord)
  const format = useMutation({
    mutationFn: formatPool,
    onSuccess: (r) => {
      setOpen(false)
      setDone(r.files)
      // Todo lo que cuelga del disco cambió: carpetas, papelera, enlaces, buckets.
      return queryClient.invalidateQueries({ predicate: (q) => q.queryKey[0] !== 'auth' })
    },
  })
  const close = (o: boolean) => {
    if (format.isPending) return
    setOpen(o)
    if (!o) {
      setTyped('')
      format.reset()
    }
  }

  return (
    <div className="grid grid-cols-1 gap-3 rounded-xl border border-destructive/40 p-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="grid min-w-0 gap-1">
          <span className="text-sm font-semibold text-destructive">{t('Formatear pool')}</span>
          <span className="text-[13px] text-muted-foreground">
            {t('Borra todos los archivos, carpetas, papelera, enlaces y buckets S3, y sus bloques en cada cuenta. Cuentas, usuarios, ajustes y llaves S3 se mantienen.')}
          </span>
        </div>
        <Button variant="destructive" onClick={() => setOpen(true)}>
          <Trash2 /> {t('Formatear pool')}
        </Button>
      </div>
      {done !== null && (
        <p role="status" className="rounded-lg bg-background px-3.5 py-2.5 text-sm">
          {done === 1 ? t('Pool formateado: 1 archivo borrado.') : t('Pool formateado: {n} archivos borrados.', { n: done.toLocaleString(getLang()) })}
        </p>
      )}
      <AlertDialog open={open} onOpenChange={close}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('¿Formatear el pool?')}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('Se borran para siempre todos los archivos (también los de la papelera), las carpetas, los enlaces públicos y los buckets S3, y se destruyen sus bloques en las cuentas conectadas. El índice vuelve a empezar de cero. No se puede deshacer.')}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="grid grid-cols-1 gap-1.5">
            <Label htmlFor="format-confirm">{t('Escribe {palabra} para confirmar', { palabra: word })}</Label>
            <Input
              id="format-confirm"
              autoComplete="off"
              className="text-base md:text-sm"
              value={typed}
              disabled={format.isPending}
              onChange={(e) => setTyped(e.target.value)}
            />
          </div>
          <ErrorText error={format.error} />
          <AlertDialogFooter>
            <AlertDialogCancel disabled={format.isPending}>{t('Cancelar')}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={typed.trim() !== word || format.isPending}
              onClick={(e) => {
                e.preventDefault()
                format.mutate()
              }}
            >
              {format.isPending ? t('Formateando…') : t('Formatear')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

/** Archivos con pedazos perdidos y reparación individual. */
function DegradedFiles() {
  const queryClient = useQueryClient()
  const { data: health } = useSuspenseQuery(healthQuery)
  const rebuild = useMutation({
    mutationFn: runRebuildFile,
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['health'] }),
  })
  if (health.degraded_files.length === 0) return null
  return (
    <div className="grid grid-cols-1 gap-2">
      <span className="text-sm font-semibold">{t('Archivos con pedazos perdidos')}</span>
      <ul className="divide-y rounded-xl border">
        {health.degraded_files.map((f) => (
          <li key={f.id} className="flex flex-wrap items-center justify-between gap-2 px-4 py-2.5 text-sm">
            <span className="grid grid-cols-1 min-w-0">
              <Link to="/" search={{ folder: f.folder_id }} className="truncate font-medium hover:underline">
                {f.name}
              </Link>
              <span className="text-xs text-muted-foreground">
                {formatBytes(f.size)} · {f.layout === 'rs' ? `${f.data_shards}+${f.parity_shards}` : t('entero')}
              </span>
            </span>
            <span className="flex items-center gap-2">
              <Tag tone={f.status === 'lost' ? 'danger' : 'warning'}>{f.status === 'lost' ? t('Perdido') : t('Degradado')}</Tag>
              <Button size="sm" variant="outline" disabled={rebuild.isPending || f.layout !== 'rs'} onClick={() => rebuild.mutate(f.id)}>
                {t('Reparar')}
              </Button>
            </span>
          </li>
        ))}
      </ul>
      <ErrorText error={rebuild.error} />
    </div>
  )
}

function Accounts({ accounts, codes, onAdd }: { accounts: Account[]; codes: Map<number, string>; onAdd: () => void }) {
  const queryClient = useQueryClient()
  const { data: health } = useSuspenseQuery(healthQuery)
  const [editing, setEditing] = useState<Account | null>(null)
  const [reconnecting, setReconnecting] = useState<Account | null>(null)
  const [removing, setRemoving] = useState<Account | null>(null)
  const invalidate = () =>
    Promise.all(['quota', 'folder', 'search', 'health'].map((key) => queryClient.invalidateQueries({ queryKey: [key] })))
  const sync = useMutation({ mutationFn: (a: Account) => syncAccount(a.id), onSettled: invalidate })
  const remove = useMutation({
    mutationFn: (a: Account) => deleteAccount(a.id),
    onSuccess: () => invalidate().then(() => setRemoving(null)),
  })
  const chunks = new Map(health.accounts.map((a) => [a.account_id, a]))

  const chip = (a: Account, i: number) => (
    <span
      className={cn(
        'flex size-9 shrink-0 items-center justify-center rounded-lg font-mono text-xs font-bold text-white',
        accountColors[i % accountColors.length],
      )}
    >
      {codes.get(a.id)}
    </span>
  )
  const status = (a: Account) =>
    a.status === 'reconnect_required' ? (
      <Tag tone="warning" title={a.last_error}>
        {t('Sesión caducada')}
      </Tag>
    ) : a.throttled_until ? (
      <Tag
        tone="warning"
        title={t('El proveedor limitó las descargas. Se lee de las demás cuentas con la paridad y se vuelve a probar a las {hora}.', {
          hora: new Date(a.throttled_until * 1000).toLocaleTimeString(getLang(), { hour: '2-digit', minute: '2-digit' }),
        })}
      >
        {t('Transferencia agotada')}
      </Tag>
    ) : !a.enabled ? (
      <Tag>{t('Sin subidas')}</Tag>
    ) : (
      <Tag tone="success">{t('En línea')}</Tag>
    )
  const usage = (a: Account, i: number) => (
    <span className="grid grid-cols-1 gap-1">
      <span className="font-mono text-xs text-secondary-foreground">
        {formatBytes(a.occupied)} / {formatBytes(a.capacity)}
      </span>
      <span className="h-1.5 overflow-hidden rounded-full bg-secondary">
        <span className={cn('block h-full', accountColors[i % accountColors.length])} style={{ width: `${percent(a.occupied, a.capacity)}%` }} />
      </span>
      {a.contrib_mode !== 'all' && (
        <span className="text-[11px] text-muted-foreground">
          {contributionLabel(a)} ·{' '}
          {t('en la cuenta {usado} de {total}', { usado: formatBytes(a.provider_used), total: formatBytes(a.quota_total) })}
        </span>
      )}
    </span>
  )
  const pieces = (a: Account) => {
    const h = chunks.get(a.id)
    return (
      <>
        {h ? h.chunks.toLocaleString(getLang()) : 0}
        {h && h.missing > 0 && <span className="text-warning"> · {t('{n} perdidos', { n: h.missing })}</span>}
      </>
    )
  }
  // inline: Reconectar junto al menú (tabla); en tarjetas va en su propia fila.
  const actions = (a: Account, inline = true) => {
    const reconnect = a.status === 'reconnect_required'
    return (
      <>
        {reconnect && inline && (
          <Button size="sm" onClick={() => setReconnecting(a)}>
            {t('Reconectar')}
          </Button>
        )}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon" aria-label={t('Acciones de {nombre}', { nombre: a.label })}>
              <MoreVertical />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem disabled={reconnect || sync.isPending} onSelect={() => sync.mutate(a)}>
              <RefreshCw className="size-4" /> {t('Sincronizar')}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => setEditing(a)}>
              <Settings2 className="size-4" /> {t('Configurar')}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem variant="destructive" onSelect={() => setRemoving(a)}>
              <Trash2 className="size-4" /> {t('Quitar del pool')}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </>
    )
  }

  return (
    <section className="grid grid-cols-1 min-w-0 gap-3.5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-xl font-bold">{t('Cuentas conectadas')}</h2>
        <Button onClick={onAdd}>
          <Plus /> {t('Añadir cuenta')}
        </Button>
      </div>
      {accounts.length === 0 ? (
        <Panel className="text-sm text-muted-foreground">{t('Aún no hay cuentas. Añade la primera para empezar a guardar archivos.')}</Panel>
      ) : (
        <>
        <ul className="grid grid-cols-1 gap-2.5 md:hidden">
          {accounts.map((a, i) => (
            <li key={a.id} className="grid grid-cols-1 gap-3 rounded-2xl border bg-card p-4">
              <div className="flex items-center gap-3">
                {chip(a, i)}
                <span className="grid grid-cols-1 min-w-0 flex-1">
                  <span className="truncate font-semibold">{a.label}</span>
                  <span className="truncate text-xs text-muted-foreground">
                    {a.provider.toUpperCase()} · {a.email}
                  </span>
                </span>
                {actions(a, false)}
              </div>
              {usage(a, i)}
              <div className="flex flex-wrap items-center gap-2 text-[13px] text-secondary-foreground">
                {status(a)}
                <span className="font-mono">
                  {pieces(a)} {t('pedazos')}
                </span>
                {a.status === 'reconnect_required' && (
                  <Button size="sm" className="ml-auto" onClick={() => setReconnecting(a)}>
                    {t('Reconectar')}
                  </Button>
                )}
              </div>
            </li>
          ))}
        </ul>
        <div className="hidden overflow-x-auto rounded-2xl border bg-card md:block">
          <table className="w-full min-w-[640px] border-collapse text-sm">
            <thead>
              <tr className="text-left text-xs tracking-wider text-muted-foreground uppercase">
                <th className="border-b px-5 py-3.5 font-semibold">{t('Cuenta')}</th>
                <th className="border-b px-3 py-3.5 font-semibold">{t('Aporta')}</th>
                <th className="border-b px-3 py-3.5 font-semibold">{t('Estado')}</th>
                <th className="border-b px-3 py-3.5 font-semibold">{t('Pedazos')}</th>
                <th className="border-b px-5 py-3.5">
                  <span className="sr-only">{t('Acciones')}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {accounts.map((a, i) => (
                <tr key={a.id} className="border-b border-secondary last:border-0">
                  <td className="px-5 py-3">
                    <span className="flex items-center gap-3">
                      {chip(a, i)}
                      <span className="grid grid-cols-1 min-w-0">
                        <span className="truncate font-semibold">{a.label}</span>
                        <span className="truncate text-xs text-muted-foreground">
                          {a.provider.toUpperCase()} · {a.email}
                        </span>
                      </span>
                    </span>
                  </td>
                  <td className="min-w-36 px-3 py-3">{usage(a, i)}</td>
                  <td className="px-3 py-3">{status(a)}</td>
                  <td className="px-3 py-3 font-mono text-[13px] text-secondary-foreground">{pieces(a)}</td>
                  <td className="px-5 py-2 text-right whitespace-nowrap">{actions(a)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        </>
      )}
      <ErrorText error={sync.error} />
      <p className="text-[13px] text-muted-foreground">
        {t('Al quitar una cuenta, CloudMesh regenera sus pedazos en las demás con la paridad. Lo guardado en el proveedor no se borra.')}
      </p>

      <SettingsDialog account={editing} onClose={() => setEditing(null)} />
      <ReconnectDialog account={reconnecting} onClose={() => setReconnecting(null)} />
      <AlertDialog open={removing !== null} onOpenChange={(o) => !o && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('¿Quitar {nombre} del pool?', { nombre: removing?.label ?? '' })}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('CloudMesh deja de usar esta cuenta y repara con la paridad los archivos que tenían pedazos en ella. Los archivos sin paridad (1+0 o importados) que solo estaban en esta cuenta desaparecen del disco.')}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <ErrorText error={remove.error} />
          <AlertDialogFooter>
            <AlertDialogCancel>{t('Cancelar')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault()
                if (removing) remove.mutate(removing)
              }}
            >
              {t('Quitar')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}

const providerInfo: Record<string, { name: string; hint: string; icon: LucideIcon }> = {
  mega: { name: 'MEGA', hint: msg('Correo + contraseña'), icon: Cloud },
  fake: { name: 'Fake', hint: msg('Solo desarrollo · contraseña «ok»'), icon: FlaskConical },
}
const comingSoon: [string, LucideIcon][] = [
  ['Google Drive', HardDrive],
  ['Dropbox', Package],
  ['OneDrive', Cloud],
  ['S3 / B2 / R2', Database],
]

function AddAccountDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const providers = useQuery(providersQuery)
  const available = providers.data ?? ['mega']
  const [provider, setProvider] = useState('mega')

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg md:max-w-3xl lg:max-w-4xl">
        <DialogHeader>
          <DialogTitle className="text-xl">{t('Añadir cuenta al pool')}</DialogTitle>
          <DialogDescription>{t('Suma su espacio al disco')}</DialogDescription>
        </DialogHeader>
        <ConnectForm
          provider={provider}
          onDone={onClose}
          onCancel={onClose}
          providerPicker={
            <div className="grid grid-cols-1 gap-2.5">
              <div role="radiogroup" aria-label={t('Proveedor')} className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                {available.map((p) => {
                  const on = p === provider
                  const info = providerInfo[p]
                  const Icon = info?.icon ?? Cloud
                  return (
                    <button
                      key={p}
                      type="button"
                      role="radio"
                      aria-checked={on}
                      onClick={() => setProvider(p)}
                      className={cn(
                        'flex min-w-0 items-center gap-3 rounded-xl p-3 text-left transition-colors',
                        on ? 'bg-accent/60 ring-2 ring-primary' : 'border hover:bg-muted',
                      )}
                    >
                      <span
                        className={cn(
                          'flex size-9 shrink-0 items-center justify-center rounded-lg',
                          on ? 'bg-primary text-primary-foreground' : 'bg-secondary text-secondary-foreground',
                        )}
                      >
                        <Icon className="size-4.5" aria-hidden />
                      </span>
                      <span className="grid min-w-0 flex-1 gap-0.5">
                        <span className="truncate text-sm font-semibold">{info?.name ?? p.toUpperCase()}</span>
                        <span className="truncate text-xs text-muted-foreground">{info ? t(info.hint) : ''}</span>
                      </span>
                      {on && <CircleCheck className="size-4 shrink-0 text-primary" aria-hidden />}
                    </button>
                  )
                })}
              </div>
              <p className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                <span>{t('Próximamente')}:</span>
                {comingSoon.map(([name, Icon]) => (
                  <span key={name} className="flex items-center gap-1 rounded-md border border-dashed px-2 py-0.5">
                    <Icon className="size-3.5" aria-hidden />
                    {name}
                  </span>
                ))}
              </p>
            </div>
          }
        />
      </DialogContent>
    </Dialog>
  )
}

const helpRows = [
  [msg('Sin protección'), 'JBOD', '1', msg('0 caídas'), msg('100% siempre')],
  [msg('Copia doble'), 'RAID 1', '2', msg('1 caída'), msg('50% siempre')],
  [msg('Copia triple'), 'RAID 1 ×3', '3', msg('2 caídas'), msg('33% siempre')],
  [msg('Paridad simple'), 'RAID 5', '3', msg('1 caída'), '3 → 67% · 4 → 75% · 5 → 80% · 7 → 86%'],
  [msg('Paridad doble'), 'RAID 6', '4', msg('2 caídas'), '4 → 50% · 5 → 60% · 6 → 67% · 8 → 75%'],
]

function HelpDialog({ open, onClose, accounts }: { open: boolean; onClose: () => void; accounts: number }) {
  const usable = helpRows.filter((r) => Number(r[2]) <= accounts).length
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="text-xl">{t('¿Cuántas cuentas necesita cada protección?')}</DialogTitle>
          <DialogDescription>{t('La meta es recuperar datos si una cuenta falla, no ganar velocidad.')}</DialogDescription>
        </DialogHeader>
        <div className="overflow-x-auto rounded-xl border">
          <table className="w-full min-w-[520px] border-collapse text-sm">
            <thead>
              <tr className="text-left text-xs tracking-wider text-muted-foreground uppercase">
                <th className="border-b px-4 py-3 font-semibold">{t('Opción')}</th>
                <th className="border-b px-3 py-3 font-semibold">{t('Mínimo')}</th>
                <th className="border-b px-3 py-3 font-semibold">{t('Aguanta')}</th>
                <th className="border-b px-3 py-3 font-semibold">{t('Espacio útil según cuentas')}</th>
              </tr>
            </thead>
            <tbody>
              {helpRows.map(([name, raid, min, tol, eff]) => (
                <tr key={name} className="border-b border-secondary last:border-0">
                  <td className="px-4 py-2.5">
                    <span className="grid grid-cols-1">
                      <span className="font-semibold">{t(name)}</span>
                      <span className="text-xs text-muted-foreground">≈ {raid}</span>
                    </span>
                  </td>
                  <td className="px-3 py-2.5">{min}</td>
                  <td className="px-3 py-2.5">{t(tol)}</td>
                  <td className="px-3 py-2.5 text-secondary-foreground">{t(eff)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <ul className="grid grid-cols-1 list-disc gap-2 pl-5 text-sm text-secondary-foreground">
          <li>{t('Por debajo del mínimo la paridad no tiene sentido: paridad simple con 2 cuentas es una copia doble.')}</li>
          <li>
            <strong className="text-foreground">{t('Las copias no mejoran con más cuentas; la paridad sí.')}</strong>{' '}
            {t('Por eso la paridad conviene a partir de 3 o 4 cuentas.')}
          </li>
          <li>
            <strong className="text-foreground">{t('Ten siempre una cuenta de sobra.')}</strong>{' '}
            {t('Si una cae y no hay otra libre donde reconstruir, los datos siguen legibles pero el pool queda degradado hasta que añadas otra.')}
          </li>
          <li>
            <strong className="text-foreground">{t('Hay un ancho máximo:')}</strong>{' '}
            {t('unos 6+1 para paridad simple y 10+2 para doble. Con más pedazos por franja, reparar es más lento y crece el riesgo de que fallen dos cuentas a la vez.')}
          </li>
        </ul>
        <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-4 text-sm">
          <span>
            {accounts === 1
              ? t('Tienes 1 cuenta activa: puedes usar {n} de las cinco opciones.', { n: usable })
              : t('Tienes {cuentas} cuentas activas: puedes usar {n} de las cinco opciones.', { cuentas: accounts, n: usable })}
          </span>
          <Button onClick={onClose}>{t('Entendido')}</Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}

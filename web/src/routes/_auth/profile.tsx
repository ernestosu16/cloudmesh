import { useState, type FormEvent } from 'react'
import { createFileRoute, Link, useRouter } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient, useSuspenseQuery } from '@tanstack/react-query'
import { cn } from 'cn'
import { Plus } from 'lucide-react'
import {
  changePassword,
  logoutOthers,
  meQuery,
  quotaQuery,
  sessionsQuery,
  settingsQuery,
  updateMe,
  updateSettings,
  type Account,
  type RoutingPolicy,
} from '@/lib/api'
import { displayName, formatBytes, initials, percent, shortDate } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { ReconnectDialog } from '@/features/accounts/CredentialsDialog'
import { SettingsDialog } from '@/features/accounts/SettingsDialog'
import { accountCodes, accountColors } from '@/features/accounts/codes'
import { contributionLabel } from '@/features/accounts/Contribution'
import { Avatar } from '@/features/shell/Logo'
import { DisableTOTPDialog, EnableTOTPDialog, RecoveryCodesDialog } from '@/features/auth/TwoFactorDialogs'
import { ErrorText, Panel, Tag } from '@/features/shell/Page'
import { can, roleHints, roleLabels } from '@/features/auth/roles'
import { msg, t } from '@/lib/i18n'
import { LanguageSelect } from '@/features/shell/LanguageSelect'
import { ThemeSelect } from '@/features/shell/ThemeSelect'

export const Route = createFileRoute('/_auth/profile')({
  // Cuentas y ajustes del pool solo los ve un administrador.
  loader: ({ context }) =>
    can(context.user, 'admin')
      ? Promise.all([context.queryClient.ensureQueryData(quotaQuery), context.queryClient.ensureQueryData(settingsQuery)])
      : undefined,
  component: ProfilePage,
})

const policies: { value: RoutingPolicy; label: string }[] = [
  { value: 'most_free', label: msg('Cuenta con más espacio libre') },
  { value: 'round_robin', label: msg('Repartir por igual') },
  { value: 'priority', label: msg('Llenar en orden de prioridad') },
]

function ProfilePage() {
  const ctx = Route.useRouteContext()
  // /me se invalida tras subir: el uso propio sale de ahí y no del contexto de la ruta.
  const user = useQuery(meQuery).data ?? ctx.user
  const queryClient = useQueryClient()
  const sessions = useQuery(sessionsQuery)
  const [changing, setChanging] = useState(false)
  const [totp, setTotp] = useState<'enable' | 'disable' | 'recovery' | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const router = useRouter()
  const afterTotp = async (text: string) => {
    setTotp(null)
    setNotice(text)
    // El usuario de la sesión llega por el contexto de _auth: se vuelve a pedir y se recarga la ruta.
    await queryClient.fetchQuery({ ...meQuery, staleTime: 0 })
    void queryClient.invalidateQueries({ queryKey: sessionsQuery.queryKey })
    await router.invalidate()
  }
  const closeOthers = useMutation({
    mutationFn: logoutOthers,
    onSuccess: () => {
      setNotice(t('Se cerraron las demás sesiones.'))
      return queryClient.invalidateQueries({ queryKey: sessionsQuery.queryKey })
    },
  })
  const others = (sessions.data?.active ?? 1) - 1

  return (
    <section className="grid max-w-5xl grid-cols-1 gap-6">
      <Panel className="flex flex-wrap items-center gap-5">
        <Avatar text={initials(displayName(user))} className="size-20 font-heading text-3xl font-bold" />
        <div className="grid grid-cols-1 min-w-0 flex-[1_1_15rem] gap-1">
          <h1 className="text-[clamp(22px,6vw,28px)] font-bold break-all">{displayName(user)}</h1>
          <span className="text-secondary-foreground">
            {user.name && <span className="break-all">{user.email} · </span>}
            {user.name ? t('miembro desde {fecha}', { fecha: shortDate(user.created_at) }) : t('Miembro desde {fecha}', { fecha: shortDate(user.created_at) })}
          </span>
          <span className="mt-1 flex flex-wrap items-center gap-2 text-[13px] text-muted-foreground">
            <Tag tone="accent">{roleLabels[user.role]}</Tag>
            {roleHints[user.role]}
          </span>
        </div>
        <div className="grid min-w-0 flex-[1_1_14rem] gap-2">
          <div className="flex flex-wrap items-baseline justify-between gap-x-2">
            <span className="text-[13px] font-semibold">{t('Tu espacio')}</span>
            <span className="font-mono text-xs text-secondary-foreground">
              {formatBytes(user.used_bytes)} / {user.quota_bytes > 0 ? formatBytes(user.quota_bytes) : t('sin límite')}
            </span>
          </div>
          {user.quota_bytes > 0 && (
            <div className="h-2 overflow-hidden rounded-full bg-border">
              <div className="h-full bg-primary" style={{ width: `${percent(user.used_bytes, user.quota_bytes)}%` }} />
            </div>
          )}
          <span className="text-xs text-muted-foreground">{t('Lo que has subido, incluida la papelera.')}</span>
        </div>
      </Panel>

      <div className="flex flex-wrap items-start gap-6">
      <PersonalData name={user.name} email={user.email} />

      <Panel className="grid min-w-0 flex-[1_1_22rem] grid-cols-1 gap-1">
        <h2 className="mb-2 text-xl font-bold">{t('Seguridad')}</h2>
        <SecurityRow title={t('Contraseña')} hint={t('Al cambiarla se cierran tus otras sesiones')}>
          <Button variant="outline" onClick={() => setChanging(true)}>
            {t('Cambiar')}
          </Button>
        </SecurityRow>
        <SecurityRow
          title={t('Verificación en dos pasos')}
          hint={user.totp_enabled ? t('Activa · app de autenticación') : t('Desactivada: solo se pide la contraseña')}
          ok={user.totp_enabled}
        >
          {user.totp_enabled ? (
            <Button variant="ghost" className="text-destructive hover:text-destructive" onClick={() => setTotp('disable')}>
              {t('Desactivar')}
            </Button>
          ) : (
            <Button variant="outline" onClick={() => setTotp('enable')}>
              {t('Activar')}
            </Button>
          )}
        </SecurityRow>
        {user.totp_enabled && (
          <SecurityRow
            title={t('Códigos de recuperación')}
            hint={
              user.recovery_codes_left > 0
                ? t('{n} sin usar · sirven para entrar si pierdes el móvil', { n: user.recovery_codes_left })
                : t('No te queda ninguno: genera nuevos')
            }
            ok={user.recovery_codes_left > 2}
          >
            <Button variant="outline" onClick={() => setTotp('recovery')}>
              {t('Generar nuevos')}
            </Button>
          </SecurityRow>
        )}
        <SecurityRow
          title={t('Sesiones activas')}
          hint={
            sessions.data
              ? sessions.data.active === 1
                ? t('1 dispositivo, incluido este')
                : t('{n} dispositivos, incluido este', { n: sessions.data.active })
              : t('Cargando…')
          }
        >
          <Button
            variant="ghost"
            className="text-destructive hover:text-destructive"
            disabled={others <= 0 || closeOthers.isPending}
            onClick={() => closeOthers.mutate()}
          >
            {t('Cerrar las demás')}
          </Button>
        </SecurityRow>
        {notice && <p className="pt-2 text-sm text-success">{notice}</p>}
        <ErrorText error={closeOthers.error} />
      </Panel>
      </div>

      {can(user, 'admin') && <StorageAccounts />}

      <EnableTOTPDialog
        open={totp === 'enable'}
        onClose={() => setTotp(null)}
        onEnabled={() => void afterTotp(t('Verificación en dos pasos activada. Se cerraron tus otras sesiones.'))}
      />
      <RecoveryCodesDialog
        open={totp === 'recovery'}
        onClose={() => setTotp(null)}
        onDone={() => void afterTotp(t('Códigos de recuperación nuevos generados. Los anteriores ya no valen.'))}
      />
      <DisableTOTPDialog
        open={totp === 'disable'}
        onClose={() => setTotp(null)}
        onDisabled={() => void afterTotp(t('Verificación en dos pasos desactivada.'))}
      />
      <PasswordDialog
        open={changing}
        onClose={() => setChanging(false)}
        onChanged={() => {
          setChanging(false)
          setNotice(t('Contraseña cambiada. Tus otras sesiones se cerraron.'))
          void queryClient.invalidateQueries({ queryKey: sessionsQuery.queryKey })
        }}
      />
    </section>
  )
}

function PersonalData({ name: initial, email }: { name: string; email: string }) {
  const queryClient = useQueryClient()
  const router = useRouter()
  const [name, setName] = useState(initial)
  const save = useMutation({
    mutationFn: () => updateMe(name),
    onSuccess: async (u) => {
      setName(u.name)
      // /me trae datos que esta respuesta no lleva (uso, códigos de recuperación):
      // se vuelve a pedir y se recarga el contexto de _auth.
      await queryClient.fetchQuery({ ...meQuery, staleTime: 0 })
      return router.invalidate()
    },
  })
  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    save.mutate()
  }
  return (
    <Panel className="min-w-0 flex-[1_1_22rem]">
      <form onSubmit={onSubmit} className="grid grid-cols-1 gap-4">
        <h2 className="text-xl font-bold">{t('Datos personales')}</h2>
        <div className="grid gap-1.5">
          <Label htmlFor="me-name">{t('Nombre')}</Label>
          <Input id="me-name" maxLength={80} placeholder={t('Cómo te ven los demás')} value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="me-email">{t('Correo')}</Label>
          <Input id="me-email" type="email" value={email} readOnly className="bg-background text-secondary-foreground" />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="me-lang">{t('Idioma')}</Label>
          <LanguageSelect id="me-lang" />
          <span className="text-xs text-muted-foreground">{t('Se guarda en este navegador.')}</span>
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="me-theme">{t('Tema')}</Label>
          <ThemeSelect id="me-theme" />
          <span className="text-xs text-muted-foreground">{t('Se guarda en este navegador.')}</span>
        </div>
        <ErrorText error={save.error} />
        <div className="flex flex-wrap items-center gap-3">
          <Button type="submit" disabled={save.isPending || name.trim() === initial}>
            {t('Guardar cambios')}
          </Button>
          {save.isSuccess && name.trim() === initial && <span className="text-sm text-success">{t('Guardado')}</span>}
        </div>
      </form>
    </Panel>
  )
}

function SecurityRow({ title, hint, ok, children }: { title: string; hint: string; ok?: boolean; children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-3 border-t py-3.5">
      <span className="grid grid-cols-1 flex-[1_1_12rem] gap-0.5">
        <span className="text-sm font-semibold">{title}</span>
        <span className={cn('text-[13px]', ok ? 'text-success' : 'text-muted-foreground')}>{hint}</span>
      </span>
      {children}
    </div>
  )
}

function StorageAccounts() {
  const queryClient = useQueryClient()
  const { data: quota } = useSuspenseQuery(quotaQuery)
  const { data: settings } = useSuspenseQuery(settingsQuery)
  const [editing, setEditing] = useState<Account | null>(null)
  const [reconnecting, setReconnecting] = useState<Account | null>(null)
  const codes = accountCodes(quota.accounts)
  const policy = useMutation({
    mutationFn: (routing_policy: RoutingPolicy) => updateSettings({ ...settings, routing_policy }),
    onSuccess: (s) => queryClient.setQueryData(settingsQuery.queryKey, s),
  })

  return (
    <Panel className="grid grid-cols-1 gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="grid grid-cols-1 gap-1">
          <h2 className="text-xl font-bold">{t('Cuentas de almacenamiento')}</h2>
          <span className="text-sm text-secondary-foreground">
            {t('Todas se suman en un único disco de {total}. Nadie ve en qué cuenta está cada archivo.', { total: formatBytes(quota.total) })}
          </span>
        </div>
        <div className="grid grid-cols-1 gap-1.5">
          <Label htmlFor="routing">{t('Distribuir archivos nuevos')}</Label>
          <Select value={settings.routing_policy} onValueChange={(v) => policy.mutate(v as RoutingPolicy)} disabled={policy.isPending}>
            <SelectTrigger id="routing" className="w-full min-w-64">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {policies.map((p) => (
                <SelectItem key={p.value} value={p.value}>
                  {t(p.label)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>
      <ErrorText error={policy.error} />

      <div className="flex h-3 overflow-hidden rounded-full bg-secondary" aria-hidden>
        {quota.accounts.map((a, i) => (
          <span key={a.id} className={accountColors[i % accountColors.length]} style={{ width: `${percent(a.occupied, quota.total)}%` }} />
        ))}
      </div>

      <div className="grid grid-cols-[repeat(auto-fill,minmax(240px,1fr))] gap-3.5">
        {quota.accounts.map((a, i) => {
          const reconnect = a.status === 'reconnect_required'
          return (
            <article key={a.id} className="grid grid-cols-1 gap-3 rounded-xl border p-4">
              <div className="flex items-center gap-2.5">
                <span
                  className={cn(
                    'flex size-9 shrink-0 items-center justify-center rounded-lg font-mono text-xs font-bold text-white',
                    accountColors[i % accountColors.length],
                  )}
                >
                  {codes.get(a.id)}
                </span>
                <span className="grid grid-cols-1 min-w-0">
                  <span className="truncate font-semibold">{a.label}</span>
                  <span className="truncate text-xs text-muted-foreground">{a.email}</span>
                </span>
              </div>
              <span className={cn('text-[13px] font-medium', reconnect ? 'text-warning' : 'text-success')}>
                {t('{hecho} de {total}', { hecho: formatBytes(a.occupied), total: formatBytes(a.capacity) })} · {reconnect ? t('sesión caducada') : a.enabled ? t('en línea') : t('sin subidas')}
              </span>
              {a.contrib_mode !== 'all' && (
                <span className="-mt-2 text-xs text-muted-foreground">
                  {t('Aporte: {aporte} · cuenta de {total}', { aporte: contributionLabel(a), total: formatBytes(a.quota_total) })}
                </span>
              )}
              <Button variant="outline" onClick={() => (reconnect ? setReconnecting(a) : setEditing(a))}>
                {reconnect ? t('Reconectar') : t('Configurar')}
              </Button>
            </article>
          )
        })}
        <Link
          to="/storage"
          hash="add"
          className="grid grid-cols-1 content-between gap-3 rounded-xl border border-dashed border-input bg-background p-4 hover:border-primary"
        >
          <span className="flex items-center gap-2.5">
            <span className="flex size-9 items-center justify-center rounded-lg bg-secondary-foreground text-white">
              <Plus className="size-4" aria-hidden />
            </span>
            <span className="grid grid-cols-1">
              <span className="font-semibold">{t('Añadir cuenta al pool')}</span>
              <span className="text-xs text-muted-foreground">{t('Suma su espacio al disco')}</span>
            </span>
          </span>
          <span className="text-sm font-semibold text-primary">{t('Conectar')}</span>
        </Link>
      </div>

      <SettingsDialog account={editing} onClose={() => setEditing(null)} />
      <ReconnectDialog account={reconnecting} onClose={() => setReconnecting(null)} />
    </Panel>
  )
}

function PasswordDialog({ open, onClose, onChanged }: { open: boolean; onClose: () => void; onChanged: () => void }) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [repeat, setRepeat] = useState('')
  const change = useMutation({
    mutationFn: () => changePassword(current, next),
    onSuccess: () => {
      setCurrent('')
      setNext('')
      setRepeat('')
      onChanged()
    },
  })
  const mismatch = repeat !== '' && next !== repeat
  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (!mismatch) change.mutate()
  }
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t('Cambiar contraseña')}</DialogTitle>
          <DialogDescription>{t('Mínimo 10 caracteres. Las demás sesiones se cerrarán.')}</DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="grid grid-cols-1 gap-3.5">
          <div className="grid grid-cols-1 gap-1.5">
            <Label htmlFor="pw-current">{t('Contraseña actual')}</Label>
            <Input id="pw-current" type="password" autoComplete="current-password" required value={current} onChange={(e) => setCurrent(e.target.value)} />
          </div>
          <div className="grid grid-cols-1 gap-1.5">
            <Label htmlFor="pw-new">{t('Nueva contraseña')}</Label>
            <Input id="pw-new" type="password" autoComplete="new-password" required minLength={10} value={next} onChange={(e) => setNext(e.target.value)} />
          </div>
          <div className="grid grid-cols-1 gap-1.5">
            <Label htmlFor="pw-repeat">{t('Repite la nueva')}</Label>
            <Input
              id="pw-repeat"
              type="password"
              autoComplete="new-password"
              required
              aria-invalid={mismatch}
              value={repeat}
              onChange={(e) => setRepeat(e.target.value)}
            />
            {mismatch && <p className="text-xs text-destructive">{t('No coincide.')}</p>}
          </div>
          <ErrorText error={change.error} />
          <div className="flex justify-end gap-2.5">
            <Button type="button" variant="outline" onClick={onClose}>
              {t('Cancelar')}
            </Button>
            <Button type="submit" disabled={change.isPending || mismatch}>
              {t('Guardar')}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}

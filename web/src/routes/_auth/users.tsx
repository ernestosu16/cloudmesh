import { useState, type FormEvent } from 'react'
import { createFileRoute, useRouter } from '@tanstack/react-router'
import { useMutation, useQueryClient, useSuspenseQuery } from '@tanstack/react-query'
import { Mail, Pencil, ShieldCheck, ShieldOff, Trash2, X } from 'lucide-react'
import {
  createInvitation,
  createUser,
  deleteUser,
  invitationsQuery,
  meQuery,
  resetUserTotp,
  revokeInvitation,
  updateUserAccess,
  usersQuery,
  type Invitation,
  type NewInvitation,
  type Role,
  type User,
} from '@/lib/api'
import { displayName, formatBytes, initials, percent, relativeTime, shortDate } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
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
import { Avatar } from '@/features/shell/Logo'
import { Chip, ErrorText, PageHeader, Panel, Tag } from '@/features/shell/Page'
import { CopyField } from '@/features/s3/S3Dialogs'
import { requireRole, roleHints, roleLabels } from '@/features/auth/roles'
import { t } from '@/lib/i18n'

export const Route = createFileRoute('/_auth/users')({
  beforeLoad: requireRole('admin'),
  loader: ({ context }) =>
    Promise.all([context.queryClient.ensureQueryData(usersQuery), context.queryClient.ensureQueryData(invitationsQuery)]),
  component: UsersPage,
})

const avatarColors = ['bg-accent-foreground', 'bg-chart-5', 'bg-success', 'bg-warning', 'bg-chart-3', 'bg-secondary-foreground']

const roles: Role[] = ['admin', 'editor', 'reader']

const GiB = 1024 ** 3
/** Cuota en el formulario: GB con decimales; vacío o 0 es sin límite. */
const toBytes = (gb: string) => (gb.trim() === '' ? 0 : Math.round(Number(gb) * GiB))
const toGB = (bytes: number) => (bytes > 0 ? String(Math.round((bytes / GiB) * 100) / 100) : '')
const quotaOk = (gb: string) => gb.trim() === '' || (Number.isFinite(Number(gb)) && Number(gb) >= 0)

function RoleSelect({ id, value, onChange }: { id: string; value: Role; onChange: (r: Role) => void }) {
  return (
    <Select value={value} onValueChange={(v) => onChange(v as Role)}>
      <SelectTrigger id={id} className="w-full">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {roles.map((r) => (
          <SelectItem key={r} value={r}>
            {roleLabels[r]}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function QuotaInput({ id, value, onChange }: { id: string; value: string; onChange: (v: string) => void }) {
  return (
    <div className="relative">
      <Input
        id={id}
        type="number"
        inputMode="decimal"
        min={0}
        step="any"
        placeholder={t('Sin límite')}
        className="pr-12"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        aria-invalid={!quotaOk(value)}
      />
      <span className="pointer-events-none absolute top-1/2 right-3 -translate-y-1/2 text-sm text-muted-foreground">GB</span>
    </div>
  )
}

/** Uso frente a cuota: barra solo si hay límite. */
function Usage({ user, className }: { user: User; className?: string }) {
  const over = user.quota_bytes > 0 && user.used_bytes >= user.quota_bytes
  return (
    <span className={className ?? 'grid min-w-0 gap-1'}>
      <span className={over ? 'font-mono text-xs text-destructive' : 'font-mono text-xs text-secondary-foreground'}>
        {formatBytes(user.used_bytes)} / {user.quota_bytes > 0 ? formatBytes(user.quota_bytes) : t('sin límite')}
      </span>
      {user.quota_bytes > 0 && (
        <span className="block h-1.5 w-full max-w-40 overflow-hidden rounded-full bg-border">
          <span className={over ? 'block h-full bg-destructive' : 'block h-full bg-primary'} style={{ width: `${percent(user.used_bytes, user.quota_bytes)}%` }} />
        </span>
      )}
    </span>
  )
}

function UsersPage() {
  const { user: me } = Route.useRouteContext()
  const { data: users } = useSuspenseQuery(usersQuery)
  const queryClient = useQueryClient()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState<Role>('editor')
  const [quota, setQuota] = useState('')
  const [filter, setFilter] = useState<Role | 'all'>('all')
  const [editing, setEditing] = useState<User | null>(null)
  const [deleting, setDeleting] = useState<User | null>(null)
  const [resetting, setResetting] = useState<User | null>(null)

  const { data: inv } = useSuspenseQuery(invitationsQuery)
  const [mode, setMode] = useState<'invite' | 'password'>('invite')
  const [invited, setInvited] = useState<NewInvitation | null>(null)
  const create = useMutation({
    mutationFn: async () => {
      if (mode === 'invite') {
        setInvited(await createInvitation(email, role, toBytes(quota)))
        return
      }
      await createUser(email, password, role, toBytes(quota))
    },
    onSuccess: () => {
      setEmail('')
      setPassword('')
      setQuota('')
      return queryClient.invalidateQueries({ queryKey: ['users'] })
    },
  })
  const revoke = useMutation({
    mutationFn: (i: Invitation) => revokeInvitation(i.id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: invitationsQuery.queryKey }),
  })
  const reset = useMutation({
    mutationFn: (u: User) => resetUserTotp(u.id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['users'] }).then(() => setResetting(null)),
  })
  const mfa = (u: User) =>
    u.totp_enabled ? (
      <Tag tone="success">
        <ShieldCheck className="size-3.5" aria-hidden /> 2FA
      </Tag>
    ) : (
      <Tag>
        <ShieldOff className="size-3.5" aria-hidden /> {t('Sin 2FA')}
      </Tag>
    )
  const remove = useMutation({
    mutationFn: (u: User) => deleteUser(u.id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['users'] }).then(() => setDeleting(null)),
  })

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    create.mutate()
  }
  const count = (r: Role) => users.filter((u) => u.role === r).length
  const shown = filter === 'all' ? users : users.filter((u) => u.role === filter)
  const edit = (u: User) =>
    u.id === me.id && u.role === 'admin' && count('admin') === 1 ? undefined : (
      <Button variant="ghost" size="icon" aria-label={t('Cambiar rol y cuota de {email}', { email: u.email })} onClick={() => setEditing(u)}>
        <Pencil />
      </Button>
    )

  return (
    <section className="grid grid-cols-1 gap-6">
      <PageHeader title={t('Usuarios y acceso')} description={t('Quién puede entrar a CloudMesh, qué puede hacer y cuánto puede subir.')} />

      <Panel className="grid grid-cols-1 gap-3 p-4 sm:p-5">
        <div role="group" aria-label={t('Cómo dar de alta')} className="flex flex-wrap gap-1.5">
          <Chip active={mode === 'invite'} onClick={() => setMode('invite')}>
            {t('Invitar por enlace')}
          </Chip>
          <Chip active={mode === 'password'} onClick={() => setMode('password')}>
            {t('Crear con contraseña')}
          </Chip>
        </div>
        <form onSubmit={onSubmit} className="flex flex-wrap items-end gap-3">
          <div className="grid grid-cols-1 min-w-0 flex-[3_1_16rem] gap-1.5">
            <Label htmlFor="new-email">{t('Correo electrónico')}</Label>
            <Input id="new-email" type="email" required placeholder={t('nombre@empresa.com')} value={email} onChange={(e) => setEmail(e.target.value)} />
          </div>
          {mode === 'password' && (
          <div className="grid grid-cols-1 min-w-0 flex-[2_1_12rem] gap-1.5">
            <Label htmlFor="new-password">{t('Contraseña inicial')}</Label>
            <Input
              id="new-password"
              type="password"
              autoComplete="new-password"
              required
              minLength={10}
              placeholder={t('Mínimo 10 caracteres')}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>
          )}
          <div className="grid min-w-0 flex-[1_1_10rem] grid-cols-1 gap-1.5">
            <Label htmlFor="new-role">{t('Rol')}</Label>
            <RoleSelect id="new-role" value={role} onChange={setRole} />
          </div>
          <div className="grid min-w-0 flex-[1_1_8rem] grid-cols-1 gap-1.5">
            <Label htmlFor="new-quota">{t('Cuota')}</Label>
            <QuotaInput id="new-quota" value={quota} onChange={setQuota} />
          </div>
          <Button type="submit" size="lg" disabled={create.isPending || !quotaOk(quota)} className="flex-none">
            {mode === 'invite' ? (
              <>
                <Mail /> {t('Invitar')}
              </>
            ) : (
              t('Crear usuario')
            )}
          </Button>
        </form>
        <div className="grid gap-1">
          <ErrorText error={create.error} />
          {create.isSuccess && mode === 'password' && (
            <p className="text-sm text-success">{t('Usuario creado. Pásale la contraseña por un canal seguro; podrá cambiarla en su perfil.')}</p>
          )}
          <p className="text-[13px] text-muted-foreground">
            {mode === 'invite'
              ? inv.mail_enabled
                ? t('Le llega un correo con un enlace de un solo uso para elegir su contraseña; caduca en 7 días.')
                : t('Obtienes un enlace de un solo uso para pasárselo; caduca en 7 días. Configura CLOUDMESH_SMTP_URL para mandarlo por correo.')
              : t('Tú eliges la contraseña inicial y se la pasas.')}
          </p>
        </div>
      </Panel>

      {inv.invitations.length > 0 && (
        <section className="grid grid-cols-1 gap-2.5" aria-label={t('Invitaciones pendientes')}>
          <h2 className="text-lg font-bold">{t('Invitaciones pendientes')}</h2>
          <ul className="grid grid-cols-1 gap-2.5 md:grid-cols-2">
            {inv.invitations.map((i) => (
              <li key={i.id} className="flex items-center gap-3 rounded-2xl border border-dashed bg-card p-4">
                <span className="flex size-10 shrink-0 items-center justify-center rounded-full bg-accent text-primary">
                  <Mail className="size-4" aria-hidden />
                </span>
                <span className="grid min-w-0 flex-1 gap-1">
                  <span className="truncate font-semibold">{i.email}</span>
                  <span className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                    <Tag>{roleLabels[i.role]}</Tag>
                    {i.quota_bytes > 0 && <span>{formatBytes(i.quota_bytes)}</span>}
                    <span>{t('caduca {cuando}', { cuando: relativeTime(i.expires_at) })}</span>
                  </span>
                </span>
                <Button
                  variant="ghost"
                  size="icon"
                  className="text-destructive hover:text-destructive"
                  aria-label={t('Anular la invitación de {email}', { email: i.email })}
                  disabled={revoke.isPending}
                  onClick={() => revoke.mutate(i)}
                >
                  <X />
                </Button>
              </li>
            ))}
          </ul>
          <ErrorText error={revoke.error} />
        </section>
      )}

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        {roles.map((r) => (
          <div key={r} className="grid gap-1 rounded-2xl border bg-card p-4">
            <span className="flex items-center justify-between gap-2">
              <span className="font-semibold">{roleLabels[r]}</span>
              <span className="font-mono text-sm text-muted-foreground">{count(r)}</span>
            </span>
            <span className="text-[13px] text-muted-foreground">{roleHints[r]}</span>
          </div>
        ))}
      </div>

      <div role="group" aria-label={t('Filtrar por rol')} className="flex min-w-0 gap-1.5 overflow-x-auto [scrollbar-width:none]">
        <Chip active={filter === 'all'} onClick={() => setFilter('all')}>
          {t('Todos')} · {users.length}
        </Chip>
        {roles.map((r) => (
          <Chip key={r} active={filter === r} onClick={() => setFilter(r)}>
            {roleLabels[r]} · {count(r)}
          </Chip>
        ))}
      </div>

      <ul className="grid grid-cols-1 gap-2.5 md:hidden">
        {shown.map((u, i) => (
          <li key={u.id} className="flex flex-wrap items-center gap-3 rounded-2xl border bg-card p-4">
            <Avatar text={initials(displayName(u))} className={avatarColors[i % avatarColors.length]} />
            <span className="grid min-w-0 flex-[1_1_10rem] grid-cols-1 gap-1">
              <span className="truncate font-semibold">{displayName(u)}</span>
              {u.name && <span className="truncate text-[13px] text-muted-foreground">{u.email}</span>}
              <span className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <Tag tone={u.role === 'admin' ? 'accent' : 'neutral'}>{roleLabels[u.role]}</Tag>
                {u.id === me.id && <Tag tone="success">{t('Tú')}</Tag>}
                {mfa(u)}
              </span>
              <Usage user={u} />
            </span>
            {/* Con tres acciones bajan a su propia fila en lugar de estrechar el nombre. */}
            <span className="ml-auto flex shrink-0 items-center">
              {edit(u)}
              {u.id !== me.id && u.totp_enabled && (
                <Button variant="ghost" size="icon" aria-label={t('Quitar la verificación en dos pasos de {email}', { email: u.email })} onClick={() => setResetting(u)}>
                  <ShieldOff />
                </Button>
              )}
              {u.id !== me.id && (
                <Button variant="ghost" size="icon" className="text-destructive hover:text-destructive" aria-label={t('Eliminar {email}', { email: u.email })} onClick={() => setDeleting(u)}>
                  <Trash2 />
                </Button>
              )}
            </span>
          </li>
        ))}
      </ul>

      <div className="hidden overflow-x-auto rounded-2xl border bg-card md:block">
        <table className="w-full min-w-[760px] border-collapse text-sm">
          <thead>
            <tr className="text-left text-xs tracking-wider text-muted-foreground uppercase">
              <th className="border-b px-5 py-3.5 font-semibold">{t('Usuario')}</th>
              <th className="border-b px-3 py-3.5 font-semibold">{t('Rol')}</th>
              <th className="border-b px-3 py-3.5 font-semibold">{t('Espacio')}</th>
              <th className="border-b px-3 py-3.5 font-semibold">{t('Alta')}</th>
              <th className="border-b px-3 py-3.5 font-semibold">2FA</th>
              <th className="border-b px-5 py-3.5">
                <span className="sr-only">{t('Acciones')}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {shown.map((u, i) => (
              <tr key={u.id} className="border-b border-secondary last:border-0">
                <td className="px-5 py-3">
                  <span className="flex items-center gap-3">
                    <Avatar text={initials(displayName(u))} className={avatarColors[i % avatarColors.length]} />
                    <span className="grid min-w-0">
                      <span className="truncate font-semibold">{displayName(u)}</span>
                      {u.name && <span className="truncate text-[13px] text-muted-foreground">{u.email}</span>}
                    </span>
                    {u.id === me.id && <Tag tone="success">{t('Tú')}</Tag>}
                  </span>
                </td>
                <td className="px-3 py-3">
                  <Tag tone={u.role === 'admin' ? 'accent' : 'neutral'}>{roleLabels[u.role]}</Tag>
                </td>
                <td className="px-3 py-3">
                  <Usage user={u} />
                </td>
                <td className="px-3 py-3 text-secondary-foreground">{shortDate(u.created_at)}</td>
                <td className="px-3 py-3">{mfa(u)}</td>
                <td className="px-5 py-2 text-right whitespace-nowrap">
                  {edit(u)}
                  {u.id !== me.id && u.totp_enabled && (
                    <Button variant="ghost" size="sm" onClick={() => setResetting(u)}>
                      <ShieldOff /> {t('Quitar 2FA')}
                    </Button>
                  )}
                  {u.id !== me.id && (
                    <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={() => setDeleting(u)}>
                      <Trash2 /> {t('Eliminar')}
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <p className="text-[13px] text-muted-foreground">
        {t('Todos ven el mismo disco; el rol decide qué pueden cambiar. La cuota limita lo que sube cada uno (papelera incluida; por S3 cuenta para quien creó la llave). Los archivos anteriores a las cuotas no son de nadie. Desde la terminal,')}{' '}
        <code className="rounded bg-secondary px-1 font-mono">{'cloudmesh create-user'}</code> {t('crea administradores.')}
      </p>

      <Dialog open={invited !== null} onOpenChange={(o) => !o && setInvited(null)}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{t('Invitación lista')}</DialogTitle>
            <DialogDescription className="break-all">
              {invited?.emailed
                ? t('Le enviamos un correo a {email}.', { email: invited.invitation.email })
                : t('Para {email}.', { email: invited?.invitation.email ?? '' })}
            </DialogDescription>
          </DialogHeader>
          {invited?.email_error && <p className="rounded-xl bg-warning-soft px-3.5 py-2.5 text-[13px] text-warning">{invited.email_error}.</p>}
          {invited && <CopyField label={t('Enlace de invitación')} value={invited.url} />}
          <p className="text-[13px] text-muted-foreground">
            {t('Sirve una sola vez y caduca en 7 días. No se vuelve a mostrar: si se pierde, invita otra vez y el anterior deja de valer.')}
          </p>
          <div className="flex justify-end">
            <Button onClick={() => setInvited(null)}>{t('Listo')}</Button>
          </div>
        </DialogContent>
      </Dialog>

      <AccessDialog user={editing} isMe={editing?.id === me.id} onClose={() => setEditing(null)} />

      <AlertDialog open={resetting !== null} onOpenChange={(o) => !o && setResetting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('¿Quitar la verificación en dos pasos de {email}?', { email: resetting?.email ?? '' })}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('Úsalo si perdió su móvil. Podrá entrar solo con la contraseña y volver a activarla desde su perfil.')}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <ErrorText error={reset.error} />
          <AlertDialogFooter>
            <AlertDialogCancel>{t('Cancelar')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault()
                if (resetting) reset.mutate(resetting)
              }}
            >
              {t('Quitar 2FA')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={deleting !== null} onOpenChange={(o) => !o && setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('¿Eliminar a {email}?', { email: deleting?.email ?? '' })}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('Se cierran sus sesiones y se desactivan los enlaces que creó. Los archivos del disco no se tocan.')}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <ErrorText error={remove.error} />
          <AlertDialogFooter>
            <AlertDialogCancel>{t('Cancelar')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault()
                if (deleting) remove.mutate(deleting)
              }}
            >
              {t('Eliminar')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}

function AccessDialog({ user, isMe, onClose }: { user: User | null; isMe: boolean; onClose: () => void }) {
  return (
    <Dialog open={user !== null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        {/* key: el formulario arranca con los valores del usuario elegido. */}
        {user && <AccessForm key={user.id} user={user} isMe={isMe} onClose={onClose} />}
      </DialogContent>
    </Dialog>
  )
}

function AccessForm({ user, isMe, onClose }: { user: User; isMe: boolean; onClose: () => void }) {
  const queryClient = useQueryClient()
  const router = useRouter()
  const [role, setRole] = useState<Role>(user.role)
  const [quota, setQuota] = useState(toGB(user.quota_bytes))
  const save = useMutation({
    mutationFn: () => updateUserAccess(user.id, role, toBytes(quota)),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['users'] })
      if (isMe) {
        // Quien se quita el rol de administrador sale de esta vista al recargar la ruta.
        await queryClient.fetchQuery({ ...meQuery, staleTime: 0 })
        await router.invalidate()
      }
      onClose()
    },
  })
  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    save.mutate()
  }
  return (
    <form onSubmit={onSubmit} className="grid grid-cols-1 gap-4">
      <DialogHeader>
        <DialogTitle>{t('Rol y cuota')}</DialogTitle>
        <DialogDescription className="break-all">{displayName(user)}</DialogDescription>
      </DialogHeader>
      <div className="grid gap-1.5">
        <Label htmlFor="edit-role">{t('Rol')}</Label>
        <RoleSelect id="edit-role" value={role} onChange={setRole} />
        <p className="text-xs text-muted-foreground">{roleHints[role]}</p>
      </div>
      <div className="grid gap-1.5">
        <Label htmlFor="edit-quota">{t('Cuota')}</Label>
        <QuotaInput id="edit-quota" value={quota} onChange={setQuota} />
        <p className="text-xs text-muted-foreground">
          {t('Ahora usa {tamaño}. Bajarla por debajo no borra nada: solo impide subir más.', { tamaño: formatBytes(user.used_bytes) })}
        </p>
      </div>
      {isMe && role !== 'admin' && (
        <p className="rounded-xl bg-warning-soft px-3.5 py-2.5 text-[13px] text-warning">{t('Perderás el acceso a esta vista al guardar.')}</p>
      )}
      <ErrorText error={save.error} />
      <div className="flex justify-end gap-2.5">
        <Button type="button" variant="outline" onClick={onClose}>
          {t('Cancelar')}
        </Button>
        <Button type="submit" disabled={save.isPending || !quotaOk(quota)}>
          {t('Guardar')}
        </Button>
      </div>
    </form>
  )
}

import { useRef, useState, type ChangeEvent, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useLocation, useNavigate, useRouteContext, useSearch } from '@tanstack/react-router'
import { Boxes, Database, Folder, FolderUp, Link2, LogOut, Menu, Trash2, Upload, User, Users } from 'lucide-react'
import { logout, meQuery, quotaQuery, settingsQuery } from '@/lib/api'
import { usableBytes } from '@/features/accounts/usable'
import { displayName, formatBytes, initials, percent } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Sheet, SheetContent, SheetTitle } from '@/components/ui/sheet'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { UploadPanel } from '@/features/uploads/UploadPanel'
import { uploads } from '@/features/uploads/store'
import { Avatar, Logo } from '@/features/shell/Logo'
import { can } from '@/features/auth/roles'
import { msg, t } from '@/lib/i18n'

// role: el mínimo que exige la vista; debe coincidir con el beforeLoad de cada ruta.
const nav = [
  { to: '/', label: msg('Mi espacio'), icon: Folder, exact: true, role: 'reader' },
  { to: '/shares', label: msg('Enlaces compartidos'), icon: Link2, exact: false, role: 'editor' },
  { to: '/s3', label: msg('Buckets S3'), icon: Boxes, exact: false, role: 'admin' },
  { to: '/users', label: msg('Usuarios'), icon: Users, exact: false, role: 'admin' },
  { to: '/storage', label: msg('Almacenamiento'), icon: Database, exact: false, role: 'admin' },
  { to: '/trash', label: msg('Papelera'), icon: Trash2, exact: false, role: 'editor' },
] as const

/** React no tipa webkitdirectory; con él, el selector elige carpetas enteras. */
const directoryProps = { webkitdirectory: '', directory: '' } as Record<string, string>

const mobileNav = [
  { to: '/', label: msg('Archivos'), icon: Folder, exact: true, role: 'reader' },
  { to: '/shares', label: msg('Enlaces'), icon: Link2, exact: false, role: 'editor' },
  { to: '/storage', label: msg('Espacio'), icon: Database, exact: false, role: 'admin' },
  { to: '/trash', label: msg('Papelera'), icon: Trash2, exact: false, role: 'editor' },
  { to: '/profile', label: msg('Perfil'), icon: User, exact: false, role: 'reader' },
] as const

const navLink =
  'flex items-center gap-3 rounded-lg px-3 py-2.5 text-[15px] text-sidebar-foreground hover:bg-muted [&.active]:bg-sidebar-accent [&.active]:font-semibold [&.active]:text-sidebar-accent-foreground'

export function AppShell({ children }: { children: ReactNode }) {
  const [menuOpen, setMenuOpen] = useState(false)
  const { user } = useRouteContext({ from: '/_auth' })
  // Admin: archivos, enlaces, espacio y perfil; editor cambia espacio por papelera.
  const tabs = mobileNav.filter((n) => can(user, n.role)).filter((n, _, all) => all.length <= 4 || n.to !== '/trash')

  return (
    <div className="min-h-dvh pr-[env(safe-area-inset-right)] pl-[env(safe-area-inset-left)] lg:flex">
      <aside className="sticky top-0 hidden h-dvh w-64 shrink-0 overflow-y-auto border-r bg-sidebar px-4 py-5 lg:flex">
        <Sidebar />
      </aside>

      <Sheet open={menuOpen} onOpenChange={setMenuOpen}>
        <SheetContent className="overflow-y-auto px-4 py-5">
          <SheetTitle className="sr-only">{t('Menú')}</SheetTitle>
          <Sidebar onNavigate={() => setMenuOpen(false)} />
        </SheetContent>
      </Sheet>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-30 flex h-[calc(4rem+env(safe-area-inset-top))] items-center justify-between gap-3 border-b bg-card/95 px-2 pt-[env(safe-area-inset-top)] backdrop-blur lg:hidden">
          <Button variant="ghost" size="icon-lg" aria-label={t('Abrir menú')} onClick={() => setMenuOpen(true)}>
            <Menu className="size-5" />
          </Button>
          <Link to="/" search={{}} aria-label={t('Inicio')}>
            <Logo />
          </Link>
          <Link to="/profile" aria-label={t('Perfil')} className="flex size-11 items-center justify-center">
            <Avatar text={initials(displayName(user))} />
          </Link>
        </header>

        <main className="mx-auto w-full max-w-7xl flex-1 px-4 py-6 pb-28 sm:px-6 lg:px-10 lg:py-8 lg:pb-12">{children}</main>
      </div>

      <nav
        aria-label={t('Aplicación')}
        className="fixed inset-x-0 bottom-0 z-30 grid auto-cols-fr grid-flow-col border-t bg-card pb-[env(safe-area-inset-bottom)] lg:hidden"
      >
        {tabs.map(({ to, label, icon: Icon, exact }) => (
          <Link
            key={to}
            to={to}
            activeOptions={{ exact, includeSearch: false }}
            className="flex h-16 flex-col items-center justify-center gap-1 text-[11px] font-medium text-muted-foreground [&.active]:font-semibold [&.active]:text-primary"
          >
            <Icon className="size-5" aria-hidden />
            {t(label)}
          </Link>
        ))}
      </nav>
      <UploadPanel />
    </div>
  )
}

/** Contenido del lateral: subir, navegación, espacio y usuario. */
function Sidebar({ onNavigate }: { onNavigate?: () => void }) {
  const ctx = useRouteContext({ from: '/_auth' })
  // /me se invalida tras subir: así el uso propio está al día sin navegar.
  const user = useQuery(meQuery).data ?? ctx.user
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const { pathname } = useLocation()
  const search = useSearch({ strict: false })
  const fileInput = useRef<HTMLInputElement>(null)
  const dirInput = useRef<HTMLInputElement>(null)
  const quota = useQuery({ ...quotaQuery, enabled: can(user, 'admin') })
  const settings = useQuery({ ...settingsQuery, enabled: can(user, 'admin') })
  // Mismo «útil» que la página de almacenamiento: descontada la paridad.
  const usable = quota.data && settings.data ? usableBytes(quota.data, settings.data) : 0

  const signOut = useMutation({
    mutationFn: logout,
    onSettled: () => {
      queryClient.setQueryData(meQuery.queryKey, null)
      queryClient.removeQueries({ predicate: (q) => q.queryKey[0] !== 'auth' })
      void navigate({ to: '/login', replace: true })
    },
  })

  // En el drive se sube a la carpeta abierta; desde otra vista, a la raíz.
  const targetFolder = pathname === '/' ? (search.folder ?? 0) : 0
  const onPick = (e: ChangeEvent<HTMLInputElement>) => {
    if (e.target.files?.length) {
      uploads.enqueue(targetFolder, e.target.files)
      if (pathname !== '/') void navigate({ to: '/', search: {} })
      onNavigate?.()
    }
    e.target.value = ''
  }

  return (
    <div className="flex w-full flex-col gap-6">
      <Link to="/" search={{}} onClick={onNavigate} className="px-2">
        <Logo />
      </Link>

      {can(user, 'editor') && (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button size="lg">
              <Upload /> {t('Subir')}
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start">
            <DropdownMenuItem onSelect={() => fileInput.current?.click()}>
              <Upload className="size-4" /> {t('Archivos')}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => dirInput.current?.click()}>
              <FolderUp className="size-4" /> {t('Carpeta')}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      )}
      <input ref={fileInput} type="file" multiple className="hidden" onChange={onPick} />
      <input ref={dirInput} type="file" multiple {...directoryProps} className="hidden" onChange={onPick} />

      <nav aria-label={t('Aplicación')} className="flex flex-col gap-0.5">
        {nav
          .filter((n) => can(user, n.role))
          .map(({ to, label, icon: Icon, exact }) => (
          <Link
            key={to}
            to={to}
            search={to === '/' ? {} : undefined}
            activeOptions={{ exact, includeSearch: false }}
            onClick={onNavigate}
            className={navLink}
          >
            <Icon className="size-[18px]" aria-hidden />
            {t(label)}
          </Link>
        ))}
      </nav>

      {can(user, 'admin') ? (
        quota.data && settings.data && (
        <div className="grid gap-2.5 rounded-xl bg-background p-4">
          <div className="flex flex-wrap items-baseline justify-between gap-x-2 gap-y-0.5">
            <span className="text-[13px] font-semibold">{t('Almacenamiento')}</span>
            <span className="font-mono text-xs whitespace-nowrap text-secondary-foreground">
              {formatBytes(quota.data.used)} / {formatBytes(usable)}
            </span>
          </div>
          <div className="h-2 overflow-hidden rounded-full bg-border">
            <div className="h-full bg-primary" style={{ width: `${percent(quota.data.used, usable)}%` }} />
          </div>
        </div>
        )
      ) : (
        <div className="grid gap-2.5 rounded-xl bg-background p-4">
          <div className="flex flex-wrap items-baseline justify-between gap-x-2 gap-y-0.5">
            <span className="text-[13px] font-semibold">{t('Tu espacio')}</span>
            <span className="font-mono text-xs whitespace-nowrap text-secondary-foreground">
              {formatBytes(user.used_bytes)}
              {user.quota_bytes > 0 && ` / ${formatBytes(user.quota_bytes)}`}
            </span>
          </div>
          {user.quota_bytes > 0 && (
            <div className="h-2 overflow-hidden rounded-full bg-border">
              <div className="h-full bg-primary" style={{ width: `${percent(user.used_bytes, user.quota_bytes)}%` }} />
            </div>
          )}
          <span className="text-xs text-muted-foreground">
            {user.quota_bytes > 0 ? t('Cuota asignada por un administrador; la papelera también cuenta.') : t('Sin límite de cuota.')}
          </span>
        </div>
      )}

      <div className="mt-auto flex items-center gap-1 border-t pt-3">
        <Link
          to="/profile"
          onClick={onNavigate}
          className="flex min-w-0 flex-1 items-center gap-2.5 rounded-lg px-2 py-2 hover:bg-muted [&.active]:bg-sidebar-accent"
        >
          <Avatar text={initials(displayName(user))} />
          <span className="grid min-w-0">
            <span className="truncate text-sm font-semibold">{displayName(user)}</span>
            <span className="truncate text-xs text-muted-foreground">{user.name ? user.email : t('Mi perfil')}</span>
          </span>
        </Link>
        <Button variant="ghost" size="icon" aria-label={t('Cerrar sesión')} onClick={() => signOut.mutate()} disabled={signOut.isPending}>
          <LogOut />
        </Button>
      </div>
    </div>
  )
}

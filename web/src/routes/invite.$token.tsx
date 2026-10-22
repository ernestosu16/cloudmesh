import { useState, type FormEvent } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { acceptInvitation, meQuery, publicInvitationQuery } from '@/lib/api'
import { shortDate } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Logo } from '@/features/shell/Logo'
import { ErrorText, Panel } from '@/features/shell/Page'
import { roleHints, roleLabels } from '@/features/auth/roles'
import { LanguageSelect } from '@/features/shell/LanguageSelect'
import { ThemeSelect } from '@/features/shell/ThemeSelect'
import { t } from '@/lib/i18n'

export const Route = createFileRoute('/invite/$token')({
  component: InvitePage,
})

function InvitePage() {
  const { token } = Route.useParams()
  const inv = useQuery(publicInvitationQuery(token))
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [repeat, setRepeat] = useState('')
  const accept = useMutation({
    mutationFn: () => acceptInvitation(token, name, password),
    onSuccess: (u) => {
      queryClient.setQueryData(meQuery.queryKey, u)
      void navigate({ to: '/', search: {}, replace: true })
    },
  })
  const mismatch = repeat !== '' && password !== repeat
  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (!mismatch) accept.mutate()
  }

  return (
    <div className="flex min-h-dvh flex-col items-center justify-center gap-6 px-4 py-10">
      <Logo />
      <Panel className="grid w-full max-w-md grid-cols-1 gap-5">
        {inv.isPending ? (
          <p className="text-sm text-muted-foreground">{t('Comprobando la invitación…')}</p>
        ) : inv.error || !inv.data ? (
          <div className="grid gap-3">
            <h1 className="text-2xl font-bold">{t('Invitación no válida')}</h1>
            <p className="text-sm text-secondary-foreground">
              {t('El enlace no existe, caducó o ya se usó. Pide a quien te invitó que te mande otro.')}
            </p>
            <Button asChild variant="outline">
              <Link to="/login" search={{ redirect: '/' }}>
                {t('Ya tengo cuenta')}
              </Link>
            </Button>
          </div>
        ) : (
          <form onSubmit={onSubmit} className="grid grid-cols-1 gap-4">
            <div className="grid gap-1">
              <h1 className="text-2xl font-bold">{t('Te invitan a CloudMesh')}</h1>
              <p className="text-sm text-secondary-foreground">
                {t('Cuenta para')} <strong className="break-all text-foreground">{inv.data.email}</strong> · {roleLabels[inv.data.role]}
              </p>
              <p className="text-[13px] text-muted-foreground">
                {roleHints[inv.data.role]} {t('El enlace caduca el {fecha}.', { fecha: shortDate(inv.data.expires_at) })}
              </p>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="inv-name">{t('Tu nombre (opcional)')}</Label>
              <Input id="inv-name" maxLength={80} autoComplete="name" value={name} onChange={(e) => setName(e.target.value)} />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="inv-password">{t('Contraseña')}</Label>
              <Input
                id="inv-password"
                type="password"
                autoComplete="new-password"
                required
                minLength={10}
                placeholder={t('Mínimo 10 caracteres')}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="inv-repeat">{t('Repite la contraseña')}</Label>
              <Input
                id="inv-repeat"
                type="password"
                autoComplete="new-password"
                required
                aria-invalid={mismatch}
                value={repeat}
                onChange={(e) => setRepeat(e.target.value)}
              />
              {mismatch && <p className="text-xs text-destructive">{t('No coincide.')}</p>}
            </div>
            <ErrorText error={accept.error} />
            <Button type="submit" size="lg" disabled={accept.isPending || mismatch}>
              {t('Crear mi cuenta y entrar')}
            </Button>
          </form>
        )}
      </Panel>
      <div className="flex flex-wrap justify-center gap-2">
        <LanguageSelect className="w-40" />
        <ThemeSelect className="w-40" />
      </div>
    </div>
  )
}

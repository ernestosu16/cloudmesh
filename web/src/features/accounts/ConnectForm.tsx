import { useState, type FormEvent, type ReactNode } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { cn } from 'cn'
import { Check, CircleCheck, TriangleAlert } from 'lucide-react'
import { ApiError, connectAccount, testAccount, type Contribution, type TestResult } from '@/lib/api'
import { formatBytes } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ContributionField, contributionValid } from './Contribution'
import { t } from '@/lib/i18n'

/** Mensaje de un error de proveedor; mfa_required lo trata el formulario. */
const errorText = (err: unknown) =>
  err instanceof ApiError ? (err.code === 'mfa_required' ? null : err.message) : err ? t('No se pudo conectar con el servidor') : null

/**
 * Alta de una cuenta en tres pasos: proveedor (lo elige quien lo usa, en
 * providerPicker), acceso con «Probar conexión» opcional y ajustes.
 */
export function ConnectForm({
  provider,
  providerPicker,
  onDone,
  onCancel,
}: {
  provider: string
  providerPicker: ReactNode
  onDone: () => void
  onCancel?: () => void
}) {
  const queryClient = useQueryClient()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [mfa, setMfa] = useState('')
  const [needsMfa, setNeedsMfa] = useState(false)
  const [label, setLabel] = useState('')
  const [contrib, setContrib] = useState<Contribution>({ contrib_mode: 'all', contrib_value: 0 })
  const [tested, setTested] = useState<(TestResult & { key: string }) | null>(null)

  const key = `${provider}|${email.trim().toLowerCase()}|${password}`
  const testOk = tested?.key === key ? tested : null
  const onMfa = (err: unknown) => {
    if (err instanceof ApiError && err.code === 'mfa_required') setNeedsMfa(true)
  }

  const test = useMutation({
    mutationFn: () => testAccount({ provider, email, password, mfa: mfa || undefined }),
    onSuccess: (r) => setTested({ ...r, key }),
    onError: onMfa,
  })
  const connect = useMutation({
    mutationFn: () => connectAccount({ provider, label: label || undefined, email, password, mfa: mfa || undefined, ...contrib }),
    onSuccess: () => {
      setEmail('')
      setPassword('')
      setMfa('')
      setNeedsMfa(false)
      setLabel('')
      setContrib({ contrib_mode: 'all', contrib_value: 0 })
      setTested(null)
      test.reset()
      void queryClient.invalidateQueries({ queryKey: ['quota'] })
      onDone()
    },
    onError: onMfa,
  })

  const hasAccess = email.trim() !== '' && password !== '' && (!needsMfa || /^\d{6}$/.test(mfa))
  const step = !hasAccess ? 2 : 3
  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    connect.mutate()
  }

  return (
    <form onSubmit={onSubmit} className="grid grid-cols-1 gap-5">
      <Step n={1} title={t('Proveedor')} state="done">
        {providerPicker}
      </Step>

      {/* En pantallas anchas, acceso y ajustes van lado a lado. */}
      <div className="grid grid-cols-1 gap-5 md:grid-cols-2 md:gap-8">
        <Step n={2} title={t('Acceso')} state={testOk ? 'done' : step === 2 ? 'current' : 'todo'}>
          <div className="flex flex-wrap gap-3">
            <div className="grid min-w-0 flex-[1_1_14rem] gap-1.5">
              <Label htmlFor="conn-email">{t('Correo de la cuenta')}</Label>
              <Input id="conn-email" type="email" autoComplete="off" required placeholder={t('cuenta@correo.com')} value={email} onChange={(e) => setEmail(e.target.value)} />
            </div>
            <div className="grid min-w-0 flex-[1_1_14rem] gap-1.5">
              <Label htmlFor="conn-password">{t('Contraseña')}</Label>
              <Input id="conn-password" type="password" autoComplete="off" required value={password} onChange={(e) => setPassword(e.target.value)} />
            </div>
          </div>
          {needsMfa && (
            <div className="grid gap-1.5">
              <Label htmlFor="conn-mfa">{t('Código de verificación (2FA)')}</Label>
              <Input
                id="conn-mfa"
                inputMode="numeric"
                autoComplete="one-time-code"
                placeholder="123456"
                autoFocus
                value={mfa}
                onChange={(e) => setMfa(e.target.value.trim())}
              />
              <p className="text-[13px] text-muted-foreground">{t('La cuenta tiene verificación en dos pasos.')}</p>
            </div>
          )}
          <div className="flex flex-wrap items-center gap-3">
            <Button type="button" variant="outline" disabled={!hasAccess || test.isPending} onClick={() => test.mutate()}>
              {test.isPending ? t('Probando…') : t('Probar conexión')}
            </Button>
            {testOk &&
              (testOk.already_linked ? (
                <span role="status" className="flex items-center gap-1.5 text-[13px] font-medium text-warning">
                  <TriangleAlert className="size-4" aria-hidden /> {t('Esa cuenta ya está en el pool.')}
                </span>
              ) : (
                <span role="status" className="flex items-center gap-1.5 text-[13px] font-medium text-success">
                  <CircleCheck className="size-4" aria-hidden />{' '}
                  {t('Conexión correcta: {usado} usados de {total}.', {
                    usado: formatBytes(testOk.quota_used),
                    total: formatBytes(testOk.quota_total),
                  })}
                </span>
              ))}
          </div>
          {errorText(test.error) && (
            <p role="alert" className="text-sm text-destructive">
              {errorText(test.error)}
            </p>
          )}
          <p className="text-[13px] text-muted-foreground">
            {t('La contraseña se usa una sola vez para abrir una sesión. CloudMesh guarda solo la sesión, cifrada.')}
          </p>
        </Step>

        <Step n={3} title={t('Ajustes')} state={step === 3 ? 'current' : 'todo'}>
          <div className="grid gap-1.5">
            <Label htmlFor="conn-label">{t('Nombre interno (opcional)')}</Label>
            <Input id="conn-label" maxLength={60} placeholder={email || t('Mega personal')} value={label} onChange={(e) => setLabel(e.target.value)} />
          </div>
          <ContributionField value={contrib} onChange={setContrib} total={testOk && !testOk.already_linked ? testOk.quota_total : undefined} />
        </Step>
      </div>

      {errorText(connect.error) && (
        <p role="alert" className="text-sm text-destructive">
          {errorText(connect.error)}
        </p>
      )}
      <div className="flex flex-wrap justify-end gap-2.5 border-t pt-4">
        {onCancel && (
          <Button type="button" size="lg" variant="outline" onClick={onCancel}>
            {t('Cancelar')}
          </Button>
        )}
        <Button type="submit" size="lg" disabled={!hasAccess || !contributionValid(contrib) || connect.isPending || testOk?.already_linked}>
          {connect.isPending ? t('Conectando…') : t('Añadir al pool')}
        </Button>
      </div>
    </form>
  )
}

/** Paso del alta; el círculo dice si está hecho (check), en curso o pendiente. */
function Step({ n, title, state, children }: { n: number; title: string; state: 'done' | 'current' | 'todo'; children: ReactNode }) {
  return (
    <fieldset className="grid min-w-0 grid-cols-1 content-start gap-3" aria-current={state === 'current' ? 'step' : undefined}>
      <legend className={cn('mb-1 flex items-center gap-2 text-sm font-semibold', state === 'todo' && 'text-muted-foreground')}>
        <span
          className={cn(
            'inline-flex size-6 items-center justify-center rounded-full text-xs',
            state === 'done' ? 'bg-primary text-primary-foreground' : state === 'current' ? 'bg-accent text-accent-foreground ring-2 ring-primary' : 'bg-secondary',
          )}
        >
          {state === 'done' ? <Check className="size-3.5" aria-hidden /> : n}
        </span>
        {title}
      </legend>
      {children}
    </fieldset>
  )
}

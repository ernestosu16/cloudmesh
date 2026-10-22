import { useState, type FormEvent } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { ApiError, login, meQuery } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { t } from '@/lib/i18n'

export function LoginForm({ redirectTo }: { redirectTo: string }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [needsCode, setNeedsCode] = useState(false)
  const [recovery, setRecovery] = useState(false)
  const queryClient = useQueryClient()
  const navigate = useNavigate()

  const mutation = useMutation({
    mutationFn: () => login(email, password, needsCode ? code : undefined),
    onSuccess: (user) => {
      queryClient.setQueryData(meQuery.queryKey, user)
      void navigate({ to: redirectTo, replace: true })
    },
    onError: (err) => {
      if (err instanceof ApiError && err.code === 'totp_required') setNeedsCode(true)
      if (err instanceof ApiError && err.code === 'invalid_totp') setCode('')
    },
  })

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    mutation.mutate()
  }

  const errorMessage =
    mutation.error instanceof ApiError
      ? mutation.error.code === 'totp_required'
        ? null
        : mutation.error.message
      : mutation.error
        ? t('No se pudo conectar con el servidor')
        : null

  return (
    <Card className="w-full max-w-sm">
      <CardHeader>
        <CardTitle className="text-2xl">{t('Iniciar sesión')}</CardTitle>
        <CardDescription>{t('Inicia sesión para acceder a tu drive.')}</CardDescription>
      </CardHeader>
      <form onSubmit={onSubmit}>
        <CardContent className="grid gap-4">
          <div className="grid gap-2">
            <Label htmlFor="email">Email</Label>
            <Input
              id="email"
              type="email"
              autoComplete="username"
              required
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="password">{t('Contraseña')}</Label>
            <Input
              id="password"
              type="password"
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>
          {needsCode && (
            <div className="grid gap-2">
              <Label htmlFor="code">{recovery ? t('Código de recuperación') : t('Código de verificación')}</Label>
              <Input
                id="code"
                inputMode={recovery ? 'text' : 'numeric'}
                autoComplete="one-time-code"
                autoCapitalize="none"
                spellCheck={false}
                maxLength={recovery ? 16 : 7}
                placeholder={recovery ? 'xxxx-xxxx-xxxx' : '123 456'}
                required
                autoFocus
                value={code}
                onChange={(e) => setCode(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                {recovery ? t('Cada código sirve una sola vez.') : t('Ábrelo en tu app de autenticación.')}{' '}
                <button
                  type="button"
                  className="font-semibold text-primary hover:underline"
                  onClick={() => {
                    setRecovery((r) => !r)
                    setCode('')
                  }}
                >
                  {recovery ? t('Usar la app') : t('¿Sin el móvil? Usa un código de recuperación')}
                </button>
              </p>
            </div>
          )}
          {errorMessage && (
            <p role="alert" className="text-sm text-destructive">
              {errorMessage}
            </p>
          )}
        </CardContent>
        <CardFooter className="mt-6">
          <Button type="submit" className="w-full" disabled={mutation.isPending}>
            {mutation.isPending ? t('Entrando…') : needsCode ? t('Verificar y entrar') : t('Entrar')}
          </Button>
        </CardFooter>
      </form>
    </Card>
  )
}

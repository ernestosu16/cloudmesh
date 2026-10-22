import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { KeyRound } from 'lucide-react'
import { ApiError, completeSetup, meQuery, setupQuery } from '@/lib/api'
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

const schema = () =>
  z
    .object({
      code: z.string().trim().min(8, t('Código de 8 caracteres')),
      email: z.string().trim().email(t('Email inválido')),
      password: z.string().min(10, t('Mínimo 10 caracteres')),
      confirm: z.string(),
    })
    .refine((v) => v.password === v.confirm, { path: ['confirm'], message: t('Las contraseñas no coinciden') })
type Values = z.infer<ReturnType<typeof schema>>

export function SetupForm() {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const form = useForm<Values>({
    resolver: zodResolver(schema()),
    defaultValues: { code: '', email: '', password: '', confirm: '' },
  })

  const mutation = useMutation({
    mutationFn: (v: Values) => completeSetup(v.code, v.email, v.password),
    onSuccess: (user) => {
      queryClient.setQueryData(meQuery.queryKey, user)
      queryClient.setQueryData(setupQuery.queryKey, { needed: false })
      void navigate({ to: '/', replace: true })
    },
  })

  const serverError =
    mutation.error instanceof ApiError
      ? mutation.error.message
      : mutation.error
        ? t('No se pudo conectar con el servidor')
        : null
  const errors = form.formState.errors

  return (
    <Card className="w-full max-w-md">
      <CardHeader>
        <CardTitle className="text-2xl">{t('Bienvenido a CloudMesh')}</CardTitle>
        <CardDescription>
          {t('Esta instancia aún no tiene administrador. Crea el primero con el código que el servidor escribió en su log al arrancar.')}
        </CardDescription>
      </CardHeader>
      <form onSubmit={form.handleSubmit((v) => mutation.mutate(v))}>
        <CardContent className="grid gap-4">
          <div className="rounded-md border bg-muted/40 p-3 text-xs text-muted-foreground">
            <p className="flex items-center gap-1.5 font-medium text-foreground">
              <KeyRound className="size-3.5" /> {t('Dónde está el código')}
            </p>
            <p className="mt-1">
              {t('En Docker:')} <code className="rounded bg-muted px-1">docker logs &lt;{t('contenedor')}&gt;</code>.{' '}
              {t('Busca la línea con')} <code className="rounded bg-muted px-1">setup_code</code>.{' '}
              {t('Si lo perdiste, reinicia el contenedor: se genera uno nuevo.')}
            </p>
          </div>
          <Field label={t('Código de configuración')} error={errors.code?.message}>
            <Input
              id="code"
              placeholder="XXXX-XXXX"
              autoComplete="off"
              autoCapitalize="characters"
              className="font-mono tracking-widest uppercase"
              {...form.register('code')}
            />
          </Field>
          <Field label={t('Email del administrador')} error={errors.email?.message}>
            <Input id="email" type="email" autoComplete="username" {...form.register('email')} />
          </Field>
          <Field label={t('Contraseña')} error={errors.password?.message}>
            <Input id="password" type="password" autoComplete="new-password" {...form.register('password')} />
          </Field>
          <Field label={t('Repite la contraseña')} error={errors.confirm?.message}>
            <Input id="confirm" type="password" autoComplete="new-password" {...form.register('confirm')} />
          </Field>
          {serverError && (
            <p role="alert" className="text-sm text-destructive">
              {serverError}
            </p>
          )}
        </CardContent>
        <CardFooter className="mt-6">
          <Button type="submit" className="w-full" disabled={mutation.isPending}>
            {mutation.isPending ? t('Creando…') : t('Crear administrador')}
          </Button>
        </CardFooter>
      </form>
    </Card>
  )
}

function Field({
  label,
  error,
  children,
}: {
  label: string
  error?: string
  children: React.ReactElement<{ id?: string }>
}) {
  return (
    <div className="grid gap-2">
      <Label htmlFor={children.props.id}>{label}</Label>
      {children}
      {error && <p className="text-xs text-destructive">{error}</p>}
    </div>
  )
}

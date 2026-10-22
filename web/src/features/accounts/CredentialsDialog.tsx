import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ApiError, reconnectAccount, type Account } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { t } from '@/lib/i18n'

const schema = () =>
  z.object({
    email: z.string().trim().email(t('Email inválido')),
    password: z.string().min(1, t('Obligatoria')),
    mfa: z.string().trim().regex(/^\d{6}$/, t('Código de 6 dígitos')).or(z.literal('')),
  })
type FormValues = z.infer<ReturnType<typeof schema>>

/**
 * Renueva la sesión de una cuenta existente. Si el proveedor pide 2FA,
 * muestra el campo del código y reintenta con él.
 */
function ReconnectForm({ account, onDone }: { account: Account; onDone: () => void }) {
  const queryClient = useQueryClient()
  const [needsMfa, setNeedsMfa] = useState(false)

  const form = useForm<FormValues>({
    resolver: zodResolver(schema()),
    defaultValues: { email: account.email, password: '', mfa: '' },
  })

  const mutation = useMutation({
    mutationFn: (v: FormValues) => reconnectAccount(account.id, v.password, v.mfa || undefined),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['quota'] })
      onDone()
    },
    onError: (err) => {
      if (err instanceof ApiError && err.code === 'mfa_required') {
        setNeedsMfa(true)
        setTimeout(() => form.setFocus('mfa'), 0)
      }
    },
  })

  const serverError =
    mutation.error instanceof ApiError && mutation.error.code !== 'mfa_required'
      ? mutation.error.message
      : mutation.error && !(mutation.error instanceof ApiError)
        ? t('No se pudo conectar con el servidor')
        : null

  return (
    <form onSubmit={form.handleSubmit((v) => mutation.mutate(v))} className="grid gap-3.5">
      <div className="flex flex-wrap gap-3">
        <Field id="cred-email" label={t('Correo de la cuenta')} error={form.formState.errors.email?.message} className="flex-[1_1_14rem]">
          <Input id="cred-email" type="email" autoComplete="off" readOnly {...form.register('email')} />
        </Field>
        <Field id="cred-password" label={t('Contraseña')} error={form.formState.errors.password?.message} className="flex-[1_1_14rem]">
          <Input id="cred-password" type="password" autoComplete="off" {...form.register('password')} />
        </Field>
      </div>
      {needsMfa && (
        <Field id="cred-mfa" label={t('Código de verificación (2FA)')} error={form.formState.errors.mfa?.message}>
          <Input id="cred-mfa" inputMode="numeric" autoComplete="one-time-code" placeholder="123456" {...form.register('mfa')} />
        </Field>
      )}
      <p className="text-[13px] text-muted-foreground">
        {t('La contraseña se usa una sola vez para abrir una sesión. CloudMesh guarda solo la sesión, cifrada.')}
      </p>
      {serverError && (
        <p role="alert" className="text-sm text-destructive">
          {serverError}
        </p>
      )}
      <div className="flex flex-wrap justify-end gap-2.5">
        <Button type="button" variant="outline" onClick={onDone}>
          {t('Cancelar')}
        </Button>
        <Button type="submit" disabled={mutation.isPending}>
          {mutation.isPending ? t('Conectando…') : t('Reconectar')}
        </Button>
      </div>
    </form>
  )
}

/** Diálogo para renovar la sesión de una cuenta en reconnect_required. */
export function ReconnectDialog({ account, onClose }: { account: Account | null; onClose: () => void }) {
  return (
    <Dialog open={account !== null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t('Reconectar {nombre}', { nombre: account?.label ?? '' })}</DialogTitle>
          <DialogDescription>{t('La sesión con el proveedor expiró. Introduce la contraseña para renovarla.')}</DialogDescription>
        </DialogHeader>
        {account && <ReconnectForm key={account.id} account={account} onDone={onClose} />}
      </DialogContent>
    </Dialog>
  )
}

function Field({
  id,
  label,
  error,
  className,
  children,
}: {
  id: string
  label: string
  error?: string
  className?: string
  children: React.ReactNode
}) {
  return (
    <div className={`grid min-w-0 gap-1.5 ${className ?? ''}`}>
      <Label htmlFor={id}>{label}</Label>
      {children}
      {error && <p className="text-xs text-destructive">{error}</p>}
    </div>
  )
}

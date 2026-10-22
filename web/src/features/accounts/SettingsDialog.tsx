import { useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ApiError, updateAccount, type Account } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { ContributionField, contributionValid } from './Contribution'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { t } from '@/lib/i18n'

const schema = () =>
  z.object({
    label: z.string().trim().min(1, t('Obligatoria')).max(60, t('Máximo 60 caracteres')),
    priority: z.number({ message: t('Número entre 0 y 1000') }).int().min(0).max(1000),
    enabled: z.boolean(),
    contrib_mode: z.enum(['all', 'reserve', 'limit']),
    contrib_value: z.number(),
  })
type FormValues = z.infer<ReturnType<typeof schema>>

export function SettingsDialog({ account, onClose }: { account: Account | null; onClose: () => void }) {
  const queryClient = useQueryClient()
  const form = useForm<FormValues>({
    resolver: zodResolver(schema()),
    defaultValues: { label: '', priority: 100, enabled: true, contrib_mode: 'all', contrib_value: 0 },
  })

  useEffect(() => {
    if (account)
      form.reset({
        label: account.label,
        priority: account.priority,
        enabled: account.enabled,
        contrib_mode: account.contrib_mode,
        contrib_value: account.contrib_value,
      })
  }, [account, form])

  const mutation = useMutation({
    mutationFn: (v: FormValues) => updateAccount(account!.id, v),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['quota'] })
      onClose()
    },
  })

  const enabled = form.watch('enabled')
  const contrib = { contrib_mode: form.watch('contrib_mode'), contrib_value: form.watch('contrib_value') }

  return (
    <Dialog open={account !== null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-md">
        <form onSubmit={form.handleSubmit((v) => mutation.mutate(v))} className="grid grid-cols-1 gap-4">
          <DialogHeader>
            <DialogTitle>{t('Ajustes de la cuenta')}</DialogTitle>
            <DialogDescription>{account?.email}</DialogDescription>
          </DialogHeader>

          <div className="grid gap-2">
            <Label htmlFor="s-label">{t('Etiqueta')}</Label>
            <Input id="s-label" {...form.register('label')} />
            {form.formState.errors.label && (
              <p className="text-xs text-destructive">{form.formState.errors.label.message}</p>
            )}
          </div>
          <div className="grid gap-2">
            <Label htmlFor="s-priority">{t('Prioridad')}</Label>
            <Input
              id="s-priority"
              type="number"
              min={0}
              max={1000}
              {...form.register('priority', { valueAsNumber: true })}
            />
            {form.formState.errors.priority && (
              <p className="text-xs text-destructive">{form.formState.errors.priority.message}</p>
            )}
            <p className="text-xs text-muted-foreground">
              {t('Menor número, mayor preferencia en la política de enrutado por prioridad.')}
            </p>
          </div>
          <div className="flex items-center justify-between gap-4 rounded-md border p-3">
            <div className="grid gap-0.5">
              <Label htmlFor="s-enabled">{t('Recibir subidas')}</Label>
              <p className="text-xs text-muted-foreground">
                {t('Desactívalo para seguir leyendo sin que entren archivos nuevos.')}
              </p>
            </div>
            <Switch
              id="s-enabled"
              checked={enabled}
              onCheckedChange={(v) => form.setValue('enabled', v, { shouldDirty: true })}
            />
          </div>

          <ContributionField
            value={contrib}
            total={account?.quota_total}
            onChange={(c) => {
              form.setValue('contrib_mode', c.contrib_mode, { shouldDirty: true })
              form.setValue('contrib_value', c.contrib_value, { shouldDirty: true })
            }}
          />

          {mutation.error && (
            <p role="alert" className="text-sm text-destructive">
              {mutation.error instanceof ApiError ? mutation.error.message : t('Error de red')}
            </p>
          )}

          <DialogFooter className="gap-2 sm:gap-0">
            <Button type="button" variant="outline" onClick={onClose}>
              {t('Cancelar')}
            </Button>
            <Button type="submit" disabled={mutation.isPending || !contributionValid(contrib)}>
              {t('Guardar')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

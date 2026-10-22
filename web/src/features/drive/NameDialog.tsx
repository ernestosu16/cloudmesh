import { useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { ApiError } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { t } from '@/lib/i18n'

const schema = () =>
  z.object({
    name: z
      .string()
      .trim()
      .min(1, t('Obligatorio'))
      .max(255, t('Máximo 255 caracteres'))
      .refine((v) => !/[/\\]/.test(v), t('No puede contener / ni \\')),
  })
type Values = z.infer<ReturnType<typeof schema>>

interface Props {
  open: boolean
  title: string
  initial?: string
  submitLabel: string
  error?: unknown
  pending?: boolean
  onSubmit: (name: string) => void
  onClose: () => void
}

/** Pedir un nombre: crear o renombrar carpeta. */
export function NameDialog({ open, title, initial = '', submitLabel, error, pending, onSubmit, onClose }: Props) {
  const form = useForm<Values>({ resolver: zodResolver(schema()), defaultValues: { name: initial } })

  useEffect(() => {
    if (open) form.reset({ name: initial })
  }, [open, initial, form])

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-sm">
        <form onSubmit={form.handleSubmit((v) => onSubmit(v.name))} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
          </DialogHeader>
          <div className="grid gap-2">
            <Label htmlFor="folder-name">{t('Nombre')}</Label>
            <Input id="folder-name" autoFocus autoComplete="off" {...form.register('name')} />
            {form.formState.errors.name && (
              <p className="text-xs text-destructive">{form.formState.errors.name.message}</p>
            )}
            {error ? (
              <p role="alert" className="text-xs text-destructive">
                {error instanceof ApiError ? error.message : t('Error de red')}
              </p>
            ) : null}
          </div>
          <DialogFooter className="gap-2 sm:gap-0">
            <Button type="button" variant="outline" onClick={onClose}>
              {t('Cancelar')}
            </Button>
            <Button type="submit" disabled={pending}>
              {submitLabel}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

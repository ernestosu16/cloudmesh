import type { ReactNode } from 'react'
import { cn } from 'cn'
import { ApiError } from '@/lib/api'
import { t } from '@/lib/i18n'

/** Título de vista con descripción y acciones que bajan de línea en móvil. */
export function PageHeader({ title, description, children }: { title: string; description?: ReactNode; children?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-3.5">
      <div className="min-w-0">
        <h1 className="text-[28px] leading-tight font-bold sm:text-3xl">{title}</h1>
        {description && <p className="mt-1.5 text-secondary-foreground">{description}</p>}
      </div>
      {children && <div className="flex flex-wrap gap-2.5">{children}</div>}
    </div>
  )
}

/** Bloque blanco con borde: la unidad de las vistas. */
export function Panel({ className, ...props }: React.ComponentProps<'section'>) {
  return <section className={cn('rounded-2xl border bg-card p-5 sm:p-6', className)} {...props} />
}

const tones = {
  neutral: 'bg-secondary text-secondary-foreground',
  accent: 'bg-accent text-accent-foreground',
  success: 'bg-success-soft text-success',
  warning: 'bg-warning-soft text-warning',
  danger: 'bg-destructive/10 text-destructive',
} as const

export type Tone = keyof typeof tones

/** Etiqueta redondeada de estado. */
export function Tag({ tone = 'neutral', className, ...props }: React.ComponentProps<'span'> & { tone?: Tone }) {
  return (
    <span
      className={cn('inline-flex w-fit items-center gap-1 rounded-full px-2.5 py-1 text-xs font-semibold whitespace-nowrap', tones[tone], className)}
      {...props}
    />
  )
}

/** Chip de filtro (aria-pressed). */
export function Chip({ active, className, ...props }: React.ComponentProps<'button'> & { active: boolean }) {
  return (
    <button
      type="button"
      aria-pressed={active}
      className={cn(
        'min-h-9 shrink-0 rounded-full border px-3.5 text-[13px] font-medium transition-colors',
        active ? 'border-foreground bg-foreground text-background' : 'border-border bg-card text-secondary-foreground hover:bg-muted',
        className,
      )}
      {...props}
    />
  )
}

/** Mensaje de error de una mutación. */
export function ErrorText({ error }: { error: unknown }) {
  if (!error) return null
  return (
    <p role="alert" className="text-sm wrap-anywhere text-destructive">
      {error instanceof ApiError ? error.message : t('No se pudo conectar con el servidor')}
    </p>
  )
}

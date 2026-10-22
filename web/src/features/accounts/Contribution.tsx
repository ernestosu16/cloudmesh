import { useId, useState } from 'react'
import { cn } from 'cn'
import type { Contribution, ContribMode } from '@/lib/api'
import { formatBytes } from '@/lib/format'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { msg, t } from '@/lib/i18n'

const GiB = 2 ** 30
const TiB = 2 ** 40

const modes: { value: ContribMode; label: string }[] = [
  { value: 'all', label: msg('Todo el espacio libre de la cuenta') },
  { value: 'reserve', label: msg('Dejar un porcentaje libre') },
  { value: 'limit', label: msg('Límite fijo') },
]

/** Mismas reglas que el servidor: 1–90 % libre o un tope de al menos 1 GiB. */
export function contributionValid(c: Contribution) {
  if (c.contrib_mode === 'reserve') return c.contrib_value >= 1 && c.contrib_value <= 90
  if (c.contrib_mode === 'limit') return c.contrib_value >= GiB
  return true
}

/** Texto corto del aporte para tablas y tarjetas. */
export function contributionLabel(c: Contribution) {
  if (c.contrib_mode === 'reserve') return t('deja {n} % libre', { n: c.contrib_value })
  if (c.contrib_mode === 'limit') return t('hasta {tamaño}', { tamaño: formatBytes(c.contrib_value) })
  return t('toda la cuenta')
}

/**
 * Selector de cuánto aporta una cuenta al disco. total es el tamaño de la
 * cuenta si se conoce, para explicar el resultado.
 */
export function ContributionField({
  value,
  onChange,
  total,
}: {
  value: Contribution
  onChange: (c: Contribution) => void
  total?: number
}) {
  const id = useId()
  const [unit, setUnit] = useState<'GB' | 'TB'>(() =>
    value.contrib_mode === 'limit' && value.contrib_value >= TiB && value.contrib_value % TiB === 0 ? 'TB' : 'GB',
  )
  const factor = unit === 'TB' ? TiB : GiB
  const setMode = (mode: ContribMode) =>
    onChange({ contrib_mode: mode, contrib_value: mode === 'reserve' ? 10 : mode === 'limit' ? 100 * GiB : 0 })
  const resulting =
    total === undefined
      ? undefined
      : value.contrib_mode === 'reserve'
        ? Math.floor((total * (100 - value.contrib_value)) / 100)
        : value.contrib_mode === 'limit'
          ? Math.min(total, value.contrib_value)
          : total

  return (
    <div className="grid grid-cols-1 gap-2.5">
      <div className="grid gap-1.5">
        <Label htmlFor={`${id}-mode`}>{t('Espacio que aporta al disco')}</Label>
        <Select value={value.contrib_mode} onValueChange={(v) => setMode(v as ContribMode)}>
          <SelectTrigger id={`${id}-mode`} className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {modes.map((m) => (
              <SelectItem key={m.value} value={m.value}>
                {t(m.label)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {value.contrib_mode === 'reserve' && (
        <div className="grid gap-1.5">
          <Label htmlFor={`${id}-pct`}>{t('Dejar libre')}</Label>
          <div className="flex items-center gap-2">
            <Input
              id={`${id}-pct`}
              type="number"
              inputMode="numeric"
              min={1}
              max={90}
              className="w-28"
              value={value.contrib_value}
              onChange={(e) => onChange({ contrib_mode: 'reserve', contrib_value: Math.round(Number(e.target.value) || 0) })}
            />
            <span className="text-sm text-secondary-foreground">%</span>
          </div>
          <p className="text-[13px] text-muted-foreground">
            {t('CloudMesh nunca llenará la cuenta por encima de ese margen, para que puedas seguir usándola fuera.')}
          </p>
        </div>
      )}

      {value.contrib_mode === 'limit' && (
        <div className="grid gap-1.5">
          <Label htmlFor={`${id}-limit`}>{t('Límite')}</Label>
          <div className="flex flex-wrap items-center gap-2">
            <Input
              id={`${id}-limit`}
              type="number"
              inputMode="decimal"
              min={1}
              step="any"
              className="w-32"
              value={Number((value.contrib_value / factor).toFixed(2))}
              onChange={(e) => onChange({ contrib_mode: 'limit', contrib_value: Math.round((Number(e.target.value) || 0) * factor) })}
            />
            <div role="radiogroup" aria-label={t('Unidad')} className="flex rounded-lg bg-secondary p-1">
              {(['GB', 'TB'] as const).map((u) => (
                <button
                  key={u}
                  type="button"
                  role="radio"
                  aria-checked={unit === u}
                  onClick={() => setUnit(u)}
                  className={cn('min-h-9 rounded-md px-3 text-sm font-semibold', unit === u ? 'bg-card shadow-sm' : 'text-secondary-foreground')}
                >
                  {u}
                </button>
              ))}
            </div>
          </div>
          <p className="text-[13px] text-muted-foreground">
            {t('CloudMesh no llenará la cuenta por encima de este tamaño. Si es mayor que la cuenta, se usa solo lo que tenga.')}
          </p>
        </div>
      )}

      {!contributionValid(value) ? (
        <p className="text-xs text-destructive">
          {value.contrib_mode === 'reserve' ? t('Entre 1 y 90 %.') : t('El límite debe ser de al menos 1 GB.')}
        </p>
      ) : (
        resulting !== undefined &&
        value.contrib_mode !== 'all' && (
          <p className="text-[13px] text-secondary-foreground">
            {t('Aporta hasta')} <strong className="text-foreground">{formatBytes(resulting)}</strong>{' '}
            {t('de {total}.', { total: formatBytes(total!) })}
          </p>
        )
      )}
    </div>
  )
}

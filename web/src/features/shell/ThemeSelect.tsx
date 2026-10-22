import { Monitor, Moon, Sun } from 'lucide-react'
import { cn } from 'cn'
import { t } from '@/lib/i18n'
import { setTheme, themes, useTheme, type Theme } from '@/lib/theme'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

const icons = { system: Monitor, light: Sun, dark: Moon }

/**
 * Selector de tema claro, oscuro o del sistema; se guarda en este navegador.
 * `compact` deja solo el icono en pantallas chicas (cabeceras estrechas).
 */
export function ThemeSelect({ id, className, compact }: { id?: string; className?: string; compact?: boolean }) {
  const theme = useTheme()
  const Icon = icons[theme]
  return (
    <Select value={theme} onValueChange={(v) => setTheme(v as Theme)}>
      <SelectTrigger id={id} aria-label={id ? undefined : t('Tema')} className={cn('w-full', compact && 'max-sm:*:data-[slot=select-value]:hidden!', className)}>
        <Icon className="size-4 text-muted-foreground" aria-hidden />
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {themes.map((th) => (
          <SelectItem key={th.value} value={th.value}>
            {t(th.label)}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

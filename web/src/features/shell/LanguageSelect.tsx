import { Languages } from 'lucide-react'
import { cn } from 'cn'
import { languages, setLang, t, useLang, type Lang } from '@/lib/i18n'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

/** Selector del idioma de la interfaz; se guarda en este navegador. */
export function LanguageSelect({ id, className }: { id?: string; className?: string }) {
  const lang = useLang()
  return (
    <Select value={lang} onValueChange={(v) => setLang(v as Lang)}>
      <SelectTrigger id={id} aria-label={id ? undefined : t('Idioma')} className={cn('w-full', className)}>
        <Languages className="size-4 text-muted-foreground" aria-hidden />
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {languages.map((l) => (
          <SelectItem key={l.value} value={l.value}>
            {l.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

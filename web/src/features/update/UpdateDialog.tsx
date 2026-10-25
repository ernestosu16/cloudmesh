import { useEffect, useState } from 'react'
import { RefreshCw } from 'lucide-react'
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { useUploads } from '@/features/uploads/store'
import { getLang, t } from '@/lib/i18n'
import { useUpdate } from './useUpdate'

/** Si se deja para más tarde, se vuelve a ofrecer pasado este rato. */
const REARM_MS = 15 * 60_000

/**
 * Avisa de que hay una versión nueva y recarga cuando el usuario lo pide.
 * Nunca recarga sola: cortaría subidas o un formulario a medias. Mientras haya
 * subidas en curso, actualizar espera a que acaben.
 */
export function UpdateDialog() {
  const update = useUpdate()
  const { pending } = useUploads()
  const [postponed, setPostponed] = useState(false)

  useEffect(() => {
    if (!postponed) return
    const timer = window.setTimeout(() => setPostponed(false), REARM_MS)
    return () => window.clearTimeout(timer)
  }, [postponed])

  if (!update) return null
  const date = new Date(update.built_at)
  const published = Number.isNaN(date.getTime())
    ? null
    : new Intl.DateTimeFormat(getLang(), { dateStyle: 'medium', timeStyle: 'short' }).format(date)

  return (
    <AlertDialog open={!postponed} onOpenChange={(open) => !open && setPostponed(true)}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t('Nueva versión disponible')}</AlertDialogTitle>
          <AlertDialogDescription>
            {published
              ? t('Se publicó una actualización de CloudMesh el {fecha}. La página se recargará para aplicarla.', { fecha: published })
              : t('Hay una actualización de CloudMesh lista. La página se recargará para aplicarla.')}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {pending > 0 && (
          <p className="text-sm text-warning">
            {pending === 1
              ? t('Queda 1 subida en curso: podrás actualizar cuando termine.')
              : t('Quedan {n} subidas en curso: podrás actualizar cuando terminen.', { n: pending })}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel>{t('Más tarde')}</AlertDialogCancel>
          {/* Un Button normal: el Action de Radix cerraría el diálogo antes de recargar. */}
          <Button disabled={pending > 0} onClick={() => window.location.reload()} autoFocus>
            <RefreshCw /> {t('Actualizar ahora')}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

import { useEffect, useState } from 'react'
import { CHECK_INTERVAL_MS, MIN_CHECK_GAP_MS, currentBuildId, fetchBuildInfo, type BuildInfo } from './check'

/**
 * Vigila si el servidor ya sirve otra build que la de esta página. Devuelve la
 * nueva, o null mientras la página está al día; al encontrarla deja de mirar.
 * Además de cada pocos minutos, mira al volver a la pestaña o a la red (una
 * webapp instalada puede quedarse abierta días) y cuando falla la carga de un
 * trozo de la app: tras un despliegue, los de la build vieja ya no existen.
 */
export function useUpdate(): BuildInfo | null {
  const [update, setUpdate] = useState<BuildInfo | null>(null)

  useEffect(() => {
    const current = currentBuildId()
    if (!current) return

    let found = false
    let inFlight = false
    let last = 0

    const check = async (force = false) => {
      if (found || inFlight || (!force && document.visibilityState === 'hidden')) return
      if (!force && Date.now() - last < MIN_CHECK_GAP_MS) return
      inFlight = true
      last = Date.now()
      try {
        const remote = await fetchBuildInfo()
        if (remote && remote.build_id !== current) {
          stop()
          setUpdate(remote)
        }
      } finally {
        inFlight = false
      }
    }
    const onCheck = () => void check()
    const onVisible = () => {
      if (document.visibilityState === 'visible') void check()
    }
    const onPreloadError = () => void check(true)

    const interval = window.setInterval(onCheck, CHECK_INTERVAL_MS)
    window.addEventListener('focus', onCheck)
    window.addEventListener('online', onCheck)
    window.addEventListener('vite:preloadError', onPreloadError)
    document.addEventListener('visibilitychange', onVisible)
    void check(true)

    function stop() {
      found = true
      window.clearInterval(interval)
      window.removeEventListener('focus', onCheck)
      window.removeEventListener('online', onCheck)
      window.removeEventListener('vite:preloadError', onPreloadError)
      document.removeEventListener('visibilitychange', onVisible)
    }
    return stop
  }, [])

  return update
}

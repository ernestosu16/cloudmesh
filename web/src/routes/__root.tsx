import { Outlet, createRootRouteWithContext } from '@tanstack/react-router'
import type { QueryClient } from '@tanstack/react-query'
import { useLang } from '@/lib/i18n'

interface RouterContext {
  queryClient: QueryClient
}

export const Route = createRootRouteWithContext<RouterContext>()({
  component: Root,
})

/** Cambiar de idioma vuelve a montar las vistas: todos los textos se recalculan. */
function Root() {
  const lang = useLang()
  return (
    <div key={lang} className="min-h-dvh bg-background text-foreground">
      <Outlet />
    </div>
  )
}

import { createFileRoute, redirect } from '@tanstack/react-router'
import { z } from 'zod'
import { meQuery, setupQuery } from '@/lib/api'
import { LoginForm } from '@/features/auth/LoginForm'
import { Logo } from '@/features/shell/Logo'
import { LanguageSelect } from '@/features/shell/LanguageSelect'
import { ThemeSelect } from '@/features/shell/ThemeSelect'

export const Route = createFileRoute('/login')({
  validateSearch: z.object({ redirect: z.string().optional() }),
  beforeLoad: async ({ context, search }) => {
    const { needed } = await context.queryClient.ensureQueryData(setupQuery)
    if (needed) throw redirect({ to: '/setup' })
    const me = await context.queryClient.ensureQueryData(meQuery)
    if (me) throw redirect({ to: search.redirect ?? '/' })
  },
  component: LoginPage,
})

function LoginPage() {
  const { redirect: target } = Route.useSearch()
  return (
    <main className="flex min-h-dvh flex-col items-center justify-center gap-6 px-4 py-8">
      <Logo />
      <LoginForm redirectTo={target ?? '/'} />
      <div className="flex flex-wrap justify-center gap-2">
        <LanguageSelect className="w-40" />
        <ThemeSelect className="w-40" />
      </div>
    </main>
  )
}

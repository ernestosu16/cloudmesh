import { createFileRoute, redirect } from '@tanstack/react-router'
import { setupQuery } from '@/lib/api'
import { SetupForm } from '@/features/auth/SetupForm'
import { Logo } from '@/features/shell/Logo'
import { LanguageSelect } from '@/features/shell/LanguageSelect'
import { ThemeSelect } from '@/features/shell/ThemeSelect'

export const Route = createFileRoute('/setup')({
  beforeLoad: async ({ context }) => {
    const { needed } = await context.queryClient.fetchQuery(setupQuery)
    if (!needed) throw redirect({ to: '/login' })
  },
  component: () => (
    <main className="flex min-h-dvh flex-col items-center justify-center gap-6 px-4 py-8">
      <Logo />
      <SetupForm />
      <div className="flex flex-wrap justify-center gap-2">
        <LanguageSelect className="w-40" />
        <ThemeSelect className="w-40" />
      </div>
    </main>
  ),
})

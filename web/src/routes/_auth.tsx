import { Outlet, createFileRoute, redirect } from '@tanstack/react-router'
import { meQuery, setupQuery } from '@/lib/api'
import { AppShell } from '@/features/auth/AppShell'

/** Layout sin path: todo lo que cuelga de _auth exige sesión. */
export const Route = createFileRoute('/_auth')({
  beforeLoad: async ({ context, location }) => {
    const me = await context.queryClient.ensureQueryData(meQuery)
    if (!me) {
      const { needed } = await context.queryClient.ensureQueryData(setupQuery)
      if (needed) throw redirect({ to: '/setup' })
      throw redirect({ to: '/login', search: { redirect: location.href } })
    }
    return { user: me }
  },
  component: () => (
    <AppShell>
      <Outlet />
    </AppShell>
  ),
})

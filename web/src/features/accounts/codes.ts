import type { Account } from '@/lib/api'

/** Colores de cuenta, en el orden del pool. */
export const accountColors = ['bg-chart-1', 'bg-chart-2', 'bg-chart-3', 'bg-chart-4', 'bg-chart-5', 'bg-secondary-foreground']

/** Código corto por cuenta: inicial del proveedor y su número (M1, M2, G1). */
export function accountCodes(accounts: Account[]) {
  const seen = new Map<string, number>()
  return new Map(
    accounts.map((a) => {
      const n = (seen.get(a.provider) ?? 0) + 1
      seen.set(a.provider, n)
      return [a.id, `${a.provider[0]?.toUpperCase() ?? '?'}${n}`]
    }),
  )
}

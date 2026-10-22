import type { Account, Settings } from '@/lib/api'

/** Espacio útil del pool: con franjas k+m solo k de cada k+m bytes son datos. */
export function usableBytes(quota: { total: number; accounts: Account[] }, settings: Settings): number {
  const pool = quota.accounts.filter((a) => a.enabled && a.status === 'active')
  const k = settings.data_shards
  const m = settings.parity_shards
  const striped = k + m > 1 && pool.length >= k + m
  return striped ? (quota.total * k) / (k + m) : quota.total
}

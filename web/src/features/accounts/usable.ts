import type { Account, Settings } from '@/lib/api'

/**
 * Espacio útil del pool: con franjas k+m solo k de cada k+m bytes son datos.
 * Lo ocupado (bloques con su paridad, tal como están en las cuentas) se
 * escala igual, para que «usado de útil» no compare bytes brutos con netos.
 */
export function usableSpace(quota: { used: number; total: number; accounts: Account[] }, settings: Settings): { used: number; total: number } {
  const pool = quota.accounts.filter((a) => a.enabled && a.status === 'active')
  const k = settings.data_shards
  const m = settings.parity_shards
  const ratio = k + m > 1 && pool.length >= k + m ? k / (k + m) : 1
  return { used: quota.used * ratio, total: quota.total * ratio }
}

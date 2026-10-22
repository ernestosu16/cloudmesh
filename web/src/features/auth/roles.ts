import { redirect } from '@tanstack/react-router'
import type { Role, User } from '@/lib/api'
import { t } from '@/lib/i18n'

const level: Record<Role, number> = { reader: 1, editor: 2, admin: 3 }

/** Mismo criterio que el servidor: reader < editor < admin. */
export const can = (user: Pick<User, 'role'>, role: Role) => (level[user.role] ?? 0) >= level[role]

// Getters: el texto sale en el idioma activo cada vez que se lee.
export const roleLabels: Record<Role, string> = {
  get admin() {
    return t('Administrador')
  },
  get editor() {
    return t('Editor')
  },
  get reader() {
    return t('Lector')
  },
}

export const roleHints: Record<Role, string> = {
  get admin() {
    return t('Todo: cuentas de almacenamiento, usuarios, ajustes, buckets S3 y mantenimiento.')
  },
  get editor() {
    return t('Sube, organiza y comparte archivos; usa la papelera.')
  },
  get reader() {
    return t('Ve y descarga; no puede subir, cambiar, compartir ni borrar.')
  },
}

/** beforeLoad de una ruta que exige un rol: sin él, vuelve a Mi espacio. */
export const requireRole =
  (role: Role) =>
  ({ context }: { context: { user: User } }) => {
    if (!can(context.user, role)) throw redirect({ to: '/', search: {} })
  }

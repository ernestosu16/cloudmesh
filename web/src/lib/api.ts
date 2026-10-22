import { queryOptions } from '@tanstack/react-query'
import { t } from '@/lib/i18n'

/** Error devuelto por la API en formato {error:{code,message}}. */
export class ApiError extends Error {
  status: number
  code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

/** fetch tipado contra /api con cookies de sesión y errores normalizados. */
export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`/api${path}`, {
    credentials: 'same-origin',
    ...init,
    headers: {
      Accept: 'application/json',
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...init?.headers,
    },
  })
  if (!res.ok) {
    let code = 'http_error'
    let message = res.statusText
    try {
      const body = (await res.json()) as { error?: { code: string; message: string } }
      if (body.error) ({ code, message } = body.error)
    } catch {
      // cuerpo no JSON: se mantiene statusText
    }
    throw new ApiError(res.status, code, message)
  }
  if (res.status === 204) return undefined as T
  return res.json() as Promise<T>
}

export interface Health {
  status: string
}

export const getHealth = () => api<Health>('/healthz')

// ---- Auth -----------------------------------------------------------------

/** Admin lo gestiona todo; editor sube, organiza, comparte y usa la papelera; reader ve y descarga. */
export type Role = 'admin' | 'editor' | 'reader'

export interface User {
  id: number
  email: string
  /** Nombre visible; vacío muestra el email. */
  name: string
  created_at: number
  totp_enabled: boolean
  /** Códigos de recuperación sin usar (con 2FA activa). */
  recovery_codes_left: number
  role: Role
  /** 0: sin límite. */
  quota_bytes: number
  /** Lo que ocupan sus archivos, papelera incluida (en /me y en la lista de usuarios). */
  used_bytes: number
}

/** code: TOTP, solo para usuarios con verificación en dos pasos (si falta, ApiError totp_required). */
export const login = (email: string, password: string, code?: string) =>
  api<User>('/auth/login', { method: 'POST', body: JSON.stringify({ email, password, code }) })

export const logout = () => api<void>('/auth/logout', { method: 'POST' })

/** Usuario actual; null si no hay sesión (401). Otros errores se propagan. */
export const meQuery = queryOptions({
  queryKey: ['auth', 'me'],
  queryFn: async (): Promise<User | null> => {
    try {
      return await api<User>('/auth/me')
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) return null
      throw err
    }
  },
  staleTime: 5 * 60_000,
  retry: false,
})

export const updateMe = (name: string) => api<User>('/auth/me', { method: 'PATCH', body: JSON.stringify({ name }) })

export interface TOTPSetup {
  secret: string
  url: string
}

export const totpSetup = () => api<TOTPSetup>('/auth/2fa/setup', { method: 'POST' })

/** Activa la 2FA y devuelve los códigos de recuperación, que solo se muestran esta vez. */
export const totpEnable = (code: string) =>
  api<{ recovery_codes: string[] }>('/auth/2fa/enable', { method: 'POST', body: JSON.stringify({ code }) })

/** Códigos de recuperación nuevos; los anteriores dejan de valer. */
export const regenerateRecoveryCodes = (password: string, code: string) =>
  api<{ recovery_codes: string[] }>('/auth/2fa/recovery', { method: 'POST', body: JSON.stringify({ password, code }) })

export const totpDisable = (password: string, code: string) =>
  api<void>('/auth/2fa/disable', { method: 'POST', body: JSON.stringify({ password, code }) })

export const resetUserTotp = (id: number) => api<void>(`/users/${id}/2fa`, { method: 'DELETE' })

export const changePassword = (current_password: string, new_password: string) =>
  api<void>('/auth/password', { method: 'POST', body: JSON.stringify({ current_password, new_password }) })

export const sessionsQuery = queryOptions({
  queryKey: ['auth', 'sessions'],
  queryFn: () => api<{ active: number }>('/auth/sessions'),
})

export const logoutOthers = () => api<void>('/auth/sessions', { method: 'DELETE' })

// ---- Usuarios ---------------------------------------------------------------

export const usersQuery = queryOptions({
  queryKey: ['users'],
  queryFn: () => api<User[]>('/users'),
})

export interface Invitation {
  id: number
  email: string
  role: Role
  quota_bytes: number
  created_at: number
  expires_at: number
  created_by_email?: string
}

export const invitationsQuery = queryOptions({
  queryKey: ['users', 'invitations'],
  queryFn: () => api<{ invitations: Invitation[]; mail_enabled: boolean }>('/invitations'),
})

export interface NewInvitation {
  invitation: Invitation
  /** Enlace de un solo uso; solo viaja en esta respuesta. */
  url: string
  emailed: boolean
  email_error?: string
}

export const createInvitation = (email: string, role: Role, quota_bytes: number) =>
  api<NewInvitation>('/invitations', { method: 'POST', body: JSON.stringify({ email, role, quota_bytes }) })

export const revokeInvitation = (id: number) => api<void>(`/invitations/${id}`, { method: 'DELETE' })

export const publicInvitationQuery = (token: string) =>
  queryOptions({
    queryKey: ['public-invitation', token],
    queryFn: () => api<{ email: string; role: Role; expires_at: number }>(`/public/invitations/${token}`),
    retry: false,
  })

export const acceptInvitation = (token: string, name: string, password: string) =>
  api<User>(`/public/invitations/${token}`, { method: 'POST', body: JSON.stringify({ name, password }) })

export const createUser = (email: string, password: string, role: Role, quota_bytes: number) =>
  api<User>('/users', { method: 'POST', body: JSON.stringify({ email, password, role, quota_bytes }) })

export const updateUserAccess = (id: number, role: Role, quota_bytes: number) =>
  api<User>(`/users/${id}`, { method: 'PATCH', body: JSON.stringify({ role, quota_bytes }) })

export const deleteUser = (id: number) => api<void>(`/users/${id}`, { method: 'DELETE' })

// ---- Cuentas --------------------------------------------------------------

export type AccountStatus = 'active' | 'reconnect_required'

/** Cuánto puede llenar CloudMesh de una cuenta: todo, dejar un % libre o un tope en bytes. */
export type ContribMode = 'all' | 'reserve' | 'limit'

export interface Contribution {
  contrib_mode: ContribMode
  contrib_value: number
}

export interface Account {
  id: number
  provider: string
  label: string
  email: string
  status: AccountStatus
  enabled: boolean
  priority: number
  /** Bytes que CloudMesh ocupa en la cuenta. */
  quota_used: number
  /** Lo usado en la cuenta entera al último sondeo, también lo ajeno a CloudMesh. */
  provider_used: number
  quota_total: number
  /** Hasta dónde puede llenar CloudMesh la cuenta según su aporte. */
  capacity: number
  /** Parte del aporte sin hueco: lo propio o lo que la cuenta real ya no admite. */
  occupied: number
  contrib_mode: ContribMode
  contrib_value: number
  last_error?: string
  last_sync_at: number
  created_at: number
  /** Unix: el proveedor limitó la transferencia y hasta entonces se evita leer de la cuenta. */
  throttled_until?: number
}

export interface QuotaSummary {
  used: number
  total: number
  accounts: Account[]
}

export interface ConnectInput extends Partial<Contribution> {
  provider: string
  label?: string
  email: string
  password: string
  mfa?: string
}

export interface AccountSettings extends Contribution {
  label: string
  priority: number
  enabled: boolean
}

export interface TestResult {
  email: string
  quota_used: number
  quota_total: number
  already_linked: boolean
}

export const quotaQuery = queryOptions({
  queryKey: ['quota'],
  queryFn: () => api<QuotaSummary>('/quota'),
})

export const connectAccount = (input: ConnectInput) =>
  api<Account>('/accounts', { method: 'POST', body: JSON.stringify(input) })

/** Comprueba las credenciales sin dar de alta; el alta posterior reutiliza esa sesión. */
export const testAccount = (input: Omit<ConnectInput, 'label'>) =>
  api<TestResult>('/accounts/test', { method: 'POST', body: JSON.stringify(input) })

export const reconnectAccount = (id: number, password: string, mfa?: string) =>
  api<Account>(`/accounts/${id}/reconnect`, { method: 'POST', body: JSON.stringify({ password, mfa }) })

export const updateAccount = (id: number, settings: AccountSettings) =>
  api<Account>(`/accounts/${id}`, { method: 'PATCH', body: JSON.stringify(settings) })

export const deleteAccount = (id: number) => api<void>(`/accounts/${id}`, { method: 'DELETE' })

export const syncAccount = (id: number) =>
  api<{ account: Account; result: SyncResult }>(`/accounts/${id}/sync`, { method: 'POST' })

// ---- Drive virtual --------------------------------------------------------

export interface Folder {
  id: number
  parent_id: number
  name: string
  created_at: number
  /** Solo la ven los administradores y los usuarios con acceso. */
  restricted?: boolean
}

export type AccessLevel = 'read' | 'write'

export interface FolderGrant {
  user_id: number
  level: AccessLevel
  email?: string
  name?: string
}

export interface FolderAccess {
  restricted: boolean
  grants: FolderGrant[]
}

export const folderAccessQuery = (id: number) =>
  queryOptions({ queryKey: ['folder-access', id], queryFn: () => api<FolderAccess>(`/folders/${id}/access`) })

export const setFolderAccess = (id: number, access: FolderAccess) =>
  api<FolderAccess>(`/folders/${id}/access`, { method: 'PUT', body: JSON.stringify(access) })

export type FileStatus = 'uploading' | 'ready' | 'degraded' | 'lost'

export interface DriveFile {
  id: number
  folder_id: number
  name: string
  size: number
  mime: string
  mtime: number
  layout: 'whole' | 'rs'
  data_shards: number
  parity_shards: number
  status: FileStatus
  created_at: number
  /** > 0: en la papelera. */
  deleted_at?: number
}

export interface Listing {
  folder: Folder
  breadcrumbs: Folder[]
  folders: Folder[]
  files: DriveFile[]
  /** Se puede subir y cambiar aquí (además de lo que permita el rol). */
  writable: boolean
}

export interface SearchResult {
  folders: Folder[]
  files: DriveFile[]
}

export interface SyncResult {
  added: number
  updated: number
  removed: number
}

export const ROOT_FOLDER = 0

export const folderQuery = (id: number) =>
  queryOptions({
    queryKey: ['folder', id],
    queryFn: () => api<Listing>(`/folders/${id}`),
  })

export const searchQuery = (q: string) =>
  queryOptions({
    queryKey: ['search', q],
    queryFn: () => api<SearchResult>(`/search?q=${encodeURIComponent(q)}`),
    enabled: q.trim().length > 0,
  })

/** Con existing, si ya hay una carpeta con ese nombre la devuelve en vez de fallar. */
export const createFolder = (parent_id: number, name: string, existing = false) =>
  api<Folder>('/folders', { method: 'POST', body: JSON.stringify({ parent_id, name, existing }) })

export const updateFolder = (id: number, patch: { name?: string; parent_id?: number }) =>
  api<Folder>(`/folders/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })

export const deleteFolder = (id: number) => api<void>(`/folders/${id}`, { method: 'DELETE' })

// ---- Configuración inicial -------------------------------------------------

export const setupQuery = queryOptions({
  queryKey: ['setup'],
  queryFn: () => api<{ needed: boolean }>('/setup'),
  staleTime: 60_000,
  retry: false,
})

export const completeSetup = (code: string, email: string, password: string) =>
  api<User>('/setup', { method: 'POST', body: JSON.stringify({ code, email, password }) })

// ---- Archivos --------------------------------------------------------------

export interface FileChunk {
  stripe: number
  idx: number
  kind: 'data' | 'parity'
  account_id: number
  account_label: string
  account_status: AccountStatus
  size: number
}

export interface FileDetail {
  file: DriveFile
  chunks: FileChunk[]
}

export const providersQuery = queryOptions({
  queryKey: ['providers'],
  queryFn: () => api<string[]>('/providers'),
  staleTime: Infinity,
})

export const fileDetailQuery = (id: number) =>
  queryOptions({ queryKey: ['file', id], queryFn: () => api<FileDetail>(`/files/${id}`) })

export const fileContentUrl = (id: number, download = false) =>
  `/api/files/${id}/content${download ? '?download=1' : ''}`

export const updateFile = (id: number, patch: { name?: string; folder_id?: number }) =>
  api<DriveFile>(`/files/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })

export const deleteFile = (id: number) => api<void>(`/files/${id}`, { method: 'DELETE' })

/**
 * Sube un archivo con XMLHttpRequest para tener progreso. Devuelve el archivo
 * creado; rechaza con ApiError en errores de la API.
 */
export function uploadFile(
  folderId: number,
  file: File,
  onProgress: (loaded: number) => void,
  signal?: AbortSignal,
): Promise<DriveFile> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    const url = `/api/files?folder=${folderId}&name=${encodeURIComponent(file.name)}`
    xhr.open('POST', url)
    xhr.responseType = 'json'
    xhr.upload.onprogress = (e) => onProgress(e.loaded)
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(xhr.response as DriveFile)
      } else {
        const body = xhr.response as { error?: { code: string; message: string } } | null
        reject(new ApiError(xhr.status, body?.error?.code ?? 'http_error', body?.error?.message ?? xhr.statusText))
      }
    }
    xhr.onerror = () => reject(new Error(t('No se pudo conectar con el servidor')))
    xhr.onabort = () => reject(new DOMException('Cancelada', 'AbortError'))
    signal?.addEventListener('abort', () => xhr.abort())
    xhr.send(file)
  })
}

// ---- Ajustes ---------------------------------------------------------------

export type RoutingPolicy = 'most_free' | 'round_robin' | 'priority'

export interface Settings {
  routing_policy: RoutingPolicy
  data_shards: number
  parity_shards: number
  chunk_size: number
}

export const settingsQuery = queryOptions({
  queryKey: ['settings'],
  queryFn: () => api<Settings>('/settings'),
})

export const updateSettings = (s: Settings) =>
  api<Settings>('/settings', { method: 'PATCH', body: JSON.stringify(s) })

// ---- Salud del pool --------------------------------------------------------

export interface AccountHealth {
  account_id: number
  chunks: number
  missing: number
}

export interface Health {
  files: Record<FileStatus, number>
  missing_chunks: number
  /** Bloques anteriores al cifrado que el mantenimiento aún no ha convertido. */
  plain_chunks: number
  /** Archivos enteros encontrados en el proveedor por el sync: no se cifran. */
  whole_files: number
  accounts: AccountHealth[]
  degraded_file_ids: number[]
  degraded_files: DriveFile[]
  manifest_written_at: number
}

export interface ScrubResult {
  account_id: number
  checked: number
  missing: number
  restored: number
  orphans: number
  files: number
}

export interface RebuildResult {
  file_id: number
  rebuilt: number
  pending: number
  status: FileStatus
  reason?: string
}

export const healthQuery = queryOptions({
  queryKey: ['health'],
  queryFn: () => api<Health>('/health'),
  staleTime: 10_000,
})

export const runScrub = () => api<ScrubResult[]>('/health/scrub', { method: 'POST' })
export const runRebuildAll = () => api<RebuildResult[]>('/health/rebuild', { method: 'POST' })
export const runRebuildFile = (id: number) => api<RebuildResult>(`/files/${id}/rebuild`, { method: 'POST' })
export const manifestExportUrl = '/api/health/manifest/export'

export const writeManifest = () =>
  api<{ accounts: number; written_at: number }>('/health/manifest', { method: 'POST' })

/** Vacía el pool: borra archivos, carpetas, bloques, enlaces y buckets. */
export const formatPool = () => api<{ files: number }>('/health/format', { method: 'POST' })

// ---- Enlaces compartidos ---------------------------------------------------

export type ShareKind = 'file' | 'folder'

export interface Share {
  id: number
  token: string
  kind: ShareKind
  target_id: number
  name: string
  parent_id: number
  size: number
  has_password: boolean
  /** Informativo: los enlaces no tienen límite. */
  downloads: number
  created_at: number
  created_by: number
  created_by_email?: string
}

/** Como en Mega: solo lectura y contraseña opcional. undefined conserva la actual, '' la quita. */
export interface ShareOptions {
  password?: string
}

export const sharesQuery = queryOptions({
  queryKey: ['shares'],
  queryFn: () => api<Share[]>('/shares'),
})

export const createShare = (kind: ShareKind, target_id: number, opt: ShareOptions) =>
  api<Share>('/shares', { method: 'POST', body: JSON.stringify({ kind, target_id, ...opt }) })

export const updateShare = (id: number, opt: ShareOptions) =>
  api<Share>(`/shares/${id}`, { method: 'PATCH', body: JSON.stringify(opt) })

export const deleteShare = (id: number) => api<void>(`/shares/${id}`, { method: 'DELETE' })

export const shareUrl = (token: string) => `${window.location.origin}/s/${token}`


// ---- Enlace público (sin sesión) --------------------------------------------

export interface PublicShare {
  kind: ShareKind
  name: string
  size: number
  locked: boolean
  file?: DriveFile
}

export interface PublicListing {
  path: Folder[]
  folders: Folder[]
  files: DriveFile[]
}

const publicBase = (token: string) => `/public/shares/${encodeURIComponent(token)}`

export const publicShareQuery = (token: string) =>
  queryOptions({
    queryKey: ['public-share', token],
    queryFn: () => api<PublicShare>(publicBase(token)),
    retry: false,
  })

export const publicFolderQuery = (token: string, folder: number) =>
  queryOptions({
    queryKey: ['public-share', token, 'folder', folder],
    queryFn: () => api<PublicListing>(`${publicBase(token)}/folders/${folder}`),
    retry: false,
  })

export const unlockShare = (token: string, password: string) =>
  api<void>(`${publicBase(token)}/unlock`, { method: 'POST', body: JSON.stringify({ password }) })

export const publicContentUrl = (token: string, fileId: number, download = false) =>
  `/api${publicBase(token)}/files/${fileId}/content${download ? '?download=1' : ''}`

export const publicZipUrl = (token: string, folder = 0) => `/api${publicBase(token)}/zip?folder=${folder}`

// ---- Papelera ---------------------------------------------------------------

export interface TrashItem {
  file: DriveFile
  /** Vacío con folder_exists es la raíz; sin folder_exists restaurar lo deja en la raíz. */
  folder_name: string
  folder_exists: boolean
  purge_at: number
}

export const trashQuery = queryOptions({
  queryKey: ['trash'],
  queryFn: () => api<TrashItem[]>('/trash'),
})

export const restoreTrash = (id: number) => api<DriveFile>(`/trash/${id}/restore`, { method: 'POST' })

export const purgeTrash = (id: number) => api<void>(`/trash/${id}`, { method: 'DELETE' })

export const emptyTrash = () => api<{ purged: number }>('/trash', { method: 'DELETE' })

// ---- API S3 ------------------------------------------------------------------

export interface S3Info {
  enabled: boolean
  /** URL pública configurada; vacía si no se definió CLOUDMESH_S3_URL. */
  endpoint: string
  region: string
  /** Dominio del estilo virtual-host (<bucket>.<dominio>); vacío si solo hay estilo de ruta. */
  domain: string
}

/** Regla CORS en el formato del panel de R2 (el mismo que PutBucketCors). */
export interface CORSRule {
  ID?: string
  AllowedOrigins: string[]
  AllowedMethods: string[]
  AllowedHeaders?: string[]
  ExposeHeaders?: string[]
  MaxAgeSeconds?: number
}

/** Como en R2: borrar objetos del prefijo y cancelar subidas a medias pasados N días (0: no). */
export interface LifecycleRule {
  id: string
  enabled: boolean
  prefix: string
  expire_days: number
  abort_days: number
}

export interface BucketConfig {
  /** Lectura de objetos sin firma; listar sigue pidiendo llave. */
  public: boolean
  cors: CORSRule[]
  lifecycle: LifecycleRule[]
}

export interface Bucket extends BucketConfig {
  id: number
  name: string
  folder_id: number
  folder_path: string
  created_at: number
}

/** Igual que en Cloudflare R2. */
export type S3Permission = 'admin_rw' | 'admin_ro' | 'object_rw' | 'object_ro'

export interface S3Key {
  id: number
  access_key_id: string
  name: string
  permission: S3Permission
  /** Vacío: todos los buckets. */
  buckets: string[]
  created_at: number
  created_by_email?: string
  last_used_at: number
}

export interface NewS3Key extends S3Key {
  secret_access_key: string
}

export const s3InfoQuery = queryOptions({
  queryKey: ['s3', 'info'],
  queryFn: () => api<S3Info>('/s3'),
  staleTime: Infinity,
})

export const bucketsQuery = queryOptions({
  queryKey: ['s3', 'buckets'],
  queryFn: () => api<Bucket[]>('/s3/buckets'),
})

export const s3KeysQuery = queryOptions({
  queryKey: ['s3', 'keys'],
  queryFn: () => api<S3Key[]>('/s3/keys'),
})

/** folder_id 0 crea la carpeta Buckets/<nombre>. */
export const createBucket = (name: string, folder_id: number) =>
  api<Bucket>('/s3/buckets', { method: 'POST', body: JSON.stringify({ name, folder_id }) })

export const configureBucket = (id: number, config: BucketConfig) =>
  api<Bucket>(`/s3/buckets/${id}`, { method: 'PATCH', body: JSON.stringify(config) })

export const unlinkBucket = (id: number) => api<void>(`/s3/buckets/${id}`, { method: 'DELETE' })

export const createS3Key = (name: string, permission: S3Permission, bucket_ids: number[]) =>
  api<NewS3Key>('/s3/keys', { method: 'POST', body: JSON.stringify({ name, permission, bucket_ids }) })

/** Nuevo secreto para la llave; el anterior deja de valer. */
export const regenerateS3Key = (id: number) => api<NewS3Key>(`/s3/keys/${id}/regenerate`, { method: 'POST' })

export const deleteS3Key = (id: number) => api<void>(`/s3/keys/${id}`, { method: 'DELETE' })

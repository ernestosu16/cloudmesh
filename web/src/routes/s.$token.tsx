import { useState, type FormEvent } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { z } from 'zod'
import { Download, Eye, Folder, Lock } from 'lucide-react'
import {
  ApiError,
  publicContentUrl,
  publicFolderQuery,
  publicShareQuery,
  publicZipUrl,
  unlockShare,
  type DriveFile,
  type PublicShare,
} from '@/lib/api'
import { formatBytes } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Logo } from '@/features/shell/Logo'
import { ErrorText } from '@/features/shell/Page'
import { FileTypeIcon } from '@/features/drive/icons'
import { PreviewDialog } from '@/features/drive/FileDialogs'
import { LanguageSelect } from '@/features/shell/LanguageSelect'
import { ThemeSelect } from '@/features/shell/ThemeSelect'
import { t } from '@/lib/i18n'

export const Route = createFileRoute('/s/$token')({
  validateSearch: z.object({ folder: z.number().int().nonnegative().optional().catch(undefined) }),
  component: PublicSharePage,
})

function PublicSharePage() {
  const { token } = Route.useParams()
  const share = useQuery(publicShareQuery(token))

  return (
    <div className="flex min-h-dvh flex-col">
      <header className="border-b bg-card">
        <div className="mx-auto flex max-w-6xl items-center justify-between gap-3 px-4 py-3.5 sm:px-6">
          <Logo />
          <span className="flex items-center gap-2 sm:gap-3">
            <span className="hidden text-[13px] text-muted-foreground sm:inline">{t('Enlace compartido')}</span>
            <LanguageSelect className="w-auto" />
            <ThemeSelect className="w-auto" compact />
          </span>
        </div>
      </header>
      {share.isPending ? (
        <p className="p-10 text-center text-sm text-muted-foreground">{t('Cargando…')}</p>
      ) : share.error ? (
        <Unavailable error={share.error} />
      ) : share.data.locked ? (
        <Unlock token={token} share={share.data} />
      ) : (
        <Content token={token} share={share.data} />
      )}
    </div>
  )
}

function Unavailable({ error }: { error: unknown }) {
  const code = error instanceof ApiError ? error.code : ''
  const message = code === 'not_found' ? t('Este enlace no existe o fue desactivado.') : t('No se pudo abrir el enlace.')
  return (
    <main className="flex flex-1 items-center justify-center px-4 py-16">
      <div className="grid grid-cols-1 max-w-md gap-2 rounded-2xl border bg-card p-8 text-center">
        <h1 className="text-2xl font-bold">{t('Enlace no disponible')}</h1>
        <p className="text-secondary-foreground">{message} {t('Pide a quien te lo envió uno nuevo.')}</p>
      </div>
    </main>
  )
}

function Unlock({ token, share }: { token: string; share: PublicShare }) {
  const queryClient = useQueryClient()
  const [password, setPassword] = useState('')
  const unlock = useMutation({
    mutationFn: () => unlockShare(token, password),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['public-share', token] }),
  })
  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    unlock.mutate()
  }
  return (
    <main className="flex flex-1 items-center justify-center px-4 py-16">
      <form onSubmit={onSubmit} className="grid grid-cols-1 w-full max-w-md gap-4.5 rounded-2xl border bg-card p-6 sm:p-8">
        <span className="flex size-12 items-center justify-center rounded-xl bg-accent text-primary">
          <Lock className="size-6" aria-hidden />
        </span>
        <h1 className="text-[26px] font-bold">{share.kind === 'folder' ? t('Esta carpeta está protegida') : t('Este archivo está protegido')}</h1>
        <p className="leading-relaxed wrap-anywhere text-secondary-foreground">
          {t('Te compartieron «{nombre}». Introduce la contraseña que te enviaron para verlo.', { nombre: share.name })}
        </p>
        <div className="grid grid-cols-1 gap-1.5">
          <Label htmlFor="share-password">{t('Contraseña')}</Label>
          <Input id="share-password" type="password" autoFocus required value={password} onChange={(e) => setPassword(e.target.value)} />
        </div>
        <ErrorText error={unlock.error} />
        <Button type="submit" size="lg" disabled={unlock.isPending}>
          {t('Desbloquear')}
        </Button>
      </form>
    </main>
  )
}

function Content({ token, share }: { token: string; share: PublicShare }) {
  const [preview, setPreview] = useState<DriveFile | null>(null)
  const contentUrl = (id: number, download?: boolean) => publicContentUrl(token, id, download)
  return (
    <main className="mx-auto grid w-full max-w-6xl gap-6 px-4 py-8 sm:px-6 sm:py-10">
      {share.kind === 'file' && share.file ? (
        <SingleFile file={share.file} contentUrl={contentUrl} onPreview={setPreview} />
      ) : (
        <FolderView token={token} share={share} contentUrl={contentUrl} onPreview={setPreview} />
      )}
      <PreviewDialog file={preview} contentUrl={contentUrl} onClose={() => setPreview(null)} />
    </main>
  )
}

function SingleFile({
  file,
  contentUrl,
  onPreview,
}: {
  file: DriveFile
  contentUrl: (id: number, download?: boolean) => string
  onPreview: (f: DriveFile) => void
}) {
  return (
    <div className="mx-auto grid w-full max-w-lg gap-5 rounded-2xl border bg-card p-6 sm:p-8">
      <span className="flex size-14 items-center justify-center rounded-xl bg-secondary text-muted-foreground">
        <FileTypeIcon mime={file.mime} className="size-7" />
      </span>
      <div className="grid grid-cols-1 gap-1.5">
        <h1 className="text-[clamp(24px,4vw,32px)] font-bold wrap-anywhere">{file.name}</h1>
        <p className="flex flex-wrap gap-x-2 text-sm text-muted-foreground">
          <span>{formatBytes(file.size)}</span>
        </p>
      </div>
      <div className="flex flex-wrap gap-2.5">
        <Button asChild size="lg">
          <a href={contentUrl(file.id, true)} download={file.name}>
            <Download /> {t('Descargar')}
          </a>
        </Button>
        <Button variant="outline" size="lg" onClick={() => onPreview(file)}>
          <Eye /> {t('Vista previa')}
        </Button>
      </div>
    </div>
  )
}

/** Miniatura con la imagen misma, hasta 15 MB. */
const thumbnail = (f: DriveFile) => f.mime.startsWith('image/') && f.mime !== 'image/svg+xml' && f.size <= 15 << 20

const tints = [
  'bg-[#E4E9F1] dark:bg-[#1c2533]',
  'bg-[#E9E4F1] dark:bg-[#251f33]',
  'bg-[#F1EAE4] dark:bg-[#2e251d]',
  'bg-[#E4F1EC] dark:bg-[#1a2c26]',
  'bg-[#F1E4E8] dark:bg-[#2e1f25]',
]

function FolderView({
  token,
  share,
  contentUrl,
  onPreview,
}: {
  token: string
  share: PublicShare
  contentUrl: (id: number, download?: boolean) => string
  onPreview: (f: DriveFile) => void
}) {
  const { folder = 0 } = Route.useSearch()
  const listing = useQuery(publicFolderQuery(token, folder))
  if (listing.isPending) return <p className="text-sm text-muted-foreground">{t('Cargando…')}</p>
  if (listing.error) return <ErrorText error={listing.error} />
  const { path, folders, files } = listing.data
  const current = path[path.length - 1]
  const n = folders.length + files.length
  const size = [...folders, ...files].reduce((a, f) => a + (f.size ?? 0), 0)

  return (
    <>
      <div className="flex flex-wrap items-end justify-between gap-5">
        <div className="grid grid-cols-1 min-w-0 gap-2">
          {path.length > 1 && (
            <nav aria-label={t('Ruta')} className="flex flex-wrap items-center gap-1.5 text-sm">
              {path.slice(0, -1).map((f, i) => (
                <span key={f.id} className="flex min-w-0 items-center gap-1.5">
                  <Link to="/s/$token" params={{ token }} search={i === 0 ? {} : { folder: f.id }} className="max-w-48 truncate text-muted-foreground hover:text-foreground">
                    {f.name}
                  </Link>
                  <span className="text-muted-foreground/70" aria-hidden>
                    /
                  </span>
                </span>
              ))}
            </nav>
          )}
          <h1 className="text-[clamp(28px,4vw,40px)] font-bold wrap-anywhere">{current?.name ?? share.name}</h1>
          <p className="flex flex-wrap gap-x-2 text-sm text-muted-foreground">
            <span>
              {n === 1 ? t('1 elemento') : t('{n} elementos', { n })}
              {size > 0 && ` · ${formatBytes(size)}`}
            </span>
            </p>
        </div>
        {n > 0 && (
          <Button asChild size="lg">
            <a href={publicZipUrl(token, folder)} download>
              <Download /> {t('Descargar todo (ZIP)')}
            </a>
          </Button>
        )}
      </div>

      {n === 0 ? (
        <p className="rounded-2xl border bg-card p-10 text-center text-sm text-muted-foreground">{t('Carpeta vacía.')}</p>
      ) : (
        <ul className="grid grid-cols-[repeat(auto-fill,minmax(160px,1fr))] gap-4">
          {folders.map((f) => (
            <li key={`d${f.id}`}>
              <Link
                to="/s/$token"
                params={{ token }}
                search={{ folder: f.id }}
                className="flex h-full flex-col overflow-hidden rounded-2xl border bg-card hover:border-primary"
              >
                <span className="flex h-28 items-center justify-center bg-muted text-muted-foreground">
                  <Folder className="size-8" aria-hidden />
                </span>
                <span className="grid grid-cols-1 min-w-0 px-3.5 py-1.5">
                  <span className="truncate text-sm font-medium">{f.name}</span>
                  {f.size !== undefined && <span className="font-mono text-xs text-muted-foreground">{formatBytes(f.size)}</span>}
                </span>
              </Link>
            </li>
          ))}
          {files.map((f, i) => (
            <li key={`f${f.id}`} className="flex flex-col overflow-hidden rounded-2xl border bg-card">
              <button type="button" onClick={() => onPreview(f)} aria-label={t('Abrir {nombre}', { nombre: f.name })} className="text-left hover:opacity-90">
                <span className={`flex h-28 items-center justify-center overflow-hidden text-muted-foreground ${tints[i % tints.length]}`}>
                  {thumbnail(f) ? (
                    <img src={contentUrl(f.id)} alt="" loading="lazy" decoding="async" className="size-full object-cover" />
                  ) : (
                    <FileTypeIcon mime={f.mime} className="size-8" />
                  )}
                </span>
              </button>
              <div className="flex items-center gap-1 py-1.5 pr-1 pl-3.5">
                <span className="grid grid-cols-1 min-w-0 flex-1">
                  <span className="truncate text-sm font-medium">{f.name}</span>
                  <span className="font-mono text-xs text-muted-foreground">{formatBytes(f.size)}</span>
                </span>
                <Button asChild variant="ghost" size="icon" aria-label={t('Descargar {nombre}', { nombre: f.name })}>
                  <a href={contentUrl(f.id, true)} download={f.name}>
                    <Download />
                  </a>
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </>
  )
}

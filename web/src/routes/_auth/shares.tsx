import { useRef, useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useSuspenseQuery } from '@tanstack/react-query'
import { cn } from 'cn'
import { sharesQuery, type Share } from '@/lib/api'
import { shortDate } from '@/lib/format'
import { PageHeader, Panel, Tag } from '@/features/shell/Page'
import { ShareEditor } from '@/features/shares/ShareEditor'
import { requireRole } from '@/features/auth/roles'
import { t } from '@/lib/i18n'

export const Route = createFileRoute('/_auth/shares')({
  beforeLoad: requireRole('editor'),
  loader: ({ context }) => context.queryClient.ensureQueryData(sharesQuery),
  component: SharesPage,
})

/** Como en Mega: abierto o con contraseña. */
const Protection = ({ s }: { s: Share }) => (s.has_password ? <Tag tone="accent">{t('Contraseña')}</Tag> : <Tag>{t('Abierto')}</Tag>)

function SharesPage() {
  const { data: shares } = useSuspenseQuery(sharesQuery)
  const [selectedId, setSelectedId] = useState<number | null>(null)
  const selected = shares.find((s) => s.id === selectedId) ?? shares[0]
  const editor = useRef<HTMLElement>(null)
  // En móvil el editor queda debajo de la lista: se lleva a la vista al elegir.
  const select = (id: number) => {
    setSelectedId(id)
    if (!window.matchMedia('(min-width: 768px)').matches) {
      requestAnimationFrame(() => editor.current?.scrollIntoView({ behavior: 'smooth', block: 'start' }))
    }
  }

  return (
    <section className="grid grid-cols-1 gap-6">
      <PageHeader
        title={t('Enlaces compartidos')}
        description={t('Archivos y carpetas accesibles con un enlace de solo lectura, con contraseña si hace falta.')}
      />
      {shares.length === 0 ? (
        <Panel className="text-center text-sm text-muted-foreground">
          {t('Aún no hay enlaces. Comparte un archivo o una carpeta desde')}{' '}
          <Link to="/" search={{}} className="font-semibold text-primary hover:underline">
            {t('Mi espacio')}
          </Link>
          .
        </Panel>
      ) : (
        <div className="flex flex-wrap items-start gap-7">
          <ul className="grid grid-cols-1 w-full gap-2.5 md:hidden">
            {shares.map((s) => {
              const on = s.id === selected?.id
              return (
                <li key={s.id}>
                  <button
                    type="button"
                    aria-pressed={on}
                    onClick={() => select(s.id)}
                    className={cn('grid w-full gap-2 rounded-2xl border bg-card p-4 text-left', on && 'ring-2 ring-primary')}
                  >
                    <span className="flex items-start justify-between gap-3">
                      <span className="grid grid-cols-1 min-w-0">
                        <span className="truncate font-semibold">
                          {s.name}
                          {s.kind === 'folder' && '/'}
                        </span>
                        <span className="truncate font-mono text-xs text-muted-foreground">/s/{s.token}</span>
                      </span>
                      <Protection s={s} />
                    </span>
                    <span className="flex flex-wrap gap-x-3 gap-y-1 text-[13px] text-secondary-foreground">
                      <span>{t('{n} descargas', { n: s.downloads })}</span>
                      <span>{t('Creado el {fecha}', { fecha: shortDate(s.created_at) })}</span>
                    </span>
                  </button>
                </li>
              )
            })}
          </ul>

          <div className="hidden min-w-0 flex-[999_1_26rem] overflow-x-auto rounded-2xl border bg-card md:block">
            <table className="w-full min-w-[480px] border-collapse text-sm">
              <thead>
                <tr className="text-left text-xs tracking-wider text-muted-foreground uppercase">
                  <th className="border-b px-5 py-3.5 font-semibold">{t('Elemento')}</th>
                  <th className="border-b px-3 py-3.5 font-semibold">{t('Protección')}</th>
                  <th className="border-b px-3 py-3.5 font-semibold">{t('Descargas')}</th>
                  <th className="border-b px-3 py-3.5 font-semibold">{t('Creado')}</th>
                </tr>
              </thead>
              <tbody>
                {shares.map((s) => {
                  const on = s.id === selected?.id
                  return (
                    <tr key={s.id} className={cn('border-b border-secondary last:border-0', on && 'bg-accent/50')}>
                      <td className="px-5 py-3.5">
                        <button type="button" aria-pressed={on} onClick={() => select(s.id)} className="grid grid-cols-1 max-w-80 gap-0.5 text-left">
                          <span className="truncate font-semibold hover:underline">
                            {s.name}
                            {s.kind === 'folder' && '/'}
                          </span>
                          <span className="truncate font-mono text-xs text-muted-foreground">/s/{s.token}</span>
                        </button>
                      </td>
                      <td className="px-3 py-3.5">
                        <Protection s={s} />
                      </td>
                      <td className="px-3 py-3.5 font-mono text-[13px]">{s.downloads}</td>
                      <td className="px-3 py-3.5 text-secondary-foreground">{shortDate(s.created_at)}</td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>

          {selected && (
            <Panel ref={editor} aria-label={t('Editar enlace')} className="min-w-0 scroll-mt-20 flex-[1_1_22rem] shadow-[0_24px_60px_-36px_rgba(15,23,32,.4)]">
              <ShareEditor
                key={selected.id}
                target={{ kind: selected.kind, id: selected.target_id, name: selected.name }}
                share={selected}
                onDeleted={() => setSelectedId(null)}
              />
            </Panel>
          )}
        </div>
      )}
    </section>
  )
}

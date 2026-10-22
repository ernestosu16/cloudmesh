import { Fragment } from 'react'
import { Link } from '@tanstack/react-router'
import type { Folder } from '@/lib/api'
import { t } from '@/lib/i18n'

interface Props {
  breadcrumbs: Folder[]
  current: Folder
}

export function Breadcrumbs({ breadcrumbs, current }: Props) {
  const items = [...breadcrumbs, current].filter((f) => f.id !== 0)
  return (
    <nav aria-label={t('Ruta')} className="flex min-w-0 items-center gap-1.5 overflow-x-auto text-[15px]">
      {items.length === 0 ? (
        <span className="shrink-0 font-semibold" aria-current="page">
          {t('Mi espacio')}
        </span>
      ) : (
        <Link to="/" search={{}} className="shrink-0 text-muted-foreground hover:text-foreground">
          {t('Mi espacio')}
        </Link>
      )}
      {items.map((f, i) => {
        const last = i === items.length - 1
        return (
          <Fragment key={f.id}>
            <span className="shrink-0 text-muted-foreground/70" aria-hidden>
              /
            </span>
            {last ? (
              <span className="truncate font-semibold" aria-current="page">
                {f.name}
              </span>
            ) : (
              <Link
                to="/"
                search={{ folder: f.id }}
                className="max-w-40 truncate text-muted-foreground hover:text-foreground"
              >
                {f.name}
              </Link>
            )}
          </Fragment>
        )
      })}
    </nav>
  )
}

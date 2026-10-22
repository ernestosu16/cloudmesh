import { cn } from 'cn'

/** Marca: nube sobre cuadrado azul y el nombre. */
export function Logo({ className, compact }: { className?: string; compact?: boolean }) {
  return (
    <span className={cn('flex items-center gap-2.5 text-foreground', className)}>
      <svg width="28" height="28" viewBox="0 0 32 32" aria-hidden className="shrink-0">
        <rect width="32" height="32" rx="8" className="fill-primary" />
        <path
          d="M9 21h13a4 4 0 0 0 .6-7.95A6 6 0 0 0 11 13.5 3.75 3.75 0 0 0 9 21z"
          fill="none"
          stroke="#fff"
          strokeWidth="2"
          strokeLinejoin="round"
        />
      </svg>
      {!compact && <span className="font-heading text-xl font-bold tracking-tight">CloudMesh</span>}
    </span>
  )
}

/** Avatar redondo con iniciales. */
export function Avatar({ text, className }: { text: string; className?: string }) {
  return (
    <span
      aria-hidden
      className={cn(
        'flex size-9 shrink-0 items-center justify-center rounded-full bg-accent-foreground text-sm font-semibold text-white dark:text-background',
        className,
      )}
    >
      {text}
    </span>
  )
}

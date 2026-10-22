import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { useMutation } from '@tanstack/react-query'
import { renderSVG } from 'uqr'
import { Check, Copy, Download, Smartphone } from 'lucide-react'
import { regenerateRecoveryCodes, totpDisable, totpEnable, totpSetup } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { ErrorText } from '@/features/shell/Page'
import { t } from '@/lib/i18n'

const codeOk = (c: string) => /^\d{6}$/.test(c.replace(/\s/g, ''))

/** Código de la app (6 cifras) o de recuperación (12 caracteres, con o sin guiones). */
export const secondFactorOk = (c: string) => codeOk(c) || c.replace(/[\s-]/g, '').length === 12

/**
 * Lista de códigos de recuperación recién generados: se muestran una sola vez,
 * con copiar y descargar como .txt.
 */
export function RecoveryCodes({ codes, email, onDone }: { codes: string[]; email?: string; onDone: () => void }) {
  const [copied, setCopied] = useState(false)
  const text = codes.join('\n')
  const download = () => {
    const title = t('Códigos de recuperación de CloudMesh')
    const blob = new Blob([`${title}${email ? ` (${email})` : ''}\n${t('Cada uno sirve una sola vez.')}\n\n${text}\n`], {
      type: 'text/plain',
    })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = 'cloudmesh-codigos-recuperacion.txt'
    a.click()
    URL.revokeObjectURL(a.href)
  }
  return (
    <div className="grid grid-cols-1 gap-4">
      <p className="rounded-xl bg-warning-soft px-3.5 py-2.5 text-[13px] text-warning">
        {t('Guárdalos ahora en un sitio seguro: no se vuelven a mostrar. Cada uno sirve una vez para entrar si pierdes el móvil.')}
      </p>
      <ol className="grid grid-cols-2 gap-x-4 gap-y-1.5 rounded-xl border bg-background p-4 font-mono text-sm">
        {codes.map((c) => (
          <li key={c}>{c}</li>
        ))}
      </ol>
      <div className="flex flex-wrap justify-end gap-2.5">
        <Button
          type="button"
          variant="outline"
          onClick={() =>
            void navigator.clipboard.writeText(text).then(() => {
              setCopied(true)
              setTimeout(() => setCopied(false), 2000)
            })
          }
        >
          {copied ? <Check /> : <Copy />} {copied ? t('Copiados') : t('Copiar')}
        </Button>
        <Button type="button" variant="outline" onClick={download}>
          <Download /> {t('Descargar')}
        </Button>
        <Button type="button" onClick={onDone}>
          {t('Ya los guardé')}
        </Button>
      </div>
    </div>
  )
}

/** Secreto en grupos de cuatro para teclearlo sin perderse. */
const grouped = (secret: string) => secret.match(/.{1,4}/g)?.join(' ') ?? secret

/**
 * Activar la verificación en dos pasos: genera el secreto al abrir, lo muestra
 * como QR (o enlace otpauth:// en el móvil) y pide un código para confirmar.
 */
export function EnableTOTPDialog({ open, onClose, onEnabled }: { open: boolean; onClose: () => void; onEnabled: () => void }) {
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t('Verificación en dos pasos')}</DialogTitle>
          <DialogDescription>
            {t('Además de la contraseña, al entrar se pedirá un código de tu app de autenticación (Google Authenticator, Aegis, 1Password…).')}
          </DialogDescription>
        </DialogHeader>
        {open && <EnableSteps onEnabled={onEnabled} onClose={onClose} />}
      </DialogContent>
    </Dialog>
  )
}

function EnableSteps({ onEnabled, onClose }: { onEnabled: () => void; onClose: () => void }) {
  const [code, setCode] = useState('')
  const [copied, setCopied] = useState(false)
  const setup = useMutation({ mutationFn: totpSetup })
  const enable = useMutation({ mutationFn: () => totpEnable(code.replace(/\s/g, '')) })
  // Un secreto nuevo por apertura del diálogo; sin confirmar no se activa nada.
  // Si se pide dos veces, gana la última, que es la que muestra el diálogo.
  const { mutate: start } = setup
  useEffect(() => start(), [start])
  const qr = useMemo(() => (setup.data ? renderSVG(setup.data.url, { border: 1 }) : ''), [setup.data])

  // Activada: antes de cerrar se muestran los códigos de recuperación.
  if (enable.data) return <RecoveryCodes codes={enable.data.recovery_codes} onDone={onEnabled} />
  if (setup.isIdle || setup.isPending) return <p className="text-sm text-muted-foreground">{t('Generando…')}</p>
  if (setup.error || !setup.data) return <ErrorText error={setup.error} />

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    enable.mutate()
  }
  return (
    <form onSubmit={onSubmit} className="grid grid-cols-1 gap-4">
      <div className="grid gap-2">
        <span className="text-sm font-semibold">{t('1. Escanea el código con la app')}</span>
        <div
          role="img"
          aria-label={t('Código QR para la app de autenticación')}
          className="mx-auto w-48 rounded-xl bg-white p-2 [&>svg]:h-auto [&>svg]:w-full"
          dangerouslySetInnerHTML={{ __html: qr }}
        />
        <Button asChild variant="outline" className="sm:hidden">
          <a href={setup.data.url}>
            <Smartphone /> {t('Abrir en la app de este móvil')}
          </a>
        </Button>
        <div className="grid gap-1">
          <span className="text-[13px] text-muted-foreground">{t('O escribe esta clave:')}</span>
          <div className="flex items-center gap-2 rounded-xl border bg-background py-1 pr-1 pl-3">
            <code className="min-w-0 flex-1 font-mono text-[13px] break-all">{grouped(setup.data.secret)}</code>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              aria-label={t('Copiar clave')}
              onClick={() =>
                void navigator.clipboard.writeText(setup.data.secret).then(() => {
                  setCopied(true)
                  setTimeout(() => setCopied(false), 2000)
                })
              }
            >
              {copied ? <Check /> : <Copy />}
            </Button>
          </div>
        </div>
      </div>
      <div className="grid gap-1.5">
        <Label htmlFor="totp-code">{t('2. Escribe el código que muestra la app')}</Label>
        <Input
          id="totp-code"
          inputMode="numeric"
          autoComplete="one-time-code"
          maxLength={7}
          placeholder="123 456"
          value={code}
          onChange={(e) => setCode(e.target.value)}
        />
      </div>
      <p className="text-[13px] text-muted-foreground">{t('Al activarla se cierran tus demás sesiones.')}</p>
      <ErrorText error={enable.error} />
      <div className="flex justify-end gap-2.5">
        <Button type="button" variant="outline" onClick={onClose}>
          {t('Cancelar')}
        </Button>
        <Button type="submit" disabled={!codeOk(code) || enable.isPending}>
          {t('Activar')}
        </Button>
      </div>
    </form>
  )
}

/** Desactivar la propia 2FA: contraseña y un código vigente. */
export function DisableTOTPDialog({ open, onClose, onDisabled }: { open: boolean; onClose: () => void; onDisabled: () => void }) {
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const disable = useMutation({
    mutationFn: () => totpDisable(password, code.replace(/\s/g, '')),
    onSuccess: () => {
      setPassword('')
      setCode('')
      onDisabled()
    },
  })
  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    disable.mutate()
  }
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t('Desactivar la verificación en dos pasos')}</DialogTitle>
          <DialogDescription>{t('Para entrar bastará con la contraseña.')}</DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="grid grid-cols-1 gap-3.5">
          <div className="grid gap-1.5">
            <Label htmlFor="totp-off-password">{t('Contraseña')}</Label>
            <Input
              id="totp-off-password"
              type="password"
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="totp-off-code">{t('Código de la app o de recuperación')}</Label>
            <Input
              id="totp-off-code"
              autoComplete="one-time-code"
              autoCapitalize="none"
              maxLength={16}
              placeholder="123 456"
              value={code}
              onChange={(e) => setCode(e.target.value)}
            />
          </div>
          <ErrorText error={disable.error} />
          <div className="flex justify-end gap-2.5">
            <Button type="button" variant="outline" onClick={onClose}>
              {t('Cancelar')}
            </Button>
            <Button type="submit" variant="destructive" disabled={!password || !secondFactorOk(code) || disable.isPending}>
              {t('Desactivar')}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** Generar códigos de recuperación nuevos: contraseña y código de la app. */
export function RecoveryCodesDialog({ open, onClose, onDone }: { open: boolean; onClose: () => void; onDone: () => void }) {
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const regen = useMutation({ mutationFn: () => regenerateRecoveryCodes(password, code.replace(/\s/g, '')) })
  const close = () => {
    setPassword('')
    setCode('')
    const done = regen.isSuccess
    regen.reset()
    if (done) onDone()
    else onClose()
  }
  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    regen.mutate()
  }
  return (
    <Dialog open={open} onOpenChange={(o) => !o && close()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t('Códigos de recuperación')}</DialogTitle>
          <DialogDescription>{t('Se generan diez nuevos y los anteriores dejan de valer.')}</DialogDescription>
        </DialogHeader>
        {regen.data ? (
          <RecoveryCodes codes={regen.data.recovery_codes} onDone={close} />
        ) : (
          <form onSubmit={onSubmit} className="grid grid-cols-1 gap-3.5">
            <div className="grid gap-1.5">
              <Label htmlFor="rc-password">{t('Contraseña')}</Label>
              <Input id="rc-password" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="rc-code">{t('Código de la app')}</Label>
              <Input
                id="rc-code"
                inputMode="numeric"
                autoComplete="one-time-code"
                maxLength={7}
                placeholder="123 456"
                value={code}
                onChange={(e) => setCode(e.target.value)}
              />
            </div>
            <ErrorText error={regen.error} />
            <div className="flex justify-end gap-2.5">
              <Button type="button" variant="outline" onClick={close}>
                {t('Cancelar')}
              </Button>
              <Button type="submit" disabled={!password || !codeOk(code) || regen.isPending}>
                {t('Generar')}
              </Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

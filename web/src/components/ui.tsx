// The small set of surfaces every screen is built from. Dense, quiet, one accent.

import type { ButtonHTMLAttributes, InputHTMLAttributes, ReactNode, SelectHTMLAttributes } from 'react'

import { classes } from '../lib/format'

export function Card({
  title,
  subtitle,
  actions,
  children,
  className,
}: {
  title?: ReactNode
  subtitle?: ReactNode
  actions?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <section
      className={classes(
        'rounded-lg border border-ink-700 bg-ink-900/70 shadow-sm shadow-black/30',
        className,
      )}
    >
      {(title || actions) && (
        <header className="flex items-start justify-between gap-4 border-b border-ink-700 px-4 py-3">
          <div>
            {title && <h2 className="text-sm font-semibold text-ink-100">{title}</h2>}
            {subtitle && <p className="mt-0.5 text-xs text-ink-400">{subtitle}</p>}
          </div>
          {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
        </header>
      )}
      {children}
    </section>
  )
}

type ButtonVariant = 'primary' | 'ghost' | 'danger' | 'discord'

export function Button({
  variant = 'ghost',
  className,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant }) {
  const styles: Record<ButtonVariant, string> = {
    primary: 'bg-accent-600 text-white hover:bg-accent-500 border-accent-600',
    ghost: 'bg-ink-800 text-ink-200 hover:bg-ink-700 border-ink-600',
    // Used for revocation and retraction. It is a strong colour because both are consequential,
    // not because either is an error.
    danger: 'bg-ink-800 text-rose-300 hover:bg-rose-950/60 border-rose-900/70',
    // Discord's own control, in Discord's own colours. It is a VARIANT rather than a bespoke
    // element so that it inherits the height, radius, border, transition, disabled treatment and
    // focus behaviour of every button beside it — a sign-in button that sits in the visual system
    // instead of looking pasted into it. `components/ProviderButton.tsx` is what decides when it
    // is used, and it decides on provider `kind`.
    discord:
      'bg-discord-blurple text-white border-discord-blurple ' +
      'hover:bg-discord-blurple-dark hover:border-discord-blurple-dark',
  }
  return (
    <button
      type="button"
      {...rest}
      className={classes(
        'inline-flex items-center gap-1.5 rounded border px-2.5 py-1.5 text-xs font-medium',
        'transition-colors disabled:cursor-not-allowed disabled:opacity-40',
        styles[variant],
        className,
      )}
    />
  )
}

export function Input({ className, ...rest }: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      {...rest}
      className={classes(
        'w-full rounded border border-ink-600 bg-ink-850 px-2.5 py-1.5 text-xs text-ink-100',
        'placeholder:text-ink-500 focus:border-accent-500 focus:outline-none',
        className,
      )}
    />
  )
}

export function Select({ className, ...rest }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select
      {...rest}
      className={classes(
        'rounded border border-ink-600 bg-ink-850 px-2 py-1.5 text-xs text-ink-100',
        'focus:border-accent-500 focus:outline-none',
        className,
      )}
    />
  )
}

export function Field({
  label,
  hint,
  error,
  children,
}: {
  label: string
  hint?: ReactNode
  error?: string
  children: ReactNode
}) {
  return (
    <label className="block">
      <span className="mb-1 block text-[11px] font-medium tracking-wide text-ink-300 uppercase">
        {label}
      </span>
      {children}
      {hint && !error && <span className="mt-1 block text-[11px] text-ink-500">{hint}</span>}
      {error && <span className="mt-1 block text-[11px] text-rose-400">{error}</span>}
    </label>
  )
}

/** Banner is a persistent statement of fact, not a toast. It does not dismiss itself. */
export function Banner({
  tone = 'info',
  title,
  children,
}: {
  tone?: 'info' | 'warn' | 'accent'
  title: ReactNode
  children?: ReactNode
}) {
  const tones = {
    info: 'border-ink-600 bg-ink-850 text-ink-200',
    warn: 'border-amber-800/70 bg-amber-950/40 text-amber-200',
    accent: 'border-accent-600/60 bg-accent-600/10 text-accent-400',
  }
  return (
    <div className={classes('rounded-md border px-3 py-2 text-xs', tones[tone])}>
      <p className="font-semibold">{title}</p>
      {children && <div className="mt-1 leading-relaxed opacity-90">{children}</div>}
    </div>
  )
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="px-4 py-10 text-center">
      <p className="text-sm text-ink-300">{title}</p>
      {children && <p className="mx-auto mt-1 max-w-md text-xs text-ink-500">{children}</p>}
    </div>
  )
}

export function Spinner({ label = 'Loading' }: { label?: string }) {
  return (
    <div className="flex items-center gap-2 px-4 py-8 text-xs text-ink-400">
      <span className="h-3 w-3 animate-spin rounded-full border-2 border-ink-600 border-t-accent-500" />
      {label}
    </div>
  )
}

export function Th({ children, className }: { children?: ReactNode; className?: string }) {
  return (
    <th
      className={classes(
        'border-b border-ink-700 px-3 py-2 text-left text-[11px] font-semibold',
        'tracking-wide text-ink-400 uppercase',
        className,
      )}
    >
      {children}
    </th>
  )
}

export function Td({
  children,
  className,
  title,
  colSpan,
}: {
  children?: ReactNode
  className?: string
  title?: string
  colSpan?: number
}) {
  return (
    <td
      title={title}
      colSpan={colSpan}
      className={classes('border-b border-ink-800/70 px-3 py-2 align-middle', className)}
    >
      {children}
    </td>
  )
}

/** Mono renders an identifier or a code: tabular, selectable, never truncated silently. */
export function Mono({
  children,
  className,
  title,
}: {
  children: ReactNode
  className?: string
  title?: string
}) {
  return (
    <span title={title} className={classes('font-mono text-[11px] text-ink-300 select-all', className)}>
      {children}
    </span>
  )
}

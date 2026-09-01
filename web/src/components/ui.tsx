// The small set of surfaces every screen is built from. Dense, quiet, one accent.
//
// The look is the nParse+ house palette — see `../index.css` for where each value came from and why
// this repository joined the registry's palette rather than dragonkillparty's. Three rules from
// those siblings are structural rather than decorative, and they are why the controls below look
// the way they do:
//
//   BUTTONS ARE OUTLINED, NEVER FILLED. All three siblings say this independently — Nocturne's
//   button sheet ("a filled primary button reads as a different product"), regserve's "the plate is
//   the field, the gold is what sits on it", and nParse+'s own `chrome.py`, whose primary button is
//   `border: 1px solid accent; color: heading` with the plate band only on hover. It is the one
//   place all three converge, so it is the one that is least safe to break.
//
//   HOVER STATES SNAP. Nocturne ships no motion tokens at all and neither of the other two has a
//   transition anywhere. The countdown bar in `WindowBar` keeps its width transition, which is a
//   different thing: it is smoothing a VALUE, not decorating a pointer.
//
//   ELEVATION IS A HAIRLINE, NOT A DROP SHADOW. A card is a surface with a rule around it.

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
      className={classes('rounded-lg border border-ink-700 bg-ink-850', className)}
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
    // Gold on a hairline, never a gold field. The hover is the plate band — `chrome.py`'s
    // `chrome_band`, which is `#6b5a3a` at 30% — and NOT a gold tint: regserve's BRAND002 fails a
    // gold value in any `background`, and lists `#6b5a3a` as deliberately exempt because it is a
    // hairline colour and banning it would ban a 1px rule.
    primary: 'border-accent-500 text-accent-400 hover:bg-plate-line/30',
    ghost: 'border-ink-700 text-ink-200 hover:bg-ink-200/8',
    // Used for revocation and retraction. It is a strong colour because both are consequential,
    // not because either is an error. The text is `skins.py`'s detrimental header tint rather than
    // `chrome.py`'s BAD itself: BAD is a fill value and lands at 3.62:1 as small type here, where
    // the tint it was published with clears AA at 8.14:1.
    danger: 'border-danger/60 text-danger-ink hover:bg-danger/15',
    // Discord's own control, in Discord's own colours. It is a VARIANT rather than a bespoke
    // element so that it inherits the height, radius, border, disabled treatment and focus
    // behaviour of every button beside it — a sign-in button that sits in the visual system
    // instead of looking pasted into it. `components/ProviderButton.tsx` is what decides when it
    // is used, and it decides on provider `kind`. It is the one FILLED button in the console, for
    // the same reason the blurple is not ours to re-pick: the mark and the field together are what
    // people recognise.
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
        'disabled:cursor-not-allowed disabled:opacity-40',
        styles[variant],
        className,
      )}
    />
  )
}

// An input is a WELL — darker than the surface it sits in, which is `theme.py`'s `map_input_bg`.
// The edge is `#968c7b` rather than the plate border, because WCAG 1.4.11 holds a control's edge to
// 3:1 and the plate border is 2.83:1 on this ground; regserve reached for the same value for the
// same reason. The focus ring is the accent, which is the one place a form shows the brand while
// somebody is actually using it.
const FIELD = 'border-ink-600 bg-ink-950 text-ink-100 focus:border-accent-400 focus:outline-none'

export function Input({ className, ...rest }: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      {...rest}
      className={classes(
        'w-full rounded border px-2.5 py-1.5 text-xs placeholder:text-ink-500',
        FIELD,
        className,
      )}
    />
  )
}

export function Select({ className, ...rest }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select {...rest} className={classes('rounded border px-2 py-1.5 text-xs', FIELD, className)} />
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
      <span className="caps mb-1 block text-[11px] text-ink-400">{label}</span>
      {children}
      {hint && !error && <span className="mt-1 block text-[11px] text-ink-500">{hint}</span>}
      {error && <span className="mt-1 block text-[11px] text-danger-ink">{error}</span>}
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
  // The accent tone is regserve's `.install` panel: a hairline all round and a 4px gold rule down
  // the leading edge. The gold is an edge, never the field.
  const tones = {
    info: 'border-ink-700 bg-ink-850 text-ink-200',
    warn: 'border-warn/40 bg-warn/12 text-warn',
    accent: 'border-ink-700 border-l-4 border-l-accent-400 bg-ink-850 text-ink-200',
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
      <span className="h-3 w-3 animate-spin rounded-full border-2 border-ink-700 border-t-accent-400" />
      {label}
    </div>
  )
}

export function Th({ children, className }: { children?: ReactNode; className?: string }) {
  return (
    <th
      className={classes(
        'caps border-b border-ink-700 px-3 py-2 text-left text-[11px] text-ink-400',
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
      className={classes('border-b border-ink-700/60 px-3 py-2 align-middle', className)}
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

// The six states, six visually distinct treatments.
//
// The vocabulary is exactly `target_state.status` and nothing else: `unknown`, `no_timer`,
// `pre_window`, `in_window`, `overdue`, `up`. There is no seventh chip and no "error" chip.
//
// **`overdue` is real intel, not a failure.** Past `close_at` means the ToD is wrong, the timer is
// wrong, or somebody killed it quietly — every one of which is a thing a raid leader wants to look
// at. Rendered in red beside actual failures it becomes the row people learn to scroll past, so it
// gets its own hue and its own weight.
//
// The nParse+ re-skin did not soften that, it sharpened it. Red is spoken for twice over in the
// client's own vocabulary: `chrome.py`'s BAD is "detrimental, failed", and `spellwindow.py`'s
// FADE_TARGET is the colour every running bar drifts toward as it empties. A red overdue row would
// therefore read as *a failure* and as *nothing left to do*, and it is neither. It wears
// `chrome.py`'s TIMER instead — still a timer, still the row you are watching, past its window.
//
// `no_timer` is distinct from `unknown` on purpose: `unknown` is "nobody has reported this",
// `no_timer` is "we have a ToD and no respawn window to hang off it". An unseeded instance is
// entirely `no_timer`, it is a degraded state rather than a broken one, and the board must still
// be able to say "died 4 hours ago".
//
// HUE IS NEVER THE ONLY CARRIER. dragonkillparty's design system states the rule this file now
// follows — "never use hue alone to carry meaning, because a red pill and a green pill are the
// same pill to a large minority of officers" — and the re-skin is what made it urgent: `no_timer`
// and `in_window` are neighbouring warms in the client's palette. So every chip carries three
// separable signals: a WORD, a SHAPE, and a WEIGHT. The shapes are drawn here as inline SVG rather
// than picked from Unicode because nParse+ has already been bitten by a codepoint the bundled font
// had no cmap entry for, and a status that renders as a tofu box is worse than no status at all.
//
// WEIGHT is the third: `in_window` and `up` are the only FILLED chips, because they are the only
// two that mean "look now". The band and rule alphas on the other four — 14% and 34% — are
// `skins.py`'s own `_tints`, which is how nParse+ paints a tinted group header.

import type { ReactNode } from 'react'

import type { BoardEntry } from '../api'
import { classes } from '../lib/format'

type Status = BoardEntry['status']

/**
 * Glyph draws one status's shape.
 *
 * Six silhouettes that survive greyscale and an 8px box: hollow circle, hollow triangle, chevron,
 * filled diamond, hollow diamond, filled circle. `in_window` and `overdue` deliberately share the
 * diamond and differ only in fill — the window is the same window, open or passed.
 */
function Glyph({ shape }: { shape: Status }) {
  const stroke = { fill: 'none', stroke: 'currentColor', strokeWidth: 1.3 }
  const shapes: Record<Status, ReactNode> = {
    unknown: <circle cx="4" cy="4" r="2.6" {...stroke} />,
    no_timer: <path d="M4 1 L7.2 6.7 L0.8 6.7 Z" {...stroke} strokeLinejoin="miter" />,
    pre_window: <path d="M2.4 1.2 L5.6 4 L2.4 6.8" {...stroke} strokeLinecap="butt" />,
    in_window: <path d="M4 0.6 L7.4 4 L4 7.4 L0.6 4 Z" fill="currentColor" />,
    overdue: <path d="M4 0.9 L7.1 4 L4 7.1 L0.9 4 Z" {...stroke} strokeLinejoin="miter" />,
    up: <circle cx="4" cy="4" r="3" fill="currentColor" />,
  }
  return (
    <svg viewBox="0 0 8 8" aria-hidden="true" className="h-2 w-2 shrink-0">
      {shapes[shape]}
    </svg>
  )
}

const TREATMENTS: Record<Status, { label: string; className: string; title: string }> = {
  unknown: {
    label: 'no ToD',
    className: 'border-status-unknown/50 text-status-unknown-ink',
    title: 'Nobody has reported a time of death for this target.',
  },
  no_timer: {
    label: 'no timer',
    className:
      'border-status-notimer/34 bg-status-notimer/14 text-status-notimer-ink',
    title:
      'There is a time of death but no respawn window: this instance has no timer for this ' +
      'target. Timers are community-derived and load from a separate seed.',
  },
  pre_window: {
    label: 'pre-window',
    className:
      'border-status-prewindow/34 bg-status-prewindow/14 text-status-prewindow-ink',
    title: 'Dead, timer running. The window has not opened yet.',
  },
  in_window: {
    label: 'in window',
    className: 'border-status-inwindow bg-status-inwindow text-ink-950 font-semibold',
    title: 'The spawn window is open now.',
  },
  overdue: {
    label: 'overdue',
    className:
      'border-status-overdue/34 bg-status-overdue/14 text-status-overdue-ink font-semibold',
    title:
      'Past the close of the window. That means the ToD is wrong, the timer is wrong, or ' +
      'somebody killed it quietly — it is intel, not an error.',
  },
  up: {
    label: 'UP',
    className: 'border-status-up bg-status-up text-ink-950 font-bold',
    title: 'Reported up — post-quake.',
  },
}

export function StatusChip({ status, className }: { status: Status; className?: string }) {
  const treatment = TREATMENTS[status]
  return (
    <span
      title={treatment.title}
      className={classes(
        'caps inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[10px]',
        treatment.className,
        className,
      )}
    >
      <Glyph shape={status} />
      {treatment.label}
    </span>
  )
}

/** STATUS_ORDER is the filter dropdown's order: the states a raid leader scans for come first. */
export const STATUS_ORDER: Status[] = [
  'in_window',
  'pre_window',
  'overdue',
  'up',
  'no_timer',
  'unknown',
]

export function statusLabel(status: Status): string {
  return TREATMENTS[status].label
}

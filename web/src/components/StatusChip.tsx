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
// `no_timer` is distinct from `unknown` on purpose: `unknown` is "nobody has reported this",
// `no_timer` is "we have a ToD and no respawn window to hang off it". An unseeded instance is
// entirely `no_timer`, it is a degraded state rather than a broken one, and the board must still
// be able to say "died 4 hours ago".

import type { BoardEntry } from '../api'
import { classes } from '../lib/format'

type Status = BoardEntry['status']

const TREATMENTS: Record<Status, { label: string; className: string; title: string }> = {
  unknown: {
    label: 'no ToD',
    className: 'border-[var(--color-status-unknown)]/50 text-[var(--color-status-unknown)]',
    title: 'Nobody has reported a time of death for this target.',
  },
  no_timer: {
    label: 'no timer',
    className:
      'border-[var(--color-status-notimer)]/60 text-[var(--color-status-notimer)] bg-[var(--color-status-notimer)]/8',
    title:
      'There is a time of death but no respawn window: this instance has no timer for this ' +
      'target. Timers are community-derived and load from a separate seed.',
  },
  pre_window: {
    label: 'pre-window',
    className:
      'border-[var(--color-status-prewindow)]/60 text-[var(--color-status-prewindow)] bg-[var(--color-status-prewindow)]/10',
    title: 'Dead, timer running. The window has not opened yet.',
  },
  in_window: {
    label: 'in window',
    className:
      'border-[var(--color-status-inwindow)] text-ink-950 bg-[var(--color-status-inwindow)] font-semibold',
    title: 'The spawn window is open now.',
  },
  overdue: {
    label: 'overdue',
    className:
      'border-[var(--color-status-overdue)] text-[var(--color-status-overdue)] bg-[var(--color-status-overdue)]/15 font-semibold',
    title:
      'Past the close of the window. That means the ToD is wrong, the timer is wrong, or ' +
      'somebody killed it quietly — it is intel, not an error.',
  },
  up: {
    label: 'UP',
    className:
      'border-[var(--color-status-up)] text-ink-950 bg-[var(--color-status-up)] font-bold tracking-wide',
    title: 'Reported up — post-quake.',
  },
}

export function StatusChip({ status, className }: { status: Status; className?: string }) {
  const treatment = TREATMENTS[status]
  return (
    <span
      title={treatment.title}
      className={classes(
        'inline-flex items-center rounded border px-1.5 py-0.5 text-[10px] tracking-wide uppercase',
        treatment.className,
        className,
      )}
    >
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

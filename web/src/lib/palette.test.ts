// The brand gates, driven against the sheet that actually ships.
//
// Four rules from `../index.css` and `../components/StatusChip.tsx` are load-bearing enough that a
// comment stating them is not enough. Each is broken by somebody with good intentions and a colour
// picker, and none of them fails visibly on the machine of the person who breaks it:
//
//   every declared pair clears WCAG AA      the ratios in index.css are recomputed, not trusted
//   overdue is never red                    the distinction the status set is built around
//   hue is never the only carrier           six statuses, six shapes, six words
//   the version is never baked into the bundle
//
// The first two are `nparse-plugin-regserve`'s BRAND001 and BRAND002 restated for this console, and
// the shape is deliberately the same so somebody who has read one recognises the other. The IDs stay
// HERE and not in `docs/concepts/invariants.md`: those are the registry's gate ids, this repository
// emits nothing under either name, and DOC003 fails a page naming a gate that never ran.

import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { contrast, hueGap, over, parseHex, tokens, type Rgb } from './palette.ts'

const read = (rel: string) => readFileSync(new URL(rel, import.meta.url), 'utf8')

const CSS = read('../index.css')
const CHIP = read('../components/StatusChip.tsx')
const MARK = read('../components/Mark.tsx')
const HTML = read('../../index.html')
const VITE = read('../../vite.config.ts')

const PALETTE = tokens(CSS)
const at = (name: string): Rgb => {
  const rgb = PALETTE.get(name)
  assert.ok(rgb, `${name} is missing from index.css, or is no longer a hex this gate can measure`)
  return rgb
}

/** The six, in the order index.css declares them. */
const STATUSES = ['unknown', 'notimer', 'prewindow', 'inwindow', 'overdue', 'up'] as const

// A gate that measured nothing would pass. Assert the input is complete before asserting anything
// about it: five gates in this repository's history reported green while inspecting an empty set.
test('the palette parsed out of index.css is complete', () => {
  assert.ok(PALETTE.size >= 25, `only ${PALETTE.size} colour tokens parsed; the parse is wrong`)
  for (const s of STATUSES) {
    at(`--color-status-${s}`)
    at(`--color-status-${s}-ink`)
  }
})

test('every text pair the stylesheet declares clears WCAG AA', () => {
  const page = at('--color-ink-900')
  const plate = at('--color-plate')
  const measured: string[] = []

  // AA is 4.5:1 for text. A control's EDGE is non-text contrast under WCAG 1.4.11 and its floor is
  // 3:1, which is why it carries a different minimum rather than an exemption.
  const pairs: Array<[string, Rgb, string, Rgb, number, string]> = [
    ['--color-ink-200', at('--color-ink-200'), 'page', page, 4.5, 'body text'],
    ['--color-ink-300', at('--color-ink-300'), 'page', page, 4.5, 'secondary text'],
    ['--color-ink-400', at('--color-ink-400'), 'page', page, 4.5, 'captions and table heads'],
    ['--color-ink-500', at('--color-ink-500'), 'page', page, 4.5, 'hints, which are text'],
    ['--color-accent-400', at('--color-accent-400'), 'page', page, 4.5, 'links and the kicker'],
    // The warn and danger inks never sit on the bare page: `Banner`'s warn tone and the danger
    // button both lay their own colour under the text first. Measure the ground they are actually
    // on — the tint is lighter, so the page would be the flattering answer rather than the true one.
    [
      '--color-warn',
      at('--color-warn'),
      'its 12% band',
      over(at('--color-warn'), at('--color-ink-850'), 0.12),
      4.5,
      'a stale notice and the warn banner',
    ],
    [
      '--color-danger-ink',
      at('--color-danger-ink'),
      '--color-danger at 15%',
      over(at('--color-danger'), at('--color-ink-850'), 0.15),
      4.5,
      'a destructive control',
    ],
    ['--color-ink-600', at('--color-ink-600'), 'page', page, 3.0, "a control's edge"],
    ['--color-plate-fg', at('--color-plate-fg'), 'plate', plate, 4.5, "the rail's text"],
    ['--color-plate-muted', at('--color-plate-muted'), 'plate', plate, 4.5, "the rail's muted text"],
    ['--color-plate-accent', at('--color-plate-accent'), 'plate', plate, 4.5, 'the active section'],
  ]

  // Every status ink, measured on its own 14% band rather than on the bare page — the band is the
  // lighter ground and therefore the worse one. 14% is skins.py's own `_tints` band alpha.
  for (const s of STATUSES) {
    const fill = at(`--color-status-${s}`)
    pairs.push([
      `--color-status-${s}-ink`,
      at(`--color-status-${s}-ink`),
      `its 14% band`,
      over(fill, page, 0.14),
      4.5,
      `the ${s} chip`,
    ])
  }

  // The two filled chips put the deepest ground's colour ON the status fill.
  for (const s of ['inwindow', 'up'] as const) {
    pairs.push([
      '--color-ink-950',
      at('--color-ink-950'),
      `--color-status-${s}`,
      at(`--color-status-${s}`),
      4.5,
      `the filled ${s} chip`,
    ])
  }

  const under: string[] = []
  for (const [fgName, fg, bgName, bg, min, what] of pairs) {
    const ratio = contrast(fg, bg)
    measured.push(`${ratio.toFixed(2).padStart(5)}:1  ${fgName} on ${bgName} — ${what}`)
    if (ratio < min) under.push(`${fgName} on ${bgName} is ${ratio.toFixed(2)}:1, under ${min}:1 (${what})`)
  }

  console.log(measured.join('\n'))
  assert.ok(measured.length >= 19, 'the contrast gate measured almost nothing; it is vacant')
  assert.deepEqual(under, [], 'a pair went under its floor')
})

// BRAND002, restated. The registry's version bans a gold BACKGROUND; this one bans the thing that
// distinction exists to protect, which is `overdue` turning into a failure.
//
// `overdue` is real, actionable intel — the ToD is wrong, the timer is wrong, or somebody killed it
// quietly. Red says two wrong things about it at once in this family's own vocabulary: chrome.py's
// BAD is "detrimental, failed", and spellwindow.py fades every running bar toward that same red as
// it empties, so red also says "nothing left to do".
test('overdue is not red, and is not the failure colour', () => {
  const overdue = at('--color-status-overdue')
  const danger = at('--color-danger')

  const gap = hueGap(overdue, danger)
  assert.ok(gap !== null, 'overdue or danger has become a grey; this gate can no longer see the rule')
  assert.ok(
    gap > 60,
    `overdue is ${gap!.toFixed(0)} degrees from the failure colour. It must not read as one: ` +
      'past close_at is intel, not an error, and a row rendered as a failure is the row people ' +
      'learn to scroll past.',
  )

  // And not merely far from THIS red: not in the red band at all.
  const red = parseHex('#ff0000')!
  assert.ok(hueGap(overdue, red)! > 45, 'overdue has drifted into the reds')
})

test('hue is never the only carrier: six statuses, six distinct shapes and six words', () => {
  // The shapes are what survive greyscale and a colour-blind reader. Parse them out of the chip
  // rather than trusting the comment above them.
  const glyphs = [...CHIP.matchAll(/^\s{4}(\w+): (<(?:circle|path)[^\n]*?\/>),$/gm)]
  assert.equal(glyphs.length, 6, 'the glyph table no longer has exactly six entries')
  assert.deepEqual(
    glyphs.map((m) => m[1]).sort(),
    ['in_window', 'no_timer', 'overdue', 'pre_window', 'unknown', 'up'],
    'the glyph table does not cover exactly the six statuses',
  )
  const drawings = glyphs.map((m) => m[2])
  assert.equal(
    new Set(drawings).size,
    6,
    'two statuses draw the same shape, so hue is doing the work alone for that pair',
  )

  // And six distinct words. A shape with no word is a rebus.
  const labels = [...CHIP.matchAll(/^\s{4}label: '([^']+)',$/gm)].map((m) => m[1])
  assert.equal(new Set(labels).size, 6, `expected six distinct labels, got ${labels.join(', ')}`)
})

test('the mark is inline, and the favicon is the same drawing', () => {
  // Law 7's reason, applied to an image: a deployment with no outbound network renders a remote
  // mark as a blank square on its own landing page.
  const icon = HTML.match(/<link rel="icon" href="([^"]+)"/)
  assert.ok(icon, 'index.html declares no favicon')
  assert.ok(
    icon![1].startsWith('data:image/svg+xml,'),
    `the favicon is not inline: ${icon![1].slice(0, 60)}`,
  )

  // The component and the favicon are two hand-written copies of one drawing, so they are held to
  // the same path data. Without this they drift, and the first anybody notices is a browser tab
  // showing last month's mark.
  const decoded = decodeURIComponent(icon![1])
  for (const [name, path] of [
    ['crossbar', /export const MARK_CROSSBAR = '([^']+)'/],
    ['stem', /export const MARK_STEM = '([^']+)'/],
  ] as const) {
    const m = MARK.match(path)
    assert.ok(m, `Mark.tsx no longer exports the ${name}`)
    assert.ok(decoded.includes(m![1]), `the favicon has drifted from the component's ${name}: ${m![1]}`)
  }
})

test('the server version can only come from /meta, never from the bundle', () => {
  // A console served from a stale cache that names a version names the WRONG one, confidently, in
  // a footer. There is no build-time constant to reach for, and this is what keeps it that way.
  assert.ok(
    !/\bdefine\s*:/.test(VITE),
    'vite.config.ts declares a `define`, which is how a build-time constant gets into the bundle',
  )
  const footer = read('../components/ServerFooter.tsx')
  assert.match(footer, /api\.getServerMeta\(/, 'the footer no longer reads /meta')
  assert.ok(
    !/import\.meta\.env/.test(footer),
    'the footer reads import.meta.env, which is baked in at build time',
  )
})

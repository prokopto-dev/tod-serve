// The brand gates, driven against the sheet that actually ships.
//
// Four rules from `../index.css` and `../components/StatusChip.tsx` are load-bearing enough that a
// comment stating them is not enough. Each is broken by somebody with good intentions and a colour
// picker, and none of them fails visibly on the machine of the person who breaks it:
//
//   every STATED ratio is true and clears   recomputed from the shipped sheet, and the set of
//   its floor                                pairs is DERIVED from index.css's own annotations
//                                            rather than listed here, because a list drifts
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

import {
  annotations,
  contrast,
  hueGap,
  over,
  parseHex,
  statedRatios,
  tokens,
  type Rgb,
} from './palette.ts'

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

test('every ratio the stylesheet states is true, and clears its floor', () => {
  const stated = statedRatios(CSS)
  const claims = annotations(CSS)

  // THE CLOSED HALF, and the reason this gate is derived rather than listed. A hand-maintained list
  // of pairs shipped one review round ago missing `--color-ink-100` and `--color-accent-500`: both
  // carried a stated ratio, neither was measured, and either could have gone under AA with this
  // file still green. Counting what the block SAYS and comparing it to what the parser UNDERSTOOD
  // is what makes that impossible — a ratio written in a form the parser skips is now a failure,
  // not a silent omission.
  assert.equal(
    claims.length,
    stated.length,
    `the @theme block states ${stated.length} ratios and this gate could parse ${claims.length}. ` +
      'A ratio it cannot parse is a claim nothing enforces — write it in one of the three forms ' +
      "index.css's header documents, or take the number out of the block.",
  )
  // A gate that measured nothing would pass every assertion below it.
  assert.ok(claims.length >= 20, `only ${claims.length} annotated pairs; the gate is vacant`)

  const measured: string[] = []
  const wrong: string[] = []
  for (const claim of claims) {
    const ground =
      claim.ground.kind === 'token'
        ? at(claim.ground.token)
        : over(at(claim.ground.tint), at(claim.ground.over), claim.ground.pct / 100)
    const ratio = contrast(at(claim.fg), ground)
    const actual = ratio.toFixed(2)

    measured.push(`${actual.padStart(5)}:1  ${claim.token} — ${claim.raw}`)
    // Both directions. The ratio must be what the file SAYS it is, because a stale number beside a
    // token is the same defect as an unmeasured one — somebody reads it and believes it.
    if (actual !== Number(claim.stated).toFixed(2)) {
      wrong.push(`${claim.token} states ${claim.stated}:1 and measures ${actual}:1 (${claim.raw})`)
    }
    if (ratio < claim.floor) {
      wrong.push(`${claim.token} is ${actual}:1, under its ${claim.floor}:1 floor (${claim.raw})`)
    }
  }

  console.log(measured.join('\n'))
  assert.deepEqual(wrong, [], 'a stated ratio is wrong, or a pair went under its floor')
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

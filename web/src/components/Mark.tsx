// The tod-serve application mark: a T, cut from straight lines, inside a ring, on a notched
// Velious plate.
//
// IT IS A SIBLING OF THE nParse+ MARK, NOT A NEW IDEA. `nparse-plus@f8bc4e8:data/assets/icon.svg`
// is the only mark in this family — the plugin registry vendors it verbatim
// (`nparse-plugin-regserve@65a35ec:internal/api/webtmpl/assets/`), and dragonkillparty ships no
// mark at all. So the frame, the bevel, the ring, the gold gradient and the drawing discipline are
// that file's, reused at the same coordinates; the only thing replaced is the glyph inside the
// ring. Every colour is a real value from `nparse-plus:src/nparseplus/ui/skins.py`:
//
//     #6b5a3a              VELIOUS.glass_border / plate_border
//     #3a3122 -> #241e14   VELIOUS.plate
//     #0a0b0f -> #06070a   VELIOUS.glass (opaque; the mark has no window behind it)
//     #e2c882              VELIOUS.mark_color — the brightest gold in the app
//     #c8a951              DUXA.mark_color
//     #8a7549              LEDGER.title_color — the muted gold, the gradient's foot
//
// THE GLYPH: the T of ToD, and it is the STRUCTURE rather than a letter set inside one. The
// crossbar is a lintel the stem hangs from — the shape a grave marker makes, which is the thing
// this server records. It is drawn under the same rules as the upstream "n": straight lines only,
// no curve anywhere, butt caps and miter joins throughout, and one stroke weight heavier than the
// ring so the glyph stays the thing you see first as the raster shrinks.
//
// GEOMETRY, and why. The hard requirement is the same as upstream's: the 16px raster must keep a
// clean silhouette, because a favicon is where this is read.
//
//   - The tile, the three bevel octagons and the ring are upstream's, coordinate for coordinate.
//     Changing them would make this a different family's mark.
//   - The glyph's stroke is 21 units (1.3px at 16px), the ring's is 16 (1px), exactly as upstream.
//   - The crossbar spans x 86..170 — 84 of the ring's 140 units of inner width, 60%. That number
//     is the whole reason the mark reads: a bar spanning the FULL chord is a struck-through circle,
//     which is the "no entry" failure `icon.svg` records rejecting a diagonal for. At 60% the bar
//     visibly stops short of the ring on both sides and the shape is unambiguously a T.
//   - Every extremity clears the ring's inner edge (r=70) with the stroke's half-width counted in:
//     the crossbar's top corners reach r=62.7, its ends r=65.8, the stem's foot r=53.0.
//
// Rejected, recorded so nobody re-proposes them:
//   - Hanging an O and a D off the crossbar. Three glyphs inside a 140-unit ring is mud at 16px,
//     which is the same arithmetic that killed upstream's "+" in the ring.
//   - Tick marks flanking the stem to suggest a countdown. Two units wide at favicon size; the
//     upstream file rejected a "+" for exactly this and the reasoning transfers unchanged.
//   - Breaking the ring to seat the crossbar. Upstream priced this and it costs more than it adds.
//
// LAW 7: this is inline SVG and there is no second copy anywhere that could fail to load. A mark
// that arrives as a network request is a blank square on the landing page of a deployment with no
// outbound network, which is every deployment this project targets. `index.html`'s favicon is the
// same drawing as a `data:` URI — flattened, because at 16px a three-stop gradient is one colour —
// and `brand.test.ts` holds the two to the same path data so they cannot drift apart.

import { useId } from 'react'

import { classes } from '../lib/format'

/** The crossbar and the stem. Exported because `brand.test.ts` pins the favicon to them. */
export const MARK_CROSSBAR = 'M86,92 L170,92'
export const MARK_STEM = 'M128,92 L128,180'

export function Mark({
  className,
  title,
}: {
  className?: string
  /**
   * title names the mark for a screen reader. Omit it wherever the mark sits beside a wordmark
   * that already says the same thing — regserve learned this the loud way: a titled mark next to
   * its own name is announced twice, "nParse+ nParse+ plugin registry".
   */
  title?: string
}) {
  // Namespaced per instance: the mark renders in the nav rail and again on the landing page, and
  // two <defs> with the same gradient id in one document is undefined behaviour that browsers
  // resolve by picking whichever came first.
  const id = useId()
  const plate = `${id}-plate`
  const glass = `${id}-glass`
  const gold = `${id}-gold`

  return (
    <svg
      viewBox="0 0 256 256"
      className={classes('block', className)}
      role={title ? 'img' : undefined}
      aria-hidden={title ? undefined : true}
    >
      {title && <title>{title}</title>}
      <defs>
        <linearGradient id={plate} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="#3a3122" />
          <stop offset="1" stopColor="#241e14" />
        </linearGradient>
        <linearGradient id={glass} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="#0a0b0f" />
          <stop offset="1" stopColor="#06070a" />
        </linearGradient>
        {/* Lights the engraving from above: bright crown, muted foot. */}
        <linearGradient id={gold} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="#e2c882" />
          <stop offset="0.55" stopColor="#c8a951" />
          <stop offset="1" stopColor="#8a7549" />
        </linearGradient>
      </defs>

      {/* Bevel: border, plate, glass. Each inner octagon is inset 11 units, its notch shortened by
          11*(sqrt(2)-1) so the band stays an even width. Upstream's coordinates. */}
      <path d="M34,0 L222,0 L256,34 L256,222 L222,256 L34,256 L0,222 L0,34 Z" fill="#6b5a3a" />
      <path
        d="M40,11 L216,11 L245,40 L245,216 L216,245 L40,245 L11,216 L11,40 Z"
        fill={`url(#${plate})`}
      />
      <path
        d="M47,22 L209,22 L234,47 L234,209 L209,234 L47,234 L22,209 L22,47 Z"
        fill={`url(#${glass})`}
      />

      <circle cx="128" cy="128" r="78" fill="none" stroke={`url(#${gold})`} strokeWidth="16" />

      {/* The T: a lintel, and the stem hanging from it. */}
      <path
        d={`${MARK_CROSSBAR} ${MARK_STEM}`}
        fill="none"
        stroke={`url(#${gold})`}
        strokeWidth="21"
        strokeLinecap="butt"
        strokeLinejoin="miter"
      />
    </svg>
  )
}

/**
 * Wordmark is the mark with the product's name beside it, as the nav rail and the landing page
 * both want it.
 *
 * The lockup is regserve's: mark, name, and a tracked lowercase sub-line under the name. The mark
 * carries no title here because the name is right next to it.
 */
export function Wordmark({ className, sub = 'time of death' }: { className?: string; sub?: string }) {
  return (
    <span className={classes('flex items-center gap-2.5', className)}>
      <Mark className="h-7 w-7 shrink-0" />
      <span className="min-w-0">
        <span className="block text-sm font-semibold tracking-tight text-plate-fg">tod-serve</span>
        <span className="caps block text-[10px] text-plate-accent">{sub}</span>
      </span>
    </span>
  )
}

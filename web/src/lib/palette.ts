// The palette, as something a test can measure rather than a comment claiming a ratio.
//
// `index.css` states a contrast figure beside almost every token. A stated figure is a wish: the
// next person to nudge a hex "so it reads better" leaves the number behind, and nobody finds out
// until an officer is squinting at a board at 2am. So the numbers are recomputed from the shipped
// stylesheet by `palette.test.ts`, which is the same shape the plugin registry's BRAND001 has —
// `nparse-plugin-regserve@65a35ec:internal/api/brand_internal_test.go` — and for the same reason it
// gives there: "a 'nicer' hex is a red test rather than a review comment".
//
// Pure, and free of React and of the transport, like `../app/resource.ts` and `./asof.ts`: it reads
// a string of CSS and does arithmetic. The test is what goes to disk.

/** Rgb is an 8-bit triple. */
export interface Rgb {
  r: number
  g: number
  b: number
}

/** parseHex accepts `#rgb` and `#rrggbb`; anything else is not a colour this module measures. */
export function parseHex(value: string): Rgb | null {
  const raw = value.trim().replace(/^#/, '')
  const full = raw.length === 3 ? [...raw].map((c) => c + c).join('') : raw
  if (!/^[0-9a-fA-F]{6}$/.test(full)) return null
  return {
    r: parseInt(full.slice(0, 2), 16),
    g: parseInt(full.slice(2, 4), 16),
    b: parseInt(full.slice(4, 6), 16),
  }
}

/**
 * tokens pulls every `--color-*: #hex` declaration out of a stylesheet.
 *
 * Deliberately hex-only. A token whose value is a function — `color-mix`, `oklch` — is not a value
 * this module can measure, and silently skipping it is better than guessing: the test asserts the
 * set it found is COMPLETE against the names it expects, so a token that stops being a hex fails
 * loudly rather than dropping out of the audit.
 */
export function tokens(css: string): Map<string, Rgb> {
  const found = new Map<string, Rgb>()
  for (const match of css.matchAll(/(--color-[a-z0-9-]+)\s*:\s*([^;]+);/g)) {
    const [, name, value] = match
    if (name === undefined || value === undefined) continue
    const rgb = parseHex(value)
    if (rgb) found.set(name, rgb)
  }
  return found
}

const channel = (v: number): number => {
  const s = v / 255
  return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4)
}

/** luminance is WCAG 2.x relative luminance. */
export function luminance({ r, g, b }: Rgb): number {
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)
}

/** contrast is the WCAG ratio, 1..21, and is symmetric in its arguments. */
export function contrast(a: Rgb, b: Rgb): number {
  const la = luminance(a)
  const lb = luminance(b)
  const [hi, lo] = la > lb ? [la, lb] : [lb, la]
  return (hi + 0.05) / (lo + 0.05)
}

/**
 * over composites `fg` at `alpha` onto an opaque `bg`.
 *
 * A tinted chip's text does not sit on the page: it sits on the page with 14% of the status colour
 * laid over it, which is a lighter ground and therefore the WORSE one to measure. Measuring the
 * bare page would flatter every tinted treatment in the console.
 */
export function over(fg: Rgb, bg: Rgb, alpha: number): Rgb {
  const mix = (f: number, b: number) => Math.round(f * alpha + b * (1 - alpha))
  return { r: mix(fg.r, bg.r), g: mix(fg.g, bg.g), b: mix(fg.b, bg.b) }
}

/** hue is the HSV hue in degrees, or null for a grey — which has no hue to compare. */
export function hue({ r, g, b }: Rgb): number | null {
  const max = Math.max(r, g, b)
  const min = Math.min(r, g, b)
  const delta = max - min
  // A near-grey's hue is numerical noise: two greys a channel apart can be 120 degrees "apart".
  if (delta < 8) return null
  let h: number
  if (max === r) h = 60 * (((g - b) / delta) % 6)
  else if (max === g) h = 60 * ((b - r) / delta + 2)
  else h = 60 * ((r - g) / delta + 4)
  return (h + 360) % 360
}

/** hueGap is the shorter way round the wheel, 0..180. Null when either colour is a grey. */
export function hueGap(a: Rgb, b: Rgb): number | null {
  const [ha, hb] = [hue(a), hue(b)]
  if (ha === null || hb === null) return null
  const d = Math.abs(ha - hb) % 360
  return d > 180 ? 360 - d : d
}

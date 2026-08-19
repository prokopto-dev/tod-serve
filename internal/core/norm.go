package core

import (
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// strippedFromNames are the characters removed before a name is compared.
//
// The backtick is not hypothetical: “ Vulak`Aerr “ is a raid target, and an officer types it as
// `Vulak'Aerr`, `VulakAerr` or `vulak aerr`. Canonical §8 names those three spellings as the whole
// job of `name_norm`, and the third one is why whitespace is on this list rather than collapsed:
// `vulak aerr` and `VulakAerr` have to land on the same string.
const strippedFromNames = "'`‘’´-‐‑‒–—"

// Normalise returns the `_norm` half of a `name` / `name_norm` pair.
//
// Canonical §8: NFKC, then casefold, then strip the punctuation above — done in Go and stored in a
// plain column, because core SQLite has no NFKC, `lower()` is ASCII-only, and `ALTER TABLE ADD
// COLUMN` cannot add a STORED generated column, so every future change to this function would
// otherwise force a table rebuild.
//
// The order is the rule. NFKC first, so a name typed with a combining acute and a name typed with
// the precomposed character become the same string before anything else looks at them; casefold
// rather than `ToLower`, because `ToLower` leaves ẞ and ß different and gets Turkish dotless ı
// wrong; the strip last, because NFKC is what turns a fullwidth apostrophe into the ASCII one the
// list above names.
//
// It is deliberately lossy, and the loss is visible rather than silent: two member display names
// that differ only in case, spacing or punctuation normalise to one string, which is exactly what
// `possible_duplicate` in the member list reports. It flags; it never merges.
func Normalise(s string) string {
	folded := cases.Fold().String(norm.NFKC.String(s))

	var b strings.Builder
	b.Grow(len(folded))
	for _, r := range folded {
		switch {
		case unicode.IsSpace(r), strings.ContainsRune(strippedFromNames, r):
			// Dropped, not replaced: `` Vulak`Aerr ``, `Vulak'Aerr` and `vulak aerr` are one name.
		case unicode.Is(unicode.Mn, r):
			// A combining mark NFKC could not compose away. Dropping it is what makes a name
			// pasted from a client that decomposed it match one typed on a keyboard that did not.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

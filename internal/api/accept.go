package api

import (
	"strconv"
	"strings"
)

// The media types this binary produces. They are per-listener: the API answers JSON and the metrics
// listener answers the Prometheus text exposition, and a listener that claimed both would be
// promising something it does not serve on the route the caller asked for.
const (
	// MediaTypeJSON is what every API operation produces. Errors carry [apierr.ContentType], which
	// is `application/problem+json` — a JSON media type, and a response format rather than a
	// negotiated one.
	MediaTypeJSON = "application/json"
	// MediaTypePlainText is what the metrics exposition is.
	MediaTypePlainText = "text/plain"
)

// apiMediaTypes are what the API listener produces.
func apiMediaTypes() []string { return []string{MediaTypeJSON} }

// metricsMediaTypes are what the metrics listener produces.
func metricsMediaTypes() []string { return []string{MediaTypePlainText} }

// qScale is the denominator of a quality value. RFC 9110 permits three decimal places and no more,
// so a q is an integer per-mille here rather than a float — there is nothing to gain from a float
// and a `q=0.7` that compares unequal to itself is a thing nobody wants to debug.
const qScale = 1000

// mediaRange is one entry of an `Accept` header: a type, a subtype, either of which may be `*`, and
// its quality.
type mediaRange struct {
	typ string
	sub string
	q   int
}

// match reports how specifically the range covers a media type, and 0 when it does not cover it.
//
// The number is RFC 9110's precedence order, which is the part that makes `Accept:
// text/plain;q=0, */*` mean what it says: the exact range wins over the wildcard for `text/plain`
// alone, and everything else still falls to the wildcard.
func (m mediaRange) match(typ, sub string) int {
	switch {
	case m.typ == typ && m.sub == sub:
		return 3
	case m.typ == typ && m.sub == "*":
		return 2
	case m.typ == "*" && m.sub == "*":
		return 1
	default:
		return 0
	}
}

// acceptable reports whether an `Accept` header admits at least one of the media types served.
//
// This is here rather than reusing the framework's negotiator because that one is an EXACT string
// match against its format keys: `*/*` is not one of them, so it reports "nothing matched" for the
// header that admits everything. The framework survives that by falling back to JSON; a check that
// reads the same answer as "unacceptable" turns `curl` — which sends `Accept: */*` by default —
// into a 406 on every route. That is exactly what happened, and it is why this is a parser rather
// than a lookup.
//
// An empty header is the caller's business: absent means "anything" and must succeed, which is a
// different statement from "present and satisfiable".
func acceptable(header string, served []string) bool {
	ranges := parseAccept(header)
	for _, media := range served {
		typ, sub, ok := strings.Cut(media, "/")
		if !ok {
			continue
		}
		best, bestQ := 0, 0
		for _, r := range ranges {
			p := r.match(typ, sub)
			switch {
			case p > best:
				best, bestQ = p, r.q
			case p == best && p > 0 && r.q > bestQ:
				// Two ranges of equal specificity: the kinder reading wins, because a client that
				// wrote the same range twice with different weights did not mean to exclude it.
				bestQ = r.q
			}
		}
		// `q=0` means "not acceptable", and it is the one way a client says so explicitly.
		if best > 0 && bestQ > 0 {
			return true
		}
	}
	return false
}

// parseAccept splits an `Accept` header into its media ranges.
//
// A malformed entry is skipped rather than failing the whole header: one client sending a stray
// comma must not be told the server produces nothing it wants.
func parseAccept(header string) []mediaRange {
	var out []mediaRange
	for entry := range strings.SplitSeq(header, ",") {
		media, params, _ := strings.Cut(strings.TrimSpace(entry), ";")
		typ, sub, ok := strings.Cut(strings.ToLower(strings.TrimSpace(media)), "/")
		if !ok || typ == "" || sub == "" {
			continue
		}
		out = append(out, mediaRange{typ: typ, sub: sub, q: parseQuality(params)})
	}
	return out
}

// parseQuality reads the `q` parameter out of a media range's parameters. An absent or unreadable
// `q` is 1, per RFC 9110: a weight nobody wrote is a weight nobody meant to lower.
func parseQuality(params string) int {
	for param := range strings.SplitSeq(params, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(param), "=")
		if !ok || strings.ToLower(strings.TrimSpace(name)) != "q" {
			continue
		}
		q, valid := parsePerMille(strings.TrimSpace(value))
		if !valid {
			return qScale
		}
		return q
	}
	return qScale
}

// parsePerMille reads `1`, `1.0`, `0.5` or `0.001` as thousandths.
func parsePerMille(value string) (int, bool) {
	whole, frac, _ := strings.Cut(value, ".")
	units := 0
	if whole != "" {
		parsed, err := strconv.Atoi(whole)
		if err != nil || parsed < 0 {
			return 0, false
		}
		units = parsed
	}
	// Padded and truncated to exactly three digits, which is all RFC 9110 permits.
	frac += "000"
	thousandths, err := strconv.Atoi(frac[:3])
	if err != nil {
		return 0, false
	}
	q := units*qScale + thousandths
	if q > qScale {
		return qScale, true
	}
	return q, true
}

package consensus

import "github.com/prokopto-dev/tod-serve/internal/core"

// basisPoints is 100%. Ratios are integers in this repository — canonical conventions §3.
const basisPoints = 10000

// bounds is a resolved window: the two instants §6 computes from a ToD and a timer.
type bounds struct {
	open  core.Micros
	close core.Micros
}

// resolveBounds applies the §6 table. It reports false when there is no window to compute — no
// ToD, an unknown timer, or a timer whose kind promises offsets it does not carry.
//
// The last case cannot happen behind the schema's CHECK constraints, and degrading to "no window"
// rather than guessing an offset is the reading that fails safe: `no_timer` is an honest degraded
// state and a window built on a missing number is not.
func resolveBounds(t Timer, died core.Micros) (bounds, bool) {
	if t.Kind == WindowUnknown || t.OpenOffsetSeconds == nil {
		return bounds{}, false
	}
	open := died + core.Micros(*t.OpenOffsetSeconds*core.MicrosPerSecond)
	if t.Kind == WindowFixed {
		return bounds{open: open, close: open + core.Micros(t.FixedGraceSeconds*core.MicrosPerSecond)}, true
	}
	if t.CloseOffsetSeconds == nil {
		return bounds{}, false
	}
	return bounds{open: open, close: died + core.Micros(*t.CloseOffsetSeconds*core.MicrosPerSecond)}, true
}

// deriveWindow renders the §6 window. died is nil when no cluster anchors one, in which case the
// kind still reports the resolved timer and every boundary is null: "we know the timer and have no
// ToD" and "we have neither" are different facts and a client renders them differently.
func deriveWindow(t Timer, died *core.Micros, now core.Micros) Window {
	w := Window{Kind: t.Kind}
	if died == nil {
		return w
	}
	b, ok := resolveBounds(t, *died)
	if !ok {
		return w
	}

	open, closeAt := b.open, b.close
	w.OpenAt, w.CloseAt = &open, &closeAt
	if t.Kind == WindowFixed {
		spawn := b.open
		w.SpawnAt = &spawn
	} else {
		progress := progressBP(b, now)
		w.ProgressBP = &progress
	}
	untilOpen, untilClose := secondsUntil(b.open, now), secondsUntil(b.close, now)
	w.SecondsUntilOpen, w.SecondsUntilClose = &untilOpen, &untilClose
	return w
}

// progressBP is where now sits between open and close, in basis points by integer division,
// clamped to [0, 10000].
//
// The clamp runs before the multiplication rather than after it. A `now` far past a window would
// otherwise overflow int64 on the way to a number that was going to be clamped to 10000 anyway,
// and an overflowed progress bar is a wrong answer rather than a missing one.
func progressBP(b bounds, now core.Micros) int32 {
	if now <= b.open {
		return 0
	}
	if now >= b.close {
		return basisPoints
	}
	return int32(int64(now-b.open) * basisPoints / int64(b.close-b.open))
}

// secondsUntil is the signed offset from now to at, truncated toward zero. Negative means passed.
func secondsUntil(at, now core.Micros) int64 {
	return int64(at-now) / core.MicrosPerSecond
}

// deriveStatus applies §7. up outranks everything: a quake repopped the target and whatever ToD
// preceded it describes a life the target no longer has.
func deriveStatus(t Timer, died *core.Micros, now core.Micros, upSince *core.Micros) Status {
	switch {
	case upSince != nil:
		return StatusUp
	case died == nil:
		return StatusUnknown
	}
	b, ok := resolveBounds(t, *died)
	if !ok {
		return StatusNoTimer
	}
	switch {
	case now < b.open:
		return StatusPreWindow
	case now <= b.close:
		return StatusInWindow
	default:
		return StatusOverdue
	}
}

// windowStillOpen reports whether a cluster is still a live rival rather than history.
//
// A timer with no window has none to close, so its clusters never expire out of `alternatives`.
// Dropping them instead would hide a rival ToD on exactly the instances that know least, which is
// the opposite of what an honest degraded state does.
func windowStillOpen(t Timer, died core.Micros, now core.Micros) bool {
	b, ok := resolveBounds(t, died)
	if !ok {
		return true
	}
	return now <= b.close
}

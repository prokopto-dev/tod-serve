package api

import (
	"net"
	"sync"
	"time"

	"github.com/prokopto-dev/tod-serve/internal/core"
)

// The shared invite-code bucket's defaults.
//
// `previewInvite` and `createAuthorizationURL` both reveal whether an invite code is live, so they
// are metered from ONE bucket keyed on the caller rather than a bucket each — two buckets would
// simply hand a code-guesser twice the budget. Adding a third route that accepts a code means
// setting [Route.InviteOracle] on it, which joins this bucket rather than minting another.
const (
	// DefaultInviteBurst is how many attempts a caller may make back to back.
	DefaultInviteBurst = 10
	// DefaultInviteRefill is how long one attempt takes to come back.
	DefaultInviteRefill = 6 * time.Second
	// defaultTrackedCallers bounds the limiter's memory. An unauthenticated flood must not be able
	// to grow a map without limit, which would turn a rate limiter into the outage it prevents.
	defaultTrackedCallers = 4096
)

// RateLimit is a token bucket's shape: a burst, and how long one token takes to come back.
type RateLimit struct {
	// Burst is the bucket's capacity.
	Burst int
	// Refill is how long one token takes to return.
	Refill time.Duration
}

func (r RateLimit) orDefault() RateLimit {
	if r.Burst <= 0 {
		r.Burst = DefaultInviteBurst
	}
	if r.Refill <= 0 {
		r.Refill = DefaultInviteRefill
	}
	return r
}

// limiter is a token bucket per caller, refilled by the injected clock.
//
// The clock is injected like everywhere else, so a test asserts the refill by moving time rather
// than by waiting — SLEEP001 makes the alternative impossible anyway.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]bucketState
	limit   RateLimit
	maxKeys int
}

type bucketState struct {
	tokens   int
	lastSeen core.Micros
}

func newLimiter(limit RateLimit) *limiter {
	return &limiter{
		buckets: map[string]bucketState{},
		limit:   limit.orDefault(),
		maxKeys: defaultTrackedCallers,
	}
}

// allow takes a token for caller, and reports how long to wait when there is none.
func (l *limiter) allow(caller string, now core.Micros) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	state, known := l.buckets[caller]
	if !known {
		if len(l.buckets) >= l.maxKeys {
			l.evict(now)
		}
		state = bucketState{tokens: l.limit.Burst, lastSeen: now}
	} else {
		earned := int(now.Sub(state.lastSeen) / l.limit.Refill)
		if earned > 0 {
			state.tokens = min(state.tokens+earned, l.limit.Burst)
			state.lastSeen = state.lastSeen.Add(time.Duration(earned) * l.limit.Refill)
		}
	}

	if state.tokens <= 0 {
		l.buckets[caller] = state
		wait := l.limit.Refill - now.Sub(state.lastSeen)
		return false, max(1, int(wait.Seconds()))
	}
	state.tokens--
	if state.tokens == l.limit.Burst-1 {
		state.lastSeen = now
	}
	l.buckets[caller] = state
	return true, 0
}

// evict drops callers whose bucket has refilled completely, which is the definition of one that is
// no longer being limited. It is called only when the map is at its bound, so the ordinary path
// takes no scan at all.
func (l *limiter) evict(now core.Micros) {
	full := l.limit.Refill * time.Duration(l.limit.Burst)
	for key, state := range l.buckets {
		if now.Sub(state.lastSeen) >= full {
			delete(l.buckets, key)
		}
	}
	if len(l.buckets) < l.maxKeys {
		return
	}
	// Every tracked caller is still actively limited. Dropping the map wholesale would hand every
	// one of them a fresh burst, so the newest caller is simply not tracked and is allowed: a
	// limiter that fails closed here would let a flood lock out everybody else.
	for key := range l.buckets {
		delete(l.buckets, key)
		break
	}
}

// callerKey identifies the caller a public route is metered against.
//
// It is the socket's remote address, deliberately, and NOT `X-Forwarded-For`: that header is
// client-supplied, so metering on it would let anybody reset their own bucket by changing a string.
// An operator behind a reverse proxy therefore meters the proxy, and the correct fix is a trusted
// proxy list this codebase does not have yet — which is a smaller problem than a limit that is
// bypassed by one header.
func callerKey(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

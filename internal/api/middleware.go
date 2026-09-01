package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

type principalKey struct{}

// WithPrincipal returns a context carrying the authenticated principal.
func WithPrincipal(ctx context.Context, p auth.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the authenticated principal. The second result is false on a public route,
// which is the only place a handler should be asking.
//
// A handler on an authenticated route can rely on this: the middleware refused the request before
// the handler ran if there was no principal, so there is no "authenticated route with no caller"
// state for a handler to have to think about.
func PrincipalFrom(ctx context.Context) (auth.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(auth.Principal)
	return p, ok
}

type bodyKey struct{}

// bodyFrom returns the buffered request body. It is buffered by [withBufferedBody] so that the
// idempotency middleware can hash a request the handler has not read yet.
func bodyFrom(ctx context.Context) []byte {
	b, _ := ctx.Value(bodyKey{}).([]byte)
	return b
}

// MaxBodyBytes caps a request body. It is enforced at the outermost handler rather than per
// operation so that a body too large is refused before it is buffered, not after.
const MaxBodyBytes int64 = 1 << 20

// routeMiddleware is where authentication, tenancy, authorization and idempotency happen, once,
// before the handler runs.
//
// The ORDER is the rule, not an implementation detail:
//
//  1. A token in the query string is refused outright — canonical §7, no exception.
//  2. The credential is resolved, and the membership behind it is re-read on every request, so a
//     revocation takes effect on the next request rather than when a token expires.
//  3. The circle in the path is compared to the principal's circle. A mismatch is 404 — never 403
//     — and it happens HERE so that no handler can leak a circle's existence by answering 403
//     before it thought about tenancy.
//  4. Session-only, then step-up, then role, then scope. Each answers a different code because
//     each has a different fix.
//  5. Idempotency, last, because replaying a response for a request that was never authorized
//     would be worse than not replaying at all.
//
// Step 1 is at the TOP of this function rather than inside [Builder.authorize], and steps 2 to 5
// are reached through one tail. Both are structural: the two auth kinds that resolve no principal
// — metrics and setup — do not call `authorize` at all, so a rule living inside it was a rule they
// silently skipped. `TestRouteRegistry_EveryRoute_RefusesATokenInTheURL` drives the registry
// rather than a list, so the next auth kind is covered the day it is added.
func (b *Builder) routeMiddleware(r Route) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		// Recorded on the way out, whatever happened: a metric that only counts successes is a
		// metric that goes quiet exactly when something is wrong.
		defer func() { b.metrics.observe(r.ID, ctx.Status()) }()

		// First, for every route on both listeners, and before the rate limiter: a credential in
		// a query string is already in the access log of every proxy between here and the caller,
		// and refusing it after spending one of the caller's invite attempts would punish them for
		// our leak. `TOD_SETUP_TOKEN` is the worst one to lose this way — it takes the instance
		// over — and it reaches a route that resolves no principal at all.
		requestURL := ctx.URL()
		if err := auth.RejectTokenInURL(requestURL.Query()); err != nil {
			b.writeProblem(ctx, apierr.Wrap(apierr.CodeUnauthenticated, err,
				"a token in a URL is never accepted; use Authorization: Bearer"))
			return
		}

		// Before authentication and before the handler: a rejected probe must cost the instance
		// nothing to store, and `createAuthorizationURL` writes an `auth_flow` row.
		if r.InviteOracle {
			allowed, retryAfter := b.invites.allow(
				callerKey(ctx.RemoteAddr()), b.cfg.Clock.Now())
			if !allowed {
				b.writeProblem(ctx, apierr.New(apierr.CodeRateLimited,
					"too many invite-code attempts").WithRetryAfter(retryAfter))
				return
			}
		}

		if r.Auth == AuthMetricsToken {
			if err := b.checkMetricsToken(ctx); err != nil {
				b.writeProblem(ctx, err)
				return
			}
			b.dispatch(ctx, r, auth.Principal{}, next)
			return
		}

		// First-run setup, before anything that resolves a principal — there is nobody to resolve
		// on the database this route exists for. Both halves are here rather than in the handler
		// so that a setup route added later cannot be served with one of them missing: the token
		// is checked first, and only a caller who passed it learns whether setup is over.
		if r.Auth == AuthSetupToken {
			if err := b.checkSetupToken(ctx); err != nil {
				b.writeProblem(ctx, err)
				return
			}
			if err := b.checkSetupOpen(ctx); err != nil {
				b.writeProblem(ctx, err)
				return
			}
			b.dispatch(ctx, r, auth.Principal{}, next)
			return
		}

		p, err := b.authorize(ctx, r)
		if err != nil {
			b.writeProblem(ctx, err)
			return
		}

		if !p.IsZero() {
			ctx = huma.WithValue(ctx, principalKey{}, p)
		}
		b.dispatch(ctx, r, p, next)
	}
}

// dispatch is the one way out of [Builder.routeMiddleware], for every auth kind there is.
//
// It exists because the alternative was three `next(ctx)` calls, and two of them — the ones on the
// branches that resolve no principal — skipped idempotency entirely. `POST /setup` declares
// `CreatesState: true`, and it was served with no `Idempotency-Key` at all: the registry said one
// thing and the edge did another, which is the shape of bug a registry is supposed to make
// impossible. A route reaches its handler through here or not at all.
//
// The principal may be zero. `runIdempotent` requires and validates the header either way, and
// hands a keyless-record operation to the handler that owns its own replay — which is what
// `IdempotencyHandler` means, and the only thing available when there is no membership to key
// `(principal, key)` on.
func (b *Builder) dispatch(ctx huma.Context, r Route, p auth.Principal, next func(huma.Context)) {
	if !r.RequiresIdempotencyKey() {
		next(ctx)
		return
	}
	b.runIdempotent(ctx, r, p, next)
}

// authorize resolves the caller and decides whether the route is theirs to call.
//
// It does NOT re-check for a token in the query string: [Builder.routeMiddleware] does that for
// every route before this is reached. The check was here, and being here is exactly what let the
// two auth kinds that never call this function skip it.
func (b *Builder) authorize(ctx huma.Context, r Route) (auth.Principal, error) {
	requestURL := ctx.URL()
	query := requestURL.Query()
	if !r.Authenticated() {
		return auth.Principal{}, nil
	}

	creds := auth.Credentials{
		Authorization: ctx.Header("Authorization"),
		SessionCookie: cookieValue(ctx, auth.SessionCookie),
		Query:         query,
	}
	if !creds.Present() {
		return auth.Principal{}, apierr.New(apierr.CodeUnauthenticated,
			"this operation needs a credential")
	}
	p, err := b.cfg.Auth.Authenticate(ctx.Context(), creds)
	if err != nil {
		return auth.Principal{}, err
	}

	// Tenancy first, and before every permission question below. A 403 here would confirm that the
	// circle exists and that the caller found a real id, which is what canonical §7 hides.
	if r.CircleScoped {
		if err := checkTenancy(ctx, p); err != nil {
			return auth.Principal{}, err
		}
	}

	if r.SessionOnly() && p.Kind != auth.KindSession {
		return auth.Principal{}, apierr.New(apierr.CodeSessionRequired,
			"this operation is in the capability floor; no token reaches it at any scope")
	}
	if tier := r.StepUp(); tier != authz.StepUpNone {
		window := b.cfg.Auth.StepUpWindows().For(tier)
		if !p.SteppedUpWithin(b.cfg.Clock.Now(), window) {
			// The tier travels on the problem, not just the window. A console showing "prove who
			// you are again" wants to say WHICH bar it failed — five minutes to revoke a member
			// reads very differently from an hour to rename a circle, and a caller told only the
			// number has to guess which rule it came from.
			return auth.Principal{}, apierr.New(apierr.CodeStepUpRequired,
				"this operation needs a recently re-authenticated session").
				WithStepUpWindow(int(window.Seconds())).
				WithStepUpTier(string(tier))
		}
	}
	if err := checkPermission(p, r); err != nil {
		return auth.Principal{}, err
	}
	return p, nil
}

// checkTenancy compares the circle named in the path with the circle the principal belongs to.
//
// Anything that is not the principal's own circle answers 404, including a `circle_id` that is not
// a ULID at all. One rule with no branch: the moment there are two answers here, one of them is the
// one that tells a prober their guess was well-formed.
func checkTenancy(ctx huma.Context, p auth.Principal) error {
	raw := ctx.Param("circle_id")
	if raw == "" {
		// A circle-scoped route with no circle in the path is a registry bug, not a caller error.
		return apierr.New(apierr.CodeInternalError, "")
	}
	if raw != p.CircleID.String() {
		return apierr.New(apierr.CodeNotFound, "no such circle")
	}
	return nil
}

// checkPermission applies `granted permissions ∩ token scopes`, reporting which half failed.
//
// The two halves have different fixes — ask an officer for a role, or an instance owner for a
// grant, versus mint a token with the scope — so they have different codes. A single `forbidden`
// for both would send half the people who hit it to the wrong person.
//
// What granted the permission is [auth.Principal.Holds]'s business: a circle-realm key comes from
// the membership's role and an instance-realm one from the identity's `instance_grant` rows
// (ADR-0012). The distinction does not reach here, because the answer to "may you" is the same
// shape either way.
func checkPermission(p auth.Principal, r Route) error {
	if r.Auth == AuthSelf {
		// `self` operations are about the caller's own principal, so there is no permission to
		// check. A token still has to be entitled to act at all: `any` means any live token, and
		// its absence means the operation alters authentication state and needs a session.
		return nil
	}
	granted := false
	for _, perm := range r.Permissions {
		if p.Holds(perm) {
			granted = true
			break
		}
	}
	if !granted {
		return apierr.New(apierr.CodeForbidden,
			"you have not been granted the required permission")
	}
	for _, perm := range r.Permissions {
		if p.Can(perm) {
			return nil
		}
	}
	return apierr.New(apierr.CodeInsufficientScope,
		"your role holds the permission and this token's scopes do not reach it")
}

func cookieValue(ctx huma.Context, name string) string {
	raw := ctx.Header("Cookie")
	if raw == "" {
		return ""
	}
	// http.ParseCookie is the standard parser; a hand-rolled split would disagree with a browser
	// about quoting the first time somebody's display name ends up in another cookie.
	cookies, err := http.ParseCookie(raw)
	if err != nil {
		return ""
	}
	for _, c := range cookies {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// withBufferedBody makes the request body readable twice: once by the idempotency middleware,
// which has to hash it before the handler runs, and once by the handler itself.
//
// It is also where the body size limit is enforced, so an oversized body is refused before it is
// copied into memory rather than after.
func withBufferedBody(next http.Handler, limit int64, write func(http.ResponseWriter, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.ContentLength == 0 {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				write(w, apierr.Wrap(apierr.CodePayloadTooLarge, err,
					"the request body is larger than this operation accepts"))
				return
			}
			write(w, apierr.Wrap(apierr.CodeMalformedRequest, err,
				"the request body could not be read"))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), bodyKey{}, body)))
	})
}

// withAcceptableFormat refuses a request whose `Accept` this listener cannot satisfy.
//
// The framework's own behaviour is to fall back to JSON, which would answer a client that
// explicitly excluded JSON with JSON — a small lie told on every request. Turning its strict mode
// on instead would refuse a request with NO `Accept` at all, which means "anything" and must
// succeed. So the check is here, where both cases can be right.
//
// `served` is per listener. The API produces JSON and the metrics listener produces the Prometheus
// text exposition, and a single shared list meant the metrics listener refused every scraper —
// including one asking for exactly the format it was about to be sent.
func withAcceptableFormat(
	next http.Handler, served []string, write func(http.ResponseWriter, error),
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept := trimHeader(r.Header.Get("Accept"))
		if accept != "" && !acceptable(accept, served) {
			write(w, apierr.Newf(apierr.CodeNotAcceptable,
				"this endpoint produces %s; the Accept header admits none of it",
				strings.Join(served, ", ")))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withFrameworkProblems turns any error response that is not already a problem into one.
//
// The router answers some requests itself, before the framework or any handler sees them: an
// unmatched path is a 404 and a path that matches with a different method is a 405, both written
// as plain text. Those are the first responses a client hitting a typo will meet, and a client that
// has to parse `404 page not found` cannot branch on a code.
//
// It does NOT buffer a successful response. The decision is made at WriteHeader, where the status
// and the content type are both known: a response that is already a problem, or is not an error at
// all, is passed straight through with no copy.
func withFrameworkProblems(next http.Handler, write func(http.ResponseWriter, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pw := &problemWriter{ResponseWriter: w}
		next.ServeHTTP(pw, r)
		if !pw.decided || pw.passthrough {
			return
		}
		code, ok := apierr.CodeForStatus(pw.status)
		if !ok {
			code = apierr.CodeInternalError
		}
		write(w, apierr.New(code, http.StatusText(pw.status)))
	})
}

// problemWriter decides, once, whether a response is already shaped the way this API answers.
type problemWriter struct {
	http.ResponseWriter
	status      int
	decided     bool
	passthrough bool
}

func (w *problemWriter) WriteHeader(status int) {
	if w.decided {
		return
	}
	w.decided, w.status = true, status
	w.passthrough = status < http.StatusBadRequest ||
		strings.HasPrefix(w.Header().Get("Content-Type"), apierr.ContentType)
	if w.passthrough {
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *problemWriter) Write(b []byte) (int, error) {
	if !w.decided {
		w.WriteHeader(http.StatusOK)
	}
	if w.passthrough {
		n, err := w.ResponseWriter.Write(b)
		if err != nil {
			return n, fmt.Errorf("write response: %w", err)
		}
		return n, nil
	}
	// The router's plain-text body is discarded and replaced. Reporting the full length keeps the
	// writer honest with a caller that checks it.
	return len(b), nil
}

// withRequestID stamps every request with a correlation id and echoes it on the response.
//
// The id is a ULID like every other identifier here, so a log line, a problem body and a support
// question all quote the same shape of thing.
func withRequestID(next http.Handler, mint func() string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := mint()
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(WithRequestID(r.Context(), id)))
	})
}

// The two header values the API and the console both send.
//
// Named once because they are sent from two places that are not on one code path: the API chain
// below, and `secureConsole` in console.go, which sits OUTSIDE that chain deliberately. Two
// spellings of `nosniff` is not a bug today and is a divergence somebody introduces the first time
// one of them is tightened.
const (
	contentTypeOptions = "nosniff"
	referrerPolicy     = "no-referrer"
)

// withSecurityHeaders sets the headers every API response carries.
//
// `no-store` is the important one, and it is the one the console does NOT share: an API response
// can carry another circle's competitive intelligence, and a shared proxy caching it is a
// cross-tenant leak no test in this repository would ever see. The console's assets are hashed and
// immutable, so caching those is the point rather than the hazard.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", contentTypeOptions)
		h.Set("Referrer-Policy", referrerPolicy)
		next.ServeHTTP(w, r)
	})
}

// mintRequestID returns a request id generator over the injected clock and entropy source.
func mintRequestID(gen *core.Generator, now func() core.Micros) func() string {
	return func() string {
		u, err := gen.New(now())
		if err != nil {
			// A correlation id that could not be minted must not fail the request it was going to
			// label. An empty id is omitted from the problem body, which reads honestly as "there
			// is no id" rather than as a fake one.
			return ""
		}
		return u.String()
	}
}

// trimHeader normalises a header value the way every caller here wants it.
func trimHeader(v string) string { return strings.TrimSpace(v) }

// idempotencyTTL is how long a replayable response is kept. A day is long enough for every retry a
// client will make and short enough that the table does not become a second copy of the log.
const idempotencyTTL = 24 * time.Hour

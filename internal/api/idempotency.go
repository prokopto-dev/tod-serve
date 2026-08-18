package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// IdempotencyKeyHeader is the header a state-creating POST must carry.
const IdempotencyKeyHeader = "Idempotency-Key"

// IdempotencyReplayedHeader marks a response that was replayed rather than produced. Without it a
// client cannot tell a retry that worked from a request that ran twice, which is the one question
// idempotency exists to answer.
const IdempotencyReplayedHeader = "Idempotency-Replayed"

// maxIdempotencyKeyLen bounds the key. It is a client-chosen string that becomes an indexed column;
// a ULID is 26 characters and this leaves generous room for a client that prefers a UUID or a
// composite.
const maxIdempotencyKeyLen = 255

type idempotencyKeyKey struct{}

// IdempotencyKeyFrom returns the `Idempotency-Key` of the current request.
//
// It is how the handlers whose principal is not a membership — `redeemInvite` mints the membership
// this request is about, and an instance-realm create has none at all — reach the key. They own
// their own replay because `idempotency_record.principal_membership_id` is NOT NULL, so the shared
// table cannot hold their rows. The header is still required of them: the registry says so and the
// middleware enforces it, so a client has one rule rather than two.
func IdempotencyKeyFrom(ctx context.Context) (string, bool) {
	key, ok := ctx.Value(idempotencyKeyKey{}).(string)
	return key, ok
}

type captureKey struct{}

// captureState is the seam between the outer response recorder and the route middleware that
// decides what to do with the recorded response. The outer handler cannot know which operation it
// is serving; the route middleware cannot replace the response writer. This is how they meet.
type captureState struct {
	onComplete func(status int, body []byte)
}

// withIdempotencyCapture buffers the response of any request carrying an `Idempotency-Key`, so
// that the response can be stored for a retry to replay.
//
// It buffers ONLY those requests. Recording every response would put the whole API behind a memory
// copy for the benefit of the handful of operations that create domain state.
func withIdempotencyCapture(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(IdempotencyKeyHeader) == "" {
			next.ServeHTTP(w, r)
			return
		}
		state := &captureState{}
		rec := &responseRecorder{header: http.Header{}, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), captureKey{}, state)))

		// Recorded BEFORE the response is flushed. If the record is written and the flush then
		// fails, the client retries and replays; the other order would answer the client and then
		// forget, which turns one retry into two reports.
		if state.onComplete != nil {
			state.onComplete(rec.status, rec.body.Bytes())
		}
		rec.flushTo(w)
	})
}

// responseRecorder buffers a response so it can be stored before it is sent.
type responseRecorder struct {
	header http.Header
	body   bytes.Buffer
	status int
	wrote  bool
}

func (r *responseRecorder) Header() http.Header { return r.header }

func (r *responseRecorder) WriteHeader(status int) {
	if !r.wrote {
		r.status, r.wrote = status, true
	}
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	n, err := r.body.Write(b)
	if err != nil {
		return n, errors.New("buffer response body")
	}
	return n, nil
}

func (r *responseRecorder) flushTo(w http.ResponseWriter) {
	for k, values := range r.header {
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(r.status)
	// Deliberate waiver: the client has gone away, and there is no second response to send.
	_, _ = w.Write(r.body.Bytes())
}

// runIdempotent enforces `Idempotency-Key` and, where the principal is a membership, replays.
func (b *Builder) runIdempotent(
	ctx huma.Context, r Route, p auth.Principal, next func(huma.Context),
) {
	key := trimHeader(ctx.Header(IdempotencyKeyHeader))
	if key == "" {
		b.writeProblem(ctx, apierr.New(apierr.CodeIdempotencyKeyRequired,
			"this operation creates domain state; send an Idempotency-Key and reuse it on every retry").
			WithField("header.Idempotency-Key", "required"))
		return
	}
	if err := validateIdempotencyKey(key); err != nil {
		b.writeProblem(ctx, err)
		return
	}
	ctx = huma.WithValue(ctx, idempotencyKeyKey{}, key)

	if r.Idempotency == IdempotencyHandler || p.IsZero() {
		// No membership principal exists to key `(principal, key)` on. The header is required and
		// available; the handler owns the replay because only it knows what to key on.
		next(ctx)
		return
	}
	b.replayOrRun(ctx, p, key, next)
}

// replayOrRun is the `(membership, key)` path: the shared record, taken before the handler runs.
func (b *Builder) replayOrRun(
	ctx huma.Context, p auth.Principal, key string, next func(huma.Context),
) {
	requestCtx := ctx.Context()
	queries := b.cfg.Store.Queries()
	hash := requestHash(ctx)
	now := b.cfg.Clock.Now()

	existing, err := queries.GetIdempotencyRecord(requestCtx, sqlitegen.GetIdempotencyRecordParams{
		PrincipalMembershipID: p.MembershipID.String(),
		Key:                   key,
	})
	switch {
	case err == nil && core.Micros(existing.ExpiresAt).Before(now):
		// The record outlived its window. Clearing it is what lets a long-lived client reuse a key
		// it has forgotten about, rather than meeting a conflict it can never resolve.
		if _, delErr := queries.DeleteIdempotencyRecord(requestCtx, existing.ID); delErr != nil {
			b.writeProblem(ctx, apierr.Wrap(apierr.CodeInternalError, delErr, ""))
			return
		}
	case err == nil && !bytes.Equal(existing.RequestHash, hash):
		b.writeProblem(ctx, apierr.New(apierr.CodeIdempotencyKeyReused,
			"this Idempotency-Key was used for a different request").
			WithField("header.Idempotency-Key", "already used for a different request"))
		return
	case err == nil && existing.CompletedAt == nil:
		b.writeProblem(ctx, apierr.New(apierr.CodeIdempotencyConflict,
			"a request with this Idempotency-Key is still in flight; retry the same request"))
		return
	case err == nil:
		b.replay(ctx, existing)
		return
	case !errors.Is(err, store.ErrNoRows):
		b.writeProblem(ctx, apierr.Wrap(apierr.CodeInternalError, err, ""))
		return
	}

	recordID, err := core.NewID[core.IdempotencyRecord](b.cfg.IDs, now)
	if err != nil {
		b.writeProblem(ctx, apierr.Wrap(apierr.CodeInternalError, err, ""))
		return
	}
	created, err := queries.CreateIdempotencyRecord(requestCtx, sqlitegen.CreateIdempotencyRecordParams{
		ID:                    recordID.String(),
		PrincipalMembershipID: p.MembershipID.String(),
		Key:                   key,
		RequestHash:           hash,
		ExpiresAt:             int64(now.Add(idempotencyTTL)),
		CreatedAt:             int64(now),
		UpdatedAt:             int64(now),
	})
	if err != nil {
		// A unique-index collision means another request took the key between the read above and
		// this insert. That is exactly a retry racing itself, which is what the conflict code says.
		b.writeProblem(ctx, apierr.Wrap(apierr.CodeIdempotencyConflict, err,
			"a request with this Idempotency-Key is still in flight; retry the same request"))
		return
	}

	if state, ok := requestCtx.Value(captureKey{}).(*captureState); ok {
		state.onComplete = func(status int, body []byte) {
			b.recordOutcome(requestCtx, created.ID, status, body)
		}
	}
	next(ctx)
}

// replay writes a stored response back, marked as a replay.
func (b *Builder) replay(ctx huma.Context, rec sqlitegen.IdempotencyRecord) {
	status := http.StatusOK
	if rec.ResponseStatus != nil {
		status = int(*rec.ResponseStatus)
	}
	ctx.SetHeader("Content-Type", MediaTypeJSON)
	ctx.SetHeader(IdempotencyReplayedHeader, "true")
	ctx.SetStatus(status)
	if rec.ResponseBody != nil {
		// Deliberate waiver: the response has already been committed to by SetStatus, and a failed
		// write means the client is gone.
		_, _ = ctx.BodyWriter().Write([]byte(*rec.ResponseBody))
	}
}

// recordOutcome stores what the handler answered, or clears the record if it did not answer.
//
// A 5xx is not stored. The request did not complete, so a record claiming it did would answer every
// retry with `idempotency_conflict` forever — the opposite of what the key is for.
func (b *Builder) recordOutcome(ctx context.Context, id string, status int, body []byte) {
	queries := b.cfg.Store.Queries()
	if status >= http.StatusInternalServerError {
		if _, err := queries.DeleteIdempotencyRecord(ctx, id); err != nil {
			b.cfg.Log.ErrorContext(ctx, "clear idempotency record after a failed request",
				"record_id", id, "error", err)
		}
		return
	}
	now := b.cfg.Clock.Now()
	stored := string(body)
	_, err := queries.CompleteIdempotencyRecord(ctx, sqlitegen.CompleteIdempotencyRecordParams{
		ResponseStatus: ptrInt64(int64(status)),
		ResponseBody:   &stored,
		CompletedAt:    ptrInt64(int64(now)),
		UpdatedAt:      int64(now),
		ID:             id,
	})
	if err != nil {
		// The client already has its answer. Logging is all that is left, and a retry will simply
		// run again — which is worse than replaying and much better than failing the request.
		b.cfg.Log.ErrorContext(ctx, "record idempotent response",
			"record_id", id, "error", err)
	}
}

// requestHash identifies the request a key was used for: the method, the path and the body.
//
// Comparing it is what makes `idempotency_key_reused` possible. Without it, a client that reused a
// key for a different report would be told its second report succeeded, and be handed the first
// one's response.
func requestHash(ctx huma.Context) []byte {
	h := sha256.New()
	u := ctx.URL()
	// Deliberate waiver: hash.Hash.Write never returns an error.
	_, _ = h.Write([]byte(ctx.Method()))
	_, _ = h.Write([]byte("\n"))
	_, _ = h.Write([]byte(u.Path))
	_, _ = h.Write([]byte("\n"))
	_, _ = h.Write([]byte(u.RawQuery))
	_, _ = h.Write([]byte("\n"))
	_, _ = h.Write(bodyFrom(ctx.Context()))
	return h.Sum(nil)
}

// validateIdempotencyKey refuses a key that cannot be stored or read back.
func validateIdempotencyKey(key string) error {
	if len(key) > maxIdempotencyKeyLen {
		return apierr.Newf(apierr.CodeValidationFailed,
			"Idempotency-Key is %d characters; the maximum is %d", len(key), maxIdempotencyKeyLen).
			WithField("header.Idempotency-Key", "too long")
	}
	for _, r := range key {
		if r < 0x20 || r > 0x7e {
			return apierr.New(apierr.CodeValidationFailed,
				"Idempotency-Key must be printable ASCII").
				WithField("header.Idempotency-Key", "must be printable ASCII")
		}
	}
	return nil
}

func ptrInt64(v int64) *int64 { return &v }

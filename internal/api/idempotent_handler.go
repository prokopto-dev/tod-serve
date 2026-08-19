package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// runIdempotentHandler is the replay for an operation the registry marks [IdempotencyHandler].
//
// The shared middleware cannot do it for these: it replays before the handler runs, keyed on
// `(membership, key)`, and an instance-realm create is not about the caller's membership at all —
// the response it should replay is a circle, not something the principal owns. The header is still
// required, so a client has one rule rather than two, and this is what makes the retry mean
// something.
//
// The recorded response is stored inside the same call that produced it rather than by the outer
// capture middleware, so a handler that answered and then failed to record answers the retry by
// running again — which is the safe direction for a create guarded by a unique index.
func runIdempotentHandler[T any](
	ctx context.Context, b *Builder, p auth.Principal, key string, hash []byte,
	run func(context.Context) (T, error),
) (T, bool, error) {
	var zero T
	if key == "" {
		// Unreachable: the registry marks the route state-creating, so the middleware refused a
		// request with no key before this ran. Checked anyway, because "unreachable" is a claim
		// about the middleware that this function cannot verify.
		return zero, false, apierr.New(apierr.CodeIdempotencyKeyRequired,
			"this operation creates domain state; send an Idempotency-Key")
	}

	queries := b.cfg.Store.Queries()
	now := b.cfg.Clock.Now()
	existing, err := queries.GetIdempotencyRecord(ctx, sqlitegen.GetIdempotencyRecordParams{
		PrincipalMembershipID: p.MembershipID.String(), Key: key,
	})
	switch {
	case err == nil && core.Micros(existing.ExpiresAt).Before(now):
		if _, delErr := queries.DeleteIdempotencyRecord(ctx, existing.ID); delErr != nil {
			return zero, false, apierr.Wrap(apierr.CodeInternalError, delErr, "")
		}
	case err == nil && !equalHash(existing.RequestHash, hash):
		return zero, false, apierr.New(apierr.CodeIdempotencyKeyReused,
			"this Idempotency-Key was used for a different request").
			WithField("header.Idempotency-Key", "already used for a different request")
	case err == nil && (existing.CompletedAt == nil || existing.ResponseBody == nil):
		return zero, false, apierr.New(apierr.CodeIdempotencyConflict,
			"a request with this Idempotency-Key is still in flight; retry the same request")
	case err == nil:
		var replayed T
		if unmarshalErr := json.Unmarshal([]byte(*existing.ResponseBody), &replayed); unmarshalErr != nil {
			return zero, false, apierr.Wrap(apierr.CodeInternalError, unmarshalErr, "")
		}
		return replayed, true, nil
	case !errors.Is(err, store.ErrNoRows):
		return zero, false, apierr.Wrap(apierr.CodeInternalError, err, "")
	}

	recordID, err := core.NewID[core.IdempotencyRecord](b.cfg.IDs, now)
	if err != nil {
		return zero, false, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	created, err := queries.CreateIdempotencyRecord(ctx, sqlitegen.CreateIdempotencyRecordParams{
		ID: recordID.String(), PrincipalMembershipID: p.MembershipID.String(), Key: key,
		RequestHash: hash, ExpiresAt: int64(now.Add(idempotencyTTL)),
		CreatedAt: int64(now), UpdatedAt: int64(now),
	})
	if err != nil {
		// A unique-index collision means another request took the key between the read above and
		// this insert, which is exactly a retry racing itself.
		return zero, false, apierr.Wrap(apierr.CodeIdempotencyConflict, err,
			"a request with this Idempotency-Key is still in flight; retry the same request")
	}

	out, err := run(ctx)
	if err != nil {
		// The handler failed, so there is nothing to replay. The record is cleared rather than
		// left incomplete: an incomplete record answers every retry with `idempotency_conflict`
		// until it expires, which is the opposite of what the key is for.
		if _, delErr := queries.DeleteIdempotencyRecord(ctx, created.ID); delErr != nil {
			b.cfg.Log.ErrorContext(ctx, "clear idempotency record after a failed request",
				"record_id", created.ID, "error", delErr)
		}
		return zero, false, err
	}

	body, err := json.Marshal(out)
	if err != nil {
		return zero, false, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	stored := string(body)
	status := int64(http.StatusOK)
	completedAt := int64(now)
	if _, err := queries.CompleteIdempotencyRecord(ctx,
		sqlitegen.CompleteIdempotencyRecordParams{
			ResponseStatus: &status, ResponseBody: &stored, CompletedAt: &completedAt,
			UpdatedAt: int64(now), ID: created.ID,
		}); err != nil {
		// The domain write already happened and the caller is about to get its answer. Losing the
		// record costs a replay, not correctness.
		b.cfg.Log.ErrorContext(ctx, "record idempotent response",
			"record_id", created.ID, "error", err)
	}
	return out, false, nil
}

// hashBody identifies the request a key was used for, so a key reused for a DIFFERENT request is
// `idempotency_key_reused` rather than a replay of somebody else's answer.
func hashBody(parts ...string) []byte {
	h := sha256.New()
	for _, p := range parts {
		// Length-prefixed, so two requests whose fields differ only in where one ends and the next
		// begins cannot hash the same.
		// Deliberate waiver: hash.Hash.Write is documented never to return an error.
		_, _ = h.Write([]byte(strconv.Itoa(len(p)) + ":" + p))
	}
	return h.Sum(nil)
}

func equalHash(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

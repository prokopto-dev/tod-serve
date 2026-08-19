package invite

import (
	"context"
	"errors"
	"fmt"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// Kind says which row a code resolved to.
type Kind string

const (
	// KindInvite is an ordinary `invite` row.
	KindInvite Kind = "invite"
	// KindOwnerGrant is the one-time owner grant the CLI prints. See [Grant] for why it is not an
	// invite.
	KindOwnerGrant Kind = "owner_grant"
)

// Resolved is what a code names, whichever row it came from.
//
// `previewInvite` and `/join` both take one of these, so the two endpoints cannot disagree about
// what a code grants — which is the failure a second lookup path would produce, and the one that
// would show up as somebody joining with a role the preview did not show them.
type Resolved struct {
	Kind     Kind
	Code     Code
	InviteID core.InviteID
	CircleID core.CircleID
	Role     authz.Role
	MaxUses  int
	Uses     int
	// ExpiresAt is when it stops working. Both kinds have one: there are no eternal invites and
	// no eternal grants.
	ExpiresAt core.Micros
}

// Resolve reads the row a caller's code names, and refuses one that can no longer be redeemed.
//
// It takes the transaction's query set so `/join` can resolve and redeem under one write lock. A
// resolve outside a transaction is an answer that was true a moment ago, which is fine for a
// preview and is not fine for a redemption.
//
// Unknown, unparseable and never-issued all answer `404 invite_invalid`, identically. A dead code
// answers with the reason it is dead, which is exactly `previewInvite`'s disclosure — and
// therefore the ceiling every other public route that takes a code is held to.
func Resolve(
	ctx context.Context, q *sqlitegen.Queries, raw string, now core.Micros,
) (Resolved, error) {
	code, err := Parse(raw)
	if err != nil {
		// A malformed code and an unissued one are indistinguishable on the wire. Telling them
		// apart would hand a guesser a free syntax check.
		return Resolved{}, apierr.Wrap(apierr.CodeInviteInvalid, err, "no such invite")
	}

	row, err := q.GetInviteByCodeHash(ctx, Hash(code))
	switch {
	case err == nil:
		return resolvedInvite(row, code, now)
	case !store.IsNotFound(err):
		return Resolved{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}

	grant, err := ReadGrant(ctx, q, code)
	if errors.Is(err, store.ErrNoRows) {
		return Resolved{}, apierr.New(apierr.CodeInviteInvalid, "no such invite")
	}
	if err != nil {
		return Resolved{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	return resolvedGrant(grant, code, now)
}

func resolvedInvite(row sqlitegen.Invite, code Code, now core.Micros) (Resolved, error) {
	view, err := toView(row, now)
	if err != nil {
		return Resolved{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	// Ordered most-specific first: a revoked invite that has also expired is REVOKED, because
	// that is the fact an officer acted on and the one they will look for. internal/identity
	// orders it the same way, and the two must agree or `createAuthorizationURL` and `/join` would
	// give a code holder two different stories.
	switch {
	case view.RevokedAt != nil:
		return Resolved{}, apierr.New(apierr.CodeInviteRevoked, "this invite has been revoked")
	case !now.Before(view.ExpiresAt):
		return Resolved{}, apierr.New(apierr.CodeInviteExpired, "this invite has expired")
	case view.Uses >= view.MaxUses:
		return Resolved{}, apierr.New(apierr.CodeInviteExhausted,
			"this invite has been used as many times as it allows")
	}
	role, err := authz.ParseRole(view.Role)
	if err != nil {
		return Resolved{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	return Resolved{
		Kind: KindInvite, Code: code, InviteID: view.ID, CircleID: view.CircleID, Role: role,
		MaxUses: view.MaxUses, Uses: view.Uses, ExpiresAt: view.ExpiresAt,
	}, nil
}

func resolvedGrant(grant Grant, code Code, now core.Micros) (Resolved, error) {
	circleID, err := core.ParseID[core.Circle](grant.CircleID)
	if err != nil {
		return Resolved{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	// A grant is single-use by construction, so a consumed one is exhausted rather than revoked:
	// the code was spent, which is the same fact `invite.uses >= max_uses` records.
	switch {
	case !grant.ConsumedAt.IsZero():
		return Resolved{}, apierr.New(apierr.CodeInviteExhausted,
			"this owner grant has already been redeemed")
	case !now.Before(grant.ExpiresAt):
		return Resolved{}, apierr.New(apierr.CodeInviteExpired, "this owner grant has expired")
	}
	return Resolved{
		Kind: KindOwnerGrant, Code: code, CircleID: circleID, Role: grant.Role(),
		MaxUses: 1, Uses: 0, ExpiresAt: grant.ExpiresAt,
	}, nil
}

// Redeem consumes a resolved code and records who redeemed it.
//
// It runs inside the caller's transaction, next to the membership row it admits, because the two
// have to happen together: a consumed invite with no membership locks somebody out of a circle
// they were invited to, and a membership with an unconsumed invite hands the next holder a free
// seat.
//
// The consumption is a conditional UPDATE, not a read-then-write. `ConsumeInvite` carries
// `uses < max_uses` and `ConsumeMeta` compares the whole previous value, so two redemptions racing
// each other produce one membership and one refusal rather than two memberships.
func Redeem(
	ctx context.Context, q *sqlitegen.Queries, resolved Resolved,
	membershipID core.MembershipID, identityID string, now core.Micros,
	ids *core.Generator,
) error {
	switch resolved.Kind {
	case KindOwnerGrant:
		if _, err := ConsumeGrant(ctx, q, resolved.Code, now); err != nil {
			if errors.Is(err, ErrGrantConsumed) {
				return apierr.Wrap(apierr.CodeInviteExhausted, err,
					"this owner grant has already been redeemed")
			}
			return apierr.Wrap(apierr.CodeInternalError, err, "")
		}
		// There is no `invite_redemption` row to write: the table's `invite_id` is NOT NULL and a
		// grant is not an invite. The audit log is where a grant redemption is recorded, and the
		// caller writes it — see internal/membership.
		return nil

	case KindInvite:
		_, err := q.ConsumeInvite(ctx, sqlitegen.ConsumeInviteParams{
			UpdatedAt: int64(now), CircleID: resolved.CircleID.String(),
			ID: resolved.InviteID.String(), Now: int64(now),
		})
		if store.IsNotFound(err) {
			// It was live when it resolved and is not now. The specific reason is worth a second
			// read: "expired" and "someone else took the last use" send a person to two different
			// places.
			return deadInviteReason(ctx, q, resolved, now)
		}
		if err != nil {
			return apierr.Wrap(apierr.CodeInternalError, err, "")
		}
		redemptionID, err := core.NewID[core.InviteRedemption](ids, now)
		if err != nil {
			return apierr.Wrap(apierr.CodeInternalError, err, "")
		}
		var identity *string
		if identityID != "" {
			identity = &identityID
		}
		_, err = q.CreateInviteRedemption(ctx, sqlitegen.CreateInviteRedemptionParams{
			ID:           redemptionID.String(),
			CircleID:     resolved.CircleID.String(),
			InviteID:     resolved.InviteID.String(),
			MembershipID: membershipID.String(),
			IdentityID:   identity,
			CreatedAt:    int64(now),
		})
		if err != nil {
			return apierr.Wrap(apierr.CodeInternalError, err, "")
		}
		return nil

	default:
		// Unreachable: [Resolve] closes the set. Named anyway, because an unreachable default that
		// returns nil is how a new kind gets silently admitted.
		return apierr.Wrap(apierr.CodeInternalError,
			fmt.Errorf("redeem %q: unknown resolved kind", resolved.Kind), "")
	}
}

func deadInviteReason(
	ctx context.Context, q *sqlitegen.Queries, resolved Resolved, now core.Micros,
) error {
	row, err := q.GetInvite(ctx, sqlitegen.GetInviteParams{
		CircleID: resolved.CircleID.String(), ID: resolved.InviteID.String(),
	})
	if err != nil {
		return apierr.Wrap(apierr.CodeInviteInvalid, err, "no such invite")
	}
	_, resolveErr := resolvedInvite(row, resolved.Code, now)
	if resolveErr != nil {
		return resolveErr
	}
	// The row still reads live, so the conditional UPDATE lost a race it can only lose by the last
	// use being taken. Reporting anything else here would be a guess.
	return apierr.New(apierr.CodeInviteExhausted,
		"this invite has been used as many times as it allows")
}

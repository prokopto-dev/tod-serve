package membership

import (
	"context"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/audit"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/invite"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// Revoke ends a membership.
//
// Three things happen, and they happen together or not at all:
//
//  1. `revoked_at` is set. Nothing else. There is no token sweep, because `internal/auth` re-reads
//     the membership on every request — so this takes effect on the revoked member's next one.
//  2. If the member is WEAKLY revocable and `circle.revoke_invalidates_invites` is on, every
//     outstanding invite in the circle is revoked in the same transaction. A weak revocation's
//     damage is not the re-entry; it is the officers' belief that revocation worked, and an invite
//     still lying in Discord scrollback is the door it walks back through.
//  3. The audit row is appended, inside the same transaction, so it cannot survive a rollback.
//
// Their reports still count, and their retractions still apply. Revocation controls access, never
// history.
func (s *Service) Revoke(
	ctx context.Context, circleID core.CircleID, id core.MembershipID, actor core.MembershipID,
	reason string,
) (Revoked, error) {
	now := s.clock.Now()
	var (
		invitesRevoked int
		liveInvites    int
	)
	err := s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		existing, txErr := q.GetMembership(ctx, sqlitegen.GetMembershipParams{
			CircleID: circleID.String(), ID: id.String(),
		})
		if store.IsNotFound(txErr) {
			return apierr.New(apierr.CodeNotFound, "no such member")
		}
		if txErr != nil {
			return txErr
		}
		if existing.RevokedAt != nil {
			return apierr.New(apierr.CodeConflict, "this membership is already revoked")
		}
		if existing.Role == string(authz.RoleOwner) {
			if txErr = requireAnotherOwner(ctx, q, circleID, id); txErr != nil {
				return txErr
			}
		}

		at := int64(now)
		by := actor.String()
		revokedRow, txErr := q.RevokeMembership(ctx, sqlitegen.RevokeMembershipParams{
			RevokedAt: &at, RevokedByMembershipID: &by,
			RevokeReason: nullable(reason), UpdatedAt: at,
			CircleID: circleID.String(), ID: id.String(),
		})
		if store.IsNotFound(txErr) {
			// Somebody else revoked it between the read and the write. One revocation is enough.
			return apierr.New(apierr.CodeConflict, "this membership is already revoked")
		}
		if txErr != nil {
			return txErr
		}

		view, txErr := toView(ctx, q, revokedRow)
		if txErr != nil {
			return txErr
		}
		circleRow, txErr := q.GetCircle(ctx, circleID.String())
		if txErr != nil {
			return txErr
		}
		weak := view.RevocationStrength == string(identity.StrengthWeak)
		if weak && circleRow.RevokeInvalidatesInvites == 1 {
			invitesRevoked, txErr = invite.RevokeAllLive(ctx, q, circleID, now)
			if txErr != nil {
				return txErr
			}
			if invitesRevoked > 0 {
				if txErr = audit.Append(ctx, q, s.ids, now, audit.Entry{
					CircleID: circleID, Actor: actor, Action: audit.ActionInvitesRevoked,
					EntityType: "invite", EntityID: "",
					Detail: map[string]any{
						"count": invitesRevoked, "because": "weak_member_revoked",
						"membership_id": id.String(),
					},
				}); txErr != nil {
					return txErr
				}
			}
		}

		liveInvites, txErr = invite.CountLive(ctx, q, circleID, now)
		if txErr != nil {
			return txErr
		}
		return audit.Append(ctx, q, s.ids, now, audit.Entry{
			CircleID: circleID, Actor: actor, Action: audit.ActionMemberRevoked,
			EntityType: "membership", EntityID: id.String(),
			Detail: map[string]any{
				"role": view.Role, "revocation_strength": view.RevocationStrength,
				"invites_revoked": invitesRevoked, "reason_given": reason != "",
			},
		})
	})
	if err != nil {
		return Revoked{}, coded(err)
	}

	view, err := s.Get(ctx, circleID, id)
	if err != nil {
		return Revoked{}, err
	}
	s.log.InfoContext(ctx, "membership revoked",
		"circle_id", circleID.String(), "membership_id", id.String(),
		"revocation_strength", view.RevocationStrength, "invites_revoked", invitesRevoked)
	return Revoked{Member: view, ActiveInviteCount: liveInvites, InvitesRevoked: invitesRevoked}, nil
}

// Reinstate is the only way back in.
//
// It is a separate, audited operation requiring `member.revoke` rather than a side effect of
// redeeming another invite, because the partial unique index makes a second membership row
// unrepresentable — so "let them redeem a new invite" is not a path that exists, and this is what
// replaces it.
func (s *Service) Reinstate(
	ctx context.Context, circleID core.CircleID, id core.MembershipID, actor core.MembershipID,
) (Member, error) {
	now := s.clock.Now()
	err := s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		existing, txErr := q.GetMembership(ctx, sqlitegen.GetMembershipParams{
			CircleID: circleID.String(), ID: id.String(),
		})
		if store.IsNotFound(txErr) {
			return apierr.New(apierr.CodeNotFound, "no such member")
		}
		if txErr != nil {
			return txErr
		}
		if existing.RevokedAt == nil {
			return apierr.New(apierr.CodeConflict, "this membership is not revoked")
		}
		if _, txErr = q.ReinstateMembership(ctx, sqlitegen.ReinstateMembershipParams{
			UpdatedAt: int64(now), CircleID: circleID.String(), ID: id.String(),
		}); txErr != nil {
			return txErr
		}
		return audit.Append(ctx, q, s.ids, now, audit.Entry{
			CircleID: circleID, Actor: actor, Action: audit.ActionMemberReinstated,
			EntityType: "membership", EntityID: id.String(),
			Detail: map[string]any{"role": existing.Role},
		})
	})
	if err != nil {
		return Member{}, coded(err)
	}
	s.log.InfoContext(ctx, "membership reinstated",
		"circle_id", circleID.String(), "membership_id", id.String())
	return s.Get(ctx, circleID, id)
}

// AcceptsWeakProvider is re-exported so a caller that already holds this service does not need the
// circle service to answer the one circle question minting an invite asks.
func (s *Service) AcceptsWeakProvider(ctx context.Context, circleID core.CircleID) (bool, error) {
	return circle.AcceptsWeakProvider(ctx, s.db.Queries(), circleID)
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

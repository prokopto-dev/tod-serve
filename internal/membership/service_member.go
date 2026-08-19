package membership

import (
	"context"
	"time"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/audit"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// CreateServiceRequest is `createServiceMember`: a bot, and the human answerable for it.
type CreateServiceRequest struct {
	CircleID core.CircleID
	// Actor is the officer or owner making the request. It is the default owner of the service
	// membership, because the person who created a bot is the obvious person to answer for it.
	Actor       core.MembershipID
	DisplayName string
	Role        string
	// OwnerMembershipID names the responsible human. It defaults to Actor and must be a live
	// HUMAN membership of the same circle: a bot owned by a bot names nobody.
	OwnerMembershipID string
	ClientName        string
	Scopes            []string
	TokenTTL          time.Duration
}

// ServiceCreated is a new service membership and its token, returned once.
type ServiceCreated struct {
	Membership Member        `json:"membership"`
	Token      Token         `json:"token"`
	Circle     circle.Circle `json:"circle"`
	AsOf       core.Micros   `json:"as_of"`
}

// CreateService creates a `kind='service'` membership and mints its token.
//
// [ADR-0005] moved tokens off service accounts and onto memberships, and this is what survives of
// DKP ADR-0011's actual guarantee: `owner_membership_id` is NOT NULL for a service membership, so
// the audit always names a responsible human. The consequence is stated there and is real — the
// bot's token dies when its owner's membership is revoked.
//
// [ADR-0005]: docs/adr/0005-pats-bound-to-memberships.md
func (s *Service) CreateService(
	ctx context.Context, req CreateServiceRequest,
) (ServiceCreated, error) {
	displayName, err := validDisplayName(req.DisplayName)
	if err != nil {
		return ServiceCreated{}, err
	}
	role := authz.RoleMember
	if req.Role != "" {
		parsed, roleErr := authz.ParseRole(req.Role)
		if roleErr != nil {
			return ServiceCreated{}, apierr.Newf(apierr.CodeValidationFailed,
				"%q is not a role", req.Role).WithField("body.role", "not a role")
		}
		role = parsed
	}
	if role == authz.RoleOwner {
		// Refused rather than allowed-and-useless. A PAT reaches no capability-floor permission at
		// any scope, and every permission an owner adds over an officer is in the floor — so an
		// `owner` service membership would advertise in the member list a capability its only
		// credential can never exercise. That is the confident mistake, not a missing feature.
		return ServiceCreated{}, apierr.New(apierr.CodeValidationFailed,
			"a service membership cannot be an owner; no token reaches an owner-only operation at any scope").
			WithField("body.role", "must be officer, member or observer")
	}
	scopes, err := ParseScopes(req.Scopes)
	if err != nil {
		return ServiceCreated{}, err
	}
	name, err := tokenName(req.ClientName)
	if err != nil {
		return ServiceCreated{}, err
	}

	owner := req.OwnerMembershipID
	if owner == "" {
		owner = req.Actor.String()
	}
	ownerID, err := core.ParseID[core.Membership](owner)
	if err != nil {
		return ServiceCreated{}, apierr.Wrap(apierr.CodeValidationFailed, err,
			"owner_membership_id is not a membership id").
			WithField("body.owner_membership_id", "not a membership id")
	}

	now := s.clock.Now()
	var out ServiceCreated
	err = s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		ownerRow, txErr := q.GetMembership(ctx, sqlitegen.GetMembershipParams{
			CircleID: req.CircleID.String(), ID: ownerID.String(),
		})
		if store.IsNotFound(txErr) {
			return apierr.New(apierr.CodeValidationFailed,
				"owner_membership_id names nobody in this circle").
				WithField("body.owner_membership_id", "not a member of this circle")
		}
		if txErr != nil {
			return txErr
		}
		switch {
		case ownerRow.RevokedAt != nil:
			return apierr.New(apierr.CodeValidationFailed,
				"owner_membership_id names a revoked member").
				WithField("body.owner_membership_id", "membership is revoked")
		case ownerRow.Kind != schemaenum.MembershipKindHuman:
			return apierr.New(apierr.CodeValidationFailed,
				"a service membership is owned by a human, so that the audit names one").
				WithField("body.owner_membership_id", "must be a human membership")
		}

		membershipID, txErr := core.NewID[core.Membership](s.ids, now)
		if txErr != nil {
			return txErr
		}
		ownerRef := ownerRow.ID
		if _, txErr = q.CreateMembership(ctx, sqlitegen.CreateMembershipParams{
			ID: membershipID.String(), CircleID: req.CircleID.String(),
			Kind: schemaenum.MembershipKindService, OwnerMembershipID: &ownerRef,
			DisplayName: displayName, DisplayNameNorm: core.Normalise(displayName),
			Role: string(role), JoinedAt: int64(now),
			CreatedAt: int64(now), UpdatedAt: int64(now),
		}); txErr != nil {
			return txErr
		}

		token, txErr := s.mintToken(ctx, q, membershipID, name, scopes, req.TokenTTL, now)
		if txErr != nil {
			return txErr
		}
		if txErr = audit.Append(ctx, q, s.ids, now, audit.Entry{
			CircleID: req.CircleID, Actor: req.Actor, Action: audit.ActionServiceMemberJoined,
			EntityType: "membership", EntityID: membershipID.String(),
			Detail: map[string]any{
				"role": string(role), "owner_membership_id": ownerRef,
				"token_prefix": token.Prefix, "scopes": token.Scopes,
			},
		}); txErr != nil {
			return txErr
		}

		view, txErr := s.viewIn(ctx, q, req.CircleID, membershipID)
		if txErr != nil {
			return txErr
		}
		circleView, txErr := circle.Read(ctx, q, req.CircleID, now)
		if txErr != nil {
			return txErr
		}
		out = ServiceCreated{Membership: view, Token: token, Circle: circleView, AsOf: now}
		return nil
	})
	if err != nil {
		return ServiceCreated{}, coded(err)
	}
	return out, nil
}

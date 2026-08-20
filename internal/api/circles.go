package api

import (
	"context"
	"errors"
	"strings"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

// CircleResponse is one circle, and the instant it was read.
//
// `as_of` sits on the RESPONSE and not on the view, which is what lets the ETag be computed over
// the view alone: a tag that included the read time would change on every request and turn every
// `If-Match` into a `412`.
type CircleResponse struct {
	circle.Circle
	AsOf core.Micros `json:"as_of"`
}

type listCirclesInput struct{}

type listCirclesOutput struct{ Body Page[circle.Circle] }

type getCircleInput struct {
	CircleID    string `path:"circle_id" doc:"The circle"`
	IfNoneMatch string `header:"If-None-Match" doc:"Revalidate a cached copy"`
}

type getCircleOutput struct {
	ETag string `header:"ETag"`
	Body CircleResponse
}

type createCircleInput struct {
	Body struct {
		Name        string `json:"name" doc:"The circle's name, unique per server" maxLength:"80"`
		Description string `json:"description,omitempty" doc:"Free text" maxLength:"500"`
		Server      string `json:"server" doc:"blue, green or red. Immutable after creation" enum:"blue,green,red"`
		Timezone    string `json:"timezone,omitempty" doc:"IANA timezone, display only. Defaults to UTC"`
	}
}

type createCircleOutput struct{ Body CircleResponse }

type updateCircleInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
	IfMatch  string `header:"If-Match" doc:"The ETag a previous read returned"`
	Body     struct {
		Name                     *string `json:"name,omitempty" maxLength:"80"`
		Description              *string `json:"description,omitempty" maxLength:"500"`
		Timezone                 *string `json:"timezone,omitempty"`
		MinReportersToSupersede  *int    `json:"min_reporters_to_supersede,omitempty" minimum:"1"`
		RevokeInvalidatesInvites *bool   `json:"revoke_invalidates_invites,omitempty" doc:"Owner only: it decides whether revoking a weakly-revocable member also kills the circle's live invites"`
		State                    *string `json:"state,omitempty" enum:"active,archived"`
		// Server is accepted only so that sending it is REFUSED with the code that says why.
		// Ignoring it would let a client believe a circle had moved server, and ADR-0009 makes
		// that the one thing a circle can never do.
		Server *string `json:"server,omitempty" doc:"Rejected with 422 field_immutable; a circle is pinned to one server"`
	}
}

type updateCircleOutput struct {
	ETag string `header:"ETag"`
	Body CircleResponse
}

type setCircleProvidersInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
	IfMatch  string `header:"If-Match" doc:"The ETag a previous read of the circle returned"`
	Body     struct {
		Providers []struct {
			Key                    string   `json:"key" doc:"The provider's wire key, from listIdentityProviders"`
			DiscordGuildID         string   `json:"discord_guild_id,omitempty" doc:"The guild this circle requires membership of"`
			DiscordRequiredRoleIDs []string `json:"discord_required_role_ids,omitempty" doc:"Any one of these roles admits. Empty means anyone in the guild"`
		} `json:"providers" doc:"The complete set this circle accepts. Omitting one stops NEW joins through it and revokes nobody"`
		AcknowledgeWeakRevocation bool `json:"acknowledge_weak_revocation,omitempty" doc:"Required to accept a provider with no verifiable subject"`
	}
}

type deleteCircleInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
}

type deleteCircleOutput struct{ Body CircleResponse }

type setCircleProvidersOutput struct {
	ETag string `header:"ETag"`
	Body CircleResponse
}

// registerCircles attaches the circle operations.
//
// `deleteCircle` writes a TOMBSTONE rather than removing rows, and that is the resolution of a
// conflict rather than a shortcut: the API design said it deletes "the circle and every report in
// it", and `tod_report`, `quake_event`, `invite_redemption` and `audit_log` are append-only by
// trigger — with `foreign_keys` ON, a circle with any of those rows cannot be deleted at all. The
// invariant wins. See [circle.Service.Delete].
func (s *Server) registerCircles() error {
	return errors.Join(
		registerFailure(OpListCircles, Register(s.api, OpListCircles,
			func(ctx context.Context, _ *listCirclesInput) (*listCirclesOutput, error) {
				p, ok := PrincipalFrom(ctx)
				if !ok {
					return nil, apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
				}
				// A principal is bound to exactly one membership, so this returns exactly one
				// circle. There is no list-all operation at any permission level: a circle's
				// existence is part of what it is hiding.
				view, err := s.cfg.Circles.Get(ctx, p.CircleID)
				if err != nil {
					return nil, err
				}
				return &listCirclesOutput{
					Body: NewPage([]circle.Circle{view}, "", false, s.cfg.Clock.Now()),
				}, nil
			})),

		registerFailure(OpCreateCircle, Register(s.api, OpCreateCircle,
			func(ctx context.Context, in *createCircleInput) (*createCircleOutput, error) {
				p, ok := PrincipalFrom(ctx)
				if !ok {
					return nil, apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
				}
				key, _ := IdempotencyKeyFrom(ctx)
				hash := hashBody("createCircle", in.Body.Name, in.Body.Server,
					in.Body.Description, in.Body.Timezone)

				out, _, err := runIdempotentHandler(ctx, s.api, p, key, hash,
					func(ctx context.Context) (CircleResponse, error) {
						view, createErr := s.cfg.Circles.Create(ctx, circle.CreateRequest{
							Name:        in.Body.Name,
							Description: in.Body.Description,
							Server:      core.Server(in.Body.Server),
							Timezone:    in.Body.Timezone,
						})
						if createErr != nil {
							return CircleResponse{}, createErr
						}
						return CircleResponse{Circle: view, AsOf: s.cfg.Clock.Now()}, nil
					})
				if err != nil {
					return nil, err
				}
				return &createCircleOutput{Body: out}, nil
			})),

		registerFailure(OpGetCircle, Register(s.api, OpGetCircle,
			func(ctx context.Context, in *getCircleInput) (*getCircleOutput, error) {
				id, err := parseCircleID(in.CircleID)
				if err != nil {
					return nil, err
				}
				view, err := s.cfg.Circles.Get(ctx, id)
				if err != nil {
					return nil, err
				}
				etag, err := ETagOf(view)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}
				return &getCircleOutput{
					ETag: etag,
					Body: CircleResponse{Circle: view, AsOf: s.cfg.Clock.Now()},
				}, nil
			})),

		registerFailure(OpUpdateCircle, Register(s.api, OpUpdateCircle,
			func(ctx context.Context, in *updateCircleInput) (*updateCircleOutput, error) {
				id, err := parseCircleID(in.CircleID)
				if err != nil {
					return nil, err
				}
				if err := requireSecurityKeyForInvalidation(ctx, in); err != nil {
					return nil, err
				}
				current, err := s.cfg.Circles.Get(ctx, id)
				if err != nil {
					return nil, err
				}
				if err := RequireIfMatch(in.IfMatch, current); err != nil {
					return nil, err
				}
				view, err := s.cfg.Circles.Update(ctx, id, circle.UpdateRequest{
					Name:                     in.Body.Name,
					Description:              in.Body.Description,
					Timezone:                 in.Body.Timezone,
					MinReportersToSupersede:  in.Body.MinReportersToSupersede,
					RevokeInvalidatesInvites: in.Body.RevokeInvalidatesInvites,
					State:                    in.Body.State,
					Server:                   in.Body.Server,
				})
				if err != nil {
					return nil, err
				}
				etag, err := ETagOf(view)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}
				return &updateCircleOutput{
					ETag: etag,
					Body: CircleResponse{Circle: view, AsOf: s.cfg.Clock.Now()},
				}, nil
			})),

		registerFailure(OpDeleteCircle, Register(s.api, OpDeleteCircle,
			func(ctx context.Context, in *deleteCircleInput) (*deleteCircleOutput, error) {
				id, err := parseCircleID(in.CircleID)
				if err != nil {
					return nil, err
				}
				p, ok := PrincipalFrom(ctx)
				if !ok {
					return nil, apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
				}
				// The representation of what was deleted, returned once. After this the circle
				// answers 404 to every read, including to the owner who just deleted it, and their
				// own credential stops working on the next request — a membership in a circle that
				// no longer exists is not a membership.
				deleted, err := s.cfg.Circles.Delete(ctx, id, p.MembershipID)
				if err != nil {
					return nil, err
				}
				return &deleteCircleOutput{
					Body: CircleResponse{Circle: deleted, AsOf: s.cfg.Clock.Now()},
				}, nil
			})),

		registerFailure(OpSetCircleProviders, Register(s.api, OpSetCircleProviders,
			func(ctx context.Context, in *setCircleProvidersInput) (*setCircleProvidersOutput, error) {
				id, err := parseCircleID(in.CircleID)
				if err != nil {
					return nil, err
				}
				current, err := s.cfg.Circles.Get(ctx, id)
				if err != nil {
					return nil, err
				}
				// The ETag is the CIRCLE's, because this operation changes the circle's
				// representation — `revocation_strength` and `accepted_providers` are both on it.
				// A separate tag for a sub-resource with no GET would be one a client could never
				// have read.
				if err := RequireIfMatch(in.IfMatch, current); err != nil {
					return nil, err
				}

				providers := make([]circle.AcceptedProvider, 0, len(in.Body.Providers))
				for _, want := range in.Body.Providers {
					providers = append(providers, circle.AcceptedProvider{
						Key:                    strings.TrimSpace(want.Key),
						DiscordGuildID:         strings.TrimSpace(want.DiscordGuildID),
						DiscordRequiredRoleIDs: want.DiscordRequiredRoleIDs,
					})
				}
				view, err := s.cfg.Circles.SetProviders(ctx, id, circle.SetProvidersRequest{
					Providers:                 providers,
					AcknowledgeWeakRevocation: in.Body.AcknowledgeWeakRevocation,
				})
				if err != nil {
					return nil, err
				}
				etag, err := ETagOf(view)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}
				return &setCircleProvidersOutput{
					ETag: etag,
					Body: CircleResponse{Circle: view, AsOf: s.cfg.Clock.Now()},
				}, nil
			})),
	)
}

// requireSecurityKeyForInvalidation gates the one field of `updateCircle` that is not a setting.
//
// `revoke_invalidates_invites` is the mitigation a weakly-revocable circle leans on: revoking a
// member who joined through an unverifiable provider also kills every live invite, so the leaker
// cannot walk back in through one still sitting in Discord scrollback. Canonical §6's test for a
// security key is whether a change "changes its revocation guarantee" — which is exactly what
// switching this off does — so it takes `circle.security.manage`, the owner-only key, rather than
// the `circle.manage` the rest of this operation needs.
//
// **It is a per-FIELD check, and that is a real cost.** Every other permission in this API is
// enumerable data on the route, which is what lets the architectural tests walk the registry and
// find an operation nobody guarded; this one is a rule inside a handler and those tests are blind
// to it. The alternatives were worse: a second route for one boolean, or moving it into
// `setCircleProviders`, which is a PUT that replaces the whole provider set and would make
// toggling a flag require re-sending a list. The gate that replaces the registry's is
// TestUpdateCircle_RevokeInvalidatesInvites_NeedsTheOwnerSecurityKey.
func requireSecurityKeyForInvalidation(ctx context.Context, in *updateCircleInput) error {
	if in.Body.RevokeInvalidatesInvites == nil {
		return nil
	}
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
	}
	if !p.Can(authz.PermissionCircleSecurityManage) {
		return apierr.New(apierr.CodeForbidden,
			"revoke_invalidates_invites decides whether revoking a weakly-revocable member also "+
				"kills the circle's live invites, so it is an owner's to change").
			WithField("body.revoke_invalidates_invites", "needs circle.security.manage")
	}
	return nil
}

// parseCircleID reads the tenant out of the path.
//
// It answers 404 rather than 422 for a malformed id, matching the tenancy middleware exactly: one
// answer for "not a ULID", "no such circle" and "another circle's", so the shape of a guess tells
// a prober nothing. The middleware has already refused anything that is not the caller's own
// circle by the time this runs; this is the parse, not the check.
func parseCircleID(raw string) (core.CircleID, error) {
	id, err := core.ParseID[core.Circle](raw)
	if err != nil {
		return core.CircleID{}, apierr.Wrap(apierr.CodeNotFound, err, "no such circle")
	}
	return id, nil
}

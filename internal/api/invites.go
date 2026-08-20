package api

import (
	"context"
	"errors"
	"time"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/invite"
)

// InviteResponse is one invite, and the instant it was read.
type InviteResponse struct {
	invite.Invite
	AsOf core.Micros `json:"as_of"`
}

// MintedInviteResponse carries the code, exactly once — this is the only response it appears in.
type MintedInviteResponse struct {
	invite.Minted
	AsOf core.Micros `json:"as_of"`
}

// InvitePreview is what `previewInvite` tells somebody holding a code, BEFORE they join.
//
// `revocation_strength` is on it deliberately: the dangerous outcome of a weak circle is not the
// re-entry, it is a person joining one without knowing that revocation there is advisory. A field
// a client must render is the only thing that reliably reaches them.
type InvitePreview struct {
	Circle      InvitePreviewCircle   `json:"circle"`
	GrantedRole string                `json:"granted_role"`
	Kind        string                `json:"kind" doc:"invite, or owner_grant for the code the CLI prints on first run"`
	MaxUses     int                   `json:"max_uses"`
	Uses        int                   `json:"uses"`
	ExpiresAt   core.Micros           `json:"expires_at"`
	Providers   []circle.ProviderView `json:"providers"`

	RevocationStrength    string      `json:"revocation_strength"`
	RevocationWeakReasons []string    `json:"revocation_weak_reasons"`
	WeakProviders         []string    `json:"weak_providers"`
	AsOf                  core.Micros `json:"as_of"`
}

// InvitePreviewCircle is as much of a circle as a code holder is shown. Its id is absent: a code
// is evidence somebody handed it to you, and nothing here should teach a holder an identifier they
// can probe other routes with before they have a membership.
type InvitePreviewCircle struct {
	Name   string `json:"name"`
	Server string `json:"server"`
}

type listInvitesInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
	Cursor   string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor"`
	Limit    int    `query:"limit" doc:"Page size, 1-200" minimum:"0" maximum:"200"`
}

type listInvitesOutput struct{ Body Page[invite.Invite] }

type createInviteInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
	Body     struct {
		Role             string `json:"role,omitempty" doc:"Defaults to member. Never owner" enum:"officer,member,observer"`
		MaxUses          int    `json:"max_uses,omitempty" doc:"Defaults to 1. Clamped to 1 for a token, and for a circle that accepts a weak provider" minimum:"0" maximum:"50"`
		ExpiresInSeconds int    `json:"expires_in_seconds,omitempty" doc:"Defaults to 7 days, maximum 30. Clamped to 24 hours for a token" minimum:"0"`
		Note             string `json:"note,omitempty" doc:"Free text for the invite list" maxLength:"500"`
	}
}

type createInviteOutput struct{ Body MintedInviteResponse }

type revokeInviteInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
	InviteID string `path:"invite_id" doc:"The invite"`
}

type revokeInviteOutput struct{ Body InviteResponse }

type previewInviteInput struct {
	Body struct {
		// Code travels in the body and never in the path: a code is a bearer credential, and a
		// path segment lands in access logs, browser history and `Referer` headers.
		Code string `json:"code" doc:"The invite code, in any case, with or without the TODI- prefix"`
	}
}

type previewInviteOutput struct{ Body InvitePreview }

// registerInvites attaches the invite operations.
func (s *Server) registerInvites() error {
	return errors.Join(
		registerFailure(OpListInvites, Register(s.api, OpListInvites,
			func(ctx context.Context, in *listInvitesInput) (*listInvitesOutput, error) {
				id, err := parseCircleID(in.CircleID)
				if err != nil {
					return nil, err
				}
				limit, err := NormaliseLimit(in.Limit)
				if err != nil {
					return nil, err
				}
				after, err := ParseCursor(in.Cursor)
				if err != nil {
					return nil, err
				}
				all, err := s.cfg.Invites.List(ctx, id)
				if err != nil {
					return nil, err
				}
				// Newest first, so the cursor walks DOWN the ULID order.
				page := make([]invite.Invite, 0, len(all))
				for _, view := range all {
					if !after.IsZero() && view.ID.ULID().Compare(after) >= 0 {
						continue
					}
					page = append(page, view)
				}
				hasMore := len(page) > limit
				if hasMore {
					page = page[:limit]
				}
				next := ""
				if len(page) > 0 {
					next = page[len(page)-1].ID.String()
				}
				return &listInvitesOutput{
					Body: NewPage(page, next, hasMore, s.cfg.Clock.Now()),
				}, nil
			})),

		registerFailure(OpCreateInvite, Register(s.api, OpCreateInvite,
			func(ctx context.Context, in *createInviteInput) (*createInviteOutput, error) {
				id, err := parseCircleID(in.CircleID)
				if err != nil {
					return nil, err
				}
				p, ok := PrincipalFrom(ctx)
				if !ok {
					return nil, apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
				}
				weak, err := s.cfg.Circles.AcceptsWeakProvider(ctx, id)
				if err != nil {
					return nil, err
				}
				minted, err := s.cfg.Invites.Create(ctx, invite.CreateRequest{
					CircleID: id, Actor: p.MembershipID,
					// The clamp comes from the PRINCIPAL, never from the body. A token that could
					// say "I am a session" would be a token with no clamp at all.
					MintedByPAT:          p.Kind == auth.KindPAT,
					Role:                 authz.Role(in.Body.Role),
					MaxUses:              in.Body.MaxUses,
					TTL:                  time.Duration(in.Body.ExpiresInSeconds) * time.Second,
					Note:                 in.Body.Note,
					WeakProviderAccepted: weak,
				})
				if err != nil {
					return nil, err
				}
				return &createInviteOutput{
					Body: MintedInviteResponse{Minted: minted, AsOf: s.cfg.Clock.Now()},
				}, nil
			})),

		registerFailure(OpRevokeInvite, Register(s.api, OpRevokeInvite,
			func(ctx context.Context, in *revokeInviteInput) (*revokeInviteOutput, error) {
				circleID, err := parseCircleID(in.CircleID)
				if err != nil {
					return nil, err
				}
				inviteID, err := core.ParseID[core.Invite](in.InviteID)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeNotFound, err, "no such invite")
				}
				view, err := s.cfg.Invites.Revoke(ctx, circleID, inviteID)
				if err != nil {
					return nil, err
				}
				return &revokeInviteOutput{
					Body: InviteResponse{Invite: view, AsOf: s.cfg.Clock.Now()},
				}, nil
			})),

		registerFailure(OpPreviewInvite, Register(s.api, OpPreviewInvite,
			func(ctx context.Context, in *previewInviteInput) (*previewInviteOutput, error) {
				now := s.cfg.Clock.Now()
				resolved, err := invite.Resolve(
					ctx, s.cfg.Store.Queries(), in.Body.Code, now)
				if err != nil {
					return nil, err
				}
				view, err := s.cfg.Circles.Get(ctx, resolved.CircleID)
				if err != nil {
					return nil, err
				}
				return &previewInviteOutput{Body: InvitePreview{
					Circle:      InvitePreviewCircle{Name: view.Name, Server: view.Server},
					GrantedRole: string(resolved.Role),
					Kind:        string(resolved.Kind),
					MaxUses:     resolved.MaxUses,
					Uses:        resolved.Uses,
					ExpiresAt:   resolved.ExpiresAt,
					Providers:   view.AcceptedProviders,

					RevocationStrength:    view.RevocationStrength,
					RevocationWeakReasons: view.RevocationWeakReasons,
					WeakProviders:         view.WeakProviders,
					AsOf:                  now,
				}}, nil
			})),
	)
}

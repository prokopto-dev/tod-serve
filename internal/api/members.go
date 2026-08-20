package api

import (
	"context"
	"errors"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/membership"
)

// MemberResponse is one membership, and the instant it was read. `as_of` is on the response rather
// than on the view so the ETag can be computed over the view alone.
type MemberResponse struct {
	membership.Member
	AsOf core.Micros `json:"as_of"`
}

// RevokedResponse is what `revokeMember` answers.
//
// It carries `revocation_strength` — inherited from the membership representation — and
// `active_invite_count`, so a client can say "you also have 2 live invites" without a separate
// warnings channel being invented beside it. `invites_revoked` is the other half of that sentence:
// when the circle is weakly revocable and `revoke_invalidates_invites` is on, those invites went
// with the member, in the same transaction.
type RevokedResponse struct {
	membership.Revoked
	AsOf core.Micros `json:"as_of"`
}

// ServiceMemberResponse carries the bot's membership and its token, once.
type ServiceMemberResponse struct {
	membership.ServiceCreated
}

type listMembersInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
	Cursor   string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor"`
	Limit    int    `query:"limit" doc:"Page size, 1-200" minimum:"0" maximum:"200"`
}

type listMembersOutput struct{ Body Page[membership.Member] }

type getMemberInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
	MemberID string `path:"member_id" doc:"The membership"`
}

type getMemberOutput struct {
	ETag string `header:"ETag"`
	Body MemberResponse
}

type updateMemberInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
	MemberID string `path:"member_id" doc:"The membership"`
	IfMatch  string `header:"If-Match" doc:"The ETag a previous read returned"`
	Body     struct {
		DisplayName *string `json:"display_name,omitempty" maxLength:"64"`
		Role        *string `json:"role,omitempty" enum:"owner,officer,member,observer"`
	}
}

type updateMemberOutput struct {
	ETag string `header:"ETag"`
	Body MemberResponse
}

type revokeMemberInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
	MemberID string `path:"member_id" doc:"The membership"`
	IfMatch  string `header:"If-Match" doc:"The ETag a previous read returned"`
	Body     struct {
		Reason string `json:"reason,omitempty" doc:"Why, for the audit log" maxLength:"500"`
	}
}

type revokeMemberOutput struct{ Body RevokedResponse }

type reinstateMemberInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
	MemberID string `path:"member_id" doc:"The membership"`
	IfMatch  string `header:"If-Match" doc:"The ETag a previous read returned"`
}

type reinstateMemberOutput struct {
	ETag string `header:"ETag"`
	Body MemberResponse
}

type createServiceMemberInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
	Body     struct {
		DisplayName       string   `json:"display_name" doc:"What the member list calls this bot" maxLength:"64"`
		Role              string   `json:"role,omitempty" doc:"Defaults to member. Never owner" enum:"officer,member,observer"`
		OwnerMembershipID string   `json:"owner_membership_id,omitempty" doc:"The responsible human. Defaults to the caller"`
		ClientName        string   `json:"client_name,omitempty" doc:"The token's device name" maxLength:"64"`
		Scopes            []string `json:"scopes,omitempty" doc:"Narrow the token. Empty means every scope, still bounded by the role"`
	}
}

type createServiceMemberOutput struct{ Body ServiceMemberResponse }

// registerMembers attaches the member operations.
//
// There is no delete-membership operation here, and there is none anywhere: the partial unique
// index `ux_membership_identity` makes a second row for one identity unrepresentable, which is
// what makes revocation stick. Reinstatement is the only way back in.
func (s *Server) registerMembers() error {
	return errors.Join(
		registerFailure(OpListMembers, Register(s.api, OpListMembers,
			func(ctx context.Context, in *listMembersInput) (*listMembersOutput, error) {
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
				all, err := s.cfg.Members.List(ctx, id)
				if err != nil {
					return nil, err
				}
				// The query returns oldest first, so the cursor walks UP the ULID order. Paging in
				// Go is honest at this size: a circle is four friends to sixty raiders, and a
				// second query shape would be a second thing to keep in step with this one.
				page := make([]membership.Member, 0, len(all))
				for _, view := range all {
					if !after.IsZero() && view.ID.ULID().Compare(after) <= 0 {
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
				return &listMembersOutput{
					Body: NewPage(page, next, hasMore, s.cfg.Clock.Now()),
				}, nil
			})),

		registerFailure(OpGetMember, Register(s.api, OpGetMember,
			func(ctx context.Context, in *getMemberInput) (*getMemberOutput, error) {
				circleID, memberID, err := parseMemberPath(in.CircleID, in.MemberID)
				if err != nil {
					return nil, err
				}
				view, err := s.cfg.Members.Get(ctx, circleID, memberID)
				if err != nil {
					return nil, err
				}
				etag, err := ETagOf(view)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}
				return &getMemberOutput{
					ETag: etag, Body: MemberResponse{Member: view, AsOf: s.cfg.Clock.Now()},
				}, nil
			})),

		registerFailure(OpUpdateMember, Register(s.api, OpUpdateMember,
			func(ctx context.Context, in *updateMemberInput) (*updateMemberOutput, error) {
				circleID, memberID, err := parseMemberPath(in.CircleID, in.MemberID)
				if err != nil {
					return nil, err
				}
				p, ok := PrincipalFrom(ctx)
				if !ok {
					return nil, apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
				}
				current, err := s.cfg.Members.Get(ctx, circleID, memberID)
				if err != nil {
					return nil, err
				}
				if err := RequireIfMatch(in.IfMatch, current); err != nil {
					return nil, err
				}
				view, err := s.cfg.Members.Update(ctx, circleID, memberID, p.MembershipID,
					membership.UpdateRequest{
						DisplayName: in.Body.DisplayName, Role: in.Body.Role,
					})
				if err != nil {
					return nil, err
				}
				etag, err := ETagOf(view)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}
				return &updateMemberOutput{
					ETag: etag, Body: MemberResponse{Member: view, AsOf: s.cfg.Clock.Now()},
				}, nil
			})),

		registerFailure(OpRevokeMember, Register(s.api, OpRevokeMember,
			func(ctx context.Context, in *revokeMemberInput) (*revokeMemberOutput, error) {
				circleID, memberID, err := parseMemberPath(in.CircleID, in.MemberID)
				if err != nil {
					return nil, err
				}
				p, ok := PrincipalFrom(ctx)
				if !ok {
					return nil, apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
				}
				current, err := s.cfg.Members.Get(ctx, circleID, memberID)
				if err != nil {
					return nil, err
				}
				// `If-Match` rather than `Idempotency-Key`: the membership row already exists and
				// revocation is a transition on it, so the failure to defend against is two
				// officers disagreeing, not a retry.
				if err := RequireIfMatch(in.IfMatch, current); err != nil {
					return nil, err
				}
				revoked, err := s.cfg.Members.Revoke(
					ctx, circleID, memberID, p.MembershipID, in.Body.Reason)
				if err != nil {
					return nil, err
				}
				return &revokeMemberOutput{
					Body: RevokedResponse{Revoked: revoked, AsOf: s.cfg.Clock.Now()},
				}, nil
			})),

		registerFailure(OpReinstateMember, Register(s.api, OpReinstateMember,
			func(ctx context.Context, in *reinstateMemberInput) (*reinstateMemberOutput, error) {
				circleID, memberID, err := parseMemberPath(in.CircleID, in.MemberID)
				if err != nil {
					return nil, err
				}
				p, ok := PrincipalFrom(ctx)
				if !ok {
					return nil, apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
				}
				current, err := s.cfg.Members.Get(ctx, circleID, memberID)
				if err != nil {
					return nil, err
				}
				if err := RequireIfMatch(in.IfMatch, current); err != nil {
					return nil, err
				}
				view, err := s.cfg.Members.Reinstate(ctx, circleID, memberID, p.MembershipID)
				if err != nil {
					return nil, err
				}
				etag, err := ETagOf(view)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}
				return &reinstateMemberOutput{
					ETag: etag, Body: MemberResponse{Member: view, AsOf: s.cfg.Clock.Now()},
				}, nil
			})),

		registerFailure(OpCreateServiceMember, Register(s.api, OpCreateServiceMember,
			func(ctx context.Context, in *createServiceMemberInput) (*createServiceMemberOutput, error) {
				circleID, err := parseCircleID(in.CircleID)
				if err != nil {
					return nil, err
				}
				p, ok := PrincipalFrom(ctx)
				if !ok {
					return nil, apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
				}
				// The replay is the middleware's here: the principal IS a membership, so
				// `(membership, key)` in `idempotency_record` is exactly the right key and the
				// shared path already handles it.
				created, err := s.cfg.Members.CreateService(ctx, membership.CreateServiceRequest{
					CircleID: circleID, Actor: p.MembershipID,
					DisplayName:       in.Body.DisplayName,
					Role:              in.Body.Role,
					OwnerMembershipID: in.Body.OwnerMembershipID,
					ClientName:        in.Body.ClientName,
					Scopes:            in.Body.Scopes,
				})
				if err != nil {
					return nil, err
				}
				return &createServiceMemberOutput{
					Body: ServiceMemberResponse{ServiceCreated: created},
				}, nil
			})),
	)
}

// parseMemberPath reads the two ids out of a member path.
//
// A malformed member id answers 404, not 422, for the same reason a malformed circle id does: the
// shape of a guess must not tell a prober whether the id existed.
func parseMemberPath(rawCircle, rawMember string) (core.CircleID, core.MembershipID, error) {
	circleID, err := parseCircleID(rawCircle)
	if err != nil {
		return core.CircleID{}, core.MembershipID{}, err
	}
	memberID, err := core.ParseID[core.Membership](rawMember)
	if err != nil {
		return core.CircleID{}, core.MembershipID{},
			apierr.Wrap(apierr.CodeNotFound, err, "no such member")
	}
	return circleID, memberID, nil
}

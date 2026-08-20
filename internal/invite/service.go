package invite

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// The bounds an invite is minted within.
const (
	// DefaultTTL is how long an invite lives when the request does not say.
	DefaultTTL = 7 * 24 * time.Hour
	// MaxTTL is the longest any invite may live. `expires_at` is NOT NULL and the domain model
	// says there are no eternal invites; without a ceiling, "30 days" becomes "3650" the first
	// time somebody is tired of reminting one, which is the 50-use invite left lying around that
	// weak revocation is already fighting.
	MaxTTL = 30 * 24 * time.Hour
	// DefaultMaxUses is how many redemptions an invite allows when the request does not say. One,
	// because a one-time login link is exactly this and it is the case that should need no flag.
	DefaultMaxUses = 1
	// MaxUsesCeiling bounds `max_uses` for everyone. A four-friends circle does not need fifty.
	MaxUsesCeiling = 50
)

// The values `capped_by` takes. Never hide a row silently: if a request asked for more than it
// got, the response says which rule narrowed it.
const (
	// CappedByPAT is canonical §6's clamp: an invite minted by a token is one use, at most 24
	// hours, and a role no higher than `member`, whatever the request asked for. That clamp is the
	// whole reason `invite.create` is outside the capability floor while `token.mint` is inside it.
	CappedByPAT = "pat"
	// CappedByWeakProvider is the `local` ceiling. A `local` identity has no credential to
	// re-present, so `POST /sessions` cannot work for it and every lost token becomes a new
	// invite; one use is the mitigation, and it applies to any invite into a circle that accepts
	// an unverifiable provider, because that is exactly the set of invites `local` can redeem.
	CappedByWeakProvider = "weak_provider"
)

// patTTLCeiling is the 24 hours canonical §6 names.
const patTTLCeiling = 24 * time.Hour

// Config is what a [Service] needs. Every field is required: a service that invents a clock or an
// entropy source behaves differently in a test than in production.
type Config struct {
	Store   *store.DB
	Clock   clock.Clock
	IDs     *core.Generator
	Entropy io.Reader
	Log     *slog.Logger
}

// Service mints, lists, revokes and resolves invites.
type Service struct {
	db      *store.DB
	clock   clock.Clock
	ids     *core.Generator
	entropy io.Reader
	log     *slog.Logger
}

// New returns a service.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("invite service: no store")
	case cfg.Clock == nil:
		return nil, errors.New("invite service: no clock")
	case cfg.IDs == nil:
		return nil, errors.New("invite service: no id generator")
	case cfg.Entropy == nil:
		// No fallback to crypto/rand. A generator that quietly reaches for a default is one
		// nobody notices was given the wrong source — and this one mints the credential that
		// admits a stranger to a circle.
		return nil, errors.New("invite service: no entropy source")
	case cfg.Log == nil:
		return nil, errors.New("invite service: no logger")
	}
	return &Service{
		db: cfg.Store, clock: cfg.Clock, ids: cfg.IDs, entropy: cfg.Entropy, log: cfg.Log,
	}, nil
}

// Invite is one invite as a client reads it. The code is not here and cannot be: the database holds
// its hash, and the plaintext existed only in the response that created it.
type Invite struct {
	ID                    core.InviteID     `json:"id"`
	CircleID              core.CircleID     `json:"circle_id"`
	CodePrefix            string            `json:"code_prefix"`
	Role                  string            `json:"role"`
	MaxUses               int               `json:"max_uses"`
	Uses                  int               `json:"uses"`
	ExpiresAt             core.Micros       `json:"expires_at"`
	RevokedAt             *core.Micros      `json:"revoked_at"`
	CreatedByMembershipID core.MembershipID `json:"created_by_membership_id"`
	MintedByKind          string            `json:"minted_by_kind"`
	Note                  string            `json:"note"`
	CreatedAt             core.Micros       `json:"created_at"`
	// Live says whether it can still be redeemed at the response's `as_of`. It is derived rather
	// than stored so that an expiry needs no sweep to take effect.
	Live bool `json:"live"`
}

// Minted is a freshly created invite: the representation, plus the code, exactly once.
type Minted struct {
	Invite
	// Code is the whole credential. This is the only response it ever appears in.
	Code Code `json:"code"`
	// CappedBy names the rule that narrowed the request below what it asked for, empty when
	// nothing did. Canonical §6 requires the PAT clamp to say so; the weak-provider ceiling says
	// so for the same reason.
	CappedBy string `json:"capped_by,omitempty"`
}

// CreateRequest is `createInvite`, after the edge has decided who is asking.
type CreateRequest struct {
	CircleID core.CircleID
	// Actor is the membership minting the invite. `invite.created_by_membership_id` is NOT NULL:
	// every code names a responsible member.
	Actor core.MembershipID
	// MintedByPAT says the caller presented a token rather than a session. It is what applies the
	// clamp, and it comes from the authenticated principal rather than from the request body.
	MintedByPAT bool
	Role        authz.Role
	MaxUses     int
	TTL         time.Duration
	Note        string
	// WeakProviderAccepted says the circle accepts a provider with no verifiable subject, which
	// forces one use. The caller reads it from the circle rather than this package doing so,
	// because the circle's accepted-provider set is internal/circle's to answer.
	WeakProviderAccepted bool
}

// Create mints an invite.
//
// The clamp is applied here rather than at the edge, and it is applied to the VALUES rather than
// refused: canonical §6 says a PAT's request is narrowed to one use, 24 hours and a role at most
// `member`, and that the response says so. Refusing instead would break the bot that posts an
// invite link on request, which is the integration the whole trade exists to permit.
func (s *Service) Create(ctx context.Context, req CreateRequest) (Minted, error) {
	role, maxUses, ttl, cappedBy, err := clamp(req)
	if err != nil {
		return Minted{}, err
	}

	code, err := Mint(s.entropy)
	if err != nil {
		return Minted{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	now := s.clock.Now()
	id, err := core.NewID[core.Invite](s.ids, now)
	if err != nil {
		return Minted{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	mintedBy := schemaenum.InviteMintedByKindSession
	if req.MintedByPAT {
		mintedBy = schemaenum.InviteMintedByKindPAT
	}

	row, err := s.db.Queries().CreateInvite(ctx, sqlitegen.CreateInviteParams{
		ID:                    id.String(),
		CircleID:              req.CircleID.String(),
		CodeHash:              Hash(code),
		CodePrefix:            code.Prefix(),
		Role:                  string(role),
		MaxUses:               int64(maxUses),
		ExpiresAt:             int64(now.Add(ttl)),
		CreatedByMembershipID: req.Actor.String(),
		MintedByKind:          mintedBy,
		Note:                  req.Note,
		CreatedAt:             int64(now),
		UpdatedAt:             int64(now),
	})
	if err != nil {
		return Minted{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	view, err := toView(row, now)
	if err != nil {
		return Minted{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	// The prefix is loggable and is how a leaked code is traced back to the officer who minted it.
	// The code itself never reaches a log line.
	s.log.InfoContext(ctx, "invite minted",
		slog.String("invite_id", view.ID.String()),
		slog.String("circle_id", view.CircleID.String()),
		slog.String("code_prefix", view.CodePrefix),
		slog.String("role", view.Role),
		slog.String("minted_by_kind", view.MintedByKind),
		slog.String("capped_by", cappedBy))
	return Minted{Invite: view, Code: code, CappedBy: cappedBy}, nil
}

// clamp applies the caps, and returns which one bit.
//
// The order matters: the PAT clamp is at least as strong as the weak-provider ceiling on every
// axis, so when both apply the response names the PAT. Naming the weaker one would tell a bot
// author that raising `max_uses` would work if only the circle dropped `local`.
func clamp(req CreateRequest) (authz.Role, int, time.Duration, string, error) {
	role, maxUses, ttl := req.Role, req.MaxUses, req.TTL
	if role == "" {
		role = authz.RoleMember
	}
	if maxUses == 0 {
		maxUses = DefaultMaxUses
	}
	if ttl == 0 {
		ttl = DefaultTTL
	}

	// Refused, never clamped. `CHECK (role <> 'owner')` makes an owner invite unrepresentable, so
	// there is no value to narrow to that would be what the caller asked for — see [Grant] for the
	// one path that does make an owner.
	if role == authz.RoleOwner {
		return "", 0, 0, "", apierr.New(apierr.CodeValidationFailed,
			"an invite cannot grant owner; the first owner comes from `tod-serve circle create`").
			WithField("body.role", "must be officer, member or observer")
	}
	if _, ok := role.Rank(); !ok {
		return "", 0, 0, "", apierr.Newf(apierr.CodeValidationFailed,
			"%q is not a role", role).WithField("body.role", "not a role")
	}
	switch {
	case maxUses < 1:
		return "", 0, 0, "", apierr.New(apierr.CodeValidationFailed,
			"max_uses must be at least 1").WithField("body.max_uses", "must be at least 1")
	case maxUses > MaxUsesCeiling:
		return "", 0, 0, "", apierr.Newf(apierr.CodeValidationFailed,
			"max_uses is %d; the maximum is %d", maxUses, MaxUsesCeiling).
			WithField("body.max_uses", "above the maximum")
	case ttl < 0:
		return "", 0, 0, "", apierr.New(apierr.CodeValidationFailed,
			"expires_in_seconds must be positive").
			WithField("body.expires_in_seconds", "must be positive")
	case ttl > MaxTTL:
		return "", 0, 0, "", apierr.Newf(apierr.CodeValidationFailed,
			"expires_in_seconds is %d; the maximum is %d",
			int(ttl.Seconds()), int(MaxTTL.Seconds())).
			WithField("body.expires_in_seconds", "above the maximum")
	}

	capped := ""
	if req.WeakProviderAccepted && maxUses > 1 {
		maxUses, capped = 1, CappedByWeakProvider
	}
	if req.MintedByPAT {
		narrowed := false
		if maxUses > 1 {
			maxUses, narrowed = 1, true
		}
		if ttl > patTTLCeiling {
			ttl, narrowed = patTTLCeiling, true
		}
		if role.AtLeast(authz.RoleOfficer) {
			role, narrowed = authz.RoleMember, true
		}
		if narrowed || capped != "" {
			capped = CappedByPAT
		}
	}
	return role, maxUses, ttl, capped, nil
}

// List returns the circle's invites, newest first.
func (s *Service) List(ctx context.Context, circleID core.CircleID) ([]Invite, error) {
	rows, err := s.db.Queries().ListInvites(ctx, circleID.String())
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	now := s.clock.Now()
	out := make([]Invite, 0, len(rows))
	for _, row := range rows {
		view, convErr := toView(row, now)
		if convErr != nil {
			return nil, apierr.Wrap(apierr.CodeInternalError, convErr, "")
		}
		out = append(out, view)
	}
	return out, nil
}

// Get returns one invite of a circle.
func (s *Service) Get(ctx context.Context, circleID core.CircleID, id core.InviteID) (Invite, error) {
	row, err := s.db.Queries().GetInvite(ctx, sqlitegen.GetInviteParams{
		CircleID: circleID.String(), ID: id.String(),
	})
	if store.IsNotFound(err) {
		return Invite{}, apierr.New(apierr.CodeNotFound, "no such invite")
	}
	if err != nil {
		return Invite{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	view, err := toView(row, s.clock.Now())
	if err != nil {
		return Invite{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	return view, nil
}

// Revoke stops an invite being redeemed. An invite already revoked answers 404 rather than 409:
// the query names `revoked_at IS NULL`, and the alternative is a second read whose only purpose is
// to tell a caller something they can see in the list.
func (s *Service) Revoke(
	ctx context.Context, circleID core.CircleID, id core.InviteID,
) (Invite, error) {
	now := s.clock.Now()
	at := int64(now)
	row, err := s.db.Queries().RevokeInvite(ctx, sqlitegen.RevokeInviteParams{
		RevokedAt: &at, UpdatedAt: at,
		CircleID: circleID.String(), ID: id.String(),
	})
	if store.IsNotFound(err) {
		return Invite{}, apierr.New(apierr.CodeNotFound, "no such live invite")
	}
	if err != nil {
		return Invite{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	view, err := toView(row, now)
	if err != nil {
		return Invite{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	s.log.InfoContext(ctx, "invite revoked",
		slog.String("invite_id", view.ID.String()),
		slog.String("circle_id", view.CircleID.String()),
		slog.String("code_prefix", view.CodePrefix))
	return view, nil
}

// CountLive returns how many of the circle's invites can still be redeemed. It is the
// `active_invite_count` `revokeMember` carries, so the UI can say "you also have 2 live invites"
// without a second warnings channel being invented for it.
func (s *Service) CountLive(ctx context.Context, circleID core.CircleID) (int, error) {
	return CountLive(ctx, s.db.Queries(), circleID, s.clock.Now())
}

// CountLive is the same question inside a transaction.
func CountLive(
	ctx context.Context, q *sqlitegen.Queries, circleID core.CircleID, now core.Micros,
) (int, error) {
	n, err := q.CountLiveInvites(ctx, sqlitegen.CountLiveInvitesParams{
		CircleID: circleID.String(), Now: int64(now),
	})
	if err != nil {
		return 0, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	return int(n), nil
}

// RevokeAllLive revokes every outstanding invite of a circle and returns how many it revoked.
//
// It exists for one caller: revoking a weakly-revocable member when
// `circle.revoke_invalidates_invites = 1`. That has to happen in the SAME transaction as the
// revocation, so this takes the transaction's query set rather than reaching for the pool.
func RevokeAllLive(
	ctx context.Context, q *sqlitegen.Queries, circleID core.CircleID, now core.Micros,
) (int, error) {
	at := int64(now)
	n, err := q.RevokeLiveInvitesForCircle(ctx, sqlitegen.RevokeLiveInvitesForCircleParams{
		RevokedAt: &at, UpdatedAt: at, CircleID: circleID.String(), Now: at,
	})
	if err != nil {
		return 0, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	return int(n), nil
}

func toView(row sqlitegen.Invite, now core.Micros) (Invite, error) {
	id, err := core.ParseID[core.Invite](row.ID)
	if err != nil {
		return Invite{}, err
	}
	circleID, err := core.ParseID[core.Circle](row.CircleID)
	if err != nil {
		return Invite{}, err
	}
	actor, err := core.ParseID[core.Membership](row.CreatedByMembershipID)
	if err != nil {
		return Invite{}, err
	}
	view := Invite{
		ID: id, CircleID: circleID, CodePrefix: row.CodePrefix, Role: row.Role,
		MaxUses: int(row.MaxUses), Uses: int(row.Uses),
		ExpiresAt:             core.Micros(row.ExpiresAt),
		CreatedByMembershipID: actor,
		MintedByKind:          row.MintedByKind, Note: row.Note,
		CreatedAt: core.Micros(row.CreatedAt),
	}
	if row.RevokedAt != nil {
		revoked := core.Micros(*row.RevokedAt)
		view.RevokedAt = &revoked
	}
	view.Live = view.RevokedAt == nil &&
		now.Before(view.ExpiresAt) &&
		view.Uses < view.MaxUses
	return view, nil
}

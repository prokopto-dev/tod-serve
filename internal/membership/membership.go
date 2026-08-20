package membership

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"unicode/utf8"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/audit"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// MaxDisplayNameLen bounds a circle-local display name, in runes rather than bytes so the limit
// somebody hits is the one they can count.
const MaxDisplayNameLen = 64

// Config is what a [Service] needs. Every field is required.
type Config struct {
	Store    *store.DB
	Clock    clock.Clock
	IDs      *core.Generator
	Minter   *auth.Minter
	Identity *identity.Service
	Log      *slog.Logger
	// Entropy mints nothing here directly; it is held so the service can be constructed with the
	// same source everything else uses, and so a wiring site that forgot one fails at startup.
	Entropy io.Reader
}

// Service reads and writes memberships, and is where joining lands.
type Service struct {
	db       *store.DB
	clock    clock.Clock
	ids      *core.Generator
	minter   *auth.Minter
	identity *identity.Service
	log      *slog.Logger
}

// New returns a service.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("membership service: no store")
	case cfg.Clock == nil:
		return nil, errors.New("membership service: no clock")
	case cfg.IDs == nil:
		return nil, errors.New("membership service: no id generator")
	case cfg.Minter == nil:
		return nil, errors.New("membership service: no token minter")
	case cfg.Identity == nil:
		return nil, errors.New("membership service: no identity service")
	case cfg.Log == nil:
		return nil, errors.New("membership service: no logger")
	case cfg.Entropy == nil:
		return nil, errors.New("membership service: no entropy source")
	}
	return &Service{
		db: cfg.Store, clock: cfg.Clock, ids: cfg.IDs, minter: cfg.Minter,
		identity: cfg.Identity, log: cfg.Log,
	}, nil
}

// Member is one membership as a client reads it.
type Member struct {
	ID       core.MembershipID `json:"id"`
	CircleID core.CircleID     `json:"circle_id"`
	// IdentityID is empty for a service membership, which has no identity — it has an owner.
	IdentityID string `json:"identity_id,omitempty"`
	Kind       string `json:"kind"`
	// OwnerMembershipID names the responsible human behind a service membership. It is what
	// survives of DKP ADR-0011's guarantee after [ADR-0005] moved tokens onto memberships.
	//
	// [ADR-0005]: docs/adr/0005-pats-bound-to-memberships.md
	OwnerMembershipID  string       `json:"owner_membership_id,omitempty"`
	DisplayName        string       `json:"display_name"`
	Role               string       `json:"role"`
	AdmittedByInviteID string       `json:"admitted_by_invite_id,omitempty"`
	JoinedAt           core.Micros  `json:"joined_at"`
	RevokedAt          *core.Micros `json:"revoked_at"`
	RevokeReason       string       `json:"revoke_reason,omitempty"`
	// ProviderKey is the provider behind this membership's identity, empty for a service
	// membership. It is what makes `revocation_strength` below answerable.
	ProviderKey string `json:"provider_key,omitempty"`
	// RevocationStrength answers "will revoking THIS person stick?" — from the provider behind
	// this membership, not from the circle. A circle accepting both `discord` and `local` is weak
	// overall and its Discord members are individually durable, and telling an officer otherwise
	// would be wrong in the direction that gets a revocation reversed.
	RevocationStrength    string   `json:"revocation_strength"`
	RevocationWeakReasons []string `json:"revocation_weak_reasons"`
	WeakProviders         []string `json:"weak_providers"`
	// PossibleDuplicate flags two UNLINKED memberships sharing a normalised display name. It is
	// reported and never acted on: the fix is a deliberate officer link, and merging two people
	// because they picked the same name is a far worse mistake than showing both.
	PossibleDuplicate bool        `json:"possible_duplicate"`
	CreatedAt         core.Micros `json:"created_at"`
	UpdatedAt         core.Micros `json:"updated_at"`
}

// Revoked reports what revoking a member did, in one representation rather than in a warnings
// channel invented beside it.
type Revoked struct {
	Member
	// ActiveInviteCount is how many of the circle's invites can still be redeemed AFTER this
	// revocation, so the UI can say "you also have 2 live invites".
	ActiveInviteCount int `json:"active_invite_count"`
	// InvitesRevoked is how many went with the member, in the same transaction, because the
	// circle is weakly revocable and `revoke_invalidates_invites` is on.
	InvitesRevoked int `json:"invites_revoked"`
}

// List returns the circle's members, with `possible_duplicate` computed across the whole list —
// which is the only place it can be computed, because it is a statement about a pair.
func (s *Service) List(ctx context.Context, circleID core.CircleID) ([]Member, error) {
	return listViews(ctx, s.db.Queries(), circleID)
}

// Get returns one member of a circle.
func (s *Service) Get(
	ctx context.Context, circleID core.CircleID, id core.MembershipID,
) (Member, error) {
	views, err := listViews(ctx, s.db.Queries(), circleID)
	if err != nil {
		return Member{}, err
	}
	for _, v := range views {
		if v.ID == id {
			return v, nil
		}
	}
	return Member{}, apierr.New(apierr.CodeNotFound, "no such member")
}

// listViews builds every member's representation for one circle.
//
// It reads the whole circle even for a single-member read, deliberately: `possible_duplicate` is a
// property of a PAIR, and a `getMember` that answered `false` because it never looked would be a
// different field with the same name in two responses.
func listViews(
	ctx context.Context, q *sqlitegen.Queries, circleID core.CircleID,
) ([]Member, error) {
	rows, err := q.ListMemberships(ctx, circleID.String())
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	views := make([]Member, 0, len(rows))
	for _, row := range rows {
		view, convErr := toView(ctx, q, row)
		if convErr != nil {
			return nil, convErr
		}
		views = append(views, view)
	}
	if err := flagDuplicates(ctx, q, views); err != nil {
		return nil, err
	}
	return views, nil
}

func toView(ctx context.Context, q *sqlitegen.Queries, row sqlitegen.Membership) (Member, error) {
	id, err := core.ParseID[core.Membership](row.ID)
	if err != nil {
		return Member{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	circleID, err := core.ParseID[core.Circle](row.CircleID)
	if err != nil {
		return Member{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	view := Member{
		ID: id, CircleID: circleID, Kind: row.Kind,
		IdentityID:         deref(row.IdentityID),
		OwnerMembershipID:  deref(row.OwnerMembershipID),
		DisplayName:        row.DisplayName,
		Role:               row.Role,
		AdmittedByInviteID: deref(row.AdmittedByInviteID),
		JoinedAt:           core.Micros(row.JoinedAt),
		RevokeReason:       deref(row.RevokeReason),
		CreatedAt:          core.Micros(row.CreatedAt),
		UpdatedAt:          core.Micros(row.UpdatedAt),
	}
	if row.RevokedAt != nil {
		revoked := core.Micros(*row.RevokedAt)
		view.RevokedAt = &revoked
	}

	strength := identity.ServiceMembershipStrength()
	if row.IdentityID != nil {
		identityRow, readErr := q.GetIdentity(ctx, *row.IdentityID)
		if readErr != nil {
			return Member{}, apierr.Wrap(apierr.CodeInternalError, readErr, "")
		}
		providerRow, readErr := q.GetIdentityProvider(ctx, identityRow.ProviderID)
		if readErr != nil {
			return Member{}, apierr.Wrap(apierr.CodeInternalError, readErr, "")
		}
		view.ProviderKey = providerRow.Key
		strength = identity.MembershipStrength(identity.Provider{
			ID: providerRow.ID, Key: providerRow.Key, Kind: identity.Kind(providerRow.Kind),
			Enabled: providerRow.Enabled == 1, VerifiableSubject: providerRow.VerifiableSubject == 1,
		})
	}
	view.RevocationStrength = string(strength.Strength)
	view.RevocationWeakReasons = strength.WeakReasons
	view.WeakProviders = strength.WeakProviders
	return view, nil
}

// flagDuplicates sets `possible_duplicate` on every membership that shares a normalised display
// name with another membership it is not linked to.
//
// Linked identities are one person by an officer's explicit assertion, so two memberships in one
// link set are not a duplicate — they are the case linking exists for. Everything else that shares
// a name is flagged and left alone.
func flagDuplicates(ctx context.Context, q *sqlitegen.Queries, views []Member) error {
	byName := map[string][]int{}
	for i, v := range views {
		norm := core.Normalise(v.DisplayName)
		if norm == "" {
			continue
		}
		byName[norm] = append(byName[norm], i)
	}

	for _, group := range byName {
		if len(group) < 2 {
			continue
		}
		sets := newLinkSets()
		for _, i := range group {
			if views[i].IdentityID == "" {
				// A service membership has no identity and therefore cannot be linked to
				// anything. It stands alone, which is what makes it flaggable against a human of
				// the same name — and that pair is worth an officer's eye.
				continue
			}
			links, err := q.ListIdentityLinksFor(ctx, views[i].IdentityID)
			if err != nil {
				return apierr.Wrap(apierr.CodeInternalError, err, "")
			}
			for _, link := range links {
				sets.union(link.PrimaryIdentityID, link.LinkedIdentityID)
			}
		}
		for _, i := range group {
			for _, j := range group {
				if i == j {
					continue
				}
				if !sets.same(views[i].IdentityID, views[j].IdentityID) {
					views[i].PossibleDuplicate = true
					break
				}
			}
		}
	}
	return nil
}

// linkSets is union-find over `identity_link`. It is here rather than in internal/identity because
// the question it answers — "are these two memberships one person" — is a membership question, and
// the link table is read by identity id alone.
type linkSets struct{ parent map[string]string }

func newLinkSets() *linkSets { return &linkSets{parent: map[string]string{}} }

func (l *linkSets) find(id string) string {
	root, ok := l.parent[id]
	if !ok {
		l.parent[id] = id
		return id
	}
	if root == id {
		return id
	}
	root = l.find(root)
	l.parent[id] = root
	return root
}

func (l *linkSets) union(a, b string) { l.parent[l.find(a)] = l.find(b) }

// same reports whether two identities are one person. An empty identity — a service membership —
// is never the same as anything, including another service membership.
func (l *linkSets) same(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return l.find(a) == l.find(b)
}

// UpdateRequest is `updateMember`. Pointers, because PATCH means "the fields I sent".
type UpdateRequest struct {
	DisplayName *string
	Role        *string
}

// Update changes a member's role or display name.
//
// Demoting the last owner is `409 last_owner`, the same answer revoking them gets: a circle
// without an owner has nobody who can change its providers or delete it, and there is no operation
// anywhere that creates one out of nothing.
func (s *Service) Update(
	ctx context.Context, circleID core.CircleID, id core.MembershipID, actor core.MembershipID,
	req UpdateRequest,
) (Member, error) {
	now := s.clock.Now()
	err := s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		row, txErr := q.GetMembership(ctx, sqlitegen.GetMembershipParams{
			CircleID: circleID.String(), ID: id.String(),
		})
		if store.IsNotFound(txErr) {
			return apierr.New(apierr.CodeNotFound, "no such member")
		}
		if txErr != nil {
			return txErr
		}

		displayName := row.DisplayName
		if req.DisplayName != nil {
			displayName, txErr = validDisplayName(*req.DisplayName)
			if txErr != nil {
				return txErr
			}
		}
		role := row.Role
		if req.Role != nil {
			parsed, roleErr := authz.ParseRole(*req.Role)
			if roleErr != nil {
				return apierr.Newf(apierr.CodeValidationFailed, "%q is not a role", *req.Role).
					WithField("body.role", "not a role")
			}
			role = string(parsed)
		}
		// Only when the role actually MOVES. `role` defaults to the subject's current one, so a
		// request that carries nothing but a display name would otherwise read as a grant of
		// whatever that member already holds — and renaming an owner would be refused to every
		// officer. A guard about roles quietly becoming a guard about names is the sort of thing
		// that gets a guard removed rather than fixed.
		if role != row.Role {
			if txErr = refuseGrantAboveOwnRole(ctx, q, circleID, actor, role); txErr != nil {
				return txErr
			}
		}
		if row.Role == string(authz.RoleOwner) && role != string(authz.RoleOwner) {
			if txErr = requireAnotherOwner(ctx, q, circleID, id); txErr != nil {
				return txErr
			}
		}

		updated, txErr := q.UpdateMembership(ctx, sqlitegen.UpdateMembershipParams{
			DisplayName: displayName, DisplayNameNorm: core.Normalise(displayName),
			Role: role, UpdatedAt: int64(now),
			CircleID: circleID.String(), ID: id.String(),
		})
		if txErr != nil {
			return txErr
		}
		if txErr = audit.Append(ctx, q, s.ids, now, audit.Entry{
			CircleID: circleID, Actor: actor, Action: audit.ActionMemberUpdated,
			EntityType: audit.EntityMembership, EntityID: id.String(),
			Detail: map[string]any{
				"role_before": row.Role, "role_after": updated.Role,
				"display_name_changed": updated.DisplayName != row.DisplayName,
			},
		}); txErr != nil {
			return txErr
		}
		return nil
	})
	if err != nil {
		return Member{}, coded(err)
	}
	// Re-read outside the transaction so the representation carries `possible_duplicate`, which
	// is a fact about the whole member list rather than about the row that just changed.
	return s.Get(ctx, circleID, id)
}

// refuseGrantAboveOwnRole stops a member handing out a role stronger than the one they hold.
//
// `member.manage` is held by officers, and without this an officer could set ANY member's role —
// including their own — to `owner`, which adds `circle.security.manage`, `circle.delete`,
// `token.mint` and `token.revoke`. That is self-service promotion to the strongest role in the
// circle, and the rest of the design goes to real lengths to make becoming an owner deliberate:
// `invite` carries `CHECK (role <> 'owner')` precisely so that "a leaked bot token can add a
// visible, revocable member — not seize the circle", and the only other door is a code the CLI
// prints once on the operator's own terminal. An officer promoting themselves walks straight past
// both.
//
// The rule is the ordinary one and it is the ordering the role enum already carries: you may grant
// what you hold, and not more. An owner may still make another owner, which is how ownership is
// handed over.
//
// It does NOT stop an officer changing a member who currently outranks them — an officer can still
// demote an owner, bounded by `last_owner`, so a circle always keeps one. Whether that should also
// be refused is a policy question rather than an escalation: an officer who demotes an owner gains
// nothing, because this function stops them taking the role. It is named here rather than decided
// quietly.
func refuseGrantAboveOwnRole(
	ctx context.Context, q *sqlitegen.Queries, circleID core.CircleID,
	actor core.MembershipID, granting string,
) error {
	granted, err := authz.ParseRole(granting)
	if err != nil {
		return err
	}
	actorRow, err := q.GetMembership(ctx, sqlitegen.GetMembershipParams{
		CircleID: circleID.String(), ID: actor.String(),
	})
	if store.IsNotFound(err) {
		// The actor is authenticated and this is their own circle — the tenancy middleware settled
		// that before the handler ran — so a missing row is a database somebody edited by hand.
		// It grants nothing rather than everything.
		return apierr.New(apierr.CodeForbidden, "the acting membership no longer exists")
	}
	if err != nil {
		return err
	}
	actorRole, err := authz.ParseRole(actorRow.Role)
	if err != nil {
		// A role outside the enum grants nothing. Failing open here would make a typo in a
		// migration into a privilege escalation, which is the very thing this function prevents.
		return apierr.Wrap(apierr.CodeForbidden, err, "the acting membership has no usable role")
	}
	if !actorRole.AtLeast(granted) {
		return apierr.Newf(apierr.CodeForbidden,
			"you hold %s and cannot grant %s; a role is handed out by somebody who holds it",
			actorRole, granted).
			WithField("body.role", "above the role you hold")
	}
	return nil
}

// requireAnotherOwner refuses to leave a circle without one.
func requireAnotherOwner(
	ctx context.Context, q *sqlitegen.Queries, circleID core.CircleID, excluding core.MembershipID,
) error {
	rows, err := q.ListMemberships(ctx, circleID.String())
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.ID == excluding.String() || row.RevokedAt != nil {
			continue
		}
		if row.Role == string(authz.RoleOwner) {
			return nil
		}
	}
	return apierr.New(apierr.CodeLastOwner,
		"a circle cannot lose its last owner; promote somebody else first")
}

func validDisplayName(raw string) (string, error) {
	name := trimSpace(raw)
	switch {
	case name == "":
		return "", apierr.New(apierr.CodeValidationFailed, "a member needs a display name").
			WithField("body.display_name", "required")
	case utf8.RuneCountInString(name) > MaxDisplayNameLen:
		return "", apierr.Newf(apierr.CodeValidationFailed,
			"display_name is %d characters; the maximum is %d",
			utf8.RuneCountInString(name), MaxDisplayNameLen).
			WithField("body.display_name", "above the maximum length")
	case core.Normalise(name) == "":
		// A name of nothing but punctuation normalises to the empty string, which would make
		// `possible_duplicate` a claim about every such member at once.
		return "", apierr.New(apierr.CodeValidationFailed,
			"a display name needs at least one letter or digit").
			WithField("body.display_name", "must contain more than punctuation")
	}
	return name, nil
}

// coded keeps a problem a transaction body produced, and turns anything else into a 500. Without
// it, `InTx` would flatten a deliberate `409 last_owner` into an internal error on the way out.
func coded(err error) error {
	if _, ok := apierr.From(err); ok {
		return err
	}
	return apierr.Wrap(apierr.CodeInternalError, err, "")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

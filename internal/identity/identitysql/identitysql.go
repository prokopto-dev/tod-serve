// Package identitysql binds internal/identity's ports to the generated query set.
//
// It exists so internal/identity holds no database type at all: the service takes interfaces, and
// this is the one place that knows a `*string` column is a nullable one. That separation is what
// makes `TestDiscord_AccessToken_NeverPersisted` a real test — "no store call received the token"
// is only assertable when "store call" is something the type system names.
//
// It holds no `*sql.DB` either. internal/store still owns that; this takes the query set the
// store hands out.
package identitysql

import (
	"context"
	"errors"
	"fmt"

	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/identity/discord"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// HashCode turns an invite code into `invite.code_hash`.
//
// It is INJECTED rather than defined here, and that is the point: whichever package mints invite
// codes owns the hashing, and a second spelling of it would let this package resolve one invite
// while redemption resolved another — or none. internal/identity never hashes a code itself,
// which is why its port takes both the code and the hash.
type HashCode func(code string) []byte

// GrantByCodeHash reads the one-time owner grant a code hash names.
//
// It is INJECTED for the reason [HashCode] is, and against the same failure. A code the caller
// pastes can name an `invite` row OR the first-run owner grant, which is not an invite at all —
// it is a `tod_meta` entry under a key internal/invite owns. Spelling that key here would be a
// second lookup path, and a second lookup path is precisely how this port came to exist: with
// only the `invite` table, `createAuthorizationURL` refused every first-run owner code with
// `invite_invalid`, on the same instance where `previewInvite` had just shown it as valid.
//
// It returns `expires_at` and `consumed_at` rather than a verdict because the clock is here, which
// is the division [Store.InviteByCodeHash] already makes for an invite row. A hash naming no grant
// answers a wrapped [store.ErrNoRows], the same answer an unissued code gets.
type GrantByCodeHash func(
	ctx context.Context, q *sqlitegen.Queries, hash []byte,
) (circleID string, expiresAt, consumedAt core.Micros, err error)

// Store implements identity.Store.
type Store struct {
	q     *sqlitegen.Queries
	clock clock.Clock
	hash  HashCode
	grant GrantByCodeHash
}

// New returns a store. Every argument is required; there is no default hash and no default grant
// lookup, because a default is a second spelling nobody remembers overriding.
func New(
	q *sqlitegen.Queries, clk clock.Clock, hash HashCode, grant GrantByCodeHash,
) (*Store, error) {
	switch {
	case q == nil:
		return nil, fmt.Errorf("identity store: no query set")
	case clk == nil:
		return nil, fmt.Errorf("identity store: no clock")
	case hash == nil:
		return nil, fmt.Errorf("identity store: no invite code hash")
	case grant == nil:
		// Refused rather than treated as "this instance has no owner grants": a nil lookup would
		// make every first-run code resolve to nothing, which is the bug this port closes wearing
		// the costume of a configuration choice.
		return nil, fmt.Errorf("identity store: no owner grant lookup")
	}
	return &Store{q: q, clock: clk, hash: hash, grant: grant}, nil
}

// --- providers ---------------------------------------------------------------------------------

func (s *Store) ProviderByKey(ctx context.Context, key string) (identity.Provider, error) {
	row, err := s.q.GetIdentityProviderByKey(ctx, key)
	if store.IsNotFound(err) {
		return identity.Provider{}, fmt.Errorf("provider %q: %w", key, identity.ErrNotFound)
	}
	if err != nil {
		return identity.Provider{}, fmt.Errorf("read provider %q: %w", key, err)
	}
	return toProvider(row), nil
}

func (s *Store) ProviderByID(ctx context.Context, id string) (identity.Provider, error) {
	row, err := s.q.GetIdentityProvider(ctx, id)
	if store.IsNotFound(err) {
		return identity.Provider{}, fmt.Errorf("provider %s: %w", id, identity.ErrNotFound)
	}
	if err != nil {
		return identity.Provider{}, fmt.Errorf("read provider %s: %w", id, err)
	}
	return toProvider(row), nil
}

func (s *Store) SetProviderEnabled(ctx context.Context, id string, enabled bool, at core.Micros) (identity.Provider, error) {
	var flag int64
	if enabled {
		flag = 1
	}
	row, err := s.q.SetIdentityProviderEnabled(ctx, sqlitegen.SetIdentityProviderEnabledParams{
		Enabled: flag, UpdatedAt: int64(at), ID: id,
	})
	if store.IsNotFound(err) {
		return identity.Provider{}, fmt.Errorf("provider %s: %w", id, identity.ErrNotFound)
	}
	if err != nil {
		return identity.Provider{}, fmt.Errorf("set provider %s enabled: %w", id, err)
	}
	return toProvider(row), nil
}

func (s *Store) EnabledProviders(ctx context.Context) ([]identity.Provider, error) {
	rows, err := s.q.ListEnabledIdentityProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list enabled providers: %w", err)
	}
	out := make([]identity.Provider, 0, len(rows))
	for _, row := range rows {
		out = append(out, toProvider(row))
	}
	return out, nil
}

func (s *Store) AllProviders(ctx context.Context) ([]identity.Provider, error) {
	rows, err := s.q.ListIdentityProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	out := make([]identity.Provider, 0, len(rows))
	for _, row := range rows {
		out = append(out, toProvider(row))
	}
	return out, nil
}

func (s *Store) CreateProvider(
	ctx context.Context, p identity.Provider, at core.Micros,
) (identity.Provider, error) {
	row, err := s.q.CreateIdentityProvider(ctx, sqlitegen.CreateIdentityProviderParams{
		ID:                    p.ID,
		Key:                   p.Key,
		Kind:                  string(p.Kind),
		DisplayName:           p.DisplayName,
		Enabled:               boolToInt(p.Enabled),
		VerifiableSubject:     boolToInt(p.VerifiableSubject),
		Issuer:                nullable(p.Issuer),
		AuthorizationEndpoint: nullable(p.AuthorizationEndpoint),
		JwksUri:               nullable(p.JWKSURI),
		SubjectClaim:          nullable(p.SubjectClaim),
		ClientID:              nullable(p.ClientID),
		ClientSecret:          nullable(string(p.ClientSecret)),
		RedirectUri:           nullable(p.RedirectURI),
		TokenEndpoint:         nullable(p.TokenEndpoint),
		CreatedAt:             int64(at),
		UpdatedAt:             int64(at),
	})
	if err != nil {
		return identity.Provider{}, fmt.Errorf("create provider %q: %w", p.Key, err)
	}
	return toProvider(row), nil
}

func (s *Store) UpdateProvider(
	ctx context.Context, p identity.Provider, at core.Micros,
) (identity.Provider, error) {
	row, err := s.q.UpdateIdentityProvider(ctx, sqlitegen.UpdateIdentityProviderParams{
		DisplayName:           p.DisplayName,
		Enabled:               boolToInt(p.Enabled),
		Issuer:                nullable(p.Issuer),
		AuthorizationEndpoint: nullable(p.AuthorizationEndpoint),
		JwksUri:               nullable(p.JWKSURI),
		SubjectClaim:          nullable(p.SubjectClaim),
		ClientID:              nullable(p.ClientID),
		ClientSecret:          nullable(string(p.ClientSecret)),
		RedirectUri:           nullable(p.RedirectURI),
		TokenEndpoint:         nullable(p.TokenEndpoint),
		UpdatedAt:             int64(at),
		ID:                    p.ID,
	})
	if store.IsNotFound(err) {
		return identity.Provider{}, fmt.Errorf("provider %s: %w", p.ID, identity.ErrNotFound)
	}
	if err != nil {
		return identity.Provider{}, fmt.Errorf("update provider %s: %w", p.ID, err)
	}
	return toProvider(row), nil
}

func (s *Store) DeleteProvider(ctx context.Context, id string) error {
	if err := s.q.DeleteIdentityProvider(ctx, id); err != nil {
		return fmt.Errorf("delete provider %s: %w", id, err)
	}
	return nil
}

func toProvider(row sqlitegen.IdentityProvider) identity.Provider {
	return identity.Provider{
		ID:                    row.ID,
		Key:                   row.Key,
		Kind:                  identity.Kind(row.Kind),
		DisplayName:           row.DisplayName,
		Enabled:               row.Enabled == 1,
		VerifiableSubject:     row.VerifiableSubject == 1,
		Issuer:                deref(row.Issuer),
		AuthorizationEndpoint: deref(row.AuthorizationEndpoint),
		JWKSURI:               deref(row.JwksUri),
		SubjectClaim:          deref(row.SubjectClaim),
		ClientID:              deref(row.ClientID),
		ClientSecret:          core.Secret(deref(row.ClientSecret)),
		RedirectURI:           deref(row.RedirectUri),
		TokenEndpoint:         deref(row.TokenEndpoint),
	}
}

// --- auth flow ---------------------------------------------------------------------------------

func (s *Store) CreateAuthFlow(ctx context.Context, flow identity.AuthFlow) error {
	_, err := s.q.CreateAuthFlow(ctx, sqlitegen.CreateAuthFlowParams{
		ID:             flow.ID,
		State:          flow.State,
		PkceVerifier:   flow.PKCEVerifier,
		ProviderID:     flow.ProviderID,
		InviteCodeHash: flow.InviteCodeHash,
		CircleID:       nullable(flow.CircleID),
		ExpiresAt:      int64(flow.ExpiresAt),
		CreatedAt:      int64(flow.CreatedAt),
		UpdatedAt:      int64(flow.CreatedAt),
	})
	if err != nil {
		return fmt.Errorf("insert auth flow: %w", err)
	}
	return nil
}

func (s *Store) ConsumeAuthFlow(ctx context.Context, state string, at core.Micros) (identity.AuthFlow, error) {
	consumed := int64(at)
	row, err := s.q.ConsumeAuthFlow(ctx, sqlitegen.ConsumeAuthFlowParams{
		ConsumedAt: &consumed,
		UpdatedAt:  consumed,
		State:      state,
	})
	// No row means unknown or already consumed, and those are deliberately indistinguishable
	// here: telling them apart would answer "was this a real state?" for a caller guessing.
	if store.IsNotFound(err) {
		return identity.AuthFlow{}, fmt.Errorf("auth flow: %w", identity.ErrNotFound)
	}
	if err != nil {
		return identity.AuthFlow{}, fmt.Errorf("consume auth flow: %w", err)
	}
	return identity.AuthFlow{
		ID:             row.ID,
		State:          row.State,
		PKCEVerifier:   row.PkceVerifier,
		ProviderID:     row.ProviderID,
		InviteCodeHash: row.InviteCodeHash,
		CircleID:       deref(row.CircleID),
		ExpiresAt:      core.Micros(row.ExpiresAt),
		CreatedAt:      core.Micros(row.CreatedAt),
	}, nil
}

// --- credential ticket -------------------------------------------------------------------------

func (s *Store) CreateTicket(ctx context.Context, ticket identity.Ticket) error {
	facts, err := discord.MarshalFacts(ticket.GuildFacts)
	if err != nil {
		return fmt.Errorf("insert credential ticket: %w", err)
	}
	_, err = s.q.CreateCredentialTicket(ctx, sqlitegen.CreateCredentialTicketParams{
		ID:             ticket.ID,
		TicketHash:     ticket.Hash,
		ProviderID:     ticket.ProviderID,
		Subject:        ticket.Subject,
		DisplayName:    ticket.DisplayName,
		GuildRolesJson: facts,
		ExpiresAt:      int64(ticket.ExpiresAt),
		CreatedAt:      int64(ticket.CreatedAt),
		UpdatedAt:      int64(ticket.CreatedAt),
	})
	if err != nil {
		// The CHECK on this table refuses any TTL but 120 seconds, so a wrong one arrives here as
		// a constraint violation rather than as a long-lived ticket.
		return fmt.Errorf("insert credential ticket: %w", err)
	}
	return nil
}

func (s *Store) ReadTicket(ctx context.Context, hash []byte) (identity.Ticket, error) {
	row, err := s.q.GetCredentialTicketByHash(ctx, hash)
	if store.IsNotFound(err) {
		return identity.Ticket{}, fmt.Errorf("credential ticket: %w", identity.ErrNotFound)
	}
	if err != nil {
		return identity.Ticket{}, fmt.Errorf("read credential ticket: %w", err)
	}
	// A ticket already consumed reads as absent. The service distinguishes expiry from invalidity
	// by the row's own `expires_at`, and a consumed ticket is simply invalid.
	if row.ConsumedAt != nil {
		return identity.Ticket{}, fmt.Errorf("credential ticket is consumed: %w", identity.ErrNotFound)
	}
	return toTicket(row)
}

func (s *Store) ConsumeTicket(ctx context.Context, hash []byte, at core.Micros) (identity.Ticket, error) {
	consumed := int64(at)
	row, err := s.q.ConsumeCredentialTicket(ctx, sqlitegen.ConsumeCredentialTicketParams{
		ConsumedAt: &consumed,
		UpdatedAt:  consumed,
		TicketHash: hash,
	})
	if store.IsNotFound(err) {
		return identity.Ticket{}, fmt.Errorf("credential ticket: %w", identity.ErrNotFound)
	}
	if err != nil {
		return identity.Ticket{}, fmt.Errorf("consume credential ticket: %w", err)
	}
	return toTicket(row)
}

func toTicket(row sqlitegen.CredentialTicket) (identity.Ticket, error) {
	facts, err := discord.ParseFacts(row.GuildRolesJson)
	if err != nil {
		return identity.Ticket{}, fmt.Errorf("read credential ticket %s: %w", row.ID, err)
	}
	return identity.Ticket{
		ID:          row.ID,
		Hash:        row.TicketHash,
		ProviderID:  row.ProviderID,
		Subject:     row.Subject,
		DisplayName: row.DisplayName,
		GuildFacts:  facts,
		ExpiresAt:   core.Micros(row.ExpiresAt),
		CreatedAt:   core.Micros(row.CreatedAt),
	}, nil
}

// --- invites -----------------------------------------------------------------------------------

func (s *Store) InviteByCode(ctx context.Context, code string) (identity.Invite, error) {
	return s.InviteByCodeHash(ctx, s.hash(code))
}

// InviteByCodeHash resolves whatever a code names — an `invite` row, or the one-time owner grant
// that gives a circle its first owner.
//
// The fallback is not a convenience. A first-run owner code is the ONLY credential a fresh
// instance has, it is not an `invite` row, and without the second rung every browser sign-in that
// carried one ended at `#error=invite_invalid` — after the operator had signed in at Discord
// successfully, which is why nothing in the logs looked like a refusal. internal/invite's Resolve
// has had both rungs since it was written; this is the path that did not.
func (s *Store) InviteByCodeHash(ctx context.Context, hash []byte) (identity.Invite, error) {
	row, err := s.q.GetInviteByCodeHash(ctx, hash)
	if store.IsNotFound(err) {
		return s.ownerGrantByCodeHash(ctx, hash)
	}
	if err != nil {
		return identity.Invite{}, fmt.Errorf("read invite: %w", err)
	}

	// The circle has to still exist, and this is checked BEFORE the liveness switch below rather
	// than as another dead-code case. A tombstoned circle is not a dead invite — it is a code that
	// names nothing — so it answers what an unissued code answers, which is what `previewInvite`
	// and `/join` already answer for it.
	//
	// `GetCircle` carries `deleted_at IS NULL`, so this is the same predicate those two paths use
	// rather than a second spelling of it. Without it `createAuthorizationURL` would resolve the
	// code, write an `auth_flow` row and hand back an OAuth URL for a circle that no longer
	// exists, while `previewInvite` called it invalid — two public routes disagreeing about one
	// code, and a row stored for a circle nobody can join.
	if _, err := s.q.GetCircle(ctx, row.CircleID); err != nil {
		if store.IsNotFound(err) {
			return identity.Invite{}, fmt.Errorf("invite circle: %w", identity.ErrNotFound)
		}
		return identity.Invite{}, fmt.Errorf("read invite circle: %w", err)
	}

	out := identity.Invite{ID: row.ID, CircleID: row.CircleID, CodeHash: row.CodeHash, Live: true}
	// Ordered most-specific first: a revoked invite that has also expired is revoked, because
	// that is the fact an officer acted on and the one they will look for.
	switch now := s.clock.Now(); {
	case row.RevokedAt != nil:
		out.Live, out.DeadCode = false, identity.CodeInviteRevoked
	case core.Micros(row.ExpiresAt) <= now:
		out.Live, out.DeadCode = false, identity.CodeInviteExpired
	case row.Uses >= row.MaxUses:
		out.Live, out.DeadCode = false, identity.CodeInviteExhausted
	}
	return out, nil
}

// ownerGrantByCodeHash answers the invite lookup from the owner-grant ledger.
//
// The answer is deliberately the SAME SHAPE an invite gets, down to which code a dead one carries:
// [identity.ErrNotFound] for a hash nobody was issued and for one whose circle is gone, and
// otherwise a live-or-dead [identity.Invite]. An owner grant a guesser could tell apart from an
// ordinary invite would be the circle-existence oracle internal/invite closes deliberately, so
// widening this is not a small change.
//
// `ID` stays empty, because a grant has no `invite` row to name. Nothing in internal/identity
// reads it — the flow uses `CircleID` for the guild gate and `CodeHash` for the `auth_flow` row —
// and `TestOwnerGrant_ResolvesWithNoInviteID` is what says so out loud rather than leaving the
// next caller to discover it.
func (s *Store) ownerGrantByCodeHash(ctx context.Context, hash []byte) (identity.Invite, error) {
	circleID, expiresAt, consumedAt, err := s.grant(ctx, s.q, hash)
	if errors.Is(err, store.ErrNoRows) {
		return identity.Invite{}, fmt.Errorf("invite: %w", identity.ErrNotFound)
	}
	if err != nil {
		return identity.Invite{}, fmt.Errorf("read owner grant: %w", err)
	}

	// The same check, and for the same reason, as the invite branch above: a code naming a
	// tombstoned circle is a code that names nothing.
	if _, err := s.q.GetCircle(ctx, circleID); err != nil {
		if store.IsNotFound(err) {
			return identity.Invite{}, fmt.Errorf("invite circle: %w", identity.ErrNotFound)
		}
		return identity.Invite{}, fmt.Errorf("read owner grant circle: %w", err)
	}

	out := identity.Invite{CircleID: circleID, CodeHash: hash, Live: true}
	// Ordered as internal/invite's resolvedGrant orders it, and the two must agree or the OAuth
	// flow and `/join` would give one code holder two different stories. A grant is single-use by
	// construction, so a spent one is EXHAUSTED — the same fact `invite.uses >= max_uses` records
	// — and only an unspent one can be reported expired.
	switch now := s.clock.Now(); {
	case !consumedAt.IsZero():
		out.Live, out.DeadCode = false, identity.CodeInviteExhausted
	case expiresAt <= now:
		out.Live, out.DeadCode = false, identity.CodeInviteExpired
	}
	return out, nil
}

// --- circles -----------------------------------------------------------------------------------

func (s *Store) GuildGate(ctx context.Context, circleID, providerID string) (identity.GuildGate, error) {
	row, err := s.q.GetCircleProvider(ctx, sqlitegen.GetCircleProviderParams{
		CircleID: circleID, ProviderID: providerID,
	})
	if store.IsNotFound(err) {
		return identity.GuildGate{}, fmt.Errorf("circle provider: %w", identity.ErrNotFound)
	}
	if err != nil {
		return identity.GuildGate{}, fmt.Errorf("read circle provider: %w", err)
	}
	roleIDs, err := discord.ParseRoleIDs(row.DiscordRequiredRoleIdsJson)
	if err != nil {
		// Refused rather than defaulted to "no roles required": an unparseable list read as an
		// empty one opens the gate for everybody.
		return identity.GuildGate{}, fmt.Errorf("read circle provider %s: %w", circleID, err)
	}
	return identity.GuildGate{GuildID: deref(row.DiscordGuildID), RequiredRoleIDs: roleIDs}, nil
}

func (s *Store) AnyCircleGatesOnAGuild(ctx context.Context) (bool, error) {
	gated, err := s.q.AnyCircleGatesOnAGuild(ctx)
	if err != nil {
		return false, fmt.Errorf("read whether any circle gates on a guild: %w", err)
	}
	return gated, nil
}

func (s *Store) CircleIDsForIdentity(ctx context.Context, identityID string) ([]string, error) {
	rows, err := s.q.ListCirclesForIdentity(ctx, &identityID)
	if err != nil {
		return nil, fmt.Errorf("list circles for identity: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out, nil
}

// --- identities --------------------------------------------------------------------------------

func (s *Store) IdentityBySubject(ctx context.Context, providerID, subject string) (identity.StoredIdentity, error) {
	row, err := s.q.GetIdentityByProviderSubject(ctx, sqlitegen.GetIdentityByProviderSubjectParams{
		ProviderID: providerID, Subject: subject,
	})
	if store.IsNotFound(err) {
		return identity.StoredIdentity{}, fmt.Errorf("identity: %w", identity.ErrNotFound)
	}
	if err != nil {
		return identity.StoredIdentity{}, fmt.Errorf("read identity: %w", err)
	}
	return identity.StoredIdentity{
		ID:          row.ID,
		ProviderID:  row.ProviderID,
		Subject:     row.Subject,
		DisplayName: row.DisplayName,
		Blocked:     row.BlockedAt != nil,
	}, nil
}

// --- nullable columns --------------------------------------------------------------------------

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// nullable is the other direction. An empty string becomes NULL rather than ”: the schema's
// CHECKs are written against NULL, and a ” that satisfied a NOT NULL check would be a row that
// looks configured and is not.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// boolToInt renders a boolean the way canonical §8 stores one: INTEGER, CHECK (x IN (0,1)).
func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// The compiler holds this to the port set, so a method added to identity.Store is a build failure
// here rather than a nil interface at wiring time.
var _ identity.Store = (*Store)(nil)

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

// Store implements identity.Store.
type Store struct {
	q     *sqlitegen.Queries
	clock clock.Clock
	hash  HashCode
}

// New returns a store. Every argument is required; there is no default hash, because a default
// hash is a second hash nobody remembers overriding.
func New(q *sqlitegen.Queries, clk clock.Clock, hash HashCode) (*Store, error) {
	switch {
	case q == nil:
		return nil, fmt.Errorf("identity store: no query set")
	case clk == nil:
		return nil, fmt.Errorf("identity store: no clock")
	case hash == nil:
		return nil, fmt.Errorf("identity store: no invite code hash")
	}
	return &Store{q: q, clock: clk, hash: hash}, nil
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

func (s *Store) InviteByCodeHash(ctx context.Context, hash []byte) (identity.Invite, error) {
	row, err := s.q.GetInviteByCodeHash(ctx, hash)
	if store.IsNotFound(err) {
		return identity.Invite{}, fmt.Errorf("invite: %w", identity.ErrNotFound)
	}
	if err != nil {
		return identity.Invite{}, fmt.Errorf("read invite: %w", err)
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

// The compiler holds this to the port set, so a method added to identity.Store is a build failure
// here rather than a nil interface at wiring time.
var _ identity.Store = (*Store)(nil)

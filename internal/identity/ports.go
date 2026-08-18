package identity

import (
	"context"
	"errors"

	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity/discord"
)

// ErrNotFound is what every port returns for a row that is not there. It is one sentinel rather
// than one per port so a caller can ask the question once; which row was missing is in the
// wrapping context.
var ErrNotFound = errors.New("not found")

// AuthFlow is one in-flight browser authorization.
//
// `CircleID` is ADVISORY. Canonical §9 permits resolving an invite's circle to parameterise
// authorization — the scope set and the guild to check are decided before the browser leaves —
// and forbids treating it as a tenancy key. Redemption re-derives the circle from the invite and
// is the authority, so a flow that resolved one circle can never cause a join into another.
type AuthFlow struct {
	ID             string
	State          string
	PKCEVerifier   string
	ProviderID     string
	InviteCodeHash []byte
	CircleID       string
	ExpiresAt      core.Micros
	CreatedAt      core.Micros
}

// Ticket is a `credential_ticket`: a verified subject for 120 seconds, redeemable once.
type Ticket struct {
	ID          string
	Hash        []byte
	ProviderID  string
	Subject     string
	DisplayName string
	GuildFacts  discord.GuildFacts
	ExpiresAt   core.Micros
	CreatedAt   core.Micros
}

// Invite is as much of an invite as this package is allowed to know.
//
// It deliberately carries no code and no display information: `createAuthorizationURL` is held to
// `previewInvite`'s disclosure as a CEILING, so the less this type can express, the harder it is
// for the newer endpoint to drift wider than the older one.
type Invite struct {
	ID       string
	CircleID string
	CodeHash []byte

	// Live is whether it can still be redeemed. DeadCode says why not, and is one of
	// invite_expired, invite_revoked or invite_exhausted.
	Live     bool
	DeadCode Code
}

// StoredIdentity is a `(provider, subject)` row.
type StoredIdentity struct {
	ID          string
	ProviderID  string
	Subject     string
	DisplayName string

	// Blocked is `blocked_at IS NOT NULL` — the INSTANCE operator's decision, refused at join AND
	// at ticket redemption so that a second circle is not a second door.
	Blocked bool
}

// ProviderPort reads the registry.
type ProviderPort interface {
	ProviderByKey(ctx context.Context, key string) (Provider, error)
	ProviderByID(ctx context.Context, id string) (Provider, error)
	EnabledProviders(ctx context.Context) ([]Provider, error)
}

// AuthFlowPort writes and consumes `auth_flow`. Consumption is single-use in the database — the
// query carries `WHERE consumed_at IS NULL` and a BEFORE UPDATE trigger aborts a second one — so
// this interface does not need to promise it and could not enforce it if it did.
type AuthFlowPort interface {
	CreateAuthFlow(ctx context.Context, flow AuthFlow) error
	ConsumeAuthFlow(ctx context.Context, state string, at core.Micros) (AuthFlow, error)
}

// TicketPort writes and consumes `credential_ticket`.
//
// The single-use property is the SCHEMA's, not this interface's:
// `trg_credential_ticket_single_use` aborts an update to an already-consumed row, so a replay is
// unrepresentable rather than merely checked. Likewise the 120-second TTL is
// `CHECK (expires_at = created_at + 120 * 1000000)`, so a longer-lived ticket cannot be written
// at all. An implementation that satisfied this interface without those is a broken
// implementation, which is why the real one goes through the real tables.
type TicketPort interface {
	CreateTicket(ctx context.Context, ticket Ticket) error
	ReadTicket(ctx context.Context, hash []byte) (Ticket, error)
	ConsumeTicket(ctx context.Context, hash []byte, at core.Micros) (Ticket, error)
}

// InvitePort resolves an invite. The implementation owns the code hashing, which is why both
// lookups are here rather than a hash function being exported from this package: two spellings of
// one hash is exactly the drift that would make a flow resolve one invite and redeem another.
type InvitePort interface {
	InviteByCode(ctx context.Context, code string) (Invite, error)
	InviteByCodeHash(ctx context.Context, hash []byte) (Invite, error)
}

// CirclePort answers the two circle questions the flow is allowed to ask.
type CirclePort interface {
	// GuildGate returns the circle's Discord gate for this provider. A circle that does not
	// accept the provider returns [ErrNotFound].
	GuildGate(ctx context.Context, circleID, providerID string) (GuildGate, error)

	// AnyCircleGatesOnAGuild is an INSTANCE-level fact and names no circle. It is what the
	// no-invite authorization path uses to decide scopes, because there is no circle to resolve
	// yet and resolving one from a caller-supplied id is the existence oracle canonical §7 closes.
	AnyCircleGatesOnAGuild(ctx context.Context) (bool, error)

	// CircleIDsForIdentity are the circles a VERIFIED identity already belongs to — a lookup
	// keyed on something the caller proved, not something they supplied.
	CircleIDsForIdentity(ctx context.Context, identityID string) ([]string, error)
}

// IdentityPort reads `identity`.
type IdentityPort interface {
	IdentityBySubject(ctx context.Context, providerID, subject string) (StoredIdentity, error)
}

// Store is everything this package needs from the database.
//
// It is an interface, and the interface is why `TestDiscord_AccessToken_NeverPersisted` can be a
// test rather than a code-review habit: a recording fake sees every call the flow makes, so
// "no store call receives the token" is a thing that can be asserted rather than eyeballed.
type Store interface {
	ProviderPort
	AuthFlowPort
	TicketPort
	InvitePort
	CirclePort
	IdentityPort
}

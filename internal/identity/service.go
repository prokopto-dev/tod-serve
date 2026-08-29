package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity/discord"
	"github.com/prokopto-dev/tod-serve/internal/identity/local"
	"github.com/prokopto-dev/tod-serve/internal/identity/oidc"
)

// TicketTTL is how long a `credential_ticket` lives. It is not a policy knob: the schema carries
// `CHECK (expires_at = created_at + 120 * 1000000)`, so a ticket with any other lifetime cannot
// be written at all. The constant is here so the Go that computes `expires_at` and the SQL that
// refuses a wrong one are visibly the same number.
const TicketTTL = 120 * time.Second

// AuthFlowTTL is how long a browser has to finish an authorization. Long enough that somebody who
// alt-tabs to read the consent screen does not lose the flow; short enough that an abandoned one
// is litter for minutes rather than days. `auth_flow` rows are swept on expiry.
const AuthFlowTTL = 10 * time.Minute

// secretBytes is the entropy in a state, a PKCE verifier and a ticket. 256 bits: these are
// unguessable-by-construction values, and the cost of the extra bytes is a longer URL.
const secretBytes = 32

// Config is what a [Service] needs. Every field is required; there is no zero value that works,
// which is deliberate — a service with a nil clock or a nil entropy source would fail somewhere
// far from here.
type Config struct {
	Store   Store
	Clients Clients
	Clock   clock.Clock
	IDs     *core.Generator
	Entropy io.Reader

	// SPAJoinURL is where `completeAuthorization` sends the browser, e.g.
	// https://tod.example.com/join. The ticket rides in that URL's FRAGMENT.
	SPAJoinURL string

	// CallbackBaseURL is the absolute URL a provider redirects BACK to, minus the provider key:
	// e.g. https://tod.example.com/api/v1/auth/callback. Appending a key to it produces the
	// string an operator must have registered with that provider, character for character.
	//
	// It is a separate field from SPAJoinURL rather than derived from it because the two may
	// legitimately sit on different origins — `$TOD_SPA_JOIN_URL` exists so the console can be
	// hosted apart from the API — and the redirect URI belongs to the API's origin, always.
	//
	// It is a string rather than something this package computes because the path belongs to
	// internal/api's route registry, and internal/api imports this package. `api.CallbackBaseURL`
	// derives it there; the wiring hands over the answer.
	CallbackBaseURL string

	Logger *slog.Logger
}

// Service is the provider registry, the credential dispatch and the browser OAuth flow.
type Service struct {
	store   Store
	clients Clients
	clock   clock.Clock
	ids     *core.Generator
	entropy io.Reader
	spaJoin *url.URL
	log     *slog.Logger

	// callbackBase is CallbackBaseURL, parsed and with any trailing slash removed, so that
	// appending "/" + key yields the one canonical redirect URI for a provider.
	callbackBase string
}

// New returns a service.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("identity service: no store")
	case cfg.Clients == nil:
		return nil, errors.New("identity service: no provider clients")
	case cfg.Clock == nil:
		return nil, errors.New("identity service: no clock")
	case cfg.IDs == nil:
		return nil, errors.New("identity service: no id generator")
	case cfg.Entropy == nil:
		// No fallback to crypto/rand. A generator that quietly reaches for a default is one
		// nobody notices was given the wrong source.
		return nil, errors.New("identity service: no entropy source")
	case cfg.Logger == nil:
		return nil, errors.New("identity service: no logger")
	case cfg.SPAJoinURL == "":
		return nil, errors.New("identity service: no SPA join url to redirect to")
	case cfg.CallbackBaseURL == "":
		// No fallback to the join URL's origin. Guessing the origin a provider redirects back to
		// is guessing the string the operator registered with that provider, and a wrong guess
		// produces exactly the failure this field exists to make loud.
		return nil, errors.New("identity service: no callback base url to check redirect uris against")
	}
	spaJoin, err := url.Parse(cfg.SPAJoinURL)
	if err != nil {
		return nil, fmt.Errorf("identity service: parse SPA join url: %w", err)
	}
	if spaJoin.Scheme == "" || spaJoin.Host == "" {
		return nil, fmt.Errorf("identity service: SPA join url %q is not absolute", cfg.SPAJoinURL)
	}
	callbackBase, err := url.Parse(strings.TrimRight(cfg.CallbackBaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("identity service: parse callback base url: %w", err)
	}
	if callbackBase.Scheme == "" || callbackBase.Host == "" {
		return nil, fmt.Errorf("identity service: callback base url %q is not absolute", cfg.CallbackBaseURL)
	}

	return &Service{
		store:   cfg.Store,
		clients: cfg.Clients,
		clock:   cfg.Clock,
		ids:     cfg.IDs,
		entropy: cfg.Entropy,
		spaJoin: spaJoin,
		log:     cfg.Logger,

		callbackBase: callbackBase.String(),
	}, nil
}

// VerifyRequest is one credential presented for one provider.
type VerifyRequest struct {
	Provider   Provider
	Credential Credential

	// GuildIDs is the set of guilds the Discord `bearer_token` path should read facts for. Every
	// other path ignores it: a `provider_ticket` already carries its facts, and OIDC has no guild
	// concept at all. It comes from the CALLER's already-resolved circle — from an invite, or
	// from the `circle_id` on `/sessions` that was accepted alongside a credential.
	GuildIDs []string

	// DisplayName is the caller-supplied name. Required for `local`, optional elsewhere, where a
	// non-empty value overrides what the provider reported.
	DisplayName string
}

// Verified is what a credential established. It is the ONLY thing that survives verification —
// in particular, no provider access token is in it, because none is kept.
type Verified struct {
	ProviderID  string
	Subject     string
	DisplayName string

	// GuildFacts is empty for every provider but `discord`. An empty map is not "no roles
	// required": [EvaluateGuildGate] reads an absent fact as a rejection.
	GuildFacts GuildFacts
}

// Verify checks a credential and returns the identity it establishes.
//
// One entry point for every provider, dispatching on `kind` — which is the whole of
// [ADR-0007]'s one-join-endpoint rule expressed in Go. `/join` and `/sessions` both call this.
//
// [ADR-0007]: docs/adr/0007-one-join-endpoint.md
func (s *Service) Verify(ctx context.Context, req VerifyRequest) (Verified, error) {
	if err := req.Provider.Validate(); err != nil {
		// Not a coded error: an inconsistent provider row is this instance's bug, and presenting
		// it as the caller's fault would send somebody looking in the wrong place.
		return Verified{}, fmt.Errorf("verify credential: %w", err)
	}
	if !req.Provider.Enabled {
		return Verified{}, NewError(CodeProviderDisabled,
			fmt.Sprintf("this instance has disabled the %q provider", req.Provider.Key), nil)
	}
	if err := req.Credential.Validate(req.Provider); err != nil {
		return Verified{}, err
	}

	switch req.Credential.Kind {
	case CredentialProviderTicket:
		return s.verifyTicket(ctx, req)
	case CredentialBearerToken:
		return s.verifyDiscordBearer(ctx, req)
	case CredentialIDToken:
		return s.verifyIDToken(ctx, req)
	case CredentialNone:
		return s.verifyLocal(req)
	default:
		// Unreachable: Credential.Validate closed the set. Named anyway, because an unreachable
		// default that returns nil is how a new kind gets silently accepted.
		return Verified{}, NewValidationError(LocationCredentialKind,
			fmt.Sprintf("credential kind %q is not one this server accepts", req.Credential.Kind))
	}
}

// verifyTicket redeems a `provider_ticket`.
func (s *Service) verifyTicket(ctx context.Context, req VerifyRequest) (Verified, error) {
	got, err := s.RedeemProviderTicket(ctx, req.Credential.Ticket)
	if err != nil {
		return Verified{}, err
	}
	if got.ProviderID != req.Provider.ID {
		// The ticket is already consumed at this point, and that is correct: a ticket presented
		// for the wrong provider has been used, and letting it be retried against the right one
		// would make it multi-use in exactly the case somebody was probing.
		return Verified{}, NewError(CodeCredentialInvalid, "this ticket was issued for another provider", nil)
	}
	return s.withDisplayName(got, req.DisplayName), nil
}

// verifyDiscordBearer is the non-browser Discord path.
//
// Its safety rests ENTIRELY on the audience check inside [discord.Client.Verify] rather than on
// the shape of the flow — the browser path does not need that trust, because it runs its own
// exchange with its own secret and never receives a token from a caller.
//
// The `credential_stale` freshness rule the design pairs with it is deliberately absent, and the
// reason is in docs/design/04-identity-and-revocation.md §7: it needs the token's AGE, and
// `GET /oauth2/@me` reports `expires` with no issue time, so age is derivable only by assuming
// Discord's token lifetime. That assumption would stop being true silently. A freshness rule that
// is wrong in an unknown direction is worse than none, because it is believed.
func (s *Service) verifyDiscordBearer(ctx context.Context, req VerifyRequest) (Verified, error) {
	client, err := s.clients.Discord(req.Provider)
	if err != nil {
		return Verified{}, fmt.Errorf("verify discord bearer token: %w", err)
	}
	facts, err := client.Verify(ctx, req.Credential.Token, req.GuildIDs)
	if err != nil {
		return Verified{}, mapDiscordError(err)
	}
	if err := s.refuseBlocked(ctx, req.Provider.ID, facts.Subject); err != nil {
		return Verified{}, err
	}
	return s.withDisplayName(Verified{
		ProviderID:  req.Provider.ID,
		Subject:     facts.Subject,
		DisplayName: facts.DisplayName,
		GuildFacts:  facts.Guilds,
	}, req.DisplayName), nil
}

// verifyIDToken is the non-browser OIDC path: offline, no network beyond a possibly cached JWKS.
func (s *Service) verifyIDToken(ctx context.Context, req VerifyRequest) (Verified, error) {
	verifier, err := s.clients.OIDC(req.Provider)
	if err != nil {
		return Verified{}, fmt.Errorf("verify oidc id token: %w", err)
	}
	got, err := verifier.Verify(ctx, req.Credential.IDToken, req.Credential.Nonce)
	if err != nil {
		return Verified{}, mapOIDCError(err)
	}
	if err := s.refuseBlocked(ctx, req.Provider.ID, got.Subject); err != nil {
		return Verified{}, err
	}
	return s.withDisplayName(Verified{
		ProviderID:  req.Provider.ID,
		Subject:     got.Subject,
		DisplayName: got.DisplayName,
		GuildFacts:  GuildFacts{},
	}, req.DisplayName), nil
}

// verifyLocal mints a new identity out of nothing, which is what `local` is.
//
// There is no blocked-identity check here and there cannot be one: the subject is minted in this
// function, so it has never been seen before and cannot have been blocked. That is not a hole in
// the block, it is the honest consequence of an unverifiable subject — and it is why `local` is
// weak, ships disabled, and is never auto-accepted by a circle.
func (s *Service) verifyLocal(req VerifyRequest) (Verified, error) {
	identity, err := local.Mint(s.ids, s.clock.Now(), req.DisplayName)
	if errors.Is(err, local.ErrDisplayNameRequired) {
		return Verified{}, NewValidationError(LocationDisplayName,
			"a local identity is a self-asserted name, so the name is required")
	}
	if err != nil {
		return Verified{}, fmt.Errorf("mint local identity: %w", err)
	}
	return Verified{
		ProviderID:  req.Provider.ID,
		Subject:     identity.Subject,
		DisplayName: identity.DisplayName,
		GuildFacts:  GuildFacts{},
	}, nil
}

// RedeemProviderTicket consumes a `credential_ticket` and returns the facts it carries.
//
// Single-use and time-bounded, and neither is enforced here: `ConsumeTicket` writes under
// `WHERE consumed_at IS NULL` and a BEFORE UPDATE trigger aborts a second consumption, so a
// replay is unrepresentable at the schema rather than merely refused in Go. What this function
// adds is the DISTINCTION — a ticket that was consumed is `auth_ticket_invalid`, one that ran out
// of time is `auth_ticket_expired` — which needs a read before the write to be answerable.
func (s *Service) RedeemProviderTicket(ctx context.Context, ticket string) (Verified, error) {
	if ticket == "" {
		return Verified{}, NewValidationError(LocationCredentialTicket, "a provider_ticket credential carries a ticket")
	}
	hash := HashTicket(ticket)

	stored, err := s.store.ReadTicket(ctx, hash)
	if errors.Is(err, ErrNotFound) {
		return Verified{}, NewError(CodeAuthTicketInvalid, "this ticket is not one we issued, or has already been redeemed", nil)
	}
	if err != nil {
		return Verified{}, fmt.Errorf("read credential ticket: %w", err)
	}
	// Expiry is checked before consumption so an expired ticket reports the reason it failed. The
	// TTL itself is a CHECK on the row; this is the clock catching up with it.
	if !s.clock.Now().Before(stored.ExpiresAt) {
		return Verified{}, NewError(CodeAuthTicketExpired,
			fmt.Sprintf("a ticket is valid for %s and this one is older", TicketTTL), nil)
	}

	consumed, err := s.store.ConsumeTicket(ctx, hash, s.clock.Now())
	if errors.Is(err, ErrNotFound) {
		// Between the read and the write somebody else redeemed it. One authorization, one PAT.
		return Verified{}, NewError(CodeAuthTicketInvalid, "this ticket has already been redeemed", nil)
	}
	if err != nil {
		return Verified{}, fmt.Errorf("consume credential ticket: %w", err)
	}

	// The instance block is checked HERE as well as at join, so a second circle is not a second
	// door: a blocked identity presenting a valid ticket lands nowhere at all.
	if err := s.refuseBlocked(ctx, consumed.ProviderID, consumed.Subject); err != nil {
		return Verified{}, err
	}

	facts := consumed.GuildFacts
	if facts == nil {
		facts = GuildFacts{}
	}
	return Verified{
		ProviderID:  consumed.ProviderID,
		Subject:     consumed.Subject,
		DisplayName: consumed.DisplayName,
		GuildFacts:  facts,
	}, nil
}

// refuseBlocked implements `identity.blocked_at`: the INSTANCE operator's decision, refused at
// join AND at ticket redemption. Per-circle revocation stays the officers' tool; this is the
// operator's, for the identity that must not land in any circle at all — including one whose
// officers have never heard of them.
func (s *Service) refuseBlocked(ctx context.Context, providerID, subject string) error {
	stored, err := s.store.IdentityBySubject(ctx, providerID, subject)
	if errors.Is(err, ErrNotFound) {
		return nil // A subject we have never seen cannot be blocked.
	}
	if err != nil {
		return fmt.Errorf("read identity: %w", err)
	}
	if stored.Blocked {
		return NewError(CodeIdentityBlocked, "this identity is blocked on this instance", nil)
	}
	return nil
}

// withDisplayName lets the join request override what the provider reported. The provider's name
// is a default, not an authority: somebody's Discord global name is not necessarily what their
// guild calls them.
func (s *Service) withDisplayName(v Verified, supplied string) Verified {
	if supplied != "" {
		v.DisplayName = supplied
	}
	return v
}

// HashTicket is how a ticket becomes `credential_ticket.ticket_hash`.
//
// The column holds the hash and never the ticket, for the reason every bearer credential in this
// schema is stored hashed: a database read must not yield a working credential. SHA-256 with no
// salt and no stretching is correct here and would not be for a password — the input is 256 bits
// of server-minted entropy, so there is no dictionary to run.
func HashTicket(ticket string) []byte {
	sum := sha256.Sum256([]byte(ticket))
	return sum[:]
}

// mintSecret returns a URL-safe 256-bit value: a state, a PKCE verifier or a ticket.
func (s *Service) mintSecret() (string, error) {
	buf := make([]byte, secretBytes)
	if _, err := io.ReadFull(s.entropy, buf); err != nil {
		return "", fmt.Errorf("read entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// mapDiscordError turns the provider package's sentinels into wire codes. The mapping lives here
// so internal/identity/discord never has to know what an HTTP status is.
func mapDiscordError(err error) error {
	switch {
	case errors.Is(err, discord.ErrAudienceMismatch):
		return NewError(CodeCredentialAudienceMismatch,
			"this Discord token was minted for another application, so it is not usable here", err)
	case errors.Is(err, discord.ErrScopeDeclined):
		return NewError(CodeProviderScopeDeclined,
			"a permission this circle's checks need was not granted", err)
	case errors.Is(err, discord.ErrGuildMembershipRequired):
		return NewError(CodeGuildMembershipRequired, "this circle requires membership of a Discord guild you are not in", err)
	case errors.Is(err, discord.ErrGuildRoleRequired):
		return NewError(CodeGuildRoleRequired, "this circle requires a Discord role we hold no fact that you have", err)
	case errors.Is(err, discord.ErrCredentialInvalid):
		return NewError(CodeCredentialInvalid, "Discord refused this credential", err)
	case errors.Is(err, discord.ErrUnreachable):
		return NewError(CodeProviderUnreachable, "Discord could not be reached", err)
	default:
		return err
	}
}

// mapOIDCError does the same for the OIDC verifier.
func mapOIDCError(err error) error {
	switch {
	case errors.Is(err, oidc.ErrAudienceMismatch):
		return NewError(CodeCredentialAudienceMismatch, "this id token was minted for another client", err)
	case errors.Is(err, oidc.ErrCredentialExpired):
		return NewError(CodeCredentialExpired, "this id token has expired", err)
	case errors.Is(err, oidc.ErrCredentialInvalid):
		return NewError(CodeCredentialInvalid, "this id token is not valid", err)
	case errors.Is(err, oidc.ErrUnreachable):
		return NewError(CodeProviderUnreachable, "the identity provider's key set could not be read", err)
	default:
		return err
	}
}

// The compiler holds crypto/rand to the entropy interface main wires in, so a change to that
// field's type is a compile error here rather than a weak generator in production.
var _ io.Reader = rand.Reader

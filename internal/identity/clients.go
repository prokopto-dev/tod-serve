package identity

import (
	"errors"
	"fmt"
	"sync"

	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/identity/discord"
	"github.com/prokopto-dev/tod-serve/internal/identity/oidc"
	"github.com/prokopto-dev/tod-serve/internal/identity/outbound"
)

// Clients builds the per-provider verifiers.
//
// An interface so a test can supply a stub, and — the reason it is not just a function — so the
// OIDC verifier can be CACHED. A verifier holds the JWKS cache; rebuilding one per request would
// fetch the key set on every join, which is a request to the operator's issuer per join and a
// direct route to being rate-limited by it.
type Clients interface {
	Discord(p Provider) (*discord.Client, error)
	OIDC(p Provider) (*oidc.Verifier, error)
}

// GuardedClients is the real implementation: every provider gets a client from
// internal/identity/outbound, with an allowlist naming only the hosts that provider fetches.
type GuardedClients struct {
	clock clock.Clock

	mu       sync.Mutex
	discords map[string]cached[*discord.Client]
	oidcs    map[string]cached[*oidc.Verifier]
}

// cached pairs a built client with a fingerprint of the row it was built from, so an operator
// editing a provider takes effect on the next request rather than after a restart.
type cached[T any] struct {
	fingerprint string
	value       T
}

// NewGuardedClients returns the real client factory.
func NewGuardedClients(clk clock.Clock) (*GuardedClients, error) {
	if clk == nil {
		return nil, errors.New("identity clients: no clock")
	}
	return &GuardedClients{
		clock:    clk,
		discords: map[string]cached[*discord.Client]{},
		oidcs:    map[string]cached[*oidc.Verifier]{},
	}, nil
}

// Discord returns the client for a `discord` provider row.
func (g *GuardedClients) Discord(p Provider) (*discord.Client, error) {
	if p.Kind != KindDiscord {
		return nil, fmt.Errorf("provider %q is %s, not discord", p.Key, p.Kind)
	}
	fingerprint := fmt.Sprintf("%s|%s|%s", p.ClientID, p.RedirectURI, p.TokenEndpoint)

	g.mu.Lock()
	defer g.mu.Unlock()
	if got, ok := g.discords[p.ID]; ok && got.fingerprint == fingerprint {
		return got.value, nil
	}

	// Discord's host is fixed, so the allowlist is a constant rather than something read out of a
	// row an operator can edit. That is the whole reason `discord` is not on the SSRF surface.
	doer, err := outbound.New(outbound.Policy{AllowHosts: []string{discord.Host}})
	if err != nil {
		return nil, fmt.Errorf("build discord client for %q: %w", p.Key, err)
	}
	client, err := discord.New(doer, discord.Config{
		ClientID:      p.ClientID,
		ClientSecret:  p.ClientSecret,
		RedirectURI:   p.RedirectURI,
		TokenEndpoint: p.TokenEndpoint,
	})
	if err != nil {
		return nil, fmt.Errorf("build discord client for %q: %w", p.Key, err)
	}
	g.discords[p.ID] = cached[*discord.Client]{fingerprint: fingerprint, value: client}
	return client, nil
}

// OIDC returns the verifier for an `oidc` provider row.
func (g *GuardedClients) OIDC(p Provider) (*oidc.Verifier, error) {
	if p.Kind != KindOIDC {
		return nil, fmt.Errorf("provider %q is %s, not oidc", p.Key, p.Kind)
	}
	fingerprint := fmt.Sprintf("%s|%s|%s|%s|%s|%s",
		p.Issuer, p.ClientID, p.JWKSURI, p.SubjectClaim, p.TokenEndpoint, p.RedirectURI)

	g.mu.Lock()
	defer g.mu.Unlock()
	if got, ok := g.oidcs[p.ID]; ok && got.fingerprint == fingerprint {
		return got.value, nil
	}

	cfg := oidc.Config{
		Issuer:                p.Issuer,
		ClientID:              p.ClientID,
		ClientSecret:          p.ClientSecret,
		JWKSURI:               p.JWKSURI,
		AuthorizationEndpoint: p.AuthorizationEndpoint,
		TokenEndpoint:         p.TokenEndpoint,
		RedirectURI:           p.RedirectURI,
		SubjectClaim:          p.SubjectClaim,
	}
	// The allowlist is the provider row's OWN endpoints and nothing else. An issuer whose JWKS
	// document tries to walk the fetch onto another host does not get a second host to walk to.
	hosts, err := cfg.Hosts()
	if err != nil {
		return nil, fmt.Errorf("build oidc verifier for %q: %w", p.Key, err)
	}
	doer, err := outbound.New(outbound.Policy{AllowHosts: hosts})
	if err != nil {
		return nil, fmt.Errorf("build oidc verifier for %q: %w", p.Key, err)
	}
	verifier, err := oidc.NewVerifier(doer, g.clock, cfg)
	if err != nil {
		return nil, fmt.Errorf("build oidc verifier for %q: %w", p.Key, err)
	}
	g.oidcs[p.ID] = cached[*oidc.Verifier]{fingerprint: fingerprint, value: verifier}
	return verifier, nil
}

var _ Clients = (*GuardedClients)(nil)

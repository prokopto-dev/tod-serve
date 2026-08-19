package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"

	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/identity/identitysql"
	"github.com/prokopto-dev/tod-serve/internal/invite"
	"github.com/prokopto-dev/tod-serve/internal/membership"
	"github.com/prokopto-dev/tod-serve/internal/store"
)

// services is everything the binary wires. It exists so that the wiring is one function a test can
// call, rather than a sequence repeated in every verb.
type services struct {
	store    *store.DB
	clock    clock.Clock
	ids      *core.Generator
	log      *slog.Logger
	minter   *auth.Minter
	codec    *auth.SessionCodec
	authn    *auth.Authenticator
	identity *identity.Service
	circles  *circle.Service
	invites  *invite.Service
	members  *membership.Service
}

// identityConfig builds the configuration `identity.New` is given.
//
// **This is the wiring handoff the identity subsystem named as a residual risk.**
// `identity.New` takes an injected entropy source and returns an error on a nil one rather than
// falling back to a default, which makes "a generator that quietly reaches for a weak source" a
// construction error instead of a review habit. The OAuth `state` is 32 bytes drawn from it, and
// the callback's whole resistance to brute force rests on that, because the callback carries no
// rate-limit bucket of its own.
//
// But the absence of a default only makes the choice deliberate HERE. Nothing in the type system
// forces this line to say `rand.Reader`, so `RAND001` in internal/repogate does: it parses this
// file and requires every `Entropy` field and every named entropy sink to be exactly the `Reader`
// of a `crypto/rand` import — not merely non-nil, and not a variable that happens to hold it.
// TestWiring_IdentityService_IsGivenCryptoRandReader asserts the same thing at run time, by
// identity rather than by shape.
//
// It is a named function rather than an inline literal for exactly that reason: a gate needs
// something to point at.
func identityConfig(
	st identity.Store, clients identity.Clients, clk clock.Clock, ids *core.Generator,
	spaJoinURL string, log *slog.Logger,
) identity.Config {
	return identity.Config{
		Store:      st,
		Clients:    clients,
		Clock:      clk,
		IDs:        ids,
		Entropy:    rand.Reader,
		SPAJoinURL: spaJoinURL,
		Logger:     log,
	}
}

// dataServices wires the half of the domain that needs no credential material: circles, invites,
// and the store under them. `init`, `circle create` and `doctor` use it.
func dataServices(db *store.DB, clk clock.Clock, ids *core.Generator, log *slog.Logger) (
	*circle.Service, *invite.Service, error,
) {
	circles, err := circle.New(circle.Config{Store: db, Clock: clk, IDs: ids, Log: log})
	if err != nil {
		return nil, nil, err
	}
	invites, err := invite.New(invite.Config{
		Store: db, Clock: clk, IDs: ids, Entropy: rand.Reader, Log: log,
	})
	if err != nil {
		return nil, nil, err
	}
	return circles, invites, nil
}

// wire builds every service the API needs.
//
// The invite-code hash is handed to `identitysql.New` from internal/invite rather than defined
// there, and that direction is the point: whichever package MINTS codes owns hashing them.
// internal/identity never hashes a code — its port takes both the code and the hash — so there is
// exactly one spelling of that hash in the process. Two spellings would let the OAuth flow resolve
// one invite and redemption resolve another, and the failure would look like an expired invite
// rather than like a bug.
func wire(
	ctx context.Context, db *store.DB, log *slog.Logger, pepper, sessionKey core.Secret,
) (*services, error) {
	clk := clock.System{}
	ids := core.NewGenerator(rand.Reader)

	minter, err := auth.NewMinter(pepper, rand.Reader)
	if err != nil {
		return nil, err
	}
	codec, err := auth.NewSessionCodec(sessionKey)
	if err != nil {
		return nil, err
	}
	authn, err := auth.NewAuthenticator(db, minter, codec, clk, log, auth.DefaultStepUpWindow)
	if err != nil {
		return nil, err
	}

	identityStore, err := identitysql.New(db.Queries(), clk, invite.HashCode)
	if err != nil {
		return nil, err
	}
	clients, err := identity.NewGuardedClients(clk)
	if err != nil {
		return nil, err
	}
	spaJoinURL, err := spaJoinURL(ctx, db)
	if err != nil {
		return nil, err
	}
	identities, err := identity.New(
		identityConfig(identityStore, clients, clk, ids, spaJoinURL, log))
	if err != nil {
		return nil, err
	}

	circles, invites, err := dataServices(db, clk, ids, log)
	if err != nil {
		return nil, err
	}
	members, err := membership.New(membership.Config{
		Store: db, Clock: clk, IDs: ids, Minter: minter, Identity: identities,
		Log: log, Entropy: rand.Reader,
	})
	if err != nil {
		return nil, err
	}

	return &services{
		store: db, clock: clk, ids: ids, log: log, minter: minter, codec: codec,
		authn: authn, identity: identities, circles: circles, invites: invites, members: members,
	}, nil
}

// spaJoinURL decides where the OAuth callback sends a browser.
//
// Three sources, most specific first, and no invented default: a redirect target guessed by the
// server is a redirect somebody's browser follows. The error names all three rather than only the
// one that happened to be checked last.
func spaJoinURL(ctx context.Context, db *store.DB) (string, error) {
	if explicit := envOr(envSPAJoinURL, ""); explicit != "" {
		return explicit, nil
	}
	if public := envOr(envPublicURL, ""); public != "" {
		return joinPath(public), nil
	}
	row, err := db.Queries().GetInstance(ctx)
	if err == nil && strings.TrimSpace(row.PublicUrl) != "" {
		return joinPath(row.PublicUrl), nil
	}
	if err != nil && !store.IsNotFound(err) {
		return "", fmt.Errorf("read the instance row: %w", err)
	}
	return "", fmt.Errorf(
		"this instance has no public URL: run `tod-serve init --public-url …`, or set $%s or $%s",
		envPublicURL, envSPAJoinURL)
}

// joinPath appends the SPA's join route to a public URL, tolerating a trailing slash.
func joinPath(public string) string {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(public), "/"))
	if err != nil {
		// Left for identity.New to refuse with the URL in the message, which is more useful than
		// a parse error from here that does not say what it was parsing for.
		return public
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/join"
	return u.String()
}

// The compiler holds crypto/rand to the interface every entropy sink takes, so changing one of
// those fields to a concrete type is a build failure here rather than a weak generator in
// production.
var _ io.Reader = rand.Reader

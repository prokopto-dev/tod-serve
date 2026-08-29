package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/identity/identitysql"
	"github.com/prokopto-dev/tod-serve/internal/instancegrant"
	"github.com/prokopto-dev/tod-serve/internal/invite"
	"github.com/prokopto-dev/tod-serve/internal/membership"
	"github.com/prokopto-dev/tod-serve/internal/projection"
	"github.com/prokopto-dev/tod-serve/internal/setup"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/tod"
)

// services is everything the binary wires. It exists so that the wiring is one function a test can
// call, rather than a sequence repeated in every verb.
type services struct {
	store     *store.DB
	clock     clock.Clock
	ids       *core.Generator
	log       *slog.Logger
	minter    *auth.Minter
	codec     *auth.SessionCodec
	authn     *auth.Authenticator
	grants    *instancegrant.Service
	identity  *identity.Service
	circles   *circle.Service
	invites   *invite.Service
	members   *membership.Service
	catalogue *catalogue.Service
	tods      *tod.Service
	states    *projection.Service
	setup     *setup.Service
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
	spaJoinURL, callbackBaseURL string, log *slog.Logger,
) identity.Config {
	return identity.Config{
		Store:           st,
		Clients:         clients,
		Clock:           clk,
		IDs:             ids,
		Entropy:         rand.Reader,
		SPAJoinURL:      spaJoinURL,
		CallbackBaseURL: callbackBaseURL,
		Logger:          log,
	}
}

// dataServices wires the half of the domain that needs no credential material: circles, invites,
// and the store under them. `init`, `circle create` and `doctor` use it.
func dataServices(db *store.DB, clk clock.Clock, ids *core.Generator, log *slog.Logger) (
	*circle.Service, *invite.Service, *catalogue.Service, error,
) {
	circles, err := circle.New(circle.Config{Store: db, Clock: clk, IDs: ids, Log: log})
	if err != nil {
		return nil, nil, nil, err
	}
	invites, err := invite.New(invite.Config{
		Store: db, Clock: clk, IDs: ids, Entropy: rand.Reader, Log: log,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	// The catalogue takes no entropy source: it mints ULIDs through the shared generator and holds
	// no secret of its own. A raid target's id is not a credential.
	catalogues, err := catalogue.New(catalogue.Config{Store: db, Clock: clk, IDs: ids, Log: log})
	if err != nil {
		return nil, nil, nil, err
	}
	return circles, invites, catalogues, nil
}

// wire builds every service the API needs.
//
// The invite-code hash is handed to `identitysql.New` from internal/invite rather than defined
// there, and that direction is the point: whichever package MINTS codes owns hashing them.
// internal/identity never hashes a code — its port takes both the code and the hash — so there is
// exactly one spelling of that hash in the process. Two spellings would let the OAuth flow resolve
// one invite and redemption resolve another, and the failure would look like an expired invite
// rather than like a bug.
// public is the origin this instance is reachable at — `$TOD_PUBLIC_URL`. An EMPTY one is
// resolved from the environment and then from the instance row, which is what `serve` passes; a
// caller that already knows the answer — a test standing an instance up before any row exists —
// passes it, so this function reads no environment on its behalf.
//
// It is the ORIGIN rather than the join URL because two things hang off it and they are not the
// same string: where the callback sends a browser, and the callback's own URL, which ADR-0011
// makes a value the operator has to have registered with Discord exactly.
func wire(
	ctx context.Context, db *store.DB, log *slog.Logger, pepper, sessionKey core.Secret,
	public string,
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
	grants, err := instancegrant.New(instancegrant.Config{
		Store: db, Clock: clk, IDs: ids, Log: log,
	})
	if err != nil {
		return nil, err
	}
	authn, err := auth.NewAuthenticator(
		db, minter, codec, grants, clk, log, auth.DefaultStepUpWindow)
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
	if public == "" {
		if public, err = publicURL(ctx, db); err != nil {
			return nil, err
		}
	}
	spaJoin := spaJoinURL(public)
	// Derived from the route registry rather than spelled here, so the string an operator
	// registers with Discord and the path this binary actually serves cannot drift apart.
	callbackBase, err := api.CallbackBaseURL(public)
	if err != nil {
		return nil, err
	}
	identities, err := identity.New(
		identityConfig(identityStore, clients, clk, ids, spaJoin, callbackBase, log))
	if err != nil {
		return nil, err
	}

	circles, invites, catalogues, err := dataServices(db, clk, ids, log)
	if err != nil {
		return nil, err
	}
	members, err := membership.New(membership.Config{
		Store: db, Clock: clk, IDs: ids, Minter: minter, Identity: identities,
		// The ledger, so that redeeming the bootstrap owner code on an instance nobody
		// administers grants that identity `instance.owner` in the join's own transaction —
		// ADR-0016. It is the same service the authenticator reads grants from.
		Grants: grants,
		Log:    log, Entropy: rand.Reader,
	})
	if err != nil {
		return nil, err
	}
	tods, states, err := todServices(db, clk, ids, catalogues, log)
	if err != nil {
		return nil, err
	}
	// First-run setup composes the services above rather than reaching past them, which is what
	// makes the wizard and `tod-serve init` write the same rows through the same validation —
	// ADR-0016. It holds no credential material of its own: what authorises it is
	// `TOD_SETUP_TOKEN` at the edge, and the absence of an administrator in the ledger.
	first, err := setup.New(setup.Config{
		Store: db, Circles: circles, Invites: invites, Identities: identities,
		Catalogue: catalogues, Clock: clk, Log: log,
	})
	if err != nil {
		return nil, err
	}

	return &services{
		store: db, clock: clk, ids: ids, log: log, minter: minter, codec: codec,
		authn: authn, grants: grants, identity: identities, circles: circles, invites: invites,
		members: members, catalogue: catalogues, tods: tods, states: states, setup: first,
	}, nil
}

// todServices wires the report log and the projection over it, both onto the catalogue that is
// already built.
//
// It takes that catalogue rather than building one, and the direction is the point: the ingest path
// asks it which target a name means, the projection asks it for the EFFECTIVE timer — circle
// override, then catalogue, then unknown — and nothing asks the projection anything. A second
// catalogue here would be a second resolve ladder and a second timer precedence, and the failure
// mode of the latter is a circle override that silently stops working while the board goes on
// looking authoritative.
func todServices(
	db *store.DB, clk clock.Clock, ids *core.Generator, catalogues *catalogue.Service,
	log *slog.Logger,
) (*tod.Service, *projection.Service, error) {
	tods, err := tod.New(tod.Config{
		Store: db, Clock: clk, IDs: ids, Catalogue: catalogues, Log: log,
	})
	if err != nil {
		return nil, nil, err
	}
	states, err := projection.New(projection.Config{
		Store: db, Clock: clk, Catalogue: catalogues, Log: log,
	})
	if err != nil {
		return nil, nil, err
	}
	return tods, states, nil
}

// publicURL is the origin this instance is reachable at, and the one fact both the join redirect
// and the OAuth callback URL are derived from.
//
// Two sources, most specific first, and no invented default: an origin guessed by the server is an
// origin somebody's browser is redirected to, and — since ADR-0011 makes the callback URL a string
// the operator must have registered with Discord character for character — a guess here is a
// sign-in that lands nowhere.
//
// `$TOD_SPA_JOIN_URL` is deliberately NOT a source. It moves the console, which may legitimately
// sit on another origin; it does not move the API, and the redirect URI belongs to the API.
func publicURL(ctx context.Context, db *store.DB) (string, error) {
	if public := envOr(envPublicURL, ""); public != "" {
		return strings.TrimRight(strings.TrimSpace(public), "/"), nil
	}
	row, err := db.Queries().GetInstance(ctx)
	if err == nil && strings.TrimSpace(row.PublicUrl) != "" {
		return strings.TrimRight(strings.TrimSpace(row.PublicUrl), "/"), nil
	}
	if err != nil && !store.IsNotFound(err) {
		return "", fmt.Errorf("read the instance row: %w", err)
	}
	// Named in the order an operator should try them. `$TOD_PUBLIC_URL` is FIRST because it is what
	// the shipped compose files set and what the first-run wizard prefills its form from: an
	// instance meant to be set up in the browser has to boot before there is any row to read one
	// from, so this is part of the `.env` step rather than something setup can supply. `init`
	// remains listed because it is still the way back when nobody can sign in.
	return "", fmt.Errorf(
		"this instance has no public URL: set $%s before starting, "+
			"or run `tod-serve init --public-url …`",
		envPublicURL)
}

// spaJoinURL decides where the OAuth callback sends a browser once it holds a ticket.
//
// `$TOD_SPA_JOIN_URL` overrides it outright, for the deployment that serves the console from
// somewhere other than the API.
func spaJoinURL(public string) string {
	if explicit := envOr(envSPAJoinURL, ""); explicit != "" {
		return explicit
	}
	return joinPath(public)
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

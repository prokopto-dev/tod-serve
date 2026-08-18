package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity/discord"
	"github.com/prokopto-dev/tod-serve/internal/identity/oidc"
)

// AuthorizationRequest is `createAuthorizationURL`.
//
// It takes NO circle id, and that absence is the design. A public, pre-authentication route that
// answers differently for a real circle than an unknown one is a circle-existence oracle — and it
// would be one through the scope set alone, since `guilds.members.read` appears only for a gated
// circle. A circle here comes from a SECRET the caller was given (the invite code) or not at all.
//
// The shared rate limit both invite-code routes draw on lives at the edge, in front of this call:
// a rejected probe must create no `auth_flow` row, and the only way to guarantee that is to not
// reach this function.
type AuthorizationRequest struct {
	ProviderKey string
	InviteCode  string
}

// Authorization is what the browser is sent to, and when it stops working.
type Authorization struct {
	URL       string
	ExpiresAt core.Micros

	// State is returned for the audit log. It is not a credential — it is a CSRF nonce whose only
	// meaning is a row in `auth_flow` — but it is unguessable, so it is not logged by default.
	State string
}

// CreateAuthorizationURL starts a browser OAuth flow and records it.
//
// The PKCE verifier is minted here and stays SERVER-side: the browser never holds it, which is
// what makes an intercepted `code` useless to whoever intercepted it.
func (s *Service) CreateAuthorizationURL(ctx context.Context, req AuthorizationRequest) (Authorization, error) {
	provider, err := s.store.ProviderByKey(ctx, req.ProviderKey)
	if errors.Is(err, ErrNotFound) {
		// Providers are public — `listIdentityProviders` returns them before authentication — so
		// naming an unknown one reveals nothing that endpoint does not.
		return Authorization{}, NewValidationError("body.provider",
			fmt.Sprintf("no provider %q on this instance", req.ProviderKey))
	}
	if err != nil {
		return Authorization{}, fmt.Errorf("read provider %q: %w", req.ProviderKey, err)
	}
	// Validated BEFORE the row is used for anything, exactly as [Service.Verify] does.
	//
	// This is load-bearing here in a way it is not elsewhere, and the reason is worth stating:
	// `authorization_endpoint` is the ONE operator-supplied OIDC URL that never goes through
	// internal/identity/outbound. The browser goes there; this instance does not. So the guarded
	// client's https check, its allowlist and its deny list never see it, and
	// [Provider.Validate]'s scheme check is the only thing standing between a mistyped `http://`
	// issuer and a redirect that carries the OAuth `state` over plaintext.
	//
	// Every other URL on the row fails closed without this — the guarded client refuses a
	// non-https fetch — which is precisely why skipping the check here was survivable everywhere
	// except the one place it was not.
	if err := provider.Validate(); err != nil {
		// Not a coded error: an inconsistent provider row is this instance's misconfiguration,
		// and presenting it as the caller's fault sends somebody looking in the wrong place.
		return Authorization{}, fmt.Errorf("start authorization for %q: %w", req.ProviderKey, err)
	}
	if !provider.Enabled {
		return Authorization{}, NewError(CodeProviderDisabled,
			fmt.Sprintf("this instance has disabled the %q provider", provider.Key), nil)
	}
	if !provider.SupportsBrowserFlow() {
		return Authorization{}, NewValidationError("body.provider",
			fmt.Sprintf("provider %q has no browser flow", provider.Key))
	}

	// The circle, if any, comes from the invite and only from the invite.
	var (
		inviteHash []byte
		circleID   string
		guildGated bool
	)
	if req.InviteCode != "" {
		invite, err := s.store.InviteByCode(ctx, req.InviteCode)
		switch {
		case errors.Is(err, ErrNotFound):
			return Authorization{}, NewError(CodeInviteInvalid, "no such invite", nil)
		case err != nil:
			return Authorization{}, fmt.Errorf("read invite: %w", err)
		case !invite.Live:
			return Authorization{}, NewError(invite.DeadCode, "this invite can no longer be redeemed", nil)
		}
		inviteHash, circleID = invite.CodeHash, invite.CircleID

		gate, err := s.store.GuildGate(ctx, invite.CircleID, provider.ID)
		switch {
		case errors.Is(err, ErrNotFound):
			// The circle does not accept this provider. Saying so is within `previewInvite`'s
			// disclosure, which already lists a circle's accepted providers to a code holder.
			return Authorization{}, NewError(CodeProviderNotAccepted,
				fmt.Sprintf("this invite's circle does not accept %q", provider.Key), nil)
		case err != nil:
			return Authorization{}, fmt.Errorf("read circle provider: %w", err)
		}
		guildGated = !gate.IsZero()
	} else {
		// With no invite there is no circle to resolve, so the scope decision falls back to an
		// INSTANCE-level fact that names no particular circle. The callback picks the actual
		// guilds once step 3 has established who is asking.
		guildGated, err = s.store.AnyCircleGatesOnAGuild(ctx)
		if err != nil {
			return Authorization{}, fmt.Errorf("read whether any circle gates on a guild: %w", err)
		}
	}

	state, err := s.mintSecret()
	if err != nil {
		return Authorization{}, fmt.Errorf("mint authorization state: %w", err)
	}
	verifier, err := s.mintSecret()
	if err != nil {
		return Authorization{}, fmt.Errorf("mint pkce verifier: %w", err)
	}
	now := s.clock.Now()
	flowID, err := core.NewID[core.AuthFlow](s.ids, now)
	if err != nil {
		return Authorization{}, fmt.Errorf("mint auth flow id: %w", err)
	}

	flow := AuthFlow{
		ID:             flowID.String(),
		State:          state,
		PKCEVerifier:   verifier,
		ProviderID:     provider.ID,
		InviteCodeHash: inviteHash,
		CircleID:       circleID,
		ExpiresAt:      now.Add(AuthFlowTTL),
		CreatedAt:      now,
	}
	if err := s.store.CreateAuthFlow(ctx, flow); err != nil {
		return Authorization{}, fmt.Errorf("record auth flow: %w", err)
	}

	authorizeURL, err := s.authorizationURL(provider, state, verifier, guildGated)
	if err != nil {
		return Authorization{}, err
	}
	return Authorization{URL: authorizeURL, ExpiresAt: flow.ExpiresAt, State: state}, nil
}

func (s *Service) authorizationURL(p Provider, state, verifier string, guildGated bool) (string, error) {
	switch p.Kind {
	case KindDiscord:
		client, err := s.clients.Discord(p)
		if err != nil {
			return "", fmt.Errorf("build discord authorization url: %w", err)
		}
		return client.AuthorizationURL(state, verifier, discord.Scopes(guildGated)), nil
	case KindOIDC:
		verifierClient, err := s.clients.OIDC(p)
		if err != nil {
			return "", fmt.Errorf("build oidc authorization url: %w", err)
		}
		return verifierClient.AuthorizationURL(state, verifier, oidc.Scopes()), nil
	default:
		return "", fmt.Errorf("provider %q has no browser flow", p.Key)
	}
}

// CallbackRequest is `completeAuthorization`, the OAuth redirect target.
//
// Its own query string carries `code` and `state`, and neither is a credential for this API:
// `code` is single-use, PKCE-bound and exchanged server-side inside this call, and `state` is a
// CSRF nonce whose only meaning is a row in `auth_flow`. That is why the no-token-in-a-URL rule
// is not violated by the route that then goes on to enforce it for the ticket.
type CallbackRequest struct {
	ProviderKey string
	State       string
	Code        string

	// ProviderError is the `error` parameter, present when the provider is reporting a refusal
	// rather than returning a code — `access_denied` when somebody clicks Cancel.
	ProviderError string
}

// Callback is where to send the browser. Location is populated on success AND on failure: there
// is one rule for the redirect rather than one per outcome.
type Callback struct {
	Location string

	// Code is the error carried in the fragment, empty on success. It is here so the caller can
	// audit-log the outcome without re-parsing the URL it is about to send.
	Code Code
}

// CompleteAuthorization finishes a browser OAuth flow.
//
// It always returns a Callback whose Location is safe to redirect to. The error it returns
// alongside is for the log and the audit trail: the browser gets `#error=<code>`, and the
// operator gets the reason.
//
// The access token obtained here is DISCARDED inside this function. It is never returned, never
// stored, and never passed to a port — `TestDiscord_AccessToken_NeverPersisted` asserts the last
// of those over a recording fake, because it is the one that would be a database column if
// somebody got it wrong.
func (s *Service) CompleteAuthorization(ctx context.Context, req CallbackRequest) (Callback, error) {
	ticket, err := s.completeAuthorization(ctx, req)
	if err != nil {
		code, ok := CodeOf(err)
		if !ok {
			// An uncoded failure is this instance's bug. The browser is still told something —
			// leaving it on a blank callback page is worse — and the detail stays in the log.
			code = CodeCredentialInvalid
			s.log.ErrorContext(ctx, "authorization callback failed",
				slog.String("provider", req.ProviderKey), slog.Any("error", err))
		}
		return Callback{Location: s.redirect("error", string(code)), Code: code}, err
	}
	return Callback{Location: s.redirect("ticket", ticket)}, nil
}

// completeAuthorization is the flow itself, returning the ticket to hand back.
func (s *Service) completeAuthorization(ctx context.Context, req CallbackRequest) (string, error) {
	now := s.clock.Now()

	if req.State == "" {
		return "", NewError(CodeAuthFlowExpired, "this callback carries no state", nil)
	}
	// Consumed before anything else is done with it: a state is single-use, and a replayed
	// callback must not get a second exchange even if the first one failed later on.
	flow, err := s.store.ConsumeAuthFlow(ctx, req.State, now)
	if errors.Is(err, ErrNotFound) {
		return "", NewError(CodeAuthFlowExpired, "this authorization has expired or was already completed", nil)
	}
	if err != nil {
		return "", fmt.Errorf("consume auth flow: %w", err)
	}
	if !now.Before(flow.ExpiresAt) {
		return "", NewError(CodeAuthFlowExpired,
			fmt.Sprintf("an authorization is valid for %s and this one is older", AuthFlowTTL), nil)
	}

	provider, err := s.store.ProviderByID(ctx, flow.ProviderID)
	if errors.Is(err, ErrNotFound) {
		return "", NewError(CodeProviderDisabled, "the provider this authorization began with is gone", nil)
	}
	if err != nil {
		return "", fmt.Errorf("read provider: %w", err)
	}
	// The same check on the way back. The callback only FETCHES this row's endpoints, so the
	// guarded client would refuse a non-https one anyway — but as a request that failed rather
	// than as a row somebody can see is wrong, and a rule with a carve-out is a rule somebody
	// implements on the wrong side.
	if err := provider.Validate(); err != nil {
		return "", fmt.Errorf("complete authorization for %q: %w", req.ProviderKey, err)
	}
	if provider.Key != req.ProviderKey {
		// The callback path names a provider and the flow records one. A mismatch is a crafted
		// callback, not a user error.
		return "", NewError(CodeCredentialInvalid, "this callback does not match the authorization it claims", nil)
	}
	if !provider.Enabled {
		return "", NewError(CodeProviderDisabled,
			fmt.Sprintf("this instance has disabled the %q provider", provider.Key), nil)
	}

	if req.ProviderError != "" {
		return "", providerRefusal(req.ProviderError)
	}
	if req.Code == "" {
		return "", NewError(CodeCredentialInvalid, "this callback carries neither a code nor an error", nil)
	}

	verified, err := s.exchangeAndRead(ctx, provider, flow, req.Code)
	if err != nil {
		return "", err
	}

	// The invite is re-read HERE, immediately before minting, rather than only where the guilds
	// were chosen. A user can sit on a consent screen for minutes, and this is an early-out so
	// the server does not mint a credential for an invite that is already dead. It is not the
	// gate: `/join` re-checks at redemption and is what actually decides. Anything else would
	// make a 120-second-old snapshot authoritative over the live row.
	if len(flow.InviteCodeHash) > 0 {
		invite, err := s.store.InviteByCodeHash(ctx, flow.InviteCodeHash)
		switch {
		case errors.Is(err, ErrNotFound):
			return "", NewError(CodeInviteInvalid, "this invite no longer exists", nil)
		case err != nil:
			return "", fmt.Errorf("re-read invite: %w", err)
		case !invite.Live:
			return "", NewError(invite.DeadCode, "this invite can no longer be redeemed", nil)
		}
	}

	return s.mintTicket(ctx, provider, verified)
}

// exchangeAndRead performs the code exchange and reads the provider's facts. The access token
// lives entirely inside this function.
func (s *Service) exchangeAndRead(ctx context.Context, p Provider, flow AuthFlow, code string) (Verified, error) {
	switch p.Kind {
	case KindDiscord:
		client, err := s.clients.Discord(p)
		if err != nil {
			return Verified{}, fmt.Errorf("build discord client: %w", err)
		}
		token, err := client.Exchange(ctx, code, flow.PKCEVerifier)
		if err != nil {
			return Verified{}, mapDiscordError(err)
		}

		// Step 2 and 3: the audience check, then who this is. The identity is known from here on,
		// which is what makes step 4's guild lookup a lookup on something the caller PROVED.
		facts, err := client.Identify(ctx, token)
		if err != nil {
			return Verified{}, mapDiscordError(err)
		}
		if err := s.refuseBlocked(ctx, p.ID, facts.Subject); err != nil {
			return Verified{}, err
		}

		guildIDs, err := s.guildsToAsk(ctx, p, flow, facts.Subject)
		if err != nil {
			return Verified{}, err
		}
		if err := client.AddGuildFacts(ctx, token, &facts, guildIDs); err != nil {
			return Verified{}, mapDiscordError(err)
		}
		// The token goes out of scope here. Nothing below this line has it.
		return Verified{
			ProviderID:  p.ID,
			Subject:     facts.Subject,
			DisplayName: facts.DisplayName,
			GuildFacts:  facts.Guilds,
		}, nil

	case KindOIDC:
		verifier, err := s.clients.OIDC(p)
		if err != nil {
			return Verified{}, fmt.Errorf("build oidc verifier: %w", err)
		}
		idToken, err := verifier.Exchange(ctx, code, flow.PKCEVerifier)
		if err != nil {
			return Verified{}, mapOIDCError(err)
		}
		// The nonce is derived from the stored verifier rather than kept in a column of its own;
		// see oidc.NonceFor for why that is the same binding.
		got, err := verifier.Verify(ctx, idToken, oidc.NonceFor(flow.PKCEVerifier))
		if err != nil {
			return Verified{}, mapOIDCError(err)
		}
		if err := s.refuseBlocked(ctx, p.ID, got.Subject); err != nil {
			return Verified{}, err
		}
		return Verified{
			ProviderID:  p.ID,
			Subject:     got.Subject,
			DisplayName: got.DisplayName,
			GuildFacts:  GuildFacts{},
		}, nil

	default:
		return Verified{}, fmt.Errorf("provider %q has no browser flow", p.Key)
	}
}

// guildsToAsk decides which guilds need a member object.
//
// With an invite it is that invite's circle, if it gates. Without one it is the circles THIS
// IDENTITY already has a membership in — a lookup keyed on the subject established one step
// earlier. Either way the set comes from a secret or from a verified identity, never from a
// caller-supplied id, so there is nothing here to enumerate.
func (s *Service) guildsToAsk(ctx context.Context, p Provider, flow AuthFlow, subject string) ([]string, error) {
	var circleIDs []string
	if len(flow.InviteCodeHash) > 0 {
		invite, err := s.store.InviteByCodeHash(ctx, flow.InviteCodeHash)
		if errors.Is(err, ErrNotFound) {
			return nil, NewError(CodeInviteInvalid, "this invite no longer exists", nil)
		}
		if err != nil {
			return nil, fmt.Errorf("read invite: %w", err)
		}
		circleIDs = []string{invite.CircleID}
	} else {
		stored, err := s.store.IdentityBySubject(ctx, p.ID, subject)
		if errors.Is(err, ErrNotFound) {
			return nil, nil // A subject with no identity here belongs to no circle here.
		}
		if err != nil {
			return nil, fmt.Errorf("read identity: %w", err)
		}
		circleIDs, err = s.store.CircleIDsForIdentity(ctx, stored.ID)
		if err != nil {
			return nil, fmt.Errorf("read circles for identity: %w", err)
		}
	}

	var guildIDs []string
	for _, circleID := range circleIDs {
		gate, err := s.store.GuildGate(ctx, circleID, p.ID)
		if errors.Is(err, ErrNotFound) {
			continue // That circle does not accept this provider, so it asks nothing of it.
		}
		if err != nil {
			return nil, fmt.Errorf("read circle provider: %w", err)
		}
		if !gate.IsZero() && !slices.Contains(guildIDs, gate.GuildID) {
			guildIDs = append(guildIDs, gate.GuildID)
		}
	}
	return guildIDs, nil
}

// mintTicket writes the single-use `credential_ticket` and returns the secret half.
func (s *Service) mintTicket(ctx context.Context, p Provider, v Verified) (string, error) {
	secret, err := s.mintSecret()
	if err != nil {
		return "", fmt.Errorf("mint credential ticket: %w", err)
	}
	now := s.clock.Now()
	id, err := core.NewID[core.CredentialTicket](s.ids, now)
	if err != nil {
		return "", fmt.Errorf("mint credential ticket id: %w", err)
	}

	ticket := Ticket{
		ID:          id.String(),
		Hash:        HashTicket(secret),
		ProviderID:  p.ID,
		Subject:     v.Subject,
		DisplayName: v.DisplayName,
		GuildFacts:  v.GuildFacts,
		// Exactly the TTL the schema's CHECK requires. A different arithmetic here is a write
		// that fails, not a ticket that lives longer.
		ExpiresAt: now.Add(TicketTTL),
		CreatedAt: now,
	}
	if err := s.store.CreateTicket(ctx, ticket); err != nil {
		return "", fmt.Errorf("record credential ticket: %w", err)
	}
	return secret, nil
}

// redirect builds `<spa>/join#<key>=<value>`.
//
// The FRAGMENT, never the query, and this is the one place the rule is expressed so there is one
// place to check it. A query string lands in access logs, `Referer` headers and proxy logs, and a
// `provider_ticket` is a bearer credential that mints a PAT. Failures use the same fragment for
// the same reason a success does: one rule for the redirect rather than one per outcome, because
// the outcome somebody forgets to apply it to is the one that matters.
func (s *Service) redirect(key, value string) string {
	out := *s.spaJoin
	out.Fragment = key + "=" + value
	return out.String()
}

// providerRefusal maps the provider's own `error` parameter.
//
// `access_denied` is somebody clicking Cancel or unticking a permission on the consent screen,
// which is a declined scope and is reported as one. Anything else is a provider-side failure this
// instance cannot interpret, and guessing at it would be a confident mistake.
func providerRefusal(providerError string) error {
	if providerError == "access_denied" {
		return NewError(CodeProviderScopeDeclined, "the authorization was declined at the provider", nil)
	}
	return NewError(CodeCredentialInvalid, "the provider refused this authorization", nil)
}

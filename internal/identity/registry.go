package identity

import (
	"context"
	"errors"
	"fmt"

	"github.com/prokopto-dev/tod-serve/internal/identity/local"
)

// EnableProvider turns a provider on.
//
// This is `instance.security.manage` work — session-only, step-up, PAT-forbidden — and the
// PAT-forbidden half is the point rather than an inconvenience: it is precisely what stops a
// leaked token adding a malicious OIDC issuer and pivoting through the SSRF surface that an
// operator-supplied `jwks_uri` opens. That gate lives at the edge, in the middleware; what lives
// here is the acknowledgement.
//
// Enabling an UNVERIFIABLE provider requires `acknowledge_weak_revocation: true`, or
// `422 acknowledgement_required`. The failure mode it exists for: an officer revokes a leaker, the
// leaker redeems another invite as "Tanky", and is reading the same ToDs a minute later — while
// the officers believe the problem is handled. **The false confidence is the damage**, not the
// re-entry, and a checkbox is the only thing that reliably reaches the person clicking Enable.
func (s *Service) EnableProvider(ctx context.Context, key string, acknowledgeWeakRevocation bool) (Provider, error) {
	provider, err := s.store.ProviderByKey(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return Provider{}, NewValidationError("body.provider",
			fmt.Sprintf("no provider %q on this instance", key))
	}
	if err != nil {
		return Provider{}, fmt.Errorf("read provider %q: %w", key, err)
	}
	if err := provider.Validate(); err != nil {
		return Provider{}, fmt.Errorf("enable provider %q: %w", key, err)
	}

	if !provider.VerifiableSubject && !acknowledgeWeakRevocation {
		return Provider{}, NewError(CodeAcknowledgementRequired,
			fmt.Sprintf("%q has no verifiable subject, so revoking a member who joined through it "+
				"does not stop them rejoining; set acknowledge_weak_revocation to enable it anyway",
				provider.Key), nil)
	}

	updated, err := s.store.SetProviderEnabled(ctx, provider.ID, true, s.clock.Now())
	if err != nil {
		return Provider{}, fmt.Errorf("enable provider %q: %w", key, err)
	}
	return updated, nil
}

// DisableProvider turns a provider off. No acknowledgement: turning a way in OFF is the safe
// direction, and asking for one would train people to click through the acknowledgement that
// matters.
//
// Disabling stops NEW joins. It revokes no existing membership — the alternative is a footgun that
// eventually deletes a guild's whole roster with one click.
func (s *Service) DisableProvider(ctx context.Context, key string) (Provider, error) {
	provider, err := s.store.ProviderByKey(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return Provider{}, NewValidationError("body.provider",
			fmt.Sprintf("no provider %q on this instance", key))
	}
	if err != nil {
		return Provider{}, fmt.Errorf("read provider %q: %w", key, err)
	}
	updated, err := s.store.SetProviderEnabled(ctx, provider.ID, false, s.clock.Now())
	if err != nil {
		return Provider{}, fmt.Errorf("disable provider %q: %w", key, err)
	}
	return updated, nil
}

// AutoAccepted is what a NEW circle accepts: every enabled provider with a verifiable subject.
//
// **`local` is never auto-added.** An owner must reach for it, from inside their own circle,
// having seen the `weak` field the invite preview already shows. A new circle that silently
// accepted the unverifiable provider would be a circle whose revocation is advisory before anybody
// chose that.
//
// It is a function here rather than a rule written into circle creation so that the two places
// that need it — creating a circle, and telling an owner what they would get — cannot disagree.
func AutoAccepted(enabled []Provider) []Provider {
	out := make([]Provider, 0, len(enabled))
	for _, p := range enabled {
		if p.Enabled && p.VerifiableSubject {
			out = append(out, p)
		}
	}
	return out
}

// InviteMaxUsesCeiling is the largest `max_uses` an invite minted for this provider may carry, or
// zero when the provider imposes none.
//
// `local` forces one. A `local` identity has no credential to re-present, so `POST /sessions`
// cannot work for it and every lost token becomes a new invite; invite hygiene degrades from there
// until somebody leaves a 30-day, 50-use invite lying around — the same hole the weak revocation
// opens, from the other side.
//
// Whoever mints invites applies this. It is here because the reason is here.
func InviteMaxUsesCeiling(p Provider) int {
	if p.Kind == KindLocal {
		return local.MaxInviteUses
	}
	return 0
}

// CanLink reports whether two identities may be joined by an `identity_link`.
//
// **Both participants must have `verifiable_subject = 1`**, so a `local` identity can never be
// linked. Silently unifying an unverified identity with a verified one is precisely the hole: it
// would let anyone who can assert a display name inherit, or resurrect, another person's standing.
//
// The database refuses it too — `trg_identity_link_requires_verifiable_participants` counts
// verifiable participants and aborts on anything but two, which is also how a link naming an
// identity that does not exist fails. This is the same rule at the edge, so the answer is a
// `422 link_requires_verifiable_identity` rather than a constraint violation surfacing as a 500.
func CanLink(primary, linked Provider) error {
	for _, p := range []Provider{primary, linked} {
		if !p.VerifiableSubject {
			return NewError(CodeLinkRequiresVerifiableIdentity,
				fmt.Sprintf("%q has no verifiable subject, so an identity behind it cannot be linked", p.Key), nil)
		}
	}
	return nil
}

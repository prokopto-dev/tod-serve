package identity

import (
	"errors"
	"fmt"
	"strings"
)

// ErrRedirectURIMismatch is a provider row whose `redirect_uri` is not the URL this instance is
// actually reachable at.
//
// It is its own sentinel rather than part of [ErrProviderInconsistent] because it is a different
// kind of wrong. An inconsistent row contradicts ITSELF — an `oidc` row with no issuer — and is
// wrong on any instance. A mismatched redirect URI is a perfectly well-formed row that belongs to
// a DIFFERENT deployment: the one whose public URL it names. The commonest way to produce one is
// to move an instance to a new domain, which is exactly when nobody is expecting a login to break.
var ErrRedirectURIMismatch = errors.New("this provider's redirect_uri is not this instance's callback url")

// ExpectedRedirectURI is the one string that works, for one provider key.
//
// It is the string the operator must have registered with the provider AND the string that must be
// in `identity_provider.redirect_uri`, character for character, because Discord compares a redirect
// URI literally. Exported so the administrative API, `tod-serve doctor` and the console can all
// show the operator the value to paste rather than describing how to build it.
func (s *Service) ExpectedRedirectURI(providerKey string) string {
	return s.callbackBase + "/" + providerKey
}

// checkRedirectURI refuses a provider whose redirect URI points somewhere other than here.
//
// **This is the check that turns a sign-in which lands nowhere into a sentence.** Without it the
// two ways of getting this wrong both fail somewhere the operator is not looking:
//
//   - The row disagrees with what is registered at the provider. The user reaches Discord and is
//     shown `invalid_request` on Discord's own error page, before signing in — a message about our
//     configuration, rendered by somebody else, with our name nowhere on it.
//   - The row AGREES with what is registered, and both name a host this instance is no longer at.
//     Discord is happy, the user signs in, consents, and is redirected to a dead origin. Nothing
//     on this instance ever sees the callback, so nothing on this instance ever logs a failure.
//     That is the silent one, and moving a deployment to a new domain produces it every time.
//
// Comparison is exact, and deliberately so. Any normalisation here — a tolerated trailing slash, a
// case-folded path — is a difference between what this accepts and what the provider accepts, and
// the whole problem is that the provider does not normalise.
func (s *Service) checkRedirectURI(p Provider) error {
	if !p.SupportsBrowserFlow() {
		// `local` redirects nowhere because it goes nowhere.
		return nil
	}
	want := s.ExpectedRedirectURI(p.Key)
	if p.RedirectURI == want {
		return nil
	}
	if strings.TrimSpace(p.RedirectURI) == "" {
		return fmt.Errorf("provider %q has no redirect_uri; it must be exactly %q: %w",
			p.Key, want, ErrRedirectURIMismatch)
	}
	return fmt.Errorf("provider %q has redirect_uri %q, but this instance's callback is %q: %w",
		p.Key, p.RedirectURI, want, ErrRedirectURIMismatch)
}

// redirectURIValidationError is the configuration-time rendering: a 422 pointing at the field,
// naming the value to paste.
//
// Configuration time is where this SHOULD be caught, because it is the only moment at which the
// person reading the error is the person who can fix it. The flow-time check exists for the row
// that got in another way — a direct database edit, or a `$TOD_PUBLIC_URL` changed after the row
// was written, which is the same operation as moving the deployment.
func redirectURIValidationError(err error, want string) error {
	coded := NewError(CodeValidationFailed, fmt.Sprintf(
		"redirect_uri must be exactly %q — the URL this instance serves the OAuth callback at. "+
			"Register that same string with the provider: it is compared literally, so a "+
			"different host, scheme, port, path or trailing slash is a different URI", want), err)
	coded.Location = "body.redirect_uri"
	return coded
}

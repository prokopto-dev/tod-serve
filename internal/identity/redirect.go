package identity

import (
	"errors"
	"fmt"
	"net/url"
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
// in `identity_provider.redirect_uri`. Exported so the administrative API, `tod-serve doctor` and
// the console can all show the operator the value to paste rather than describing how to build it.
//
// It is returned in canonical form — see [CanonicalRedirectURI] — because it is a value somebody
// copies, and the copy that gets pasted into a developer portal should be the one that reads
// unambiguously.
func (s *Service) ExpectedRedirectURI(providerKey string) string {
	return CanonicalRedirectURI(s.callbackBase + "/" + providerKey)
}

// CanonicalRedirectURI reduces a redirect URI to the form two of them can be compared in.
//
// The line it draws is RFC 3986's, and drawing it anywhere else costs something real in one
// direction or the other:
//
//   - Scheme and host are CASE-INSENSITIVE, and `:443` on https is the default port. So
//     `https://TOD.example.com:443/api/v1/auth/callback/discord` and
//     `https://tod.example.com/api/v1/auth/callback/discord` are the same URI, and a check that
//     called them a mismatch would fire on a correct configuration — which is how a check gets
//     switched off.
//   - The PATH is case-SENSITIVE, and a trailing slash is part of it. `…/callback/discord` and
//     `…/callback/discord/` are different URIs to Discord, which compares them literally, so a
//     check that folded them would pass a configuration the provider then rejects. That is the
//     confident mistake: telling an operator their redirect URI is fine when the provider
//     disagrees sends them to look at everything else first.
//
// Query and fragment are kept as-is rather than dropped: this instance's callback carries neither,
// so a redirect URI that has one is not this instance's callback.
func CanonicalRedirectURI(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		// Unparseable is returned unchanged rather than repaired. It will not equal the expected
		// value, which is the right answer, and it reaches the operator as the string they
		// actually typed.
		return strings.TrimSpace(raw)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if port := u.Port(); (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		u.Host = u.Hostname()
	}
	return u.String()
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
// Comparison is [CanonicalRedirectURI]'s: the parts of a URI that are case-insensitive by
// specification are folded, and nothing else is. Folding more would accept configurations the
// provider then rejects; folding less would report a correct one as broken.
func (s *Service) checkRedirectURI(p Provider) error {
	if !p.SupportsBrowserFlow() {
		// `local` redirects nowhere because it goes nowhere.
		return nil
	}
	want := s.ExpectedRedirectURI(p.Key)
	if CanonicalRedirectURI(p.RedirectURI) == want {
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

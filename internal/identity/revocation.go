package identity

import (
	"sort"

	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// Strength is `circle.revocation_strength`, and the same enum answers the membership question.
type Strength string

const (
	// StrengthDurable means revoking sticks: every way in has a subject the server can verify.
	StrengthDurable Strength = schemaenum.CircleRevocationStrengthDurable

	// StrengthWeak means revocation is advisory. A revoked person holding any invite returns
	// under a new name — and the damage is not the re-entry, it is the officers' belief that
	// revocation worked.
	StrengthWeak Strength = schemaenum.CircleRevocationStrengthWeak
)

// WeakReasonUnverifiableProvider is the one reason there currently is. It is a machine-readable
// token rather than prose because a client has to be able to RENDER it: a paragraph in an
// operations guide does not reach the officer who is about to trust a revocation.
const WeakReasonUnverifiableProvider = "unverifiable_provider"

// RevocationStrength is the derived answer, with the evidence attached.
//
// Derived on read, never stored. Storing it lets it drift the moment a provider is added — and
// the drift would be in the safe-looking direction, because the stored value would still say
// `durable` while the new provider quietly made it false.
type RevocationStrength struct {
	Strength Strength `json:"revocation_strength"`

	// WeakReasons is empty when Strength is durable.
	WeakReasons []string `json:"revocation_weak_reasons"`

	// WeakProviders names the provider KEYS responsible, so a client can say which one rather
	// than only that there is one.
	WeakProviders []string `json:"weak_providers"`

	// DisabledProviders names accepted providers the instance has since disabled, which are
	// excluded from the calculation above because they admit nobody new.
	//
	// It is here because the filter would otherwise be invisible: "never hide a row silently — if
	// a filter drops something, count it somewhere visible". Whether it reaches the wire is the
	// API's decision; the derivation's job is not to lose it.
	DisabledProviders []string `json:"-"`
}

// CircleStrength answers "can we keep people out?" — the weakest over the providers this circle
// ACCEPTS that the instance still has ENABLED. Forward-looking: it is about who can still get in.
//
// A circle that accepts nothing is durable, vacuously and correctly: nobody can join it at all.
func CircleStrength(accepted []Provider) RevocationStrength {
	out := RevocationStrength{
		Strength:          StrengthDurable,
		WeakReasons:       []string{},
		WeakProviders:     []string{},
		DisabledProviders: []string{},
	}
	for _, p := range accepted {
		if !p.Enabled {
			out.DisabledProviders = append(out.DisabledProviders, p.Key)
			continue
		}
		if !p.VerifiableSubject {
			out.Strength = StrengthWeak
			out.WeakProviders = append(out.WeakProviders, p.Key)
		}
	}
	if out.Strength == StrengthWeak {
		out.WeakReasons = append(out.WeakReasons, WeakReasonUnverifiableProvider)
	}
	// Sorted so the response is stable across map iteration wherever the caller built the slice
	// from one. A field that reorders between two identical requests breaks a client's diff and
	// every ETag over it.
	sort.Strings(out.WeakProviders)
	sort.Strings(out.DisabledProviders)
	return out
}

// MembershipStrength answers "will revoking THIS person stick?" — from the provider behind that
// membership's identity.
//
// The two questions have different answers on purpose. A circle accepting both `discord` and
// `local` is weak overall, and its Discord members are individually durable; telling an officer
// that revoking a Discord member is weak because somebody else joined with `local` would be
// wrong in the direction that gets a revocation reversed.
func MembershipStrength(p Provider) RevocationStrength {
	return CircleStrength([]Provider{p})
}

// ServiceMembershipStrength is the answer for a membership with no identity behind it — a service
// account, whose `identity_id` is NULL.
//
// It is durable: there is no third-party subject to re-present and no second door. Revoking the
// membership is checked on the next request and that is the end of it. This is a named function
// rather than a `nil` case of [MembershipStrength] so that the answer is a decision somebody
// wrote down, not a zero value.
func ServiceMembershipStrength() RevocationStrength {
	return RevocationStrength{
		Strength:          StrengthDurable,
		WeakReasons:       []string{},
		WeakProviders:     []string{},
		DisabledProviders: []string{},
	}
}

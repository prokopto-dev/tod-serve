package identity_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/identity"
)

func provider(key string, kind identity.Kind, enabled, verifiable bool) identity.Provider {
	return identity.Provider{
		ID: "id-" + key, Key: key, Kind: kind,
		Enabled: enabled, VerifiableSubject: verifiable,
	}
}

func TestCircleStrength_EveryCombination(t *testing.T) {
	t.Parallel()

	discordP := provider("discord", identity.KindDiscord, true, true)
	oidcP := provider("authentik", identity.KindOIDC, true, true)
	localP := provider("local", identity.KindLocal, true, false)
	disabledLocal := provider("local", identity.KindLocal, false, false)

	tests := []struct {
		name     string
		accepted []identity.Provider
		want     identity.RevocationStrength
	}{
		{
			name:     "no accepted providers is vacuously durable: nobody can get in at all",
			accepted: nil,
			want: identity.RevocationStrength{
				Strength: identity.StrengthDurable, WeakReasons: []string{},
				WeakProviders: []string{}, DisabledProviders: []string{},
			},
		},
		{
			name:     "every accepted provider verifiable",
			accepted: []identity.Provider{discordP, oidcP},
			want: identity.RevocationStrength{
				Strength: identity.StrengthDurable, WeakReasons: []string{},
				WeakProviders: []string{}, DisabledProviders: []string{},
			},
		},
		{
			name:     "one unverifiable provider makes the whole circle weak",
			accepted: []identity.Provider{discordP, localP},
			want: identity.RevocationStrength{
				Strength:      identity.StrengthWeak,
				WeakReasons:   []string{identity.WeakReasonUnverifiableProvider},
				WeakProviders: []string{"local"}, DisabledProviders: []string{},
			},
		},
		{
			name: "a provider the instance has disabled admits nobody new, so it does not weaken " +
				"the circle — and is counted rather than silently dropped",
			accepted: []identity.Provider{discordP, disabledLocal},
			want: identity.RevocationStrength{
				Strength: identity.StrengthDurable, WeakReasons: []string{},
				WeakProviders: []string{}, DisabledProviders: []string{"local"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := identity.CircleStrength(tt.accepted)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("revocation strength (-want +got):\n%s", diff)
			}
		})
	}
}

// The circle question and the membership question have different answers on purpose. A circle
// accepting both `discord` and `local` is weak overall, and its Discord members are individually
// durable — telling an officer otherwise would get a revocation reversed.
func TestMembershipStrength_IsIndependentOfTheCircleAnswer(t *testing.T) {
	t.Parallel()

	discordP := provider("discord", identity.KindDiscord, true, true)
	localP := provider("local", identity.KindLocal, true, false)

	circle := identity.CircleStrength([]identity.Provider{discordP, localP})
	require.Equal(t, identity.StrengthWeak, circle.Strength)

	require.Equal(t, identity.StrengthDurable, identity.MembershipStrength(discordP).Strength)
	require.Equal(t, identity.StrengthWeak, identity.MembershipStrength(localP).Strength)
	require.Equal(t, []string{"local"}, identity.MembershipStrength(localP).WeakProviders)
}

// A service membership has no third-party subject to re-present and no second door: revoking it
// is checked on the next request and that is the end of it.
func TestServiceMembershipStrength_IsDurable(t *testing.T) {
	t.Parallel()

	got := identity.ServiceMembershipStrength()

	require.Equal(t, identity.StrengthDurable, got.Strength)
	require.Empty(t, got.WeakReasons)
	require.Empty(t, got.WeakProviders)
}

// The fields are machine-readable because a client has to RENDER them: the damage from a weak
// revocation is the officers' false belief that it worked, and a paragraph in an operations guide
// does not reach them.
func TestRevocationStrength_IsMachineReadable(t *testing.T) {
	t.Parallel()

	got := identity.CircleStrength([]identity.Provider{
		provider("local", identity.KindLocal, true, false),
	})

	require.Equal(t, identity.Strength("weak"), got.Strength)
	require.Equal(t, []string{"unverifiable_provider"}, got.WeakReasons)
	require.Equal(t, []string{"local"}, got.WeakProviders)
}

// verifiable_subject is a CHECK against kind in the schema, and re-asserted in Go for everything
// assembled without going through the database. The failure it catches turns a weak circle into
// one reporting `durable`.
func TestProviderValidate_RowsInconsistentWithTheirKind_AreRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		p    identity.Provider
		ok   bool
	}{
		{"a well-formed discord row", identity.Provider{
			Key: "discord", Kind: identity.KindDiscord, VerifiableSubject: true, ClientID: "app-1",
		}, true},
		{"a local provider claiming a verifiable subject", identity.Provider{
			Key: "local", Kind: identity.KindLocal, VerifiableSubject: true,
		}, false},
		{
			"a well-formed oidc row, which needs a client id because `aud = client_id` is the audience check",
			identity.Provider{
				Key: "authentik", Kind: identity.KindOIDC, VerifiableSubject: true,
				Issuer: "https://idp.example.com", JWKSURI: "https://idp.example.com/jwks",
				ClientID: "tod-serve-instance",
			},
			true,
		},
		{"an oidc provider claiming an unverifiable subject", identity.Provider{
			Key: "authentik", Kind: identity.KindOIDC, VerifiableSubject: false,
			Issuer: "https://idp.example.com", JWKSURI: "https://idp.example.com/jwks",
			ClientID: "tod-serve-instance",
		}, false},
		{"an oidc provider with no client id, so it has no audience to check", identity.Provider{
			Key: "authentik", Kind: identity.KindOIDC, VerifiableSubject: true,
			Issuer: "https://idp.example.com", JWKSURI: "https://idp.example.com/jwks",
		}, false},
		{"a discord provider with no application", identity.Provider{
			Key: "discord", Kind: identity.KindDiscord, VerifiableSubject: true,
		}, false},
		{"a non-discord provider carrying a client id", identity.Provider{
			Key: "local", Kind: identity.KindLocal, ClientID: "app-1",
		}, false},
		{"an oidc provider with an http issuer", identity.Provider{
			Key: "authentik", Kind: identity.KindOIDC, VerifiableSubject: true,
			Issuer: "http://idp.example.com", JWKSURI: "https://idp.example.com/jwks",
			ClientID: "tod-serve-instance",
		}, false},
		{"an unknown kind", identity.Provider{Key: "ldap", Kind: "ldap"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.p.Validate()
			if tt.ok {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, identity.ErrProviderInconsistent)
		})
	}
}

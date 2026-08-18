package identity_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/identity/local"
)

func localProvider() identity.Provider {
	return identity.Provider{
		ID: "01J0000000000000LOCALID", Key: "local", Kind: identity.KindLocal,
		DisplayName: "This server", Enabled: false, VerifiableSubject: false,
	}
}

// `local` ships disabled and enabling it needs the acknowledgement. The failure mode is not the
// re-entry — it is the officers believing revocation worked.
func TestEnableProvider_UnverifiableWithoutAcknowledgement_IsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.store.addProvider(localProvider())

	_, err := h.service.EnableProvider(t.Context(), "local", false)
	requireCode(t, err, identity.CodeAcknowledgementRequired)

	got, readErr := h.store.ProviderByKey(t.Context(), "local")
	require.NoError(t, readErr)
	require.False(t, got.Enabled, "a refused enable leaves the provider off")

	enabled, err := h.service.EnableProvider(t.Context(), "local", true)
	require.NoError(t, err)
	require.True(t, enabled.Enabled)
}

// A verifiable provider needs no acknowledgement: there is nothing to acknowledge, and asking
// anyway would train people to click through the one that matters.
func TestEnableProvider_VerifiableProvider_NeedsNoAcknowledgement(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	disabled := discordProvider()
	disabled.Enabled = false
	h.store.addProvider(disabled)

	got, err := h.service.EnableProvider(t.Context(), "discord", false)

	require.NoError(t, err)
	require.True(t, got.Enabled)
}

func TestDisableProvider_NeedsNoAcknowledgement(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.store.addProvider(localProvider())
	_, err := h.service.EnableProvider(t.Context(), "local", true)
	require.NoError(t, err)

	got, err := h.service.DisableProvider(t.Context(), "local")

	require.NoError(t, err)
	require.False(t, got.Enabled)
}

func TestEnableProvider_UnknownProvider_IsAValidationFailure(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := h.service.EnableProvider(t.Context(), "keycloak", true)
	requireCode(t, err, identity.CodeValidationFailed)

	_, err = h.service.DisableProvider(t.Context(), "keycloak")
	requireCode(t, err, identity.CodeValidationFailed)
}

// A new circle auto-accepts every enabled provider with a verifiable subject. `local` is NEVER
// auto-added: an owner must reach for it, having seen the `weak` field first.
func TestAutoAccepted_NeverIncludesAnUnverifiableProvider(t *testing.T) {
	t.Parallel()

	enabledLocal := localProvider()
	enabledLocal.Enabled = true
	disabledOIDC := provider("authentik", identity.KindOIDC, false, true)

	got := identity.AutoAccepted([]identity.Provider{discordProvider(), enabledLocal, disabledOIDC})

	require.Len(t, got, 1)
	require.Equal(t, "discord", got[0].Key)
}

// `local` forces max_uses = 1, because a local identity cannot re-auth and every lost token
// otherwise becomes another invite left lying around.
func TestInviteMaxUsesCeiling_LocalForcesOne(t *testing.T) {
	t.Parallel()

	require.Equal(t, local.MaxInviteUses, identity.InviteMaxUsesCeiling(localProvider()))
	require.Equal(t, 1, identity.InviteMaxUsesCeiling(localProvider()))
	require.Zero(t, identity.InviteMaxUsesCeiling(discordProvider()), "no ceiling for a verifiable provider")
}

// A link participant must be verifiable. The database refuses it too; this is the same rule at the
// edge, so the answer is a 422 rather than a constraint violation surfacing as a 500.
func TestCanLink_LocalProvider_Rejected(t *testing.T) {
	t.Parallel()

	discord, localP := discordProvider(), localProvider()
	oidc := provider("authentik", identity.KindOIDC, true, true)

	require.NoError(t, identity.CanLink(discord, oidc))

	requireCode(t, identity.CanLink(discord, localP), identity.CodeLinkRequiresVerifiableIdentity)
	requireCode(t, identity.CanLink(localP, discord), identity.CodeLinkRequiresVerifiableIdentity)
	requireCode(t, identity.CanLink(localP, localP), identity.CodeLinkRequiresVerifiableIdentity)
}

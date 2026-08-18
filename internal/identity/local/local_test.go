package local_test

import (
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity/local"
)

var at = core.MicrosFromTime(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))

func TestMint_SelfAssertedName_YieldsAServerMintedSubject(t *testing.T) {
	t.Parallel()

	gen := core.NewGenerator(rand.Reader)

	first, err := local.Mint(gen, at, "  Tankguy  ")
	require.NoError(t, err)
	require.Equal(t, "Tankguy", first.DisplayName, "the name is trimmed, never otherwise rewritten")
	require.Len(t, first.Subject, core.ULIDLen)

	second, err := local.Mint(gen, at, "Tankguy")
	require.NoError(t, err)
	require.NotEqual(t, first.Subject, second.Subject,
		"the same name twice is two identities; a local subject asserts nothing about who holds it")
}

func TestMint_WithoutADisplayName_IsRefused(t *testing.T) {
	t.Parallel()

	gen := core.NewGenerator(rand.Reader)

	for _, name := range []string{"", "   ", "\t\n"} {
		_, err := local.Mint(gen, at, name)
		require.ErrorIs(t, err, local.ErrDisplayNameRequired)
	}

	_, err := local.Mint(gen, at, strings.Repeat("x", local.MaxDisplayNameLen+1))
	require.ErrorIs(t, err, local.ErrDisplayNameRequired)

	// The limit is in runes, so a name of emoji is not silently a quarter of the length.
	_, err = local.Mint(gen, at, strings.Repeat("🐉", local.MaxDisplayNameLen))
	require.NoError(t, err)
}

// The mitigation that stops `local` degrading invite hygiene: a local identity cannot re-auth on
// a new device, so every lost token becomes a new invite unless the invite is single-use.
func TestMaxInviteUses_IsOne(t *testing.T) {
	t.Parallel()
	require.Equal(t, 1, local.MaxInviteUses)
}

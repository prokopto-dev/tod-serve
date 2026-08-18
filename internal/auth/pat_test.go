package auth_test

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

const testPepper = core.Secret("pepper-for-tests-only")

func testMinter(t *testing.T) *auth.Minter {
	t.Helper()
	m, err := auth.NewMinter(testPepper, rand.Reader)
	require.NoError(t, err)
	return m
}

// A minter with no pepper is refused at construction. Without one the stored value is a hash of a
// high-entropy string that anybody holding the database file and the source can recompute, so
// failing to start is the correct response.
func TestNewMinter_NoPepper_IsRefused(t *testing.T) {
	t.Parallel()
	_, err := auth.NewMinter("", rand.Reader)
	require.ErrorIs(t, err, auth.ErrNoPepper)
}

func TestMint_Token_HasTheDocumentedShape(t *testing.T) {
	t.Parallel()
	minted, err := testMinter(t).Mint()
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(minted.Token.Reveal(), auth.TokenScheme),
		"a token must be greppable; a scanner looking for a leaked credential needs something to match")
	require.Len(t, minted.Prefix, auth.PrefixLen)
	require.Len(t, minted.Hash, 32)

	prefix, secret, err := auth.Parse(minted.Token.Reveal())
	require.NoError(t, err)
	require.Equal(t, minted.Prefix, prefix)
	require.NotEmpty(t, secret.Reveal())
}

// The whole point of the pepper: the same secret hashes differently under a different instance's
// pepper, so a database lifted from one instance is useless against another.
func TestVerify_TheSameToken_HashesDifferentlyUnderADifferentPepper(t *testing.T) {
	t.Parallel()
	minted, err := testMinter(t).Mint()
	require.NoError(t, err)

	other, err := auth.NewMinter("a different pepper", rand.Reader)
	require.NoError(t, err)

	_, mine, err := testMinter(t).Verify(minted.Token.Reveal())
	require.NoError(t, err)
	_, theirs, err := other.Verify(minted.Token.Reveal())
	require.NoError(t, err)

	require.True(t, bytes.Equal(mine, minted.Hash))
	require.False(t, bytes.Equal(theirs, minted.Hash))
}

func TestParse_MalformedTokens_AreRefused(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"another product's token", "ghp_0123456789abcdef"},
		{"the scheme and nothing else", auth.TokenScheme},
		{"no secret", auth.TokenScheme + "ABCD1234"},
		{"an empty secret", auth.TokenScheme + "ABCD1234_"},
		{"a short prefix", auth.TokenScheme + "ABC_secret"},
		{"a prefix outside Crockford base32", auth.TokenScheme + "ABCDILOU_secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := auth.Parse(tc.token)
			require.ErrorIs(t, err, auth.ErrMalformedToken)
		})
	}
}

// The eight-character prefix is loggable and is how a leaked token is traced. The secret half never
// is — including through the wrong format verb, which is the day nobody plans for.
func TestParse_TheSecretHalf_NeverRenders(t *testing.T) {
	t.Parallel()
	minted, err := testMinter(t).Mint()
	require.NoError(t, err)
	_, secret, err := auth.Parse(minted.Token.Reveal())
	require.NoError(t, err)

	for _, rendered := range []string{
		fmt.Sprintf("%v", secret),
		fmt.Sprintf("%s", secret),
		fmt.Sprintf("%d", secret),
		fmt.Sprintf("%#v", secret),
		fmt.Sprintf("%+v", struct{ S core.Secret }{secret}),
	} {
		require.NotContains(t, rendered, secret.Reveal())
	}
	require.NotContains(t, fmt.Sprintf("%v", minted.Token), minted.Token.Reveal())
}

// Two tokens minted in a row must not share a prefix or a hash. A prefix collision would make the
// loggable half useless for tracing, which is the only reason it exists.
func TestMint_TwoTokens_ShareNothing(t *testing.T) {
	t.Parallel()
	m := testMinter(t)
	first, err := m.Mint()
	require.NoError(t, err)
	second, err := m.Mint()
	require.NoError(t, err)

	require.NotEqual(t, first.Prefix, second.Prefix)
	require.False(t, bytes.Equal(first.Hash, second.Hash))
	require.NotEqual(t, first.Token.Reveal(), second.Token.Reveal())
}

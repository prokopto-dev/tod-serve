package invite_test

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/invite"
)

func TestMint_ACode_HasTheDocumentedShape(t *testing.T) {
	t.Parallel()
	for range 200 {
		code, err := invite.Mint(rand.Reader)
		require.NoError(t, err)

		require.True(t, strings.HasPrefix(string(code), invite.Scheme+"-"))
		parts := strings.Split(string(code), "-")
		require.Len(t, parts, invite.Groups+1)
		require.Equal(t, invite.Scheme, parts[0])
		for _, group := range parts[1:] {
			require.Len(t, group, invite.GroupLen)
		}
		// Crockford base32 without I, L, O or U — the same alphabet a ULID uses, so an operator
		// reading a code aloud over voice chat meets one alphabet rather than two.
		for _, r := range strings.Join(parts[1:], "") {
			require.NotContains(t, "ILOU", string(r),
				"%q contains a character Crockford excludes because it looks like another", code)
		}
	}
}

func TestMint_TwoCodes_Differ(t *testing.T) {
	t.Parallel()
	seen := map[invite.Code]struct{}{}
	for range 500 {
		code, err := invite.Mint(rand.Reader)
		require.NoError(t, err)
		_, repeat := seen[code]
		require.False(t, repeat, "%q was minted twice out of 500 draws", code)
		seen[code] = struct{}{}
	}
}

// The entropy is INJECTED, so what a given draw encodes to is assertable rather than assumed. A
// generator that quietly reached for a default would make this test impossible to write, which is
// why every constructor here takes its randomness.
func TestMint_ADeterministicSource_EncodesExactlyFiftyBits(t *testing.T) {
	t.Parallel()
	// Seven bytes are read; the top fifty bits are used and the low six are discarded, so two
	// draws differing only in the discarded bits produce ONE code.
	first, err := invite.Mint(bytes.NewReader([]byte{0, 0, 0, 0, 0, 0, 0x00}))
	require.NoError(t, err)
	second, err := invite.Mint(bytes.NewReader([]byte{0, 0, 0, 0, 0, 0, 0x3f}))
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, invite.Code("TODI-00000-00000"), first)

	all, err := invite.Mint(bytes.NewReader([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}))
	require.NoError(t, err)
	require.Equal(t, invite.Code("TODI-ZZZZZ-ZZZZZ"), all)
}

func TestMint_AShortSource_IsAnError(t *testing.T) {
	t.Parallel()
	_, err := invite.Mint(bytes.NewReader([]byte{1, 2, 3}))
	require.Error(t, err)
	_, err = invite.Mint(nil)
	require.Error(t, err)
}

// **The middle of the range.** A suite written by the people who also write its only client
// encodes what we send, not what the world sends — and an invite code is typed by hand, pasted out
// of a Discord message that capitalised something, and read off a phone screen with the `TODI-`
// cut off. Every spelling below is one an officer will actually produce.
func TestParse_TheWaysAHumanTypesOneCode_AllResolveToIt(t *testing.T) {
	t.Parallel()
	const canonical = invite.Code("TODI-4KQ7M-9XPB2")

	tests := []struct {
		name  string
		typed string
	}{
		{"exactly as printed", "TODI-4KQ7M-9XPB2"},
		{"lower case, from a client that helpfully normalised it", "todi-4kq7m-9xpb2"},
		{"mixed case, typed by a person", "Todi-4Kq7m-9Xpb2"},
		{"the scheme omitted, because the link fragment was truncated", "4KQ7M-9XPB2"},
		{"the scheme omitted and lower case", "4kq7m-9xpb2"},
		{"no separators at all", "TODI4KQ7M9XPB2"},
		{"no separators and no scheme", "4KQ7M9XPB2"},
		{"leading and trailing whitespace from a paste", "  TODI-4KQ7M-9XPB2  "},
		{"a space where the dash was", "TODI 4KQ7M 9XPB2"},
		{"underscores, because somebody's client ate the dashes", "TODI_4KQ7M_9XPB2"},
		{"a newline in the middle, from a wrapped chat message", "TODI-4KQ7M-\n9XPB2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := invite.Parse(tt.typed)
			require.NoError(t, err, "typed as %q", tt.typed)
			require.Equal(t, canonical, got)
		})
	}
}

// Crockford folds the characters that look alike, and it EXCLUDES `U` rather than folding it. Both
// halves matter: a code read aloud as "oh" is a zero, and a `U` is a typo whose intent we cannot
// guess — guessing would be the confident mistake.
func TestParse_CrockfordLookAlikes_AreFolded(t *testing.T) {
	t.Parallel()
	canonical, err := invite.Parse("TODI-01234-56789")
	require.NoError(t, err)

	for _, typed := range []string{
		"TODI-O1234-56789", // capital O read as zero
		"TODI-o1234-56789", // lower case o
		"TODI-0I234-56789", // capital I read as one
		"TODI-0l234-56789", // lower case L
		"TODI-0L234-56789", // capital L
	} {
		got, err := invite.Parse(typed)
		require.NoError(t, err, "typed as %q", typed)
		require.Equal(t, canonical, got, "typed as %q", typed)
	}

	_, err = invite.Parse("TODI-U1234-56789")
	require.ErrorIs(t, err, invite.ErrMalformedCode,
		"U is excluded from Crockford base32, not folded; guessing what it meant would be worse")
}

// The scheme contains an `O` and an `I`, which the look-alike folding above turns into `0` and `1`.
// A prefix check written against the literal four characters therefore never matches its own
// scheme — which is a bug this suite found, and this test is what stops it coming back.
func TestParse_TheSchemeContainsFoldedCharacters_AndIsStillStripped(t *testing.T) {
	t.Parallel()
	want, err := invite.Parse("4KQ7M9XPB2")
	require.NoError(t, err)
	for _, typed := range []string{
		"TODI-4KQ7M-9XPB2",
		"T0D1-4KQ7M-9XPB2", // already folded, e.g. copied out of a log line
		"tOdI-4KQ7M-9XPB2",
	} {
		got, parseErr := invite.Parse(typed)
		require.NoError(t, parseErr, "typed as %q", typed)
		require.Equal(t, want, got, "typed as %q", typed)
	}
}

func TestParse_WhatIsNotACode_IsRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		typed string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"the scheme alone", "TODI-"},
		{"one group short", "TODI-4KQ7M"},
		{"one character short", "TODI-4KQ7M-9XPB"},
		{"one character long", "TODI-4KQ7M-9XPB2Z"},
		{"a whole second code appended", "TODI-4KQ7M-9XPB2TODI-4KQ7M-9XPB2"},
		{"a U, which Crockford excludes", "TODI-4KQ7U-9XPB2"},
		{"punctuation", "TODI-4KQ7M-9XPB!"},
		{"a personal access token", "tods_pat_4KQ7M9XP_aaaa"},
		{"an emoji", "TODI-4KQ7M-9XPB🙂"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := invite.Parse(tt.typed)
			require.ErrorIs(t, err, invite.ErrMalformedCode, "accepted %q", tt.typed)
		})
	}
}

// Every accepted spelling has to hash to ONE value, or the lookup finds a different row — or none
// — depending on how the person typed it, and the failure looks like an expired invite.
func TestHashCode_EverySpellingOfOneCode_HashesTheSame(t *testing.T) {
	t.Parallel()
	want := invite.HashCode("TODI-4KQ7M-9XPB2")
	require.Len(t, want, 32)
	for _, typed := range []string{
		"todi-4kq7m-9xpb2", "4KQ7M9XPB2", "  TODI 4KQ7M 9XPB2  ", "T0D1-4KQ7M-9XPB2",
	} {
		require.Equal(t, want, invite.HashCode(typed), "typed as %q", typed)
	}
}

// An unparseable code hashes to something that matches no row, so "not a code" and "a code nobody
// issued" are the same answer on the wire — which is what they should be to somebody guessing.
func TestHashCode_AnUnparseableCode_HashesToSomethingStableThatMatchesNothing(t *testing.T) {
	t.Parallel()
	junk := invite.HashCode("not a code at all")
	require.Len(t, junk, 32)
	require.Equal(t, junk, invite.HashCode("not a code at all"))
	require.NotEqual(t, junk, invite.HashCode("TODI-4KQ7M-9XPB2"))
}

func TestPrefix_IsTheFirstGroup_AndIsNeverTheWholeCode(t *testing.T) {
	t.Parallel()
	code, err := invite.Parse("TODI-4KQ7M-9XPB2")
	require.NoError(t, err)
	require.Equal(t, "4KQ7M", code.Prefix())
	require.NotContains(t, string(code)[len(invite.Scheme)+1+invite.GroupLen:], code.Prefix())
}

package core_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/core"
)

// The three spellings canonical §8 names by name. If this test ever goes red, `resolveRaidTarget`
// stops finding the mob an officer typed.
func TestNormalise_TheThreeSpellingsOfVulakAerr_AreOneString(t *testing.T) {
	t.Parallel()
	want := core.Normalise("Vulak`Aerr")
	require.NotEmpty(t, want)
	for _, typed := range []string{"Vulak'Aerr", "VulakAerr", "vulak aerr", "  VULAK AERR  "} {
		require.Equal(t, want, core.Normalise(typed), "typed as %q", typed)
	}
}

// The middle of the range, not the two ends. A display name arrives from a Discord client that
// decomposed it, from a phone keyboard that composed it, and from somebody who pasted fullwidth
// characters out of a spreadsheet — and all three are the same person to the officer reading the
// member list.
func TestNormalise_TheFormsOneNameArrivesIn_AreOneString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input []string
	}{
		{
			name: "a combining acute and the precomposed character",
			// U+00E9, then e + U+0301. NFKC is what makes these one string; without it the same
			// person appears twice in a member list and counts as two reporters.
			input: []string{"éclair", "éclair"},
		},
		{
			name:  "fullwidth characters pasted out of a spreadsheet",
			input: []string{"Tankguy", "Ｔankguy"},
		},
		{
			name:  "a curly apostrophe from a client that autocorrected it",
			input: []string{"Vulak'Aerr", "Vulak’Aerr", "Vulak´Aerr"},
		},
		{
			name:  "an en dash where somebody meant a hyphen",
			input: []string{"Lord-Nagafen", "Lord–Nagafen", "LordNagafen"},
		},
		{
			name: "the German sharp s, which ToLower does not fold",
			// strings.ToLower leaves ẞ as ß and STRASSE as strasse, so the two stay different.
			// Casefold is the whole reason this function does not use ToLower.
			input: []string{"straße", "STRASSE", "STRAẞe"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			want := core.Normalise(tt.input[0])
			require.NotEmpty(t, want)
			for _, in := range tt.input[1:] {
				require.Equal(t, want, core.Normalise(in), "%q normalised differently", in)
			}
		})
	}
}

// Names that are genuinely different must stay different. A normaliser that collapses everything
// satisfies the test above and makes every member a possible duplicate of every other.
func TestNormalise_DifferentNames_StayDifferent(t *testing.T) {
	t.Parallel()
	names := []string{"Tankguy", "Tankgal", "Vulak`Aerr", "Lord Nagafen", "ก"}
	seen := map[string]string{}
	for _, n := range names {
		got := core.Normalise(n)
		if previous, clash := seen[got]; clash {
			t.Fatalf("%q and %q both normalise to %q", previous, n, got)
		}
		seen[got] = n
	}
}

func TestNormalise_EmptyAndPunctuationOnly_AreEmpty(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "   ", "\t\n", "---", "'''", "``"} {
		require.Empty(t, core.Normalise(in), "input %q", in)
	}
}

// Idempotence is what makes it safe to normalise a value that may already be normalised — which is
// what happens the first time somebody stores a `_norm` column and then re-reads it to compare.
func TestNormalise_AppliedTwice_IsUnchanged(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"Vulak`Aerr", "éclair", "STRASSE", "  Riot Blue  ", "Ｔankguy"} {
		once := core.Normalise(in)
		require.Equal(t, once, core.Normalise(once), "input %q", in)
	}
}

func TestNormalise_Output_HasNoStrippedCharacter(t *testing.T) {
	t.Parallel()
	got := core.Normalise("  Vulak`Aerr — the’ Devourer  ")
	for _, r := range got {
		require.NotContains(t, " \t\n", string(r), "whitespace survived in %q", got)
		require.False(t, strings.ContainsRune("'`‘’´-", r),
			"stripped punctuation survived in %q", got)
	}
}

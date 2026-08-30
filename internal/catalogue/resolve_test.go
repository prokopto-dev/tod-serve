package catalogue_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// TestResolve_TheThreeSpellingsOfVulakAerr_AllLandOnIt is the case canonical §8 names as the whole
// job of `name_norm`, driven through the ladder rather than through the normaliser.
//
// core.Normalise already has its own test. This one asserts the ladder USES it: a resolve path
// that compared raw strings would pass every normaliser test and fail every officer.
func TestResolve_TheThreeSpellingsOfVulakAerr_AllLandOnIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedEmbedded()

	for _, typed := range []string{
		"Vulak`Aerr",   // the canonical spelling, backtick and all
		"Vulak'Aerr",   // the apostrophe an ordinary keyboard produces
		"VulakAerr",    // the punctuation given up on
		"vulak aerr",   // lowercase, with a space where the backtick was
		"VULAK`AERR",   // caps lock
		" Vulak`Aerr ", // pasted out of a log line with the whitespace still on it
	} {
		t.Run(typed, func(t *testing.T) {
			t.Parallel()
			got, err := f.svc.Resolve(t.Context(), catalogue.Ref{Name: typed})
			require.NoError(t, err, "%q did not resolve", typed)
			require.Equal(t, "Vulak`Aerr", got.Target.Name)
		})
	}
}

// TestResolve_TheLadder_RanksAnExactHitAboveASubstringOne is the discipline the whole design rests
// on: `createTodReport` accepts a `target_name` because this ordering holds, and the nParse+ plugin
// sends a parsed name while holding no catalogue, so it cannot notice being answered with the wrong
// mob.
//
// The cases are chosen so that each one WOULD match a lower rung as well. A ladder that fell
// through, or that scored rungs and picked a maximum, passes a test built only from unambiguous
// names and fails every one of these.
func TestResolve_TheLadder_RanksAnExactHitAboveASubstringOne(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedEmbedded()

	tests := []struct {
		name      string
		typed     string
		want      string
		matchKind catalogue.MatchKind
		why       string
	}{
		{
			name: "the canonical name, byte for byte", typed: "Lord Nagafen",
			want: "Lord Nagafen", matchKind: catalogue.MatchName,
			why: "also a prefix of itself and a substring of nothing else",
		},
		{
			name: "a name whose punctuation was dropped", typed: "vulakaerr",
			want: "Vulak`Aerr", matchKind: catalogue.MatchNameNormalised,
		},
		{
			name: "an alias, byte for byte", typed: "Naggy",
			want: "Lord Nagafen", matchKind: catalogue.MatchAlias,
		},
		{
			name: "an alias in the wrong case", typed: "naggy",
			want: "Lord Nagafen", matchKind: catalogue.MatchAliasNormalised,
		},
		{
			// The case worth the whole file. `Sev` is Severilous' alias AND a prefix of Sevalak's
			// name. A ladder that reached the prefix rung would find two targets and answer
			// `ambiguous_target`, and an officer typing the short name they have used for years
			// would get an error.
			name: "an alias that is also a prefix of another target", typed: "Sev",
			want: "Severilous", matchKind: catalogue.MatchAlias,
			why: "alias outranks prefix, so Sevalak does not turn this into a tie",
		},
		{
			name: "a lowercase alias that is also a prefix of another target", typed: "sev",
			want: "Severilous", matchKind: catalogue.MatchAliasNormalised,
		},
		{
			// `Nagafen` is an alias, and it is also a substring of `Lord Nagafen`'s name_norm.
			// Same target either way here, which is why the assertion is on the RUNG.
			name: "Nagafen resolves by alias, not by substring", typed: "Nagafen",
			want: "Lord Nagafen", matchKind: catalogue.MatchAlias,
		},
		{
			name: "a prefix of exactly one target", typed: "Trakan",
			want: "Trakanon", matchKind: catalogue.MatchPrefix,
		},
		{
			name: "a substring of exactly one target", typed: "orenaire",
			want: "Gorenaire", matchKind: catalogue.MatchSubstring,
		},
		{
			name: "a substring that spans the stripped punctuation", typed: "kaerr",
			want: "Vulak`Aerr", matchKind: catalogue.MatchSubstring,
			why: "the backtick is gone from name_norm, so `k` and `A` are adjacent",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := f.svc.Resolve(t.Context(), catalogue.Ref{Name: tt.typed})
			require.NoError(t, err, "%q did not resolve; %s", tt.typed, tt.why)
			require.Equal(t, tt.want, got.Target.Name, "%q resolved to the wrong target", tt.typed)
			require.Equal(t, tt.matchKind, got.MatchKind,
				"%q matched at the wrong rung; %s", tt.typed, tt.why)
		})
	}
}

// TestResolve_AName_MatchingSeveralTargets_IsAmbiguousWithCandidates covers the tie, including the
// one a plugin will actually hit: an officer types a word that starts five mob names.
func TestResolve_AName_MatchingSeveralTargets_IsAmbiguousWithCandidates(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedEmbedded()

	tests := []struct {
		name    string
		typed   string
		atLeast int
		expect  []string
	}{
		{
			name: "a prefix shared by five NToV lords and a classic dragon", typed: "Lord",
			atLeast: 5,
			expect:  []string{"Lord Nagafen", "Lord Vyemm", "Lord Kreizenn"},
		},
		{
			name: "a prefix shared by two of the Kael royals", typed: "Lady",
			atLeast: 2,
			expect:  []string{"Lady Vox", "Lady Mirenilla"},
		},
		{
			// The substring rung with a very short input, which is the "matches ten" case.
			name: "a two-letter substring most of the catalogue contains", typed: "ar",
			atLeast: 10,
		},
		{
			name: "the two Velious dragons whose names differ by one letter", typed: "landicar",
			atLeast: 2,
			expect:  []string{"Klandicar", "Zlandicar"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := f.svc.Resolve(t.Context(), catalogue.Ref{Name: tt.typed})
			require.Error(t, err, "%q resolved to one target; it should be ambiguous", tt.typed)

			coded, ok := apierr.From(err)
			require.True(t, ok, "the failure is not a coded problem: %v", err)
			require.Equal(t, apierr.CodeAmbiguousTarget, coded.Code())

			// The candidates ARE the contract: the plugin holds no catalogue, so this list is the
			// only thing that tells it what it might have meant.
			problem := coded.Problem()
			require.NotNil(t, problem.Meta, "an ambiguity with no meta tells the client nothing")
			require.NotEmpty(t, problem.Meta.Candidates)

			var candidates []catalogue.Target
			require.NoError(t, json.Unmarshal(problem.Meta.Candidates, &candidates))
			require.GreaterOrEqual(t, len(candidates), min(tt.atLeast, catalogue.MaxCandidates))
			require.LessOrEqual(t, len(candidates), catalogue.MaxCandidates,
				"the candidate list is uncapped; a one-letter query would return the catalogue")

			names := make([]string, 0, len(candidates))
			for _, c := range candidates {
				names = append(names, c.Name)
			}
			for _, want := range tt.expect {
				require.Contains(t, names, want)
			}
		})
	}
}

// TestResolve_ACappedCandidateList_SaysHowManyItDropped is the "never hide a row silently" rule on
// the one list in this package that is truncated.
func TestResolve_ACappedCandidateList_SaysHowManyItDropped(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	// A catalogue built for this rather than the embedded one: the cap is a property of the code,
	// and a test that leaned on how many shipped mob names happen to share a substring would break
	// the next time somebody added one.
	for i := range catalogue.MaxCandidates + 5 {
		f.target(fmt.Sprintf("Wyvern of the %02d Wastes", i), "Western Wastes")
	}

	_, err := f.svc.Resolve(t.Context(), catalogue.Ref{Name: "Wyvern"})
	require.Error(t, err)
	coded, ok := apierr.From(err)
	require.True(t, ok)

	var candidates []catalogue.Target
	require.NoError(t, json.Unmarshal(coded.Problem().Meta.Candidates, &candidates))
	require.Len(t, candidates, catalogue.MaxCandidates)
	require.Contains(t, coded.Problem().Detail, "are listed",
		"the detail does not say the list was cut; a filter that drops rows counts them somewhere")
}

// TestResolve_AnUnknownName_IsUnknownTargetAndNotAGuess is the honesty rule. A resolver that
// returned its nearest match for a name nobody has heard of is exactly the confident mistake this
// project is designed against, and a plugin cannot tell a wrong answer from a right one.
func TestResolve_AnUnknownName_IsUnknownTargetAndNotAGuess(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedEmbedded()

	for _, typed := range []string{
		"Emperor Ssraeshza", // real mob, wrong expansion for this catalogue
		"Bob",
		"zzzzzzzz",
		"...", // nothing but punctuation, which normalises away to the empty string
		"   ", // and whitespace, which does too
	} {
		t.Run(typed, func(t *testing.T) {
			t.Parallel()
			_, err := f.svc.Resolve(t.Context(), catalogue.Ref{Name: typed})
			require.Error(t, err, "%q resolved to something", typed)
			coded, ok := apierr.From(err)
			require.True(t, ok)
			require.Contains(t,
				[]apierr.Code{apierr.CodeUnknownTarget, apierr.CodeValidationFailed}, coded.Code())
		})
	}
}

// TestResolve_ARetiredTarget_KeepsItsExactNameAndLeavesTheFuzzyRungs is the rule [live] states: a
// retired mob must stay addressable so a backdated report names the right row, and must not be
// what a half-typed name resolves to.
func TestResolve_ARetiredTarget_KeepsItsExactNameAndLeavesTheFuzzyRungs(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	live := f.target("Sontalak Prime", "Western Wastes")
	retired := f.target("Sontalak Minor", "Western Wastes", "Minor")

	_, err := f.svc.Update(t.Context(), retired.ID, catalogue.UpdateRequest{
		State: ptr(schemaenum.RaidTargetStateRetired),
	}, f.inv)
	require.NoError(t, err)

	byName, err := f.svc.Resolve(t.Context(), catalogue.Ref{Name: "Sontalak Minor"})
	require.NoError(t, err, "a retired target must stay addressable by its exact name")
	require.Equal(t, retired.ID, byName.Target.ID)

	byAlias, err := f.svc.Resolve(t.Context(), catalogue.Ref{Name: "minor"})
	require.NoError(t, err, "a retired target must stay addressable by its alias")
	require.Equal(t, retired.ID, byAlias.Target.ID)

	// "Sontalak" prefixes both. It is not a tie, because the retired one is out of the fuzzy rungs.
	byPrefix, err := f.svc.Resolve(t.Context(), catalogue.Ref{Name: "Sontalak"})
	require.NoError(t, err,
		"a retired target turned a live target's prefix into an ambiguity")
	require.Equal(t, live.ID, byPrefix.Target.ID)
	require.Equal(t, catalogue.MatchPrefix, byPrefix.MatchKind)
}

// TestResolve_ARef_NamesExactlyOneOfIdAndName covers the fourth state a two-string signature would
// have had: both sent, and neither.
func TestResolve_ARef_NamesExactlyOneOfIdAndName(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := f.target("Aaryonar", "Temple of Veeshan")

	tests := []struct {
		name string
		ref  catalogue.Ref
		code apierr.Code
	}{
		{"neither", catalogue.Ref{}, apierr.CodeValidationFailed},
		{"both", catalogue.Ref{ID: target.ID, Name: "Aaryonar"}, apierr.CodeValidationFailed},
		// Whitespace trims to nothing, which is the same request as sending no name at all.
		{"a name of only whitespace", catalogue.Ref{Name: "   "}, apierr.CodeValidationFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := f.svc.Resolve(t.Context(), tt.ref)
			require.Error(t, err)
			coded, ok := apierr.From(err)
			require.True(t, ok)
			require.Equal(t, tt.code, coded.Code())
		})
	}
}

// TestResolve_AnIdThatNamesNothing_IsUnknownTargetRatherThan404 is the distinction the ToD worker
// consumes: inside a report body an unknown id is a problem with a FIELD, and answering 404 would
// tell a client the report endpoint does not exist.
func TestResolve_AnIdThatNamesNothing_IsUnknownTargetRatherThan404(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	absent, err := core.NewID[core.RaidTarget](f.ids, fixtureNow)
	require.NoError(t, err)

	_, err = f.svc.Resolve(t.Context(), catalogue.Ref{ID: absent})
	require.Error(t, err)
	coded, ok := apierr.From(err)
	require.True(t, ok)
	require.Equal(t, apierr.CodeUnknownTarget, coded.Code())

	// And the plain read of the same id is a 404, because there the id IS the resource.
	_, err = f.svc.Get(t.Context(), absent)
	require.Error(t, err)
	coded, ok = apierr.From(err)
	require.True(t, ok)
	require.Equal(t, apierr.CodeNotFound, coded.Code())
}

// TestResolve_ById_ReportsTheIdRungAndCarriesTheQuakeFlag: the projection's quake truncation reads
// `is_quake_target` off whatever this returns, so a resolution that dropped it would make an
// earthquake stop clearing the board.
func TestResolve_ById_ReportsTheIdRungAndCarriesTheQuakeFlag(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedEmbedded()

	sleeper, err := f.svc.Resolve(t.Context(), catalogue.Ref{Name: "Kerafyrm"})
	require.NoError(t, err)
	require.False(t, sleeper.Target.IsQuakeTarget,
		"waking Kerafyrm is a one-time event, not a spawn cycle")

	byID, err := f.svc.Resolve(t.Context(), catalogue.Ref{ID: sleeper.Target.ID})
	require.NoError(t, err)
	require.Equal(t, catalogue.MatchID, byID.MatchKind)
	require.Equal(t, sleeper.Target, byID.Target)

	vulak, err := f.svc.Resolve(t.Context(), catalogue.Ref{Name: "VA"})
	require.NoError(t, err)
	require.True(t, vulak.Target.IsQuakeTarget)
}

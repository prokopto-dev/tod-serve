package catalogue_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// TestEmbedded_NoTargetCarriesATimer is the licence invariant, asserted against the TYPE rather
// than against the data.
//
// SEED001 greps for a bundled seed file, which catches the obvious way to ship timer data. This
// catches the other one: a field on [catalogue.EmbeddedTarget] that a well-meaning change adds
// because "the offsets are right there in the wiki". Canonical §15 says the numbers do not ship,
// and the shape of the struct is what makes that unwriteable rather than merely undone.
func TestEmbedded_NoTargetCarriesATimer(t *testing.T) {
	t.Parallel()
	fields := structFieldNames[catalogue.EmbeddedTarget]()
	for _, banned := range []string{
		"WindowKind", "WindowOpenOffsetSeconds", "WindowCloseOffsetSeconds",
		"FixedGraceSeconds", "ClusterEpsilonSeconds", "Timer", "RespawnSeconds", "Variance",
	} {
		require.NotContains(t, fields, banned,
			"EmbeddedTarget carries %s. Target identity ships; timer data does not, and this "+
				"struct is the shape that makes the second one impossible rather than merely "+
				"absent — canonical §15, SEED001", banned)
	}
}

// TestEmbedded_EveryTarget_IsValid checks the shipped literals against the same rules an
// operator's `createRaidTarget` goes through.
//
// The list is hand-written, which is exactly why it needs a gate: a typo in an expansion or a
// category would otherwise be found by the first operator who ran `tod-serve seed targets` against
// a real database and got a CHECK constraint failure with no field name in it.
func TestEmbedded_EveryTarget_IsValid(t *testing.T) {
	t.Parallel()
	targets := catalogue.Embedded()
	require.NotEmpty(t, targets)

	expansions := lookupEnum(t, schemaenum.NameRaidTargetExpansion)
	categories := lookupEnum(t, schemaenum.NameRaidTargetCategory)

	for _, target := range targets {
		t.Run(target.Name, func(t *testing.T) {
			t.Parallel()
			require.NotEmpty(t, target.Name)
			require.NotEmpty(t, target.Zone)
			require.Equal(t, strings.TrimSpace(target.Name), target.Name,
				"the name carries whitespace that would be trimmed on the way in, so the shipped "+
					"literal and the stored row would differ")
			require.Contains(t, expansions, target.Expansion)
			require.Contains(t, categories, target.Category)
			require.NotEmpty(t, core.Normalise(target.Name),
				"the name normalises away to nothing, so it would collide with every other such "+
					"name on ux_raid_target_name_norm")
			require.LessOrEqual(t, len(target.Aliases), catalogue.MaxAliases)
			for _, alias := range target.Aliases {
				require.NotEmpty(t, core.Normalise(alias), "alias %q normalises to nothing", alias)
			}
		})
	}
}

// TestEmbedded_EveryNameAndAlias_IsUniqueAcrossTheCatalogue is the constraint the two unique
// indexes hold, checked before a database is involved.
//
// A duplicate here is not a cosmetic problem: `ux_raid_target_name_norm` and
// `ux_raid_target_alias_norm` would abort the seed transaction, and the operator's first run of
// `tod-serve seed targets` would fail on a brand-new install.
func TestEmbedded_EveryNameAndAlias_IsUniqueAcrossTheCatalogue(t *testing.T) {
	t.Parallel()
	names := map[string]string{}
	aliases := map[string]string{}

	for _, target := range catalogue.Embedded() {
		norm := core.Normalise(target.Name)
		if owner, taken := names[norm]; taken {
			t.Fatalf("%q and %q both normalise to %q; ux_raid_target_name_norm is unique",
				owner, target.Name, norm)
		}
		names[norm] = target.Name

		for _, alias := range target.Aliases {
			aliasNorm := core.Normalise(alias)
			if owner, taken := aliases[aliasNorm]; taken {
				t.Fatalf("alias %q on %q collides with %q; ux_raid_target_alias_norm is unique",
					alias, target.Name, owner)
			}
			aliases[aliasNorm] = target.Name
		}
	}
	require.NotEmpty(t, names)
	require.NotEmpty(t, aliases, "no target ships an alias; the ladder's alias rungs are untested")
}

// TestEmbedded_EveryName_ResolvesToItself is the round trip that matters: the shipped catalogue
// must be one the ladder can navigate.
//
// A target whose canonical name is a prefix of another's, with no alias to disambiguate, resolves
// to an ambiguity — which would mean a mob nobody could report by typing its own full name. That
// is a property of the LIST, not of the ladder, so it belongs here.
func TestEmbedded_EveryName_ResolvesToItself(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedEmbedded()

	for _, want := range catalogue.Embedded() {
		t.Run(want.Name, func(t *testing.T) {
			t.Parallel()
			got, err := f.svc.Resolve(t.Context(), catalogue.Ref{Name: want.Name})
			require.NoError(t, err, "%q does not resolve to itself", want.Name)
			require.Equal(t, want.Name, got.Target.Name)

			for _, alias := range want.Aliases {
				byAlias, aliasErr := f.svc.Resolve(t.Context(), catalogue.Ref{Name: alias})
				require.NoError(t, aliasErr, "alias %q does not resolve", alias)
				require.Equal(t, want.Name, byAlias.Target.Name,
					"alias %q resolves to the wrong target", alias)
			}
		})
	}
}

// TestSeedTargets_RunTwice_AddsNothingTheSecondTime. A seed an operator dares not re-run is a seed
// that stops being applied after the first install, which is when it matters least.
func TestSeedTargets_RunTwice_AddsNothingTheSecondTime(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	first := f.seedEmbedded()
	require.Equal(t, len(catalogue.Embedded()), first.TargetsAdded)
	require.Zero(t, first.TargetsPresent)
	require.Positive(t, first.AliasesAdded)

	second := f.seedEmbedded()
	require.Zero(t, second.TargetsAdded)
	require.Equal(t, len(catalogue.Embedded()), second.TargetsPresent)
	require.Zero(t, second.AliasesAdded)
	require.Zero(t, second.AliasesTaken)

	page, err := f.svc.List(t.Context(), catalogue.ListFilter{IncludeRetired: true})
	require.NoError(t, err)
	require.Equal(t, len(catalogue.Embedded()), page.Total,
		"the second run duplicated rows")
}

// TestSeedTargets_ATargetTheOperatorEdited_IsLeftAlone is the property that makes re-running safe.
//
// An operator who corrected a zone, retired a target their server no longer spawns, or renamed one
// has made a statement about their instance. A seed that stomped it would be a seed they turn off.
func TestSeedTargets_ATargetTheOperatorEdited_IsLeftAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedEmbedded()

	vulak, err := f.svc.Resolve(t.Context(), catalogue.Ref{Name: "Vulak`Aerr"})
	require.NoError(t, err)
	edited, err := f.svc.Update(t.Context(), vulak.Target.ID, catalogue.UpdateRequest{
		Zone:  ptr("Temple of Veeshan, top floor"),
		State: ptr(schemaenum.RaidTargetStateRetired),
	})
	require.NoError(t, err)

	report := f.seedEmbedded()
	require.Zero(t, report.TargetsAdded)

	after, err := f.svc.Get(t.Context(), vulak.Target.ID)
	require.NoError(t, err)
	require.Equal(t, edited.Zone, after.Zone, "the seed overwrote an operator's correction")
	require.Equal(t, schemaenum.RaidTargetStateRetired, after.State,
		"the seed un-retired a target the operator retired")
}

// TestSeedTargets_AnAliasClaimedByAnotherTarget_IsCountedNotSwallowed. The instance wins, and the
// disagreement is reported: it means our list and this operator's catalogue mean different things
// by the same short name, which is a thing they need to be told rather than a row to drop.
func TestSeedTargets_AnAliasClaimedByAnotherTarget_IsCountedNotSwallowed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	// An operator's own target has claimed `VA` before the shipped catalogue ever runs.
	mine := f.target("Vindicator Ancient", "Somewhere", "VA")

	report := f.seedEmbedded()
	require.Positive(t, report.AliasesTaken,
		"the collision was neither applied nor counted, so nobody can find out about it")

	got, err := f.svc.Resolve(t.Context(), catalogue.Ref{Name: "VA"})
	require.NoError(t, err)
	require.Equal(t, mine.ID, got.Target.ID, "the seed took an alias the instance already used")
}

// structFieldNames reflects the exported field names of a struct, for the shape assertion above.
func structFieldNames[T any]() []string {
	typ := reflect.TypeFor[T]()
	out := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		out = append(out, typ.Field(i).Name)
	}
	return out
}

func lookupEnum(t *testing.T, name string) []string {
	t.Helper()
	e, ok := schemaenum.Lookup(name)
	require.True(t, ok, "%s is not in the enum catalogue", name)
	return slices.Clone(e.Values)
}

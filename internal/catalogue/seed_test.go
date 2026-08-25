package catalogue_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

// Every number in this file is invented, and obviously so: 60, 120, 900. They are not P99 respawn
// data and must never be replaced with any, whatever the temptation to make a fixture "realistic".
// A test fixture is the first place somebody copies a value from, and canonical §15 keeps those
// numbers out of this repository entirely — SEED001 is the grep, and this comment is the reason.
const seedSource = "a test fixture, not the wiki"

// seedJSON builds a well-formed seed file with the given timer rows spliced in.
func seedJSON(rows ...string) string {
	return `{"version": 1, "source": "` + seedSource + `", "timers": [` +
		strings.Join(rows, ",") + `]}`
}

const varianceRow = `{"target": "Vulak` + "`" + `Aerr", "server": "blue",
	"window_kind": "variance",
	"window_open_offset_seconds": 60, "window_close_offset_seconds": 120}`

// TestApplySeed_AWellFormedFile_WritesEveryTimerAndItsProvenance is the happy path, and the thing
// it checks beyond the offsets is `source`: these numbers are not ours, and a row nobody can
// attribute is a row nobody can argue with.
func TestApplySeed_AWellFormedFile_WritesEveryTimerAndItsProvenance(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedEmbedded()
	circleID := f.circle("Riot")

	parsed, err := catalogue.ParseSeed(strings.NewReader(seedJSON(
		varianceRow,
		`{"target": "Naggy", "server": "blue", "window_kind": "fixed",
		  "window_open_offset_seconds": 300, "window_close_offset_seconds": 300,
		  "note": "invented"}`,
		`{"target": "Trak", "server": "green", "window_kind": "unknown"}`,
	)))
	require.NoError(t, err)

	report, err := f.svc.ApplySeed(t.Context(), parsed, f.inv)
	require.NoError(t, err)
	require.Equal(t, 3, report.TimersWritten)
	require.Equal(t, seedSource, report.Source)

	vulak, err := f.svc.Resolve(t.Context(), catalogue.Ref{Name: "Vulak`Aerr"})
	require.NoError(t, err)
	timers, err := f.svc.Timers(t.Context(), vulak.Target.ID)
	require.NoError(t, err)
	require.Len(t, timers, 1)
	require.Equal(t, int64(60), *timers[0].WindowOpenOffsetSeconds)
	require.Equal(t, seedSource, timers[0].Source,
		"a seeded window with no provenance is a number nobody can dispute")

	// And the circle now resolves through the catalogue rather than to nothing, which is the
	// whole point of running a seed.
	resolved, err := f.svc.ResolveTimer(t.Context(), f.store.Queries(), circleID, vulak.Target.ID, core.ServerBlue)
	require.NoError(t, err)
	require.Equal(t, catalogue.TimerSourceCatalogue, resolved.Source)
}

// TestApplySeed_TheLadder_ResolvesASeedsTargetNames. A seed written against a slightly different
// catalogue must not need its own matcher: two matchers is how a seed lands a window on the wrong
// mob, silently, on every instance that applied it.
func TestApplySeed_TheLadder_ResolvesASeedsTargetNames(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedEmbedded()

	for _, named := range []string{"Vulak`Aerr", "vulak aerr", "VA", "Vulak"} {
		t.Run(named, func(t *testing.T) {
			t.Parallel()
			inner := newFixture(t)
			inner.seedEmbedded()

			parsed, err := catalogue.ParseSeed(strings.NewReader(seedJSON(
				`{"target": "` + named + `", "server": "blue", "window_kind": "variance",
				  "window_open_offset_seconds": 60, "window_close_offset_seconds": 120}`)))
			require.NoError(t, err)
			_, err = inner.svc.ApplySeed(t.Context(), parsed, f.inv)
			require.NoError(t, err)

			vulak, err := inner.svc.Resolve(t.Context(), catalogue.Ref{Name: "Vulak`Aerr"})
			require.NoError(t, err)
			timers, err := inner.svc.Timers(t.Context(), vulak.Target.ID)
			require.NoError(t, err)
			require.Len(t, timers, 1, "the seed named %q and it did not land on Vulak", named)
		})
	}
}

// TestParseSeed_AMalformedFile_IsRefusedBeforeAnythingIsRead covers every way the file itself can
// be wrong. None of these reaches a store, which is the design: [catalogue.ParseSeed] takes a
// reader and nothing else, so "did this touch the database" is answerable from the signature.
func TestParseSeed_AMalformedFile_IsRefusedBeforeAnythingIsRead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		body    string
		because string
	}{
		{
			name: "not JSON at all", body: "respawn: 7 days",
			because: "somebody handed us the wiki page",
		},
		{
			name: "no version",
			body: `{"source": "x", "timers": [` + varianceRow + `]}`,
			because: "the seed repository versions separately; an absent version means the writer " +
				"did not know this contract exists",
		},
		{
			name: "a version this binary does not read",
			body: `{"version": 2, "source": "x", "timers": [` + varianceRow + `]}`,
		},
		{
			name: "a field this binary does not know",
			body: `{"version": 1, "source": "x", "confidence": "high", "timers": [` +
				varianceRow + `]}`,
			because: "read best-effort, it would silently not do what the file says",
		},
		{
			name:    "no source",
			body:    `{"version": 1, "timers": [` + varianceRow + `]}`,
			because: "these numbers are disputed and an unattributed one cannot be argued with",
		},
		{
			name: "no timers at all",
			body: `{"version": 1, "source": "x", "timers": []}`,
		},
		{
			name: "a row naming neither a target nor an id",
			body: seedJSON(`{"server": "blue", "window_kind": "unknown"}`),
		},
		{
			name: "a row naming both a target and an id",
			body: seedJSON(`{"target": "Naggy", "target_id": "01K3TGT8N9M4X0Q7R2VB6C5D1E",
				"server": "blue", "window_kind": "unknown"}`),
		},
		{
			name: "a server that does not exist",
			body: seedJSON(`{"target": "Naggy", "server": "teal", "window_kind": "unknown"}`),
		},
		{
			name: "an unknown window carrying offsets",
			body: seedJSON(`{"target": "Naggy", "server": "blue", "window_kind": "unknown",
				"window_open_offset_seconds": 60, "window_close_offset_seconds": 120}`),
		},
		{
			name: "a variance band with only one edge",
			body: seedJSON(`{"target": "Naggy", "server": "blue", "window_kind": "variance",
				"window_open_offset_seconds": 60}`),
			because: "an offset alone is not a window; the pairing CHECK refuses it too",
		},
		{
			name: "a fixed window that is a band",
			body: seedJSON(`{"target": "Naggy", "server": "blue", "window_kind": "fixed",
				"window_open_offset_seconds": 60, "window_close_offset_seconds": 120}`),
		},
		{
			name: "a band that closes before it opens",
			body: seedJSON(`{"target": "Naggy", "server": "blue", "window_kind": "variance",
				"window_open_offset_seconds": 120, "window_close_offset_seconds": 60}`),
		},
		{
			name: "a target_id that is not an id",
			body: seedJSON(`{"target_id": "nagafen", "server": "blue", "window_kind": "unknown"}`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := catalogue.ParseSeed(strings.NewReader(tt.body))
			require.Error(t, err, "the seed was accepted; %s", tt.because)
		})
	}
}

// TestApplySeed_ARowThatDoesNotResolve_LeavesTheCatalogueUntouched is the acceptance criterion the
// brief names: a malformed or PARTIAL seed must fail loudly and leave the catalogue unchanged
// rather than half-applied.
//
// The file here is individually valid on every row — it parses — and fails on the FIFTH one,
// against this instance's catalogue. That is the case a parse check cannot catch and a
// write-as-you-go loop would half-apply, leaving an operator with four windows they did not choose
// and no way to tell which run put them there.
func TestApplySeed_ARowThatDoesNotResolve_LeavesTheCatalogueUntouched(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		last string
	}{
		{
			name: "a target this instance has never heard of",
			last: `{"target": "Emperor Ssraeshza", "server": "blue", "window_kind": "unknown"}`,
		},
		{
			name: "a name that is ambiguous in this catalogue",
			last: `{"target": "Lord", "server": "blue", "window_kind": "variance",
				"window_open_offset_seconds": 60, "window_close_offset_seconds": 120}`,
		},
		{
			name: "an id that names nothing here",
			last: `{"target_id": "01K3TGT8N9M4X0Q7R2VB6C5D1E", "server": "blue",
				"window_kind": "unknown"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.seedEmbedded()

			parsed, err := catalogue.ParseSeed(strings.NewReader(seedJSON(
				varianceRow,
				`{"target": "Naggy", "server": "blue", "window_kind": "unknown"}`,
				`{"target": "Vox", "server": "blue", "window_kind": "unknown"}`,
				`{"target": "Trak", "server": "blue", "window_kind": "unknown"}`,
				tt.last,
			)))
			require.NoError(t, err, "the file itself is well formed; the failure is against the "+
				"catalogue, which is what makes this the partial-application case")

			_, err = f.svc.ApplySeed(t.Context(), parsed, f.inv)
			require.Error(t, err)

			// Not one of the four good rows landed. That is the whole assertion.
			for _, name := range []string{"Vulak`Aerr", "Naggy", "Vox", "Trak"} {
				resolved, resolveErr := f.svc.Resolve(t.Context(), catalogue.Ref{Name: name})
				require.NoError(t, resolveErr)
				timers, timerErr := f.svc.Timers(t.Context(), resolved.Target.ID)
				require.NoError(t, timerErr)
				require.Empty(t, timers,
					"%s was written before the seed failed; the catalogue is half-applied", name)
			}
		})
	}
}

// TestApplySeed_ASecondSeed_ReplacesRatherThanAccumulates. A community number being corrected is a
// correction. `raid_target_timer` is mutable — unlike the report log — precisely so that applying a
// newer seed does not leave the old window beside the new one for a reader to choose between.
func TestApplySeed_ASecondSeed_ReplacesRatherThanAccumulates(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedEmbedded()

	for _, open := range []string{"60", "90"} {
		parsed, err := catalogue.ParseSeed(strings.NewReader(seedJSON(
			`{"target": "Naggy", "server": "blue", "window_kind": "variance",
			  "window_open_offset_seconds": ` + open + `, "window_close_offset_seconds": 120}`)))
		require.NoError(t, err)
		_, err = f.svc.ApplySeed(t.Context(), parsed, f.inv)
		require.NoError(t, err)
	}

	naggy, err := f.svc.Resolve(t.Context(), catalogue.Ref{Name: "Naggy"})
	require.NoError(t, err)
	timers, err := f.svc.Timers(t.Context(), naggy.Target.ID)
	require.NoError(t, err)
	require.Len(t, timers, 1, "the second seed appended instead of correcting")
	require.Equal(t, int64(90), *timers[0].WindowOpenOffsetSeconds)
}

// TestApplySeed_ASeedNeverTouchesACircleOverride. An override is a circle saying the catalogue is
// wrong; a newer catalogue does not settle that argument, and a seed that cleared overrides would
// silently undo an officer's decision on every upgrade.
func TestApplySeed_ASeedNeverTouchesACircleOverride(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedEmbedded()
	circleID := f.circle("Riot")
	actor := f.member(circleID)

	naggy, err := f.svc.Resolve(t.Context(), catalogue.Ref{Name: "Naggy"})
	require.NoError(t, err)
	_, err = f.svc.PutOverride(t.Context(), circleID, naggy.Target.ID, actor,
		catalogue.WindowRequest{
			WindowKind:               "variance",
			WindowOpenOffsetSeconds:  ptr(int64(300)),
			WindowCloseOffsetSeconds: ptr(int64(400)),
		}, f.inv)
	require.NoError(t, err)

	parsed, err := catalogue.ParseSeed(strings.NewReader(seedJSON(
		`{"target": "Naggy", "server": "blue", "window_kind": "variance",
		  "window_open_offset_seconds": 60, "window_close_offset_seconds": 120}`)))
	require.NoError(t, err)
	_, err = f.svc.ApplySeed(t.Context(), parsed, f.inv)
	require.NoError(t, err)

	resolved, err := f.svc.ResolveTimer(t.Context(), f.store.Queries(), circleID, naggy.Target.ID, core.ServerBlue)
	require.NoError(t, err)
	require.Equal(t, catalogue.TimerSourceCircleOverride, resolved.Source,
		"a seed overrode a circle's own decision")
	require.Equal(t, int64(300), *resolved.Timer.OpenOffsetSeconds)
}

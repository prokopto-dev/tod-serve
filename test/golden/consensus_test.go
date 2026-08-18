// Package golden replays the consensus golden corpus.
//
// docs/design/03-consensus.md says the corpus is the authority when its prose and these fixtures
// disagree, so this file is a specification runner rather than an ordinary unit test. It lives
// under test/golden/ with the fixtures because that path is CODEOWNERS-protected: a change to the
// oracle, or to the thing that decides how much of the oracle counts, is a change to the spec.
package golden

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/consensus"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

// updateCorpus rewrites every fixture's `want` from what the derivation currently produces.
//
// A flag variable is package-level mutable state, which this repository bans; the exemption is
// that the testing package's flags have to be registered somewhere. Its danger is the reason for
// the CI guard below: the fastest route to a green build must never be rewriting the oracle.
var updateCorpus = flag.Bool("update", false, "rewrite the `want` block of every fixture")

// corpusDir holds the fixtures, relative to this file.
const corpusDir = "consensus"

// corpusFloor is the number of fixtures this corpus had when the derivation landed.
//
// It only ever goes up. A fixture that is deleted or renamed away takes a rule with it, and the
// deletion looks exactly like a passing build unless something counts. Raising this number is how
// you record that the specification grew; lowering it needs the review that /test/golden/ already
// demands.
const corpusFloor = 32

// fixture is one whole call to consensus.Derive and its whole expected State.
type fixture struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Now         core.Micros            `json:"now"`
	Circle      consensus.CircleConfig `json:"circle"`
	Timer       consensus.Timer        `json:"timer"`
	Quakes      []consensus.Quake      `json:"quakes"`
	Reports     []consensus.Report     `json:"reports"`
	Want        consensus.State        `json:"want"`
}

// compareAs renders ids and timestamps as the strings they are on the wire.
//
// Both round-trip exactly, so this changes what a failure *reads* like and never what it decides:
// a diff that says `2026-08-24T14:14:07.000000Z` names the window boundary that moved, and one
// that says 1787580847000000 names nothing.
var compareAs = cmp.Options{
	cmp.Transformer("micros", func(m core.Micros) string { return m.String() }),
	cmp.Transformer("reportID", func(id core.TodReportID) string { return id.String() }),
	cmp.Transformer("membershipID", func(id core.MembershipID) string { return id.String() }),
}

func corpusFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(corpusDir)
	require.NoError(t, err)

	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	return names
}

func loadFixture(t *testing.T, name string) fixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(corpusDir, name))
	require.NoError(t, err)

	var f fixture
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&f), "%s does not match the shape in consensus/README.md", name)
	require.Equal(t, strings.TrimSuffix(name, ".json"), f.Name, "fixture name must be its filename")
	require.NotEmpty(t, f.Description, "%s: a fixture with no description pins nothing anybody can read", name)
	return f
}

// TestDerive_GoldenCorpus_MatchesTheSpecification is the specification, executed.
func TestDerive_GoldenCorpus_MatchesTheSpecification(t *testing.T) {
	t.Parallel()

	for _, name := range corpusFiles(t) {
		t.Run(strings.TrimSuffix(name, ".json"), func(t *testing.T) {
			t.Parallel()
			f := loadFixture(t, name)
			got := consensus.Derive(f.Reports, f.Quakes, f.Timer, f.Now, f.Circle)

			if *updateCorpus {
				rewrite(t, name, f, got)
				return
			}
			if diff := cmp.Diff(f.Want, got, compareAs); diff != "" {
				t.Errorf("%s\n\n%s\n\n(-want +got):\n%s", name, f.Description, diff)
			}
		})
	}
}

// rewrite replaces a fixture's `want` with what the derivation produced.
func rewrite(t *testing.T, name string, f fixture, got consensus.State) {
	t.Helper()
	// Refused under CI, per the test-integrity invariants. The corpus is the authority; a build
	// that can rewrite its own oracle is a build that checks nothing.
	require.Empty(t, os.Getenv("CI"), "-update is refused in CI")

	f.Want = got
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	// The fixtures are read by people. Escaping `<` into < inside a description would make
	// every hand edit a diff nobody asked for.
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	require.NoError(t, enc.Encode(f))
	require.NoError(t, os.WriteFile(filepath.Join(corpusDir, name), []byte(buf.String()), 0o644))
}

// TestGoldenCorpus_FixtureCount_NeverShrinks is the test-integrity invariant: a rule cannot leave
// the specification by having its fixture quietly deleted.
func TestGoldenCorpus_FixtureCount_NeverShrinks(t *testing.T) {
	t.Parallel()
	require.GreaterOrEqual(t, len(corpusFiles(t)), corpusFloor,
		"a fixture was deleted or renamed away; raise corpusFloor only when the corpus grows")
}

// TestDerive_ReportsInAnyOrder_SameState pins determinism against the order the store happens to
// return rows in. Derive sorts what it is given, and must not mutate the caller's slice doing it.
func TestDerive_ReportsInAnyOrder_SameState(t *testing.T) {
	t.Parallel()

	for _, name := range corpusFiles(t) {
		t.Run(strings.TrimSuffix(name, ".json"), func(t *testing.T) {
			t.Parallel()
			f := loadFixture(t, name)
			original := slices.Clone(f.Reports)

			forwards := consensus.Derive(f.Reports, f.Quakes, f.Timer, f.Now, f.Circle)
			require.Empty(t, cmp.Diff(original, f.Reports, compareAs),
				"Derive reordered the caller's slice")

			slices.Reverse(f.Reports)
			backwards := consensus.Derive(f.Reports, f.Quakes, f.Timer, f.Now, f.Circle)
			if diff := cmp.Diff(forwards, backwards, compareAs); diff != "" {
				t.Errorf("%s: report order changed the answer (-forwards +reversed):\n%s", name, diff)
			}
		})
	}
}

// TestDerive_ReportedAtPermuted_SameState is `LatestDiedAtWins` over the whole corpus: system
// truth may say anything at all and the answer still comes from game truth.
func TestDerive_ReportedAtPermuted_SameState(t *testing.T) {
	t.Parallel()

	for _, name := range corpusFiles(t) {
		t.Run(strings.TrimSuffix(name, ".json"), func(t *testing.T) {
			t.Parallel()
			f := loadFixture(t, name)
			want := consensus.Derive(f.Reports, f.Quakes, f.Timer, f.Now, f.Circle)

			permuted := slices.Clone(f.Reports)
			for i := range permuted {
				permuted[i].ReportedAt = f.Now - core.Micros(int64(i)*core.MicrosPerSecond)
			}
			if diff := cmp.Diff(want, consensus.Derive(permuted, f.Quakes, f.Timer, f.Now, f.Circle),
				compareAs); diff != "" {
				t.Errorf("%s: reported_at changed the answer (-want +got):\n%s", name, diff)
			}
		})
	}
}

// TestDerive_CorpusCovers_EveryStatusAndConfidence asserts the corpus is not merely large. A
// corpus that never produced `overdue` would pass every day and pin nothing about it.
func TestDerive_CorpusCovers_EveryStatusAndConfidence(t *testing.T) {
	t.Parallel()

	statuses := map[consensus.Status]int{}
	confidences := map[consensus.Confidence]int{}
	reasons := map[consensus.ContestReason]int{}
	kinds := map[consensus.WindowKind]int{}
	for _, name := range corpusFiles(t) {
		f := loadFixture(t, name)
		statuses[f.Want.Status]++
		confidences[f.Want.Confidence]++
		kinds[f.Want.Window.Kind]++
		if f.Want.ContestReason != nil {
			reasons[*f.Want.ContestReason]++
		}
	}

	for _, s := range []consensus.Status{
		consensus.StatusUnknown, consensus.StatusNoTimer, consensus.StatusPreWindow,
		consensus.StatusInWindow, consensus.StatusOverdue, consensus.StatusUp,
	} {
		require.Positive(t, statuses[s], "no fixture produces status %q", s)
	}
	for _, c := range []consensus.Confidence{
		consensus.ConfidenceUnknown, consensus.ConfidenceLow,
		consensus.ConfidenceMedium, consensus.ConfidenceHigh,
	} {
		require.Positive(t, confidences[c], "no fixture produces confidence %q", c)
	}
	for _, r := range []consensus.ContestReason{
		consensus.ContestThinSupersede, consensus.ContestImplausibleOrdering,
		consensus.ContestWideSpread, consensus.ContestPendingSupersede,
	} {
		require.Positive(t, reasons[r], "no fixture produces contest_reason %q", r)
	}
	for _, k := range []consensus.WindowKind{
		consensus.WindowFixed, consensus.WindowVariance, consensus.WindowUnknown,
	} {
		require.Positive(t, kinds[k], "no fixture produces window kind %q", k)
	}
}

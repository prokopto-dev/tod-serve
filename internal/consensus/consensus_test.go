package consensus_test

import (
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/canondoc"
	"github.com/prokopto-dev/tod-serve/internal/consensus"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

// ptrTo is the one-liner a table needs to fill an optional field. Generic so the tables below read
// as data rather than as three near-identical helpers.
func ptrTo[T any](v T) *T { return &v }

func seconds(n int64) core.Micros { return core.Micros(n * core.MicrosPerSecond) }

func reportID(n uint64) core.TodReportID {
	var u core.ULID
	binary.BigEndian.PutUint64(u[8:], n)
	return core.IDFromULID[core.TodReport](u)
}

func membershipID(n uint64) core.MembershipID {
	var u core.ULID
	binary.BigEndian.PutUint64(u[8:], n)
	return core.IDFromULID[core.Membership](u)
}

// vulak is the seven-day-plus-or-minus-twelve-hours shape §3 says ε never binds on.
func vulak() consensus.Timer {
	return consensus.Timer{
		Kind:               consensus.WindowVariance,
		OpenOffsetSeconds:  ptrTo(int64(561600)),
		CloseOffsetSeconds: ptrTo(int64(648000)),
		FixedGraceSeconds:  900,
		IsQuakeTarget:      true,
	}
}

func kill(n uint64, reporter uint64, diedAt core.Micros, source consensus.Source) consensus.Report {
	return consensus.Report{
		ID:                   reportID(n),
		Kind:                 consensus.KindKill,
		DiedAt:               diedAt,
		ReportedAt:           diedAt + seconds(60),
		ReporterMembershipID: membershipID(reporter),
		Source:               source,
	}
}

func TestEpsilonSeconds_EachBranch_MatchesTheSpecification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		timer consensus.Timer
		want  int64
	}{
		{
			name:  "per-timer override wins outright",
			timer: consensus.Timer{Kind: consensus.WindowVariance, OpenOffsetSeconds: ptrTo(int64(561600)), ClusterEpsilonSeconds: ptrTo(int64(60))},
			want:  60,
		},
		{
			name:  "override wins even for an unknown window",
			timer: consensus.Timer{Kind: consensus.WindowUnknown, ClusterEpsilonSeconds: ptrTo(int64(120))},
			want:  120,
		},
		{
			name:  "a negative override reads as zero rather than as an error",
			timer: consensus.Timer{Kind: consensus.WindowVariance, OpenOffsetSeconds: ptrTo(int64(3600)), ClusterEpsilonSeconds: ptrTo(int64(-1))},
			want:  0,
		},
		{
			name:  "window_open/4 when it lands inside the clamp",
			timer: consensus.Timer{Kind: consensus.WindowVariance, OpenOffsetSeconds: ptrTo(int64(3600))},
			want:  900,
		},
		{
			name:  "clamped up to five minutes for a short window",
			timer: consensus.Timer{Kind: consensus.WindowVariance, OpenOffsetSeconds: ptrTo(int64(600))},
			want:  300,
		},
		{
			name:  "clamped down to thirty minutes for a raid dragon",
			timer: consensus.Timer{Kind: consensus.WindowVariance, OpenOffsetSeconds: ptrTo(int64(561600))},
			want:  1800,
		},
		{
			name:  "exactly at the five-minute floor",
			timer: consensus.Timer{Kind: consensus.WindowVariance, OpenOffsetSeconds: ptrTo(int64(1200))},
			want:  300,
		},
		{
			name:  "exactly at the thirty-minute ceiling",
			timer: consensus.Timer{Kind: consensus.WindowVariance, OpenOffsetSeconds: ptrTo(int64(7200))},
			want:  1800,
		},
		{
			name:  "thirty minutes flat for an unknown window",
			timer: consensus.Timer{Kind: consensus.WindowUnknown},
			want:  1800,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, consensus.EpsilonSeconds(tt.timer))
		})
	}
}

func TestConfidence_Rank_MatchesTheCanonicalOrdering(t *testing.T) {
	t.Parallel()

	// The ordering is read out of the tie-breaker document rather than repeated here: a copy would
	// make this test agree with itself, and the pair that drifts is the code and the document.
	doc, err := canondoc.LoadCanonical()
	require.NoError(t, err)
	require.Contains(t, doc.Raw(), "`unknown < low < medium < high`")

	ordered := []consensus.Confidence{
		consensus.ConfidenceUnknown, consensus.ConfidenceLow,
		consensus.ConfidenceMedium, consensus.ConfidenceHigh,
	}
	for i, c := range ordered {
		rank, ok := c.Rank()
		require.True(t, ok, "%q has no rank", c)
		require.Equal(t, i, rank)
		require.True(t, c.AtLeast(c))
		if i > 0 {
			require.True(t, c.AtLeast(ordered[i-1]))
			require.False(t, ordered[i-1].AtLeast(c))
		}
	}
}

func TestConfidence_UnrecognisedValue_IsBelowEverything(t *testing.T) {
	t.Parallel()

	// A value nobody taught the enum about must not pass a threshold check. Failing closed here is
	// the difference between a bad row being invisible and a bad row being trusted.
	bogus := consensus.Confidence("very")
	_, ok := bogus.Rank()
	require.False(t, ok)
	require.False(t, bogus.AtLeast(consensus.ConfidenceUnknown))
	require.False(t, consensus.ConfidenceHigh.AtLeast(bogus))
}

func TestDerive_NoReports_IsUnknownAndNotNoTimer(t *testing.T) {
	t.Parallel()

	got := consensus.Derive(nil, nil, vulak(), seconds(1000), consensus.CircleConfig{MinReportersToSupersede: 1})
	require.Equal(t, consensus.StatusUnknown, got.Status)
	require.Nil(t, got.DiedAt)
	require.Equal(t, consensus.ConfidenceUnknown, got.Confidence)
	// The timer is still reported: "we know the window and have no ToD" is not "we know neither".
	require.Equal(t, consensus.WindowVariance, got.Window.Kind)
	require.Nil(t, got.Window.OpenAt)
}

func TestDerive_ProgressBP_NowFarPastClose_ClampsWithoutOverflowing(t *testing.T) {
	t.Parallel()

	// (now - open) * 10000 overflows int64 long before Micros itself does. Clamping before the
	// multiplication is what keeps this a missing answer rather than a wrong one.
	const farFuture = core.Micros(8_000_000_000_000_000_000)
	got := consensus.Derive(
		[]consensus.Report{kill(1, 1, 0, consensus.SourceLogLine)},
		nil, vulak(), farFuture, consensus.CircleConfig{MinReportersToSupersede: 1})

	require.Equal(t, consensus.StatusOverdue, got.Status)
	require.NotNil(t, got.Window.ProgressBP)
	require.Equal(t, int32(10000), *got.Window.ProgressBP)
}

func TestDerive_ProgressBP_NowFarBeforeOpen_ClampsToZero(t *testing.T) {
	t.Parallel()

	got := consensus.Derive(
		[]consensus.Report{kill(1, 1, seconds(3_000_000_000), consensus.SourceLogLine)},
		nil, vulak(), 0, consensus.CircleConfig{MinReportersToSupersede: 1})

	require.Equal(t, consensus.StatusPreWindow, got.Status)
	require.NotNil(t, got.Window.ProgressBP)
	require.Equal(t, int32(0), *got.Window.ProgressBP)
}

func TestDerive_QuakeAtExactlyDiedAt_ReportSurvives(t *testing.T) {
	t.Parallel()

	// §2 truncates `died_at < Q`. A kill in the same microsecond a quake fired is not evidence of
	// anything wrong, and throwing it away would be a rule the document does not state.
	at := seconds(1000)
	got := consensus.Derive(
		[]consensus.Report{kill(1, 1, at, consensus.SourceLogLine)},
		[]consensus.Quake{{ID: core.QuakeEventID{}, OccurredAt: at}},
		vulak(), at+seconds(60), consensus.CircleConfig{MinReportersToSupersede: 1})

	require.Equal(t, consensus.StatusPreWindow, got.Status)
	require.NotNil(t, got.DiedAt)
	require.Equal(t, at, *got.DiedAt)
	require.Nil(t, got.UpSince)
}

func TestDerive_NonQuakeTarget_IgnoresQuakesEntirely(t *testing.T) {
	t.Parallel()

	timer := vulak()
	timer.IsQuakeTarget = false
	at := seconds(1000)
	got := consensus.Derive(
		[]consensus.Report{kill(1, 1, at, consensus.SourceLogLine)},
		[]consensus.Quake{{OccurredAt: at + seconds(3600)}},
		timer, at+seconds(7200), consensus.CircleConfig{MinReportersToSupersede: 1})

	require.Equal(t, consensus.StatusPreWindow, got.Status)
	require.Nil(t, got.UpSince)
}

func TestDerive_RetractionOfAnAbsentReport_IsIgnored(t *testing.T) {
	t.Parallel()

	// A retraction naming a report this call was not handed can drop nothing. Treating the id as
	// authoritative anyway would let a page boundary erase a kill.
	reports := []consensus.Report{
		kill(1, 1, 0, consensus.SourceLogLine),
		{
			ID:                   reportID(2),
			Kind:                 consensus.KindRetraction,
			ReporterMembershipID: membershipID(1),
			Source:               consensus.SourceManual,
			RetractsReportID:     ptrTo(reportID(99)),
		},
	}
	got := consensus.Derive(reports, nil, vulak(), seconds(600000), consensus.CircleConfig{MinReportersToSupersede: 1})
	require.Equal(t, 1, got.Evidence.ReportCount)
}

func TestDerive_MinReportersToSupersedeZero_BehavesAsOne(t *testing.T) {
	t.Parallel()

	// The column defaults to 1 and is CHECK-constrained, but a zero value reaching here must not
	// mean "no cluster may ever supersede". The latest kill wins, which is the documented default.
	reports := []consensus.Report{
		kill(1, 1, 0, consensus.SourceLogLine),
		kill(2, 2, seconds(600000), consensus.SourceManual),
	}
	got := consensus.Derive(reports, nil, vulak(), seconds(610000), consensus.CircleConfig{})
	require.NotNil(t, got.DiedAt)
	require.Equal(t, seconds(600000), *got.DiedAt)
}

func TestDerive_LogLineCluster_ManualReportsDoNotMoveTheEstimate(t *testing.T) {
	t.Parallel()

	// §5. The manual reports would drag the median forward by four minutes if they estimated; they
	// are corroboration instead.
	withManual := []consensus.Report{
		kill(1, 1, 0, consensus.SourceLogLine),
		kill(2, 2, seconds(60), consensus.SourceLogLine),
		kill(3, 3, seconds(120), consensus.SourceLogLine),
		kill(4, 4, seconds(240), consensus.SourceManual),
		kill(5, 5, seconds(300), consensus.SourceManual),
	}
	logLinesOnly := withManual[:3]
	cfg := consensus.CircleConfig{MinReportersToSupersede: 1}

	got := consensus.Derive(withManual, nil, vulak(), seconds(600000), cfg)
	want := consensus.Derive(logLinesOnly, nil, vulak(), seconds(600000), cfg)
	require.Equal(t, want.DiedAt, got.DiedAt)
	require.Equal(t, seconds(60), *got.DiedAt)
	// The corroboration is still counted, which is the whole point of keeping it.
	require.Equal(t, 5, got.Evidence.ReportCount)
	require.Equal(t, 3, got.Evidence.LogLineCount)
}

func TestDerive_EveryCollection_MarshalsAsAnArrayNeverNull(t *testing.T) {
	t.Parallel()

	// A client branching on `alternatives.length` must not meet `null`. Empty is a fact; null is a
	// question.
	got := consensus.Derive(nil, nil, vulak(), seconds(1000), consensus.CircleConfig{MinReportersToSupersede: 1})
	require.NotNil(t, got.Alternatives)
	require.NotNil(t, got.ImplausibleReportIDs)
	require.NotNil(t, got.Evidence.ReportIDs)

	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "null,\"alternatives_total\"")
	require.Contains(t, string(encoded), `"alternatives":[]`)
	require.Contains(t, string(encoded), `"report_ids":[]`)
	require.Contains(t, string(encoded), `"implausible_report_ids":[]`)
}

func TestDerive_SpawnAt_PresentExactlyWhenTheTimerIsFixedAndAWindowExists(t *testing.T) {
	t.Parallel()

	fixed := consensus.Timer{
		Kind:               consensus.WindowFixed,
		OpenOffsetSeconds:  ptrTo(int64(21600)),
		CloseOffsetSeconds: ptrTo(int64(21600)),
		FixedGraceSeconds:  900,
	}
	cfg := consensus.CircleConfig{MinReportersToSupersede: 1}
	reports := []consensus.Report{kill(1, 1, 0, consensus.SourceLogLine)}

	withWindow := consensus.Derive(reports, nil, fixed, seconds(22000), cfg)
	require.NotNil(t, withWindow.Window.SpawnAt)
	require.Equal(t, *withWindow.Window.OpenAt, *withWindow.Window.SpawnAt)
	require.Nil(t, withWindow.Window.ProgressBP, "a fixed timer has no band to be part-way through")

	// No ToD, so no window and therefore no spawn instant to name.
	withoutWindow := consensus.Derive(nil, nil, fixed, seconds(22000), cfg)
	require.Equal(t, consensus.WindowFixed, withoutWindow.Window.Kind)
	require.Nil(t, withoutWindow.Window.SpawnAt)

	variance := consensus.Derive(reports, nil, vulak(), seconds(600000), cfg)
	require.Nil(t, variance.Window.SpawnAt)
	require.NotNil(t, variance.Window.ProgressBP)
}

func TestDerive_CalledTwice_ReturnsEqualStates(t *testing.T) {
	t.Parallel()

	// `Deterministic` in the invariants table. Map iteration order is the usual way a Go
	// derivation stops being one, and every count above is built from a map.
	reports := []consensus.Report{
		kill(1, 1, 0, consensus.SourceLogLine),
		kill(2, 2, seconds(60), consensus.SourceManual),
		kill(3, 3, seconds(600000), consensus.SourceManual),
		kill(4, 4, seconds(600060), consensus.SourceLogLine),
	}
	cfg := consensus.CircleConfig{MinReportersToSupersede: 2}
	first := consensus.Derive(reports, nil, vulak(), seconds(605000), cfg)
	for range 50 {
		require.Empty(t, cmp.Diff(first, consensus.Derive(reports, nil, vulak(), seconds(605000), cfg)))
	}
}

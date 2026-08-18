package consensus

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/core"
)

// clusterPropertySeed seeds the generator below.
//
// `math/rand` is banned by depguard because the derivation must be replayable byte-identically,
// and a property test that cannot be replayed reports a failure nobody can reproduce. The
// generator is a splitmix64 in eleven lines and the seed is written down, so a red build here is a
// red build on every machine.
const clusterPropertySeed uint64 = 0x9E3779B97F4A7C15

// splitmix64 is the generator. It is a struct rather than a package-level variable because
// package-level mutable state is banned, and because two parallel tests sharing one stream would
// make each other's failures unreproducible.
type splitmix64 struct{ state uint64 }

func (s *splitmix64) next() uint64 {
	s.state += 0x9E3779B97F4A7C15
	z := s.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// intn returns a value in [0, n).
func (s *splitmix64) intn(n int64) int64 { return int64(s.next() % uint64(n)) }

// testReportID mints a distinct, ordered id without a generator or a clock.
func testReportID(n uint64) core.TodReportID {
	var u core.ULID
	binary.BigEndian.PutUint64(u[8:], n)
	return core.IDFromULID[core.TodReport](u)
}

func testMembershipID(n uint64) core.MembershipID {
	var u core.ULID
	binary.BigEndian.PutUint64(u[8:], n)
	return core.IDFromULID[core.Membership](u)
}

// reportsAt builds one report per offset, each from its own reporter.
func reportsAt(offsets []int64) []Report {
	out := make([]Report, 0, len(offsets))
	for i, off := range offsets {
		out = append(out, Report{
			ID:                   testReportID(uint64(i) + 1),
			Kind:                 KindKill,
			DiedAt:               core.Micros(off * core.MicrosPerSecond),
			ReporterMembershipID: testMembershipID(uint64(i) + 1),
			Source:               SourceManual,
		})
	}
	return out
}

// requireSpanBounded is the `ClusterSpanBounded` invariant: no cluster spans more than 2ε, no gap
// inside one exceeds ε, and every report lands in exactly one cluster.
func requireSpanBounded(t *testing.T, clusters [][]Report, in []Report, epsilon core.Micros) {
	t.Helper()

	seen := 0
	var previousStart core.Micros
	for i, members := range clusters {
		require.NotEmpty(t, members, "cluster %d is empty", i)
		seen += len(members)

		span := members[len(members)-1].DiedAt - members[0].DiedAt
		require.LessOrEqual(t, int64(span), int64(2*epsilon),
			"cluster %d spans %d micros with epsilon %d — reports chained into a fictitious kill",
			i, span, epsilon)

		for j := 1; j < len(members); j++ {
			gap := members[j].DiedAt - members[j-1].DiedAt
			require.LessOrEqual(t, int64(gap), int64(epsilon),
				"cluster %d joins reports %d micros apart, further than epsilon %d", i, gap, epsilon)
		}
		if i > 0 {
			require.Greater(t, int64(members[0].DiedAt), int64(previousStart),
				"clusters are not in died_at order")
		}
		previousStart = members[0].DiedAt
	}
	require.Equal(t, len(in), seen, "clustering lost or duplicated a report")
}

func TestCluster_GeneratedSequences_SpanNeverExceedsTwoEpsilon(t *testing.T) {
	t.Parallel()

	// Epsilons chosen so the sweep is exercised at both clamps and at the per-timer override.
	epsilons := []int64{60, 300, 900, 1800}
	rng := &splitmix64{state: clusterPropertySeed}

	for run := range 500 {
		epsilonSeconds := epsilons[rng.intn(int64(len(epsilons)))]
		count := int(rng.intn(40)) + 1

		offsets := make([]int64, 0, count)
		at := int64(0)
		for range count {
			// Gaps run from zero to 1.5ε, so a run produces both merges and splits rather than
			// only one of the two — a generator that always split would prove nothing about spans.
			at += rng.intn(epsilonSeconds*3/2 + 1)
			offsets = append(offsets, at)
		}

		in := reportsAt(offsets)
		epsilon := core.Micros(epsilonSeconds * core.MicrosPerSecond)
		requireSpanBounded(t, cluster(in, epsilon), in, epsilon)
		require.Equal(t, reportsAt(offsets), in, "run %d: cluster mutated its input", run)
	}
}

func TestCluster_AdversarialSequences_SpanNeverExceedsTwoEpsilon(t *testing.T) {
	t.Parallel()

	const epsilonSeconds = 1800
	epsilon := core.Micros(epsilonSeconds * core.MicrosPerSecond)

	tests := []struct {
		name     string
		offsets  []int64
		clusters []int // report count per cluster
	}{
		{"all at the same instant", []int64{0, 0, 0, 0}, []int{4}},
		{"gaps exactly epsilon", []int64{0, 1800, 3600, 5400, 7200}, []int{3, 2}},
		{"gaps one second over epsilon", []int64{0, 1801, 3602}, []int{1, 1, 1}},
		{"the 29-minute chain from §3", []int64{0, 1740, 3480, 5220, 6960}, []int{3, 2}},
		{"a single report", []int64{0}, []int{1}},
		{"one long run of zero gaps", []int64{0, 0, 0, 0, 0, 0, 0, 0}, []int{8}},
		{"span cap bites on the third report", []int64{0, 1800, 3601}, []int{2, 1}},
		{"span exactly 2 epsilon still merges", []int64{0, 1800, 1800, 3600, 3600}, []int{5}},
		{"span one second past 2 epsilon splits", []int64{0, 1800, 1800, 3600, 3601}, []int{4, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := reportsAt(tt.offsets)
			got := cluster(in, epsilon)
			requireSpanBounded(t, got, in, epsilon)

			sizes := make([]int, 0, len(got))
			for _, members := range got {
				sizes = append(sizes, len(members))
			}
			require.Equal(t, tt.clusters, sizes)
		})
	}
}

func TestCluster_ZeroEpsilon_MergesOnlyIdenticalInstants(t *testing.T) {
	t.Parallel()

	// An operator can set cluster_epsilon_seconds to zero. It is honest rather than an error: every
	// report becomes its own kill unless two land in the same microsecond.
	in := reportsAt([]int64{0, 0, 1, 1, 1})
	got := cluster(in, 0)
	requireSpanBounded(t, got, in, 0)
	require.Len(t, got, 2)
	require.Len(t, got[0], 2)
	require.Len(t, got[1], 3)
}

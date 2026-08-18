package consensus

import (
	"cmp"
	"slices"

	"github.com/prokopto-dev/tod-serve/internal/core"
)

// The ε bounds from §3. Thirty minutes covers the ordinary case of someone typing a ToD in after
// the raid; the `/4` term exists so a short-timer target added later behaves correctly rather than
// merging two genuine kills, and the floor means it never binds on a real raid dragon.
const (
	epsilonMinSeconds     = 5 * 60
	epsilonMaxSeconds     = 30 * 60
	epsilonDefaultSeconds = 30 * 60
	epsilonWindowDivisor  = 4
)

// EpsilonSeconds resolves the clustering threshold for a timer, in seconds:
//
//	ε = timer.cluster_epsilon_seconds
//	    ?? clamp(window_open_offset_seconds / 4, 5 min, 30 min)
//	    ?? 30 min                                  -- window_kind = 'unknown'
//
// It is exported because §9 wants every merge decision logged, and a caller that logs one has to
// be able to say which threshold produced it.
//
// A negative override reads as zero rather than as an error: ε is not a value the derivation can
// refuse, and a negative threshold would make the sweep's arithmetic mean something it does not.
// Zero is honest — every report is then its own kill — and it is visible in the log line.
func EpsilonSeconds(t Timer) int64 {
	if t.ClusterEpsilonSeconds != nil {
		return max(*t.ClusterEpsilonSeconds, 0)
	}
	if t.Kind == WindowUnknown || t.OpenOffsetSeconds == nil {
		return epsilonDefaultSeconds
	}
	return min(max(*t.OpenOffsetSeconds/epsilonWindowDivisor, epsilonMinSeconds), epsilonMaxSeconds)
}

// cluster groups reports into kill events by the §3 sweep.
//
// Sort by died_at, extend the cluster while the next report is within ε of the *previous* one, and
// cap the total span at 2ε. The cap is not decoration: without it five reports 29 minutes apart
// chain into a single two-hour "kill", which is the hazard
// test/golden/consensus/epsilon_chaining_hazard.json is named after.
//
// The input is not mutated. A caller handing over the slice it read out of the store would
// otherwise find it reordered, and the derivation is meant to be a function of its arguments and
// not an event in their lives.
func cluster(reports []Report, epsilon core.Micros) [][]Report {
	sorted := slices.Clone(reports)
	slices.SortFunc(sorted, func(a, b Report) int {
		// The id breaks the tie so two reports of the same instant have one order and not two.
		// Determinism here is the whole reason this package exists in the shape it does.
		if c := cmp.Compare(a.DiedAt, b.DiedAt); c != 0 {
			return c
		}
		return a.ID.Compare(b.ID)
	})

	out := make([][]Report, 0, len(sorted))
	for _, r := range sorted {
		if len(out) > 0 {
			current := out[len(out)-1]
			previous := current[len(current)-1]
			if r.DiedAt-previous.DiedAt <= epsilon && r.DiedAt-current[0].DiedAt <= 2*epsilon {
				out[len(out)-1] = append(current, r)
				continue
			}
		}
		out = append(out, []Report{r})
	}
	return out
}

// estimate is the §5 point estimate for one cluster: the median, and the median of the log lines
// alone if the cluster has any. Manual reports in a cluster that contains log lines are
// corroboration — they raise confidence — but they are not estimators.
//
// On an even count it takes the earlier of the two middle values. An early window costs a wasted
// trip; a late window costs a missed spawn. §8 flags that judgement for a raid leader to confirm.
//
// members must be non-empty and in died_at order, which is what [cluster] produces.
func estimate(members []Report) core.Micros {
	estimators := members
	if slices.ContainsFunc(members, func(r Report) bool { return r.Source == SourceLogLine }) {
		estimators = make([]Report, 0, len(members))
		for _, r := range members {
			if r.Source == SourceLogLine {
				estimators = append(estimators, r)
			}
		}
	}
	n := len(estimators)
	if n%2 == 1 {
		return estimators[n/2].DiedAt
	}
	return estimators[n/2-1].DiedAt
}

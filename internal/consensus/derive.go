package consensus

import "github.com/prokopto-dev/tod-serve/internal/core"

const (
	// spreadToleranceSeconds is the five minutes §7 sets between `medium` and `high`. The
	// `wide_spread` contest reason reuses it rather than inventing a second threshold: the
	// document names one number for "these reporters agree", and two would drift.
	spreadToleranceSeconds = 5 * 60

	// maxAlternatives is §7's cap. Everything past it is history and is one listTodReports call
	// away — [State.AlternativesTotal] says how much was dropped.
	maxAlternatives = 3
)

// spreadTolerance is spreadToleranceSeconds as an interval, spelled once so no call site has to
// remember which side of the comparison is in seconds.
const spreadTolerance = core.Micros(spreadToleranceSeconds) * core.Micros(core.MicrosPerSecond)

// Derive computes the current state of one target from its report log.
//
// It is a pure function: the same arguments produce a byte-identical [State] on every platform,
// which is what lets the nightly projection-verify job diff a cached row against a fresh
// recomputation and believe the difference. `now` is a parameter for the same reason.
//
// The steps run in the order docs/design/03-consensus.md sets out; see the package comment.
func Derive(reports []Report, quakes []Quake, timer Timer, now core.Micros, cfg CircleConfig) State {
	state := State{
		Status:               StatusUnknown,
		Window:               deriveWindow(timer, nil, now),
		Confidence:           ConfidenceUnknown,
		Alternatives:         []Alternative{},
		Evidence:             Evidence{ReportIDs: []core.TodReportID{}},
		ImplausibleReportIDs: []core.TodReportID{},
		AsOf:                 now,
	}

	kills := foldRetractions(reports)
	kills, upSince := truncateAtQuake(kills, quakes, timer)
	state.UpSince = upSince

	clusters := cluster(kills, core.Micros(EpsilonSeconds(timer)*core.MicrosPerSecond))
	if len(clusters) == 0 {
		state.Status = deriveStatus(timer, nil, now, upSince)
		return state
	}

	currentIndex, pending := selectCurrent(clusters, timer, now, cfg)
	current := clusters[currentIndex]
	died := estimate(current)

	state.DiedAt = &died
	state.Status = deriveStatus(timer, &died, now, upSince)
	state.Window = deriveWindow(timer, &died, now)
	state.Confidence = confidenceOf(current, died)
	state.Evidence = evidenceOf(current)
	state.ImplausibleReportIDs = implausibleReportIDs(clusters, currentIndex, timer)
	state.Alternatives, state.AlternativesTotal = alternativesOf(clusters, currentIndex, timer, now)

	if reason, ok := contestReason(clusters, currentIndex, timer, now, pending,
		state.ImplausibleReportIDs); ok {
		state.Contested, state.ContestReason = true, &reason
	}
	return state
}

// foldRetractions applies §1: drop every kill a retraction names.
//
// A retraction of a retraction is not supported — post a fresh kill report instead — so a
// retraction whose target is itself a retraction is ignored and the first one still stands.
// Reading it as an undo would resurrect a report somebody deliberately withdrew, from a row the
// API refuses to create in the first place (`409 already_retracted`).
func foldRetractions(reports []Report) []Report {
	kinds := make(map[core.TodReportID]ReportKind, len(reports))
	for _, r := range reports {
		kinds[r.ID] = r.Kind
	}

	retracted := make(map[core.TodReportID]struct{}, len(reports))
	for _, r := range reports {
		if r.Kind != KindRetraction || r.RetractsReportID == nil {
			continue
		}
		if kind, known := kinds[*r.RetractsReportID]; known && kind != KindKill {
			continue
		}
		retracted[*r.RetractsReportID] = struct{}{}
	}

	kills := make([]Report, 0, len(reports))
	for _, r := range reports {
		if r.Kind != KindKill {
			continue
		}
		if _, gone := retracted[r.ID]; gone {
			continue
		}
		kills = append(kills, r)
	}
	return kills
}

// truncateAtQuake applies §2. Everything before the latest quake is history and cannot form the
// current cluster; with nothing after it, the target is up and the returned instant is `up_since`.
//
// A kill at exactly the quake instant survives: the boundary is `died_at < Q`, and a target that
// died in the same microsecond a quake fired is a report we have no reason to throw away.
func truncateAtQuake(kills []Report, quakes []Quake, timer Timer) ([]Report, *core.Micros) {
	if !timer.IsQuakeTarget || len(quakes) == 0 {
		return kills, nil
	}

	latest := quakes[0].OccurredAt
	for _, q := range quakes[1:] {
		if q.OccurredAt > latest {
			latest = q.OccurredAt
		}
	}

	after := make([]Report, 0, len(kills))
	for _, r := range kills {
		if r.DiedAt >= latest {
			after = append(after, r)
		}
	}
	if len(after) == 0 {
		return after, &latest
	}
	return after, nil
}

// selectCurrent applies §4: the latest died_at wins, unless `min_reporters_to_supersede` holds a
// live, better corroborated cluster in place. It reports the chosen index and whether the choice
// was a pending supersede.
//
// "has more" is read strictly: the earlier cluster must have more distinct reporters than the
// latest one, not merely meet the threshold. A rule that let an equally thin earlier cluster hold
// the board would freeze the answer on whichever report happened to arrive first, which is the
// last-write-wins failure with the sign flipped.
func selectCurrent(clusters [][]Report, timer Timer, now core.Micros, cfg CircleConfig) (int, bool) {
	latest := len(clusters) - 1
	if cfg.MinReportersToSupersede <= 1 {
		return latest, false
	}
	thin := distinctReporters(clusters[latest])
	if thin >= cfg.MinReportersToSupersede {
		return latest, false
	}
	for i := latest - 1; i >= 0; i-- {
		if !windowStillOpen(timer, estimate(clusters[i]), now) {
			continue
		}
		if distinctReporters(clusters[i]) > thin {
			return i, true
		}
	}
	return latest, false
}

// contestReason picks the one reason §7 has room for.
//
// The precedence is the specification's silence resolved toward the worst news first. A
// contradicted answer (`implausible_ordering`) is worse than one the circle's own threshold is
// holding back (`pending_supersede`), which is worse than a thin answer displacing a corroborated
// one (`thin_supersede`), which is worse than an answer whose own reporters disagree
// (`wide_spread`). test/golden/consensus/README.md records the choice; `implausible_ordering.json`
// and `epsilon_chaining_hazard.json` pin it.
func contestReason(clusters [][]Report, currentIndex int, timer Timer, now core.Micros,
	pending bool, implausible []core.TodReportID,
) (ContestReason, bool) {
	switch {
	case len(implausible) > 0:
		return ContestImplausibleOrdering, true
	case pending:
		return ContestPendingSupersede, true
	case thinlySuperseded(clusters, currentIndex, timer, now):
		return ContestThinSupersede, true
	case spread(clusters[currentIndex]) > spreadTolerance:
		return ContestWideSpread, true
	default:
		return "", false
	}
}

// thinlySuperseded reports whether the current cluster displaced a live one with more distinct
// reporters behind it — §4's second refinement.
func thinlySuperseded(clusters [][]Report, currentIndex int, timer Timer, now core.Micros) bool {
	thin := distinctReporters(clusters[currentIndex])
	for i := range currentIndex {
		if !windowStillOpen(timer, estimate(clusters[i]), now) {
			continue
		}
		if distinctReporters(clusters[i]) > thin {
			return true
		}
	}
	return false
}

// implausibleReportIDs names the observations that cannot be true alongside the current answer.
//
// Two kills of the same target must be at least one respawn apart, so two retained clusters closer
// together than `window_open_offset_seconds` contradict each other. §8 says the report is flagged
// and kept — derived state must never veto an observation — so this returns ids rather than
// filtering anything, and the contradiction surfaces as `contest_reason`.
//
// An unknown timer flags nothing. There is no respawn interval to measure against, and inventing
// one to accuse a report with would be exactly the confident mistake this project is built against.
func implausibleReportIDs(clusters [][]Report, currentIndex int, timer Timer) []core.TodReportID {
	out := []core.TodReportID{}
	if timer.Kind == WindowUnknown || timer.OpenOffsetSeconds == nil {
		return out
	}
	respawn := core.Micros(*timer.OpenOffsetSeconds * core.MicrosPerSecond)
	current := estimate(clusters[currentIndex])
	for i, members := range clusters {
		if i == currentIndex {
			continue
		}
		if absMicros(estimate(members)-current) >= respawn {
			continue
		}
		for _, r := range members {
			out = append(out, r.ID)
		}
	}
	return out
}

// alternativesOf renders §7's rival clusters — live window only, newest first, capped at three —
// and reports how many there were before the cap.
//
// They are surfaced whether or not the state is contested. A second live cluster is something the
// officer needs to see; tying its visibility to a flag would hide rows on exactly the states no
// contest reason happens to describe.
func alternativesOf(clusters [][]Report, currentIndex int, timer Timer, now core.Micros,
) ([]Alternative, int) {
	live := make([]Alternative, 0, len(clusters))
	for i := len(clusters) - 1; i >= 0; i-- {
		if i == currentIndex {
			continue
		}
		died := estimate(clusters[i])
		if !windowStillOpen(timer, died, now) {
			continue
		}
		ev := evidenceOf(clusters[i])
		live = append(live, Alternative{
			DiedAt:                died,
			ReportCount:           ev.ReportCount,
			DistinctReporterCount: ev.DistinctReporterCount,
			Confidence:            confidenceOf(clusters[i], died),
			Window:                deriveWindow(timer, &died, now),
			ReportIDs:             ev.ReportIDs,
		})
	}
	total := len(live)
	if total > maxAlternatives {
		live = live[:maxAlternatives]
	}
	return live, total
}

// evidenceOf counts §7's evidence block over one cluster. members must be in died_at order.
func evidenceOf(members []Report) Evidence {
	ev := Evidence{
		ReportCount: len(members),
		ReportIDs:   make([]core.TodReportID, 0, len(members)),
	}
	reporters := make(map[core.MembershipID]struct{}, len(members))
	revoked := make(map[core.MembershipID]struct{}, len(members))
	for _, r := range members {
		ev.ReportIDs = append(ev.ReportIDs, r.ID)
		reporters[r.ReporterMembershipID] = struct{}{}
		if r.ReporterRevoked {
			revoked[r.ReporterMembershipID] = struct{}{}
		}
		if r.Source == SourceLogLine {
			ev.LogLineCount++
		}
	}
	ev.DistinctReporterCount = len(reporters)
	ev.RevokedReporterCount = len(revoked)
	ev.SpreadSeconds = int64(spread(members)) / core.MicrosPerSecond
	return ev
}

// confidenceOf applies the §7 table.
//
// Two of its rows leave a gap. `import` is named nowhere, and reads as `low`: a bulk-imported row
// is the weakest evidence in the system. And "≥1 log_line reporter plus ≥1 **corroborating**
// reporter" never defines corroboration, so it is read as "within the same five minutes the row
// above uses" — a second reporter forty minutes off the estimate corroborates nothing, and calling
// that `high` would be a confident mistake.
func confidenceOf(members []Report, died core.Micros) Confidence {
	if len(members) == 0 {
		return ConfidenceUnknown
	}
	reporters := make(map[core.MembershipID]struct{}, len(members))
	logLine := make(map[core.MembershipID]struct{}, len(members))
	for _, r := range members {
		reporters[r.ReporterMembershipID] = struct{}{}
		if r.Source == SourceLogLine {
			logLine[r.ReporterMembershipID] = struct{}{}
		}
	}

	if len(reporters) == 1 {
		if len(logLine) > 0 {
			return ConfidenceMedium
		}
		return ConfidenceLow
	}
	if spread(members) <= spreadTolerance {
		return ConfidenceHigh
	}
	if corroborated(members, logLine, died) {
		return ConfidenceHigh
	}
	return ConfidenceMedium
}

// corroborated reports whether some log-line reporter has a *different* reporter sitting on the
// estimate with them.
func corroborated(members []Report, logLine map[core.MembershipID]struct{}, died core.Micros) bool {
	if len(logLine) == 0 {
		return false
	}
	for _, r := range members {
		if absMicros(r.DiedAt-died) > spreadTolerance {
			continue
		}
		if len(logLine) > 1 {
			return true
		}
		if _, self := logLine[r.ReporterMembershipID]; !self {
			return true
		}
	}
	return false
}

// distinctReporters counts the memberships behind a cluster, which is what §4 and §7 both weigh —
// four reports from one person are one person.
func distinctReporters(members []Report) int {
	reporters := make(map[core.MembershipID]struct{}, len(members))
	for _, r := range members {
		reporters[r.ReporterMembershipID] = struct{}{}
	}
	return len(reporters)
}

// spread is the width of a cluster. members must be in died_at order.
func spread(members []Report) core.Micros {
	if len(members) == 0 {
		return 0
	}
	return members[len(members)-1].DiedAt - members[0].DiedAt
}

// absMicros is the magnitude of an interval.
func absMicros(m core.Micros) core.Micros {
	if m < 0 {
		return -m
	}
	return m
}

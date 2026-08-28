package projection

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// AlertMessage is the log message the verify job emits for every discrepancy it finds, and once
// more as a summary.
//
// It is a constant because it is what an operator greps for and what an alerting rule matches on.
// A message assembled at the call site is a message that changes the day somebody rewords it, and
// the alert stops firing without anybody editing the alert.
const AlertMessage = "target state cache disagreed with a recomputation"

// Discrepancy is one field of one cached row that did not survive a recomputation.
//
// It names both values because "the cache was wrong" is not actionable and "the cache said
// `in_window` and the log says `overdue`" is. The recomputation is authoritative and has already
// been written by the time a caller sees this.
type Discrepancy struct {
	CircleID core.CircleID     `json:"circle_id"`
	TargetID core.RaidTargetID `json:"target_id"`
	Field    string            `json:"field"`
	Cached   string            `json:"cached"`
	Computed string            `json:"computed"`
}

// String renders one discrepancy for a log line.
func (d Discrepancy) String() string {
	return fmt.Sprintf("%s/%s %s: cached %q, computed %q",
		d.CircleID, d.TargetID, d.Field, d.Cached, d.Computed)
}

// VerifyReport is what one run of the nightly job found and did.
type VerifyReport struct {
	CirclesChecked int `json:"circles_checked"`
	TargetsChecked int `json:"targets_checked"`
	// Repaired is how many cached rows the recomputation overwrote.
	Repaired int `json:"repaired"`
	// Orphans is how many cached rows named a target with NO rows in `tod_report` at all. They are
	// removed, and each one alerts.
	//
	// No ordinary path produces one: a cache row is written only for a target that has a log — see
	// [Service.storeOrDrop] — and the log is append-only, so a target that has ever been reported
	// keeps its rows forever. An orphan is therefore a row something wrote that should not have,
	// which is worth waking somebody for. A target whose every kill was RETRACTED is not an
	// orphan: it still has a log, and its row still says what folding that log produces.
	Orphans int `json:"orphans"`
	// Discrepancies are the individual field disagreements behind `Repaired`, so a run that
	// repaired something says what it was rather than only how many.
	Discrepancies []Discrepancy `json:"discrepancies"`
	AsOf          core.Micros   `json:"as_of"`
}

// Healthy reports whether the run found nothing to fix.
func (r VerifyReport) Healthy() bool { return r.Repaired == 0 && r.Orphans == 0 }

// Verify recomputes every cached state from the report log, diffs it against the cache, and
// **overwrites the cache with the recomputation.**
//
// The recomputation wins. That is the whole point and it is not negotiable: `target_state_cache` is
// droppable by construction and the report log is the authority, so a difference between them is
// always the cache being wrong. Preferring the cached row — or even asking a human which to keep —
// would make the cache an authority, which is the bug ADR-0004 exists to prevent.
//
// **And an alert fires**, at ERROR, once per discrepancy and once as a summary. A repair that
// happened quietly is a repair nobody investigates, and a cache that drifts is either a bug in the
// invalidation or a write that reached the database another way — both of which are things
// somebody has to look at, not things a nightly job should absorb.
//
// It is a full sweep rather than a sample. The job runs once a day over a few hundred rows.
func (s *Service) Verify(ctx context.Context) (VerifyReport, error) {
	circles, err := s.db.Queries().ListLiveCircles(ctx)
	if err != nil {
		return VerifyReport{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	// `CirclesChecked` counts up as circles are actually checked rather than being set to
	// `len(circles)` here, because a circle can be tombstoned between that listing and its turn
	// below — and a count that claimed to have checked one this job deliberately skipped would be
	// the wrong kind of reassuring.
	report := VerifyReport{AsOf: s.clock.Now()}

	for _, listed := range circles {
		circleID, parseErr := core.ParseID[core.Circle](listed.ID)
		if parseErr != nil {
			return VerifyReport{}, apierr.Wrap(apierr.CodeInternalError, parseErr, "")
		}
		// **The cached row and the recomputation it is diffed against come from one snapshot.**
		// Read as separate pooled statements they came from two instants, and a timer committing
		// between them made every affected field look like drift — an ERROR each, on a healthy
		// instance, in the job whose whole value is that its alert means something. That is the
		// cry-wolf failure this design is built against, and it is issue #17's defect wearing a
		// different consequence: this reads the same pair, `target_state_cache` beside the timer
		// that a window write commits with it.
		//
		// **Unlike [Service.Board] there is no behavioural gate on this one, and that is stated
		// rather than glossed.** The interleaving is reachable — it was reproduced here, two to
		// seven sweeps in forty — but only by padding the circle with eighty reported targets so
		// the sweep's vulnerable gap lined up with the write, which is a phase relationship tuned
		// on one machine. A sampled test that misses one run in eight, costs seventy seconds under
		// `-race` and might be silently vacuous on other hardware is a gate nobody could trust the
		// green of. `docs/concepts/invariants.md` records the fix and records that it is ungated.
		var (
			cached map[core.RaidTargetID]*sqlitegen.TargetStateCache
			states []derivedState
			gone   bool
		)
		if snapErr := s.db.InReadSnapshot(ctx, func(
			ctx context.Context, q *sqlitegen.Queries,
		) error {
			// **The circle is re-read HERE, and the row from `ListLiveCircles` above is not used
			// for anything but its id.** `min_reporters_to_supersede` is an input to the
			// derivation — it decides when a later cluster supersedes an earlier one — so an
			// `updateCircle` committing between that listing and this snapshot would recompute
			// under the OLD threshold and diff the answer against a cache the CURRENT threshold
			// produced. This job would then either miss real drift or repair a correct row into a
			// stale one, which is the one thing a repair must never do.
			circle, err := s.circle(ctx, q, circleID)
			if coded, ok := apierr.From(err); ok && coded.Code() == apierr.CodeNotFound {
				// Tombstoned since the listing. Skipping is what the tombstone means — a deleted
				// circle must not come back onto the recompute path — and it is counted by NOT
				// being counted, plus the line below.
				gone = true
				return nil
			}
			if err != nil {
				return err
			}
			if cached, err = s.cachedStates(ctx, q, circleID); err != nil {
				return err
			}
			states, err = s.deriveAll(ctx, q, circle)
			return err
		}); snapErr != nil {
			return VerifyReport{}, snapErr
		}
		if gone {
			s.log.WarnContext(ctx, "circle was deleted during the verify sweep and was skipped",
				slog.String("circle_id", circleID.String()))
			continue
		}
		report.CirclesChecked++

		for _, derived := range states {
			report.TargetsChecked++
			// The writing pool: the snapshot above is `query_only`, and the repair is a write.
			fresh, storeErr := s.store(ctx, s.db.Queries(), circleID, derived.targetID,
				derived.state, derived.rows, derived.reason)
			if storeErr != nil {
				return VerifyReport{}, storeErr
			}
			was, hit := cached[derived.targetID]
			delete(cached, derived.targetID)
			if !hit {
				// A missing row is not a discrepancy. Invalidation is a DELETE, so "absent" is the
				// ordinary state between a write and the next read, and counting it as drift would
				// make the alert fire every night on a healthy instance — which is how an alert
				// gets turned off.
				continue
			}
			found := diff(circleID, derived.targetID, *was, fresh)
			if len(found) == 0 {
				continue
			}
			report.Repaired++
			report.Discrepancies = append(report.Discrepancies, found...)
			for _, d := range found {
				s.log.ErrorContext(ctx, AlertMessage,
					slog.String("circle_id", d.CircleID.String()),
					slog.String("target_id", d.TargetID.String()),
					slog.String("field", d.Field),
					slog.String("cached", d.Cached),
					slog.String("computed", d.Computed))
			}
		}

		// Whatever is left named a target with no rows in `tod_report` at all, so `deriveAll` never
		// visited it and nothing will ever recompute it again. It is removed rather than left to be
		// believed, and it alerts: no ordinary path writes one.
		for targetID := range cached {
			if _, delErr := s.db.Queries().InvalidateTargetState(ctx,
				sqlitegen.InvalidateTargetStateParams{
					CircleID: circleID.String(), TargetID: targetID.String(),
				}); delErr != nil {
				return VerifyReport{}, apierr.Wrap(apierr.CodeInternalError, delErr, "")
			}
			report.Orphans++
			s.log.ErrorContext(ctx, AlertMessage,
				slog.String("circle_id", circleID.String()),
				slog.String("target_id", targetID.String()),
				slog.String("field", "row"),
				slog.String("cached", "present"),
				slog.String("computed", "absent"))
		}
	}

	if report.Healthy() {
		s.log.InfoContext(ctx, "target state cache verified",
			slog.Int("circles", report.CirclesChecked),
			slog.Int("targets", report.TargetsChecked))
		return report, nil
	}
	s.log.ErrorContext(ctx, AlertMessage,
		slog.Int("circles", report.CirclesChecked),
		slog.Int("targets", report.TargetsChecked),
		slog.Int("repaired", report.Repaired),
		slog.Int("orphans", report.Orphans))
	return report, nil
}

// diff compares a cached row against a fresh one, field by field.
//
// `computed_at`, `created_at` and `updated_at` are deliberately not compared: they say when the
// row was written, not what it says, and comparing them would report every row as drifted on every
// run. Everything a client can read IS compared, including the evidence counts — a cache with the
// right status and the wrong denominator is a confidence figure that lies.
func diff(
	circleID core.CircleID, targetID core.RaidTargetID, was, now sqlitegen.TargetStateCache,
) []Discrepancy {
	fields := []struct {
		name           string
		cached, actual string
	}{
		{"status", was.Status, now.Status},
		{"confidence", was.Confidence, now.Confidence},
		{"contested", strconv.FormatInt(was.Contested, 10), strconv.FormatInt(now.Contested, 10)},
		{"contest_reason", deref(was.ContestReason), deref(now.ContestReason)},
		{"died_at", formatMicros(was.DiedAt), formatMicros(now.DiedAt)},
		{"window_open_at", formatMicros(was.WindowOpenAt), formatMicros(now.WindowOpenAt)},
		{"window_close_at", formatMicros(was.WindowCloseAt), formatMicros(now.WindowCloseAt)},
		{"spawn_at", formatMicros(was.SpawnAt), formatMicros(now.SpawnAt)},
		{
			"report_count", strconv.FormatInt(was.ReportCount, 10),
			strconv.FormatInt(now.ReportCount, 10),
		},
		{
			"distinct_reporter_count", strconv.FormatInt(was.DistinctReporterCount, 10),
			strconv.FormatInt(now.DistinctReporterCount, 10),
		},
		{
			"log_line_count", strconv.FormatInt(was.LogLineCount, 10),
			strconv.FormatInt(now.LogLineCount, 10),
		},
		{"spread_seconds", formatInt(was.SpreadSeconds), formatInt(now.SpreadSeconds)},
		{
			"revoked_reporter_count", strconv.FormatInt(was.RevokedReporterCount, 10),
			strconv.FormatInt(now.RevokedReporterCount, 10),
		},
		{"latest_report_id", deref(was.LatestReportID), deref(now.LatestReportID)},
	}
	out := make([]Discrepancy, 0)
	for _, f := range fields {
		if f.cached == f.actual {
			continue
		}
		out = append(out, Discrepancy{
			CircleID: circleID, TargetID: targetID,
			Field: f.name, Cached: f.cached, Computed: f.actual,
		})
	}
	return out
}

// formatMicros renders a nullable instant. The empty string is "null" rather than an epoch: a
// window that closed at the beginning of time and a window that has no close are different facts,
// and rendering both as `0` would hide the difference in exactly the report meant to surface it.
func formatMicros(v *int64) string {
	if v == nil {
		return ""
	}
	return core.Micros(*v).String()
}

func formatInt(v *int64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatInt(*v, 10)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

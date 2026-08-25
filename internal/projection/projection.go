package projection

import (
	"context"
	"errors"
	"log/slog"
	"slices"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/consensus"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// Config is what a [Service] needs. Every field is required.
type Config struct {
	Store *store.DB
	Clock clock.Clock
	// Catalogue resolves the EFFECTIVE timer — circle override, then catalogue, then unknown.
	//
	// It is [catalogue.Service.ResolveTimer] and [catalogue.Service.ResolveTimers] that carry that
	// precedence, and nothing else here may stand in for them: `CatalogueEntry.Timer` is the
	// catalogue's own row with the circle's override deliberately NOT applied, and feeding that to
	// the derivation would make every circle override silently stop working while the board went
	// on looking authoritative. TestBoard_ACircleOverride_BeatsTheCatalogueTimer is the gate.
	Catalogue *catalogue.Service
	Log       *slog.Logger
}

// Service maintains `target_state_cache` and renders the board from it.
type Service struct {
	db        *store.DB
	clock     clock.Clock
	catalogue *catalogue.Service
	log       *slog.Logger
}

// New returns a service.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("projection service: no store")
	case cfg.Clock == nil:
		return nil, errors.New("projection service: no clock")
	case cfg.Catalogue == nil:
		return nil, errors.New("projection service: no catalogue")
	case cfg.Log == nil:
		return nil, errors.New("projection service: no logger")
	}
	return &Service{db: cfg.Store, clock: cfg.Clock, catalogue: cfg.Catalogue, log: cfg.Log}, nil
}

// EvidenceCounts is the board's half of the evidence: the numbers, without the report ids.
//
// The counts are here even for a principal that holds no attribution, deliberately — a confidence
// figure with no denominator is worse than no confidence figure, and that separation IS the
// `observer` role. The ids are one `getTargetState` call away.
type EvidenceCounts struct {
	ReportCount           int `json:"report_count"`
	DistinctReporterCount int `json:"distinct_reporter_count"`
	LogLineCount          int `json:"log_line_count"`
	// SpreadSeconds is null when there is no cluster to spread.
	SpreadSeconds *int64 `json:"spread_seconds"`
	// RevokedReporterCount is the revocation rule made visible: those reports still count.
	RevokedReporterCount int `json:"revoked_reporter_count"`
}

// BoardEntry is one row of the board.
//
// It carries no `alternatives[]` and no `report_ids[]`, and that is a decision rather than an
// omission: both are the cluster's detail, the cache does not hold them, and rebuilding them for
// every target on every poll would mean clustering a circle's whole report log to render a list.
// `getTargetState` has them, and the board says how many alternatives there are so a client can
// see that there is something to fetch.
type BoardEntry struct {
	Target catalogue.Target `json:"target"`
	Server string           `json:"server"`
	Status string           `json:"status" enum:"unknown,no_timer,pre_window,in_window,overdue,up"`
	// DiedAt is the point estimate: the derivation's answer, not any one report's `died_at`.
	DiedAt  *core.Micros     `json:"died_at"`
	UpSince *core.Micros     `json:"up_since"`
	Window  consensus.Window `json:"window"`
	// TimerSource is where the window came from: `circle_override`, `catalogue` or `none`.
	TimerSource   string  `json:"timer_source" enum:"circle_override,catalogue,none"`
	Confidence    string  `json:"confidence" enum:"unknown,low,medium,high"`
	Contested     bool    `json:"contested"`
	ContestReason *string `json:"contest_reason"`
	// ChangeReason says what kind of event last moved this answer. The answer changing with no new
	// kill is correct and expected — a backfilled corroboration shifts the median — and this is
	// what makes it visible rather than mysterious.
	ChangeReason *string        `json:"change_reason"`
	Evidence     EvidenceCounts `json:"evidence"`
	// ComputedAt is when the cached row behind this entry was derived, and is null for a target
	// nothing has been reported about. It is NOT `as_of`: the window and the status above were
	// re-rendered against `as_of` on this request, because both move with the clock and neither
	// can be cached.
	ComputedAt *core.Micros `json:"computed_at"`
}

// BoardFilter narrows the board. Every field's zero value means "do not filter on this".
type BoardFilter struct {
	// Status, Expansion, Zone and Query are the API design's filters. Query is `q`.
	Status    string
	Expansion string
	Zone      string
	Query     string
	// Contested is tri-state: nil is unfiltered, because "not contested" is a real thing to ask for
	// and a bool could not say it.
	Contested *bool
	Cursor    core.RaidTargetID
	Limit     int
}

// Board renders every active target's state for one circle, sorted by `window_open_at`.
//
// The sort is the API design's and it is the reason this collection pages the way it does: the
// cursor is the last row's target id and the next page is what follows it **in this sort order**,
// resolved in Go over a bounded set. The catalogue is a hundred-odd rows fixed by the game rather
// than by usage, so reading it whole and ordering it here costs less than a second index would.
func (s *Service) Board(
	ctx context.Context, circleID core.CircleID, filter BoardFilter,
) ([]BoardEntry, bool, error) {
	// The board is a read: no transaction, and the pool said so at the call.
	//
	// **`q` is the pool, not a snapshot, and this render is not isolated from a concurrent timer
	// write.** Each read below is its own implicit transaction, so an override that commits
	// between `ResolveTimers` and `cachedStates` gives this render the OLD timer beside the NEW
	// cached row — and because the timer carries the clustering ε, the two can describe different
	// derivations rather than merely different instants.
	//
	// It is narrower than it was: before ADR-0013 the same render could catch a committed timer
	// whose recomputation had not happened yet, or never would. That window is gone. This one is
	// bounded by the gap between two statements, and the next render is correct.
	//
	// Closing it needs a READ snapshot, which this store cannot currently give: the DSN sets
	// `_txlock=immediate`, so [store.DB.InTx] takes the WRITE lock at BEGIN and wrapping every
	// board render in one would serialise the whole instance behind the slowest reader — the cost
	// that pragma's own comment names. A deferred-transaction handle is a change to
	// `internal/store` and a decision of its own; it is not this function's to make quietly.
	q := s.db.Queries()
	circle, err := s.circle(ctx, q, circleID)
	if err != nil {
		return nil, false, err
	}
	server := core.Server(circle.Server)

	listing, err := s.catalogue.List(ctx, catalogue.ListFilter{
		Expansion: filter.Expansion, Zone: filter.Zone, Query: filter.Query,
		// No Server, deliberately: the entry's own timer is the CATALOGUE's and skips this
		// circle's override. The effective timer comes from ResolveTimers below and nowhere else,
		// and asking for the catalogue's would put a second timer in reach of a tired afternoon.
		//
		// No Limit either: the board sorts by `window_open_at`, which the catalogue does not know,
		// so the page has to be cut after that sort rather than before it.
	})
	if err != nil {
		return nil, false, err
	}
	timers, err := s.catalogue.ResolveTimers(ctx, q, circleID, server)
	if err != nil {
		return nil, false, err
	}
	cached, err := s.cachedStates(ctx, circleID)
	if err != nil {
		return nil, false, err
	}
	quakes, err := s.latestQuake(ctx, q, circleID)
	if err != nil {
		return nil, false, err
	}

	// The read-miss rebuild. Only targets that HAVE reports are rebuilt: a target nobody has
	// reported has nothing to cache, and writing a row of nulls for every mob in the game on the
	// first board render would make the cache proportional to the catalogue rather than to the
	// circle's activity.
	reported, err := s.reportedTargets(ctx, circleID)
	if err != nil {
		return nil, false, err
	}
	for _, id := range reported {
		if _, hit := cached[id]; hit {
			continue
		}
		row, rebuildErr := s.recompute(ctx, q, circleID, id, circle, timers, quakes, "")
		if rebuildErr != nil {
			return nil, false, rebuildErr
		}
		cached[id] = row
	}

	now := s.clock.Now()
	entries := make([]BoardEntry, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		timer := effectiveTimer(timers, entry.Target)
		built, buildErr := boardEntry(entry.Target, circle.Server, timer,
			cached[entry.ID], quakes, now)
		if buildErr != nil {
			return nil, false, buildErr
		}
		if !matches(built, filter) {
			continue
		}
		entries = append(entries, built)
	}
	sortBoard(entries)
	return page(entries, filter.Cursor, filter.Limit)
}

// matches applies the filters the cache cannot: `status` is re-derived against the current instant
// on every read, so filtering on it in SQL would filter on what was true when the row was written.
func matches(e BoardEntry, filter BoardFilter) bool {
	if filter.Status != "" && e.Status != filter.Status {
		return false
	}
	if filter.Contested != nil && e.Contested != *filter.Contested {
		return false
	}
	return true
}

// sortBoard orders by `window_open_at`, soonest first, with everything that has no window after
// everything that does.
//
// A target with no window is not "opening at the beginning of time": sorting nulls first would put
// every unseeded target — which on a fresh instance is all of them — above the ones a raid leader
// is actually waiting on. The target id breaks ties so two renders of one board read the same.
func sortBoard(entries []BoardEntry) {
	slices.SortFunc(entries, func(a, b BoardEntry) int {
		switch {
		case a.Window.OpenAt == nil && b.Window.OpenAt == nil:
		case a.Window.OpenAt == nil:
			return 1
		case b.Window.OpenAt == nil:
			return -1
		case *a.Window.OpenAt != *b.Window.OpenAt:
			if *a.Window.OpenAt < *b.Window.OpenAt {
				return -1
			}
			return 1
		}
		return a.Target.ID.ULID().Compare(b.Target.ID.ULID())
	})
}

// page cuts the sorted board at the cursor. The cursor is the previous page's last target id, and
// the next page is what follows it in THIS order rather than in id order — which is why the cut is
// a search for the row rather than a comparison against it.
func page(entries []BoardEntry, cursor core.RaidTargetID, limit int) ([]BoardEntry, bool, error) {
	if !cursor.IsZero() {
		found := false
		for i, e := range entries {
			if e.Target.ID == cursor {
				entries, found = entries[i+1:], true
				break
			}
		}
		if !found {
			// The row the cursor named is gone — retired, or filtered out by a filter that changed
			// between pages. An empty page is the honest answer: resuming from the top would
			// silently repeat everything the caller already has.
			return []BoardEntry{}, false, nil
		}
	}
	hasMore := len(entries) > limit
	if hasMore {
		entries = entries[:limit]
	}
	return entries, hasMore, nil
}

// effectiveTimer reads the resolved timer for a target, falling back to the honest unknown.
//
// A target absent from the map is one the catalogue resolved nothing for, which on an unseeded
// instance is every target. `IsQuakeTarget` still comes from the catalogue row, so quake
// truncation keeps working on an instance that knows no windows at all — that is the operator's
// day-one state, not an edge case.
func effectiveTimer(
	timers map[core.RaidTargetID]catalogue.ResolvedTimer, target catalogue.Target,
) catalogue.ResolvedTimer {
	if resolved, ok := timers[target.ID]; ok {
		return resolved
	}
	return catalogue.ResolvedTimer{
		Source: catalogue.TimerSourceNone,
		Timer: consensus.Timer{
			Kind:              consensus.WindowUnknown,
			FixedGraceSeconds: catalogue.DefaultFixedGraceSeconds,
			IsQuakeTarget:     target.IsQuakeTarget,
		},
	}
}

// boardEntry renders one row from the cached estimate, re-deriving everything that moves.
func boardEntry(
	target catalogue.Target, server string, timer catalogue.ResolvedTimer,
	cached *sqlitegen.TargetStateCache, quakes []consensus.Quake, now core.Micros,
) (BoardEntry, error) {
	entry := BoardEntry{
		Target: target, Server: server, TimerSource: string(timer.Source),
		Status: schemaenum.TargetStateStatusUnknown, Confidence: schemaenum.TargetStateConfidenceUnknown,
	}
	if cached == nil {
		// No cache row and no reports: the answer is a pure function of the timer and the quake
		// log, so it needs neither a row nor a read of the report log to be correct.
		status, window := consensus.Project(timer.Timer, nil, upSince(timer.Timer, quakes), now)
		entry.Status, entry.Window = string(status), window
		entry.UpSince = upSince(timer.Timer, quakes)
		return entry, nil
	}

	died := micros(cached.DiedAt)
	var up *core.Micros
	if cached.Status == schemaenum.TargetStateStatusUp {
		// `up` is reachable only by a quake, and a quake clears the whole circle's cache — so a
		// cached `up` was true when it was written and nothing but a write can have changed it.
		// The instant itself is not a column; it is the quake that caused it.
		up = upSince(timer.Timer, quakes)
	}
	status, window := consensus.Project(timer.Timer, died, up, now)
	entry.Status, entry.Window, entry.DiedAt, entry.UpSince = string(status), window, died, up
	entry.Confidence = cached.Confidence
	entry.Contested = cached.Contested == 1
	entry.ContestReason = cached.ContestReason
	entry.ChangeReason = cached.ChangeReason
	entry.Evidence = EvidenceCounts{
		ReportCount:           int(cached.ReportCount),
		DistinctReporterCount: int(cached.DistinctReporterCount),
		LogLineCount:          int(cached.LogLineCount),
		SpreadSeconds:         cached.SpreadSeconds,
		RevokedReporterCount:  int(cached.RevokedReporterCount),
	}
	computed := core.Micros(cached.ComputedAt)
	entry.ComputedAt = &computed
	return entry, nil
}

// upSince is the quake a target is up because of, or nil for a target a quake does not repop.
func upSince(timer consensus.Timer, quakes []consensus.Quake) *core.Micros {
	if !timer.IsQuakeTarget || len(quakes) == 0 {
		return nil
	}
	at := quakes[0].OccurredAt
	for _, q := range quakes[1:] {
		if q.OccurredAt > at {
			at = q.OccurredAt
		}
	}
	return &at
}

// circle reads one circle row.
//
// Like every helper a window-moving transaction can reach, it takes the query set rather than
// choosing one: inside such a transaction the pool is the snapshot from before it opened. Callers
// that are NOT in one pass `s.db.Queries()` and say so at the call.
func (s *Service) circle(
	ctx context.Context, q *sqlitegen.Queries, id core.CircleID,
) (sqlitegen.Circle, error) {
	row, err := q.GetCircle(ctx, id.String())
	if store.IsNotFound(err) {
		return sqlitegen.Circle{}, apierr.New(apierr.CodeNotFound, "no such circle")
	}
	if err != nil {
		return sqlitegen.Circle{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	return row, nil
}

func (s *Service) cachedStates(
	ctx context.Context, circleID core.CircleID,
) (map[core.RaidTargetID]*sqlitegen.TargetStateCache, error) {
	rows, err := s.db.Queries().ListTargetStates(ctx, circleID.String())
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	out := make(map[core.RaidTargetID]*sqlitegen.TargetStateCache, len(rows))
	for _, row := range rows {
		id, parseErr := core.ParseID[core.RaidTarget](row.TargetID)
		if parseErr != nil {
			return nil, apierr.Wrap(apierr.CodeInternalError, parseErr, "")
		}
		out[id] = &row
	}
	return out, nil
}

// reportedTargets is every target this circle has reported anything about, which is exactly the
// set that can have a cached state at all.
func (s *Service) reportedTargets(
	ctx context.Context, circleID core.CircleID,
) ([]core.RaidTargetID, error) {
	rows, err := s.db.Queries().ListTodReportTargets(ctx, circleID.String())
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	out := make([]core.RaidTargetID, 0, len(rows))
	for _, raw := range rows {
		id, parseErr := core.ParseID[core.RaidTarget](raw)
		if parseErr != nil {
			return nil, apierr.Wrap(apierr.CodeInternalError, parseErr, "")
		}
		out = append(out, id)
	}
	return out, nil
}

func (s *Service) latestQuake(
	ctx context.Context, q *sqlitegen.Queries, circleID core.CircleID,
) ([]consensus.Quake, error) {
	row, err := q.GetLatestQuakeEvent(ctx, circleID.String())
	if store.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	id, err := core.ParseID[core.QuakeEvent](row.ID)
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	return []consensus.Quake{{ID: id, OccurredAt: core.Micros(row.OccurredAt)}}, nil
}

// revokedReporters is the circle's revoked memberships, as a set. Their reports still count and
// their retractions still apply; this only makes the fact visible.
func (s *Service) revokedReporters(
	ctx context.Context, q *sqlitegen.Queries, circleID core.CircleID,
) (map[string]bool, error) {
	rows, err := q.ListMemberships(ctx, circleID.String())
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	revoked := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.RevokedAt != nil {
			revoked[row.ID] = true
		}
	}
	return revoked, nil
}

func micros(v *int64) *core.Micros {
	if v == nil {
		return nil
	}
	m := core.Micros(*v)
	return &m
}

// memberships is the circle's member rows, keyed by id, for rendering `reporters[]`.
func (s *Service) memberships(
	ctx context.Context, circleID core.CircleID,
) (map[string]sqlitegen.Membership, error) {
	rows, err := s.db.Queries().ListMemberships(ctx, circleID.String())
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	out := make(map[string]sqlitegen.Membership, len(rows))
	for _, row := range rows {
		out[row.ID] = row
	}
	return out, nil
}

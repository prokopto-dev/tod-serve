package catalogue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// SeedFormatVersion is the only seed-file version this binary reads.
//
// It is checked and refused rather than guessed at, because the file comes from a repository that
// versions separately from this one: a seed written for a later format that this binary read
// "best effort" would put a window somebody did not write onto a board.
const SeedFormatVersion = 1

// MaxSeedBytes bounds a seed file. It is generous for a catalogue of tens of targets across three
// servers, and it exists because the file is read whole before anything is validated — a malformed
// seed must fail on its content, not by exhausting memory first.
const MaxSeedBytes = 4 << 20

// SeedFile is the on-disk shape of a timer seed, as the separate `tod-serve-p99-seed` repository
// writes it.
//
// **No file of this shape exists in this repository, and none may.** Respawn and variance numbers
// are community-derived, genuinely disputed, and their most convenient source is a wiki whose
// licence has not been cleared — canonical §15. SEED001 in scripts/repo-gates.sh is the mechanism;
// this type is only the reader.
type SeedFile struct {
	// Version is [SeedFormatVersion]. A file that omits it is refused rather than assumed to be
	// version 1: the seed repository is the one that gains fields, so an absent version means the
	// file was written by something that did not know this contract existed.
	Version *int `json:"version"`
	// Source names where the numbers came from — a revision of the seed repository, a guild's own
	// tracking, an operator's spreadsheet. It is required, and it lands on every row written, so
	// two officers arguing about a window can see which seed said what.
	Source string      `json:"source"`
	Note   string      `json:"note"`
	Timers []SeedTimer `json:"timers"`
}

// SeedTimer is one target's window on one server.
type SeedTimer struct {
	// Target names the raid target. It runs the same resolve ladder a report does, so a seed may
	// use `VS` where the catalogue says `Venril Sathir` — and an ambiguous name is refused rather
	// than resolved to a guess.
	Target string `json:"target"`
	// TargetID pins the target outright, for a seed that would rather not depend on names.
	// Exactly one of Target and TargetID.
	TargetID string `json:"target_id"`
	Server   string `json:"server"`

	WindowKind               string `json:"window_kind"`
	WindowOpenOffsetSeconds  *int64 `json:"window_open_offset_seconds"`
	WindowCloseOffsetSeconds *int64 `json:"window_close_offset_seconds"`
	FixedGraceSeconds        *int64 `json:"fixed_grace_seconds"`
	ClusterEpsilonSeconds    *int64 `json:"cluster_epsilon_seconds"`
	// Note is this row's own provenance, beside the file's. A disputed window usually has a story.
	Note string `json:"note"`
}

// TimerSeedReport is what a run of [Service.ApplySeed] did.
//
// Every field counts something an operator can act on, including on the failure path: a seed that
// stopped part-way returns this report ALONGSIDE its error, because "how far did it get" is the
// only question worth asking at that point and a filter that drops a row counts it somewhere
// visible.
type TimerSeedReport struct {
	// TimersWritten is how many rows were committed. It counts rows that replaced an existing
	// timer as well as new ones: a seed is a statement about what the window IS, not a request to
	// add one. On the failure path it is how many landed before the run stopped.
	TimersWritten int
	// TimersTotal is how many rows the file named, so TimersWritten always has a denominator.
	TimersTotal int
	// Source is what the file said about itself, echoed back so an operator can see which seed
	// they just applied rather than trusting the filename.
	Source string
	// Changed names every (target, server) window that was written AND recomputed, deduplicated.
	//
	// It is what committed, not what was asked for: [Service.ApplySeed] writes one window per
	// transaction, so on a failure part-way this is the prefix that landed and WindowsTotal is
	// what the file named.
	Changed []SeededTimer
	// WindowsTotal is how many distinct (target, server) windows the file named.
	WindowsTotal int
}

// SeededTimer is one (target, server) window a seed run wrote.
//
// A catalogue timer is instance-wide and per-server, so one of these moves the window for every
// circle pinned to that server that has not overridden it. That fan-out is the
// [TimerInvalidator]'s, and it runs inside this window's own transaction.
type SeededTimer struct {
	TargetID core.RaidTargetID
	Server   core.Server
}

// ParseSeed reads and validates a seed file WITHOUT touching the database.
//
// Every check happens here, before a single row is written: a malformed or partial seed must fail
// loudly and leave the catalogue exactly as it was. An operator who runs a bad seed and gets
// "wrote 40 of 61 timers" has a catalogue in a state nobody designed and no way to get back.
//
// It is separated from [Service.ApplySeed] so the CLI can offer a check that touches nothing, and
// so the validation is testable without a store.
func ParseSeed(r io.Reader) (SeedFile, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxSeedBytes+1))
	if err != nil {
		return SeedFile{}, fmt.Errorf("read seed: %w", err)
	}
	if len(raw) > MaxSeedBytes {
		return SeedFile{}, fmt.Errorf("read seed: file is larger than %d bytes", MaxSeedBytes)
	}

	var file SeedFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	// Unknown fields are an error, not a shrug. The seed repository versions separately from this
	// binary, so a field this reader does not know about means the file is saying something this
	// binary would silently not do.
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&file); err != nil {
		return SeedFile{}, fmt.Errorf("parse seed: %w", err)
	}

	switch {
	case file.Version == nil:
		return SeedFile{}, fmt.Errorf(
			"parse seed: no version; this binary reads version %d", SeedFormatVersion)
	case *file.Version != SeedFormatVersion:
		return SeedFile{}, fmt.Errorf(
			"parse seed: version %d; this binary reads version %d",
			*file.Version, SeedFormatVersion)
	case file.Source == "":
		return SeedFile{}, errors.New(
			"parse seed: no source. These numbers are not ours and are disputed; a row nobody " +
				"can attribute is a row nobody can argue with")
	case len(file.Timers) == 0:
		return SeedFile{}, errors.New("parse seed: no timers in the file")
	}

	for i, timer := range file.Timers {
		if err = timer.validate(); err != nil {
			return SeedFile{}, fmt.Errorf("parse seed: timers[%d]: %w", i, err)
		}
	}
	return file, nil
}

// validate checks one row's shape. It reuses the window rules the API path uses, so a seed cannot
// write a window a person could not have.
func (t SeedTimer) validate() error {
	named := t.Target != ""
	byID := t.TargetID != ""
	switch {
	case named == byID:
		return errors.New("send exactly one of target and target_id")
	case byID:
		if _, err := core.ParseID[core.RaidTarget](t.TargetID); err != nil {
			return fmt.Errorf("target_id %q is not a target id: %w", t.TargetID, err)
		}
	}
	if _, err := core.ParseServer(t.Server); err != nil {
		return fmt.Errorf("server %q: %w", t.Server, err)
	}
	if _, err := t.window().validate("timer"); err != nil {
		return fmt.Errorf("%s: %w", t.describe(), err)
	}
	return nil
}

func (t SeedTimer) window() WindowRequest {
	return WindowRequest{
		WindowKind:               t.WindowKind,
		WindowOpenOffsetSeconds:  t.WindowOpenOffsetSeconds,
		WindowCloseOffsetSeconds: t.WindowCloseOffsetSeconds,
		FixedGraceSeconds:        t.FixedGraceSeconds,
		ClusterEpsilonSeconds:    t.ClusterEpsilonSeconds,
		Note:                     t.Note,
	}
}

func (t SeedTimer) describe() string {
	if t.TargetID != "" {
		return t.TargetID + " on " + t.Server
	}
	return strconv.Quote(t.Target) + " on " + t.Server
}

// ErrSeedRejected marks a seed that was refused before anything was written.
//
// The distinction it carries is the only one an operator can act on: a REJECTED seed wrote nothing
// and re-running the same file cannot help, because the file is what is wrong. A run that failed
// part-way through the writes wrote real rows, and re-running the same file IS the remedy. Those
// are opposite instructions, so the caller must not have to guess which it is holding — and it
// cannot infer it from the report, whose counts are all zero in both cases at the first window.
//
// Compare with [errors.Is]. `tod-serve seed timers` is the caller that branches on it.
var ErrSeedRejected = errors.New("seed rejected before anything was written")

// ApplySeed writes a parsed seed's timers, recomputing every board each one moves.
//
// Resolution and validation both happen BEFORE anything is written: a target name that does not
// resolve is discovered while the catalogue is still untouched, so a malformed file still leaves
// nothing behind.
//
// # The unit of atomicity is ONE window, not the file
//
// Each (target, server) window is written and invalidated in a transaction of its own. That is a
// deliberate trade against the whole-file transaction this used to be, and it is made twice over:
//
//   - SQLite has ONE writer. A catalogue timer fans out over every circle pinned to that server,
//     so a sixty-window seed inside one transaction holds the write lock across hundreds of
//     recomputations — every report on the instance blocked for the duration. Reports timing out
//     during a seed is a worse failure than the staleness this closes.
//   - The invariant that matters is per window: a moved window and the boards derived from it
//     commit together, or neither does. File-level atomicity is a different property, and it was
//     buying protection against a partial write that validation already prevents.
//
// **What a crash mid-seed leaves behind:** the windows written so far, each with its boards
// recomputed, and the rest untouched — every board on the instance consistent with the timer
// behind it. Nothing is half-applied at the level anything reads. The remedy is to run the same
// file again: every write here is an upsert and every recomputation is idempotent, so a re-run
// finishes the job and changes nothing it already did. The report says how far it got.
//
// That remedy is exactly wrong for a REJECTED seed, which wrote nothing because the file is what
// is wrong — hence [ErrSeedRejected], and hence a caller that must branch on it rather than read
// the counts, which are zero for both at the first window.
//
// The `source` on every row is the file's, overwritten rather than merged. A timer says where its
// numbers came from; a row whose source named a seed that no longer sets it would be a citation to
// the wrong document.
func (s *Service) ApplySeed(
	ctx context.Context, file SeedFile, inv TimerInvalidator,
) (TimerSeedReport, error) {
	if inv == nil {
		return TimerSeedReport{}, errors.Join(ErrSeedRejected,
			errNoInvalidator("apply timer seed"))
	}
	targets, err := s.loadTargets(ctx, s.db.Queries())
	if err != nil {
		return TimerSeedReport{}, errors.Join(ErrSeedRejected, err)
	}
	byID := make(map[string]Target, len(targets))
	for _, t := range targets {
		byID[t.ID.String()] = t
	}

	// Resolved first, all of them, before anything is written. A seed naming one target that does
	// not exist must not leave the sixty that do half-applied.
	rows := make([]resolvedSeedRow, 0, len(file.Timers))
	for i, timer := range file.Timers {
		target, resolveErr := resolveSeedTarget(timer, byID, targets)
		if resolveErr != nil {
			return TimerSeedReport{}, errors.Join(ErrSeedRejected,
				fmt.Errorf("seed timers[%d] (%s): %w", i, timer.describe(), resolveErr))
		}
		server, serverErr := core.ParseServer(timer.Server)
		if serverErr != nil {
			return TimerSeedReport{}, errors.Join(ErrSeedRejected,
				fmt.Errorf("seed timers[%d]: %w", i, serverErr))
		}
		window, windowErr := timer.window().validate("timer")
		if windowErr != nil {
			return TimerSeedReport{}, errors.Join(ErrSeedRejected,
				fmt.Errorf("seed timers[%d] (%s): %w", i, timer.describe(), windowErr))
		}
		rows = append(rows, resolvedSeedRow{
			target: target, server: server, window: window, note: timer.Note,
		})
	}

	// One instant for the whole file, not one per row: the rows are one statement about what the
	// windows ARE, and a `created_at` that walked forward across a seed would make two rows from
	// one file look like two decisions.
	now := s.clock.Now()

	// Grouped before anything is written, because the group IS the transaction. A seed naming one
	// (target, server) twice is a last-write-wins upsert, so its rows go in one transaction in
	// file order and the window it moved is invalidated once.
	groups := make([]seedGroup, 0, len(rows))
	at := make(map[SeededTimer]int, len(rows))
	for _, row := range rows {
		pair := SeededTimer{TargetID: row.target.ID, Server: row.server}
		i, seen := at[pair]
		if !seen {
			at[pair] = len(groups)
			groups = append(groups, seedGroup{pair: pair})
			i = len(groups) - 1
		}
		groups[i].params = append(groups[i].params, seedParams(row, file.Source, now))
	}

	report := TimerSeedReport{
		Source: file.Source, TimersTotal: len(rows), WindowsTotal: len(groups),
		Changed: make([]SeededTimer, 0, len(groups)),
	}
	for _, group := range groups {
		if err = s.applySeedWindow(ctx, group, inv); err != nil {
			// The report goes back WITH the error. "how far did it get" is the only question worth
			// asking here, and an error carrying nothing forces an operator to go and look.
			if coded, ok := apierr.From(err); ok {
				return report, coded
			}
			return report, apierr.Wrap(apierr.CodeInternalError, err, "")
		}
		report.TimersWritten += len(group.params)
		report.Changed = append(report.Changed, group.pair)
	}

	s.log.InfoContext(ctx, "timer seed applied",
		slog.Int("timers_written", report.TimersWritten),
		slog.Int("windows_recomputed", len(report.Changed)),
		slog.String("source", file.Source))
	return report, nil
}

// seedGroup is every row a seed file holds for ONE (target, server) window, and therefore one
// transaction's worth of work.
type seedGroup struct {
	pair   SeededTimer
	params []sqlitegen.PutRaidTargetTimerParams
}

// applySeedWindow writes one window and recomputes every board it moved, in one transaction.
func (s *Service) applySeedWindow(
	ctx context.Context, group seedGroup, inv TimerInvalidator,
) error {
	return s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		for _, params := range group.params {
			if _, txErr := q.PutRaidTargetTimer(ctx, params); txErr != nil {
				return txErr
			}
		}
		return inv.OnCatalogueTimerChange(ctx, q, group.pair.Server, group.pair.TargetID)
	})
}

func seedParams(row resolvedSeedRow, source string, now core.Micros) sqlitegen.PutRaidTargetTimerParams {
	return sqlitegen.PutRaidTargetTimerParams{
		TargetID: row.target.ID.String(), Server: string(row.server),
		WindowKind:               row.window.kind,
		WindowOpenOffsetSeconds:  row.window.openOffset,
		WindowCloseOffsetSeconds: row.window.closeOffset,
		FixedGraceSeconds:        row.window.grace,
		ClusterEpsilonSeconds:    row.window.epsilon,
		Source:                   &source,
		Note:                     row.note,
		CreatedAt:                int64(now), UpdatedAt: int64(now),
	}
}

// resolveSeedTarget finds the target one seed row is about.
//
// A name goes through the SAME ladder a report does. That is not convenience: a seed file written
// against a slightly different catalogue would otherwise need its own matcher, and two matchers is
// how a seed lands a window on the wrong mob.
// It takes no context because it does no I/O: the catalogue is already in memory, and the ladder
// is a pure function of it.
func resolveSeedTarget(
	timer SeedTimer, byID map[string]Target, targets []Target,
) (Target, error) {
	if timer.TargetID != "" {
		target, ok := byID[timer.TargetID]
		if !ok {
			return Target{}, fmt.Errorf("no raid target with id %s", timer.TargetID)
		}
		return target, nil
	}
	resolution, err := resolveIn(targets, timer.Target)
	if err != nil {
		return Target{}, err
	}
	return resolution.Target, nil
}

// resolvedSeedRow is one seed row after its target, server and window have been checked, and
// before anything is written.
type resolvedSeedRow struct {
	target Target
	server core.Server
	window validated
	note   string
}

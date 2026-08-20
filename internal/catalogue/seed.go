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
type TimerSeedReport struct {
	// TimersWritten is how many (target, server) windows the file set. It counts rows that
	// replaced an existing timer as well as new ones: a seed is a statement about what the window
	// IS, not a request to add one.
	TimersWritten int
	// Source is what the file said about itself, echoed back so an operator can see which seed
	// they just applied rather than trusting the filename.
	Source string
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

// ApplySeed writes a parsed seed's timers.
//
// Resolution and validation both happen BEFORE the transaction opens any write: a target name that
// does not resolve is discovered while the catalogue is still untouched. The write itself is one
// transaction, so a failure part-way leaves nothing behind.
//
// The `source` on every row is the file's, overwritten rather than merged. A timer says where its
// numbers came from; a row whose source named a seed that no longer sets it would be a citation to
// the wrong document.
func (s *Service) ApplySeed(ctx context.Context, file SeedFile) (TimerSeedReport, error) {
	targets, err := s.loadTargets(ctx)
	if err != nil {
		return TimerSeedReport{}, err
	}
	byID := make(map[string]Target, len(targets))
	for _, t := range targets {
		byID[t.ID.String()] = t
	}

	// Resolved first, all of them, before anything is written. A seed naming one target that does
	// not exist must not leave the sixty that do half-applied.
	type resolved struct {
		target Target
		server core.Server
		window validated
		note   string
	}
	rows := make([]resolved, 0, len(file.Timers))
	for i, timer := range file.Timers {
		target, resolveErr := resolveSeedTarget(timer, byID, targets)
		if resolveErr != nil {
			return TimerSeedReport{}, fmt.Errorf("seed timers[%d] (%s): %w",
				i, timer.describe(), resolveErr)
		}
		server, serverErr := core.ParseServer(timer.Server)
		if serverErr != nil {
			return TimerSeedReport{}, fmt.Errorf("seed timers[%d]: %w", i, serverErr)
		}
		window, windowErr := timer.window().validate("timer")
		if windowErr != nil {
			return TimerSeedReport{}, fmt.Errorf("seed timers[%d] (%s): %w",
				i, timer.describe(), windowErr)
		}
		rows = append(rows, resolved{
			target: target, server: server, window: window, note: timer.Note,
		})
	}

	now := s.clock.Now()
	err = s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		for _, row := range rows {
			source := file.Source
			params := sqlitegen.PutRaidTargetTimerParams{
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
			if _, txErr := q.PutRaidTargetTimer(ctx, params); txErr != nil {
				return txErr
			}
		}
		return nil
	})
	if err != nil {
		if coded, ok := apierr.From(err); ok {
			return TimerSeedReport{}, coded
		}
		return TimerSeedReport{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}

	s.log.InfoContext(ctx, "timer seed applied",
		slog.Int("timers_written", len(rows)), slog.String("source", file.Source))
	return TimerSeedReport{TimersWritten: len(rows), Source: file.Source}, nil
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

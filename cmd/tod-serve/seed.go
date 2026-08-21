package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/projection"
)

// flagSeedFile names the seed file `seed timers` reads.
const flagSeedFile = "file"

// flagSeedCheck validates a seed file and writes nothing.
const flagSeedCheck = "check"

// newSeedCommand loads the catalogue.
//
// It is two verbs and not one, and the split is the licence boundary canonical §15 draws rather
// than a convenience:
//
//   - `seed targets` needs no arguments, because target identity ships with this binary. Names,
//     zones, expansions and categories are facts about the game.
//   - `seed timers` REQUIRES `--file`, because respawn numbers do not ship and there is nothing
//     here to fall back on. They are community-derived, genuinely disputed, and they live in the
//     separate `tod-serve-p99-seed` repository.
//
// An instance that runs the first and not the second is fully working and reports `no_timer`
// everywhere. That is the honest degraded state, and it is what a fresh install looks like.
func newSeedCommand() *cobra.Command {
	seed := &cobra.Command{
		Use:   "seed",
		Short: "Load the raid-target catalogue",
		Long: "seed targets loads the embedded target identity, which ships with this binary.\n" +
			"seed timers --file loads respawn windows, which do NOT: they are community-derived,\n" +
			"disputed, and live in the separate tod-serve-p99-seed repository. An instance with\n" +
			"no timers reports no_timer everywhere and records times of death correctly.\n",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmd.Help(); err != nil {
				return fmt.Errorf("write help: %w", err)
			}
			return nil
		},
	}
	seed.AddCommand(newSeedTargetsCommand(), newSeedTimersCommand())
	return seed
}

func newSeedTargetsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "targets",
		Short: "Load the embedded raid-target identity",
		Long: "Adds every target this binary knows about that the catalogue does not already have.\n" +
			"It is additive: a target you edited or retired is left exactly as it is, so this is\n" +
			"safe to re-run after every upgrade.\n",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// `seed targets` needs no invalidation: it adds target IDENTITY, and the board reads
			// the catalogue live on every render rather than from `target_state_cache`. Only a
			// moved WINDOW makes a cached row stale.
			svc, _, closeDB, err := seedService(cmd)
			if err != nil {
				return err
			}
			defer closeDB()

			report, err := svc.SeedTargets(cmd.Context())
			if err != nil {
				return fmt.Errorf("seed targets: %w", err)
			}
			out := cmd.OutOrStdout()
			if _, err = fmt.Fprintf(out,
				"targets: %d added, %d already present, %d skipped because the name is already "+
					"how something else here is spelled\n"+
					"aliases: %d added, %d already claimed by another target\n",
				report.TargetsAdded, report.TargetsPresent, report.NamesTaken,
				report.AliasesAdded, report.AliasesTaken); err != nil {
				return fmt.Errorf("write seed result: %w", err)
			}
			// Said out loud, every time, rather than only when it is news. An operator who has run
			// this and sees a board full of `no_timer` needs the next step in front of them, not in
			// a document they have not opened.
			if _, err = fmt.Fprintf(out,
				"timers are NOT bundled: run `tod-serve seed timers --file <seed.json>` from the "+
					"tod-serve-p99-seed repository.\nUntil then every target reports no_timer, and "+
					"times of death are still recorded correctly.\n"); err != nil {
				return fmt.Errorf("write seed result: %w", err)
			}
			return nil
		},
	}
}

func newSeedTimersCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "timers",
		Short: "Load respawn windows from a seed file",
		Long: "Reads a timer seed from the separate tod-serve-p99-seed repository.\n\n" +
			"The whole file is validated before anything is written. A malformed or partial seed\n" +
			"fails and leaves the catalogue exactly as it was: there is no half-applied state.\n\n" +
			"--check validates the file and writes nothing.\n",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := cmd.Flags().GetString(flagSeedFile)
			if err != nil {
				return fmt.Errorf("read --%s: %w", flagSeedFile, err)
			}
			check, err := cmd.Flags().GetBool(flagSeedCheck)
			if err != nil {
				return fmt.Errorf("read --%s: %w", flagSeedCheck, err)
			}

			file, err := os.Open(path) //nolint:gosec // the operator names the file they are loading.
			if err != nil {
				return fmt.Errorf("open seed %s: %w", path, err)
			}
			defer func() { _ = file.Close() }() // Read-only; nothing to flush and nothing to report.

			parsed, err := catalogue.ParseSeed(file)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if check {
				if _, err = fmt.Fprintf(out, "%s: %d timers, source %q. Nothing was written.\n",
					path, len(parsed.Timers), parsed.Source); err != nil {
					return fmt.Errorf("write seed result: %w", err)
				}
				return nil
			}

			svc, states, closeDB, err := seedService(cmd)
			if err != nil {
				return err
			}
			defer closeDB()

			report, err := svc.ApplySeed(cmd.Context(), parsed)
			if err != nil {
				return fmt.Errorf("seed timers: %w", err)
			}
			if _, err = fmt.Fprintf(out, "%d timers written from %q\n",
				report.TimersWritten, report.Source); err != nil {
				return fmt.Errorf("write seed result: %w", err)
			}

			refreshed, err := invalidateSeededTimers(cmd.Context(), states, report.Changed)
			if _, writeErr := fmt.Fprintf(out,
				"%d of %d moved windows recomputed\n", refreshed, len(report.Changed)); writeErr != nil {
				return fmt.Errorf("write seed result: %w", writeErr)
			}
			// The timers are already written and the failure is reported rather than swallowed: a
			// seed that half-invalidated must be LOUD, because the alternative is a run that says
			// "61 timers written" while some boards go on serving the old window.
			//
			// The invariant is the one the timer ROUTES hold: after a zero exit, the projection has
			// been told about every window this run moved. It is deliberately not "a retry
			// converges" — that reasoning is true here, because a seed is an upsert and a re-run
			// does reach the push, and it was false for `deleteCircleTimerOverride`, where the row
			// is gone and the retry answers before it pushes. A remedy that happens to work is not
			// the same as an invariant, so this reports the gap and names the command that closes
			// it outright.
			return err
		},
	}
	cmd.Flags().String(flagSeedFile, "",
		"the seed file, from the tod-serve-p99-seed repository")
	cmd.Flags().Bool(flagSeedCheck, false, "validate the file and write nothing")
	// Required rather than defaulted. There is no bundled file for this flag to fall back to, and
	// a default path would be an invitation to put one in the repository.
	if err := cmd.MarkFlagRequired(flagSeedFile); err != nil {
		// Unreachable: the flag is declared two lines above. Panicking in command construction is
		// the one place this codebase permits it — it is main wiring, before any request exists.
		panic(err)
	}
	return cmd
}

// seedService opens the database and builds the catalogue service both verbs write through, plus
// the projection `seed timers` invalidates through.
//
// The projection is built here rather than only where it is used because `seed timers` is a WRITE
// path that moves respawn windows, and every other such path in this binary pushes an
// invalidation. This one sits outside the route registry, so the architectural gate that holds the
// routes to it cannot see this command at all — which is exactly why the wiring is not left
// optional here.
func seedService(cmd *cobra.Command) (
	*catalogue.Service, *projection.Service, func(), error,
) {
	path, err := databasePath(cmd)
	if err != nil {
		return nil, nil, nil, err
	}
	log := textLogger(cmd.OutOrStdout())
	db, closeDB, err := openStore(cmd.Context(), path, log)
	if err != nil {
		return nil, nil, nil, err
	}
	clk, ids := clock.System{}, core.NewGenerator(rand.Reader)
	svc, err := catalogue.New(catalogue.Config{
		Store: db, Clock: clk, IDs: ids, Log: log,
	})
	if err != nil {
		closeDB()
		return nil, nil, nil, err
	}
	_, states, err := todServices(db, clk, ids, svc, log)
	if err != nil {
		closeDB()
		return nil, nil, nil, err
	}
	return svc, states, closeDB, nil
}

// invalidateSeededTimers recomputes the derived state every window a seed moved.
//
// A catalogue timer is instance-wide and per-server, so ONE seeded row moves the window for every
// circle pinned to that server that has not overridden it. That fan-out is
// [projection.Service.OnCatalogueTimerChange]'s, and this loop is only the seed's half of it.
//
// It attempts every pair even after one fails, and joins the failures. A partial invalidation is
// the outcome worth naming precisely: "the seed wrote 61 timers and 3 of them left boards stale"
// is actionable, and "the seed failed" after the first bad one is not — the writes have already
// happened either way.
func invalidateSeededTimers(
	ctx context.Context, states *projection.Service, changed []catalogue.SeededTimer,
) (int, error) {
	refreshed := 0
	var failures []error
	for _, timer := range changed {
		if err := states.OnCatalogueTimerChange(ctx, timer.Server, timer.TargetID); err != nil {
			failures = append(failures, fmt.Errorf("recompute %s on %s: %w",
				timer.TargetID, timer.Server, err))
			continue
		}
		refreshed++
	}
	if len(failures) > 0 {
		return refreshed, fmt.Errorf(
			"seed timers wrote every window, but %d of %d could not be recomputed, so those "+
				"boards are serving the old window; run `tod-serve rebuild-states` to fix it: %w",
			len(failures), len(changed), errors.Join(failures...))
	}
	return refreshed, nil
}

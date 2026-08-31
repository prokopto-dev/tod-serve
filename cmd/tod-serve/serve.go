package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/ui"
)

// The server's own timeouts. A home server on a domestic connection meets slow clients routinely,
// and a handler held open by one is a handler not serving anybody.
const (
	readHeaderTimeout = 10 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 2 * time.Minute
	shutdownTimeout   = 15 * time.Second
)

// newServeCommand runs the HTTP server until the process is asked to stop.
//
// The metrics listener is separate and disabled by default — canonical §13. It is a second
// [http.Server] rather than a route on the first, so that binding it to a loopback address or
// leaving it off is a deployment decision an operator can actually make.
func newServeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Serve the API ($" + envAddr + ", default " + defaultAddr + ")",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			log := slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: slog.LevelInfo}))

			pepper := core.Secret(os.Getenv(envTokenPepper))
			if pepper.IsZero() {
				// Refused rather than defaulted. A generated pepper would change on every restart,
				// which invalidates every token an officer has already handed out.
				return fmt.Errorf(
					"%s is required: it is what makes a stolen database file useless on its own",
					envTokenPepper)
			}
			sessionKey := core.Secret(os.Getenv(envSessionKey))
			if sessionKey.IsZero() {
				return fmt.Errorf("%s is required: it signs every browser session", envSessionKey)
			}
			metricsToken := core.Secret(os.Getenv(envMetricsToken))
			// Optional, and its ABSENCE is the safe state: with no setup token the first-run
			// routes refuse every caller, which is what an instance that has finished setting up
			// should look like. ADR-0016.
			setupToken := core.Secret(os.Getenv(envSetupToken))
			if err := refusePlaceholders(map[string]core.Secret{
				envTokenPepper:  pepper,
				envSessionKey:   sessionKey,
				envMetricsToken: metricsToken,
				envSetupToken:   setupToken,
			}); err != nil {
				return err
			}

			path, err := databasePath(cmd)
			if err != nil {
				return err
			}
			db, closeDB, err := openStore(ctx, path, log)
			if err != nil {
				return err
			}
			defer closeDB()
			// Migrations are NOT applied here. `tod-serve migrate` is a separate, deliberate step:
			// a server that migrates on boot upgrades a database whenever a container restarts,
			// which is how a half-tested schema change reaches production without anybody deciding
			// to run it.
			if err := db.Ready(ctx); err != nil {
				return fmt.Errorf("%w: run `tod-serve migrate` first", err)
			}

			// The empty public URL means "resolve it": $TOD_PUBLIC_URL, then the instance
			// row, and an error naming them rather than a guess. Both the join redirect and
			// the OAuth callback URL are derived from it.
			svc, err := wire(ctx, db, log, pepper, sessionKey, "")
			if err != nil {
				return err
			}
			// The console is optional at BUILD time and required to be honest at run time: a
			// binary compiled without the web assets serves the API alone, and the log line
			// below says so. Silently serving a blank page is the failure this reports instead.
			console, consoleErr := ui.Handler()
			if consoleErr != nil && !errors.Is(consoleErr, ui.ErrNotBuilt) {
				return consoleErr
			}

			server, err := api.New(api.Config{
				Version:    version,
				Store:      db,
				Auth:       svc.authn,
				Sessions:   svc.codec,
				Circles:    svc.circles,
				Members:    svc.members,
				Invites:    svc.invites,
				Identities: svc.identity,
				Catalogue:  svc.catalogue,
				// Instance-wide policy, and the ledger that records every change to it.
				InstanceSettings: svc.settings,
				Tods:             svc.tods,
				States:           svc.states,
				// The projection IS the invalidator: it is the only thing that holds derived
				// state, so it is the only thing a moved window can make stale.
				Invalidator: svc.states,
				Clock:       svc.clock,
				Log:         log,
				IDs:         svc.ids,
				Console:     console,
				Metrics: api.MetricsConfig{
					Enabled: os.Getenv(envMetricsEnabled) == "true",
					Token:   metricsToken,
				},
				Setup:      svc.setup,
				SetupToken: api.SetupConfig{Token: setupToken},
			})
			if err != nil {
				return err
			}

			log.InfoContext(ctx, "serving",
				slog.String("addr", envOr(envAddr, defaultAddr)),
				slog.String("database", path),
				slog.Int("operations", len(server.Registered())),
				slog.Int("unimplemented", len(server.Unimplemented())),
				slog.Bool("console", console != nil))
			if console == nil {
				log.WarnContext(ctx, "this binary carries no web console; it serves the API only",
					slog.String("build_it_with", "make build-web"))
			}

			return listenAndServe(ctx, log, server)
		},
	}
}

// placeholderPrefix marks every value in `deploy/env.example` that must be replaced before an
// instance can boot.
//
// The prefix is the mechanism, not a convention: `serve` refuses any secret carrying it, so the
// example file cannot be copied to `.env` and started. `TestServe_PlaceholderSecret_Refused` drives
// this against the values that file actually ships.
const placeholderPrefix = "CHANGE_ME_"

// refusePlaceholders stops the failure `deploy/env.example` would otherwise be most likely to
// cause: a published example value copied into production and left there.
//
// `TOD_TOKEN_PEPPER` is what makes a stolen database file useless on its own — every credential is
// stored as `HMAC-SHA256(pepper, secret)` — so a shipped-and-copied pepper is a database anybody
// with the example file can forge tokens against. A boot failure naming the fix is the only
// outcome here that is not silent.
//
// It refuses an EMPTY-past-the-prefix value too, because `CHANGE_ME_` on its own is what somebody
// leaves behind after deleting the placeholder's tail.
func refusePlaceholders(secrets map[string]core.Secret) error {
	// Sorted, so a .env with two of them left in place produces the same message every time
	// rather than whichever the map happened to yield first.
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if !strings.HasPrefix(secrets[name].Reveal(), placeholderPrefix) {
			continue
		}
		return fmt.Errorf(
			"%s still holds the %s placeholder from deploy/env.example: "+
				"generate a real one with `openssl rand -base64 48`",
			name, placeholderPrefix)
	}
	return nil
}

// listenAndServe starts the listeners and drains them on the first signal.
func listenAndServe(ctx context.Context, log *slog.Logger, server *api.Server) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	main := &http.Server{
		Addr:              envOr(envAddr, defaultAddr),
		Handler:           server.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	servers := []*http.Server{main}
	if handler, enabled := server.MetricsHandler(); enabled {
		servers = append(servers, &http.Server{
			Addr:              envOr(envMetricsAddr, defaultMetricsAddr),
			Handler:           handler,
			ReadHeaderTimeout: readHeaderTimeout,
			WriteTimeout:      writeTimeout,
			IdleTimeout:       idleTimeout,
		})
	}

	failed := make(chan error, len(servers))
	for _, s := range servers {
		go func() {
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				failed <- fmt.Errorf("listen on %s: %w", s.Addr, err)
				return
			}
			failed <- nil
		}()
	}

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-failed:
		if err != nil {
			return err
		}
	}

	// context.Background() is permitted here: this is main wiring, and the context above is the
	// one that has just been cancelled — reusing it would abandon in-flight requests instead of
	// draining them.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	var errs []error
	for _, s := range servers {
		if err := s.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, fmt.Errorf("shut down %s: %w", s.Addr, err))
		}
	}
	return errors.Join(errs...)
}

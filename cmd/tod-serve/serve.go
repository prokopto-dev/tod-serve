package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/core"
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

			svc, err := wire(ctx, db, log, pepper, sessionKey)
			if err != nil {
				return err
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
				Tods:       svc.tods,
				States:     svc.states,
				// The projection IS the invalidator: it is the only thing that holds derived
				// state, so it is the only thing a moved window can make stale.
				Invalidator: svc.states,
				Clock:       svc.clock,
				Log:         log,
				IDs:         svc.ids,
				Metrics: api.MetricsConfig{
					Enabled: os.Getenv(envMetricsEnabled) == "true",
					Token:   core.Secret(os.Getenv(envMetricsToken)),
				},
			})
			if err != nil {
				return err
			}

			log.InfoContext(ctx, "serving",
				slog.String("addr", envOr(envAddr, defaultAddr)),
				slog.String("database", path),
				slog.Int("operations", len(server.Registered())),
				slog.Int("unimplemented", len(server.Unimplemented())))

			return listenAndServe(ctx, log, server)
		},
	}
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

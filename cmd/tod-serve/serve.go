package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store"
)

// The environment this command reads. Named rather than spelled at each call site, so that
// `grep -n TOD_ cmd/` lists everything an operator can set.
const (
	envAddr           = "TOD_ADDR"
	envTokenPepper    = "TOD_TOKEN_PEPPER"
	envSessionKey     = "TOD_SESSION_KEY"
	envMetricsEnabled = "TOD_METRICS_ENABLED"
	envMetricsToken   = "TOD_METRICS_TOKEN"
	envMetricsAddr    = "TOD_METRICS_ADDR"
)

const (
	defaultAddr        = ":8080"
	defaultMetricsAddr = ":9090"
)

// The server's own timeouts. A home server on a domestic connection meets slow clients routinely,
// and a handler held open by one is a handler not serving anybody.
const (
	readHeaderTimeout = 10 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 2 * time.Minute
	shutdownTimeout   = 15 * time.Second
)

// serve runs the HTTP server until the process is asked to stop.
//
// The metrics listener is separate and disabled by default — canonical §13. It is a second
// [http.Server] rather than a route on the first, so that binding it to a loopback address or
// leaving it off is a deployment decision an operator can actually make.
func serve(ctx context.Context, args []string, out io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("unknown argument %q: %s takes no arguments; it reads the environment",
			args[0], cmdServe)
	}

	log := slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: slog.LevelInfo}))

	pepper := core.Secret(os.Getenv(envTokenPepper))
	if pepper.IsZero() {
		// Refused rather than defaulted. A generated pepper would change on every restart, which
		// invalidates every token an officer has already handed out.
		return fmt.Errorf("%s is required: it is what makes a stolen database file useless on its own",
			envTokenPepper)
	}
	sessionKey := core.Secret(os.Getenv(envSessionKey))
	if sessionKey.IsZero() {
		return fmt.Errorf("%s is required: it signs every browser session", envSessionKey)
	}

	path, err := databasePath(nil, os.Getenv(dbPathEnv))
	if err != nil {
		return err
	}
	db, err := store.Open(ctx, path, log)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			log.ErrorContext(ctx, "close database", slog.Any("error", cerr))
		}
	}()
	// Migrations are NOT applied here. `tod-serve migrate` is a separate, deliberate step: a server
	// that migrates on boot upgrades a database whenever a container restarts, which is how a
	// half-tested schema change reaches production without anybody deciding to run it.
	if err := db.Ready(ctx); err != nil {
		return fmt.Errorf("%w: run `tod-serve migrate` first", err)
	}

	clk := clock.System{}
	minter, err := auth.NewMinter(pepper, rand.Reader)
	if err != nil {
		return err
	}
	codec, err := auth.NewSessionCodec(sessionKey)
	if err != nil {
		return err
	}
	authn, err := auth.NewAuthenticator(db, minter, codec, clk, log, auth.DefaultStepUpWindow)
	if err != nil {
		return err
	}

	server, err := api.New(api.Config{
		Version: version,
		Store:   db,
		Auth:    authn,
		Clock:   clk,
		Log:     log,
		IDs:     api.NewIDGenerator(),
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

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

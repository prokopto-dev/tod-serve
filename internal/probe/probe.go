// Package probe asks this process's own listener whether it is alive.
//
// It exists as a package rather than as a few lines in `cmd/tod-serve` because it is the ONE
// exception to AGENTS.md law 6 — outbound HTTP only from `internal/identity`, through the guarded
// client — and an exception has to be a place a gate can name. `NET001` allows this package by
// name and no other; `PROBE001` is the other half, and holds it to what the exception is for.
//
// The exception is narrow on purpose, because the reason law 6 exists does not apply here and the
// reason it exists elsewhere would refuse this outright: the guarded dialer's deny list covers
// loopback, so a probe of our own listener is exactly the request an SSRF guard is built to stop.
// What makes it safe is that the destination is not an input. The port comes from the listen
// address this same binary was told to bind; the HOST is a loopback literal this package writes
// itself, so there is no value a caller, an environment variable or a database row can supply that
// makes this reach another machine.
package probe

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// LivenessPath is the endpoint a probe fetches. It never touches the database — see
// `internal/api/health.go`, and `TestLiveness_MakesNoDatabaseCall`, which is what makes it safe
// for a container runtime to act on.
const LivenessPath = "/healthz"

// loopback is the host every probe targets, whatever the server was told to bind.
//
// A container binds `:8080` — every interface, loopback included — so this reaches it. A server
// bound to one specific external address is not probeable from inside its own network namespace
// this way, and this reports that as unhealthy rather than guessing; the alternative is taking the
// host from configuration, which is the thing this package must not do.
const loopback = "127.0.0.1"

// DefaultTimeout bounds one probe. Docker's own `--timeout` would kill the process anyway; this
// makes the binary answer first, so the failure is "the server did not respond" rather than a
// signal from the runtime with no message attached.
const DefaultTimeout = 5 * time.Second

// LivenessURL is the URL a probe of addr fetches.
//
// addr is a LISTEN address in the spelling [net.Listen] takes — `:8080`, `0.0.0.0:8080`,
// `[::]:8080`, `127.0.0.1:8080`. Only the port is read from it. Whatever the host half says, and
// whatever it would resolve to, the result addresses [loopback]: this function is the mechanism
// behind the claim in the package comment, and `TestLivenessURL_IsAlwaysLoopback` drives it.
func LivenessURL(addr string) (string, error) {
	_, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return "", fmt.Errorf("read the listen address %q: %w", addr, err)
	}
	if port == "" {
		return "", fmt.Errorf("read the listen address %q: it names no port", addr)
	}
	// Deliberately assembled from the constant and the port, rather than parsed and rewritten. A
	// URL that is BUILT from a literal host cannot carry a host from anywhere else; one that is
	// parsed and then corrected can, the first time somebody moves the correction.
	return "http://" + net.JoinHostPort(loopback, port) + LivenessPath, nil
}

// Liveness fetches the liveness endpoint of the server listening on addr.
//
// It reports an error when the server did not answer or answered anything other than 2xx, and
// nothing at all when it did. Liveness only: it deliberately does not fetch `/readyz`, because a
// container runtime acts on this by killing the container, and a brief database problem that would
// recover must not do that — nor must a migration in flight.
func Liveness(ctx context.Context, addr string, timeout time.Duration) error {
	url, err := LivenessURL(addr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build the liveness request for %s: %w", url, err)
	}
	// A zero-value client, with no redirect policy of its own. There is nothing for the guarded
	// client in internal/identity/outbound to guard here — its deny list covers loopback and would
	// refuse this request — and a redirect away from our own listener is a server answering
	// something no build of this one ever answers.
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("probe %s: %w", url, err)
	}
	// Deliberate waiver: the body is drained-and-closed for the connection's sake, and a failure
	// to close it cannot change whether the server answered.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("probe %s: the server answered %s", url, resp.Status)
	}
	return nil
}

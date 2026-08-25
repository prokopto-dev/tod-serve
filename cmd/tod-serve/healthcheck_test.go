package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// The verb the image's HEALTHCHECK calls. The URL arithmetic is `internal/probe`'s and is tested
// there; what this covers is the wiring an operator can get wrong — where the address comes from,
// and whether the exit code says what happened.

// listeningOn starts a server answering `/healthz` and returns its host:port.
func listeningOn(t *testing.T, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	parsed, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return parsed.Host
}

// It prints NOTHING when the server answers. The caller is a container runtime and the exit code is
// the whole report; anything on stdout would end up in `docker inspect` on every healthy probe.
func TestHealthcheck_AnAnsweringServer_ExitsZeroAndSaysNothing(t *testing.T) {
	t.Parallel()

	out, err := captureCLI(t, "healthcheck", "--addr", listeningOn(t, http.StatusOK))
	require.NoError(t, err)
	require.Empty(t, out)
}

func TestHealthcheck_AServerThatIsNotServing_IsAnError(t *testing.T) {
	t.Parallel()

	_, err := captureCLI(t, "healthcheck", "--addr", listeningOn(t, http.StatusServiceUnavailable))
	require.Error(t, err)
	require.Contains(t, err.Error(), "the server answered")
}

// The flag wins, then the environment, then the default — the same ladder every other verb uses for
// the database path. The default matters most: the image sets TOD_ADDR and the HEALTHCHECK passes
// no flag, so overriding the listen address has to move the probe with it.
func TestHealthcheck_ResolvesTheAddress_FromTheFlagThenTheEnvironment(t *testing.T) {
	// No t.Parallel: t.Setenv.
	addr := listeningOn(t, http.StatusOK)

	t.Run("the environment, with no flag", func(t *testing.T) {
		t.Setenv(envAddr, addr)
		_, err := captureCLI(t, "healthcheck")
		require.NoError(t, err)
	})

	t.Run("the flag wins over the environment", func(t *testing.T) {
		t.Setenv(envAddr, "127.0.0.1:1") // Nothing listens on port 1.
		_, err := captureCLI(t, "healthcheck", "--addr", addr)
		require.NoError(t, err)
	})

	t.Run("an address that names no port is refused, not defaulted", func(t *testing.T) {
		t.Setenv(envAddr, "localhost")
		_, err := captureCLI(t, "healthcheck")
		require.Error(t, err)
		require.Contains(t, err.Error(), "listen address")
	})
}

package probe_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/probe"
)

// PROBE001, the half a grep cannot do.
//
// `NET001` allows this package to construct a client at all; what it cannot check is what that
// client is pointed at. The rule is that the HOST is never an input — only the port is — so no
// environment variable, flag or database row can turn the container's health check into a fetcher
// for somewhere else.
//
// The table is the range and not only its ends: a bare port, an all-interfaces bind in both
// families, a host that already IS loopback, a name that RESOLVES to loopback and one that
// resolves to somebody else's machine. The last two are the ones that matter — a resolver is a
// thing an attacker can move, and the answer here does not depend on one.
func TestLivenessURL_IsAlwaysLoopback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		addr string
		want string
	}{
		{"the default listen address", ":8080", "http://127.0.0.1:8080/healthz"},
		{"every interface, IPv4", "0.0.0.0:8080", "http://127.0.0.1:8080/healthz"},
		{"every interface, IPv6", "[::]:8080", "http://127.0.0.1:8080/healthz"},
		{"already loopback", "127.0.0.1:9000", "http://127.0.0.1:9000/healthz"},
		{"IPv6 loopback", "[::1]:9000", "http://127.0.0.1:9000/healthz"},
		{"one external interface", "10.0.0.7:8080", "http://127.0.0.1:8080/healthz"},
		{"a name that resolves here", "localhost:8080", "http://127.0.0.1:8080/healthz"},
		{"a name that resolves elsewhere", "evil.example.com:8080", "http://127.0.0.1:8080/healthz"},
		{"a name with the port of something else", "metadata.google.internal:80", "http://127.0.0.1:80/healthz"},
		{"whitespace around it", "  :8080  ", "http://127.0.0.1:8080/healthz"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := probe.LivenessURL(tt.addr)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)

			// Said again through the URL parser rather than only as a string compare, because the
			// claim is about the HOST the request goes to and not about the text of the URL.
			parsed, err := url.Parse(got)
			require.NoError(t, err)
			host, _, err := net.SplitHostPort(parsed.Host)
			require.NoError(t, err)
			require.True(t, net.ParseIP(host).IsLoopback(),
				"the probe would reach %s, which is not loopback", host)
		})
	}
}

// An address with no port is refused rather than defaulted. A probe that quietly fell back to
// :8080 would report a DIFFERENT server healthy on a host running two of them.
func TestLivenessURL_WhatIsRefused(t *testing.T) {
	t.Parallel()

	for _, addr := range []string{"", "8080", "localhost", "http://127.0.0.1:8080", ":"} {
		t.Run(addr, func(t *testing.T) {
			t.Parallel()
			_, err := probe.LivenessURL(addr)
			require.Error(t, err, "%q was accepted as a listen address", addr)
		})
	}
}

// The probe itself, against a real listener on loopback.
func TestLiveness_AnsweringServer_IsAlive(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, probe.LivenessPath, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	require.NoError(t, probe.Liveness(t.Context(), addrOf(t, srv), probe.DefaultTimeout))
}

// Every non-2xx is a failure, and the middle of the range is tested rather than only its ends: a
// 503 from `/readyz`-style degradation, a 404 from a binary serving something else on that port,
// and a 302 that a client following redirects would have turned into somebody else's 200.
func TestLiveness_WhatCountsAsNotAlive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
	}{
		{"a redirect away from our own listener", http.StatusFound},
		{"not found — something else is on this port", http.StatusNotFound},
		{"unauthorized", http.StatusUnauthorized},
		{"an unhandled panic rendered as 500", http.StatusInternalServerError},
		{"service unavailable", http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", "http://example.com/")
				w.WriteHeader(tt.status)
			}))
			t.Cleanup(srv.Close)

			err := probe.Liveness(t.Context(), addrOf(t, srv), probe.DefaultTimeout)
			require.Error(t, err)
			require.Contains(t, err.Error(), "the server answered")
		})
	}
}

// 204 has no body at all, and a probe that read one would hang or fail on it. It is alive.
func TestLiveness_ASuccessWithNoBody_IsAlive(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	require.NoError(t, probe.Liveness(t.Context(), addrOf(t, srv), probe.DefaultTimeout))
}

// Nothing listening is the case the container health check exists for.
func TestLiveness_NothingListening_IsAnError(t *testing.T) {
	t.Parallel()

	// A port that was bound and released: the only way to name one nothing is listening on
	// without picking a number and hoping. `ListenConfig` rather than `net.Listen` because
	// `noctx` refuses the latter, and it is right to — a listener with no context is one nothing
	// can cancel.
	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	require.NoError(t, listener.Close())

	require.Error(t, probe.Liveness(t.Context(), addr, time.Second))
}

// addrOf returns the host:port an httptest server is listening on.
func addrOf(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	parsed, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return parsed.Host
}

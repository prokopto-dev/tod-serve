package api

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/prokopto-dev/tod-serve/internal/core"
)

// metrics counts what the API served. It is a struct rather than a package-level registry because
// package-level mutable state is banned here, and because two servers in one process — which every
// parallel test is — must not share counters.
type metrics struct {
	mu        sync.Mutex
	requests  map[requestKey]int64
	version   string
	startedAt core.Micros
}

type requestKey struct {
	operation OperationID
	status    int
}

func newMetrics(version string, startedAt core.Micros) *metrics {
	return &metrics{requests: map[requestKey]int64{}, version: version, startedAt: startedAt}
}

// observe records one served request.
func (m *metrics) observe(op OperationID, status int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[requestKey{operation: op, status: status}]++
}

// render writes the Prometheus text exposition format.
//
// It is hand-written rather than pulled from a client library on purpose: the whole exposition is
// four families, the format is stable and documented, and a metrics library is a dependency tree
// that ships in every binary for the benefit of the operators who turn `/metrics` on — which is
// off by default.
func (m *metrics) render(now core.Micros) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var b strings.Builder
	b.WriteString("# HELP tod_build_info The running build. Always 1.\n")
	b.WriteString("# TYPE tod_build_info gauge\n")
	fmt.Fprintf(&b, "tod_build_info{version=%q} 1\n", m.version)

	b.WriteString("# HELP tod_uptime_seconds Seconds since this process started serving.\n")
	b.WriteString("# TYPE tod_uptime_seconds gauge\n")
	fmt.Fprintf(&b, "tod_uptime_seconds %d\n", int64(now.Sub(m.startedAt).Seconds()))

	b.WriteString("# HELP tod_http_requests_total Requests served, by operation and status.\n")
	b.WriteString("# TYPE tod_http_requests_total counter\n")
	keys := make([]requestKey, 0, len(m.requests))
	for k := range m.requests {
		keys = append(keys, k)
	}
	// Sorted so that two scrapes of an unchanged process are byte-identical, which is what makes a
	// diff of the output readable when something is wrong.
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].operation != keys[j].operation {
			return keys[i].operation < keys[j].operation
		}
		return keys[i].status < keys[j].status
	})
	for _, k := range keys {
		fmt.Fprintf(&b, "tod_http_requests_total{operation=%q,status=\"%d\"} %d\n",
			k.operation, k.status, m.requests[k])
	}
	return b.String()
}

// metricsInput is empty: the endpoint takes nothing. The token rides in `Authorization`, checked by
// the route middleware before this handler runs.
type metricsInput struct{}

type metricsOutput struct {
	ContentType string `header:"Content-Type"`
	Body        []byte
}

// registerMetrics attaches `/metrics` to the SEPARATE listener.
//
// It goes through [Register] like every other route, so it appears in the registry and in the
// architectural tests — a route that skipped the registry because it was "just metrics" is exactly
// the route nobody would notice growing a PAT scope.
func (s *Server) registerMetrics() error {
	return registerFailure(OpGetMetrics, Register(s.metrics, OpGetMetrics,
		func(ctx context.Context, _ *metricsInput) (*metricsOutput, error) {
			return &metricsOutput{
				ContentType: "text/plain; version=0.0.4; charset=utf-8",
				Body:        []byte(s.counts.render(s.cfg.Clock.Now())),
			}, nil
		}))
}

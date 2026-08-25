package api

import (
	"net/http"
	"strings"
)

// withConsole puts the embedded admin console behind the API on one listener.
//
// The split is derived from the ROUTE REGISTRY rather than from a list written here: a path under
// [BasePath], or one of the operational endpoints the registry marks unversioned, goes to the API,
// and everything else goes to the console. So an operational endpoint added later is covered
// without anybody remembering this file, which is what
// TestConsole_TheAPIPrefixSet_ComesFromTheRegistry asserts.
//
// The ordering matters in one direction only, and it is the direction that bites: **an unknown
// path under `/api/v1` must still reach the API**, so it answers `404 not_found` as
// `application/problem+json`. Falling through to the console would answer 200 with a page of HTML,
// and a client that asked for JSON would have to parse HTML to discover it had failed — which is
// the "never HTTP 200 with an error body" rule broken by a routing decision rather than by a
// handler.
//
// It sits OUTSIDE the API's own middleware chain, deliberately. That chain negotiates
// `application/json` and would answer `406` to a browser asking for `text/html`; running the
// console through it would make every page load fail on `Accept`.
func withConsole(api http.Handler, console http.Handler) http.Handler {
	prefixes := apiPrefixes()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAPIPath(r.URL.Path, prefixes) {
			api.ServeHTTP(w, r)
			return
		}
		console.ServeHTTP(w, r)
	})
}

// apiPrefixes returns every path prefix this binary answers as an API rather than as a page.
//
// [BasePath] covers every versioned operation in one entry. The unversioned ones — `/healthz`,
// `/readyz`, `/metrics` — are listed individually out of the registry, because they sit at the
// root where the console's own routes live and there is no prefix that separates them.
func apiPrefixes() []string {
	prefixes := []string{BasePath}
	for _, route := range Routes() {
		if route.Versioned {
			continue
		}
		prefixes = append(prefixes, route.Path)
	}
	return prefixes
}

// isAPIPath reports whether a request path belongs to the API.
//
// A prefix matches the path itself or a path continuing with `/`, never a longer path that merely
// starts with the same letters: `/readyz-please` is a console route and `/api/v1x` is not the API.
func isAPIPath(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

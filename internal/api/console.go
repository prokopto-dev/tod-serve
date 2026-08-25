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
	secured := secureConsole(console)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAPIPath(r.URL.Path, prefixes) {
			api.ServeHTTP(w, r)
			return
		}
		secured.ServeHTTP(w, r)
	})
}

// The response headers every console document and asset carries.
//
// They are set HERE, in the binary, rather than on the reverse proxy in front of it. A header the
// proxy adds protects the one deployment somebody remembered to configure; a header the handler
// adds protects `docker run`, `compose.local.yaml`, a laptop, and the droplet alike. What is
// deliberately NOT here is HSTS: whether this origin is reachable over plain HTTP is a fact about
// the TLS terminator, and a binary that asserted it would be asserting something it cannot know —
// `deploy/compose.yaml` sets it on Traefik's secure router, where it is true.
const (
	// The console bundles everything it needs — `go:embed`, no CDN, and
	// `TestHandler_TheDocument_ReferencesNoExternalHost` is what holds it to that — so `'self'`
	// costs nothing and turns any injected external origin into a blocked request rather than a
	// silent fetch.
	//
	// Two relaxations, each named rather than left for somebody to discover:
	//
	//   img-src   `data:` is here for ONE resource: `<link rel="icon" href="data:,">` in
	//             web/index.html, which exists so a strict deployment does not 404 on a favicon
	//             every page load. `img-src` falls back to `default-src`, so without this the
	//             console reports a violation on every load and an operator learns to ignore the
	//             console's error log — which is where a real violation would appear.
	//
	//   style-src `'unsafe-inline'` covers style ATTRIBUTES, which CSP3 governs through
	//             `style-src-attr`'s fallback to `style-src`. web/src/components/WindowBar.tsx
	//             positions the window bar with `style={{ width: `${progress}%` }}`: the value is
	//             computed per target and there is no static class for "37.2%". `script-src` stays
	//             strict, which is the half that matters — an inline style cannot execute.
	//
	// `base-uri`, `object-src` and `form-action` are spelled out because `default-src` does not
	// cover any of them.
	consoleCSP = "default-src 'self'; " +
		"base-uri 'none'; " +
		"object-src 'none'; " +
		"frame-ancestors 'none'; " +
		"form-action 'self'; " +
		"img-src 'self' data:; " +
		"style-src 'self' 'unsafe-inline'"
)

// secureConsole attaches the headers above to everything the console serves.
//
// They go on before the handler runs, because a header set after the first byte is written is a
// header that never reaches the client — and the console's own handler writes the document itself.
func secureConsole(console http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", consoleCSP)
		// The same two values the API chain sends, from the one place they are spelled — a
		// `text/html` response sniffed as something else is how a content-type confusion becomes
		// script execution, and this console's paths name circles, targets and invites.
		h.Set("X-Content-Type-Options", contentTypeOptions)
		h.Set("Referrer-Policy", referrerPolicy)
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

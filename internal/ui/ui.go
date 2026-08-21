// Package ui serves the embedded admin console.
//
// The console is built by `make build-web` into `web/dist` and copied here, so the shipped binary
// carries it and a deployment needs no CDN, no reverse-proxy rule and no second container. A
// strict deployment has no outbound network at all, and an asset that loads from somewhere else is
// an asset that does not load.
//
// **This package declares no HTTP route.** It returns a handler; where that handler sits in front
// of or behind the API is decided in `internal/api`, which is the only package that composes the
// surface — AGENTS.md law 1.
package ui

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// assets is the built console. `all:` is required rather than stylistic: without it `go:embed`
// skips files beginning with `.`, and on a clone where nobody has run the web build the directory
// holds nothing but `.gitkeep` — an embed pattern matching no files is a COMPILE error, so the Go
// package would stop building because a JavaScript toolchain had not been run.
//
//go:embed all:dist
var assets embed.FS

// IndexFile is the document every console URL that is not a file resolves to.
const IndexFile = "index.html"

// ErrNotBuilt is returned by [Handler] when this binary was built without the console.
//
// It is an error rather than a silent fallback because the two cases look identical from the
// outside and are completely different problems: a 404 on `/board` is either "you typed something
// wrong" or "this binary has no console in it", and an operator debugging the second from the
// first will not get there.
var ErrNotBuilt = errors.New("this binary was built without the web console; run `make build-web`")

// Available reports whether the console was built into this binary.
func Available() bool {
	_, err := fs.Stat(assets, path.Join("dist", IndexFile))
	return err == nil
}

// Handler serves the console: the built assets, with every unmatched path falling back to
// `index.html` so the client-side router owns the URL space.
//
// The fallback is what makes `/join#TODI-…` work on a cold load. It is deliberately NOT a catch-all
// for every path this binary serves: `internal/api` decides which requests reach here at all, so a
// mistyped API path still gets a problem document rather than a page of HTML that a client would
// have to parse to discover it had failed.
func Handler() (http.Handler, error) {
	if !Available() {
		return nil, ErrNotBuilt
	}
	root, err := fs.Sub(assets, "dist")
	if err != nil {
		return nil, err
	}
	files := http.FileServerFS(root)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			// The console is static. A POST here is a client that believes this path is an API
			// operation, and the honest answer is that the method is not allowed.
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "the console serves GET and HEAD", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" || !exists(root, name) {
			// A route the client-side router owns — `/board`, `/join`, `/invites`. It gets the
			// document, and it gets it UNCACHED: `index.html` names the hashed asset filenames, so
			// a cached copy pointing at last release's bundle is a console that fails to load
			// after every upgrade.
			w.Header().Set("Cache-Control", "no-cache")
			serveIndex(w, r, root)
			return
		}

		if strings.HasPrefix(name, "assets/") {
			// Everything under `assets/` carries a content hash in its filename, so it is
			// immutable by construction: a change produces a different name rather than a
			// different body at the same one.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	}), nil
}

// exists reports whether the built output holds a regular file at name.
func exists(root fs.FS, name string) bool {
	info, err := fs.Stat(root, name)
	return err == nil && !info.IsDir()
}

// serveIndex writes `index.html` for a client-routed path.
//
// Written directly rather than through [http.ServeContent], which wants a modification time. An
// embedded file has none — every `embed.FS` entry reports the zero time — so the only honest value
// to pass is one that produces no `Last-Modified` at all, and reaching for a clock here would put
// a `time.Now` in a package that has no business reading one.
func serveIndex(w http.ResponseWriter, _ *http.Request, root fs.FS) {
	body, err := fs.ReadFile(root, IndexFile)
	if err != nil {
		http.Error(w, "the console is not available", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Deliberate waiver: the header and status are already written, so there is no second
	// response to send if the client hung up mid-body.
	_, _ = w.Write(body)
}

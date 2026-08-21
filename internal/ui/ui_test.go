package ui_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/ui"
)

// The console is optional at build time, so every test here either drives the built handler or
// asserts the honest refusal. A test that silently skipped when the assets were absent would be a
// test that passes on the machine where it matters least.
func handlerOrSkip(t *testing.T) http.Handler {
	t.Helper()
	handler, err := ui.Handler()
	if err != nil {
		require.ErrorIs(t, err, ui.ErrNotBuilt)
		t.Skip("this binary was built without the console; `make build-web` stages it")
	}
	return handler
}

// TestHandler_NotBuilt_IsAnErrorRatherThanAnEmptyHandler is the half that runs either way.
//
// `Available` and `Handler` have to agree, because the difference is what `tod-serve serve` logs:
// a binary with no console says so at startup instead of serving a blank page that looks like a
// broken one.
func TestHandler_Availability_AgreesWithTheHandler(t *testing.T) {
	t.Parallel()
	handler, err := ui.Handler()
	if ui.Available() {
		require.NoError(t, err)
		require.NotNil(t, handler)
		return
	}
	require.ErrorIs(t, err, ui.ErrNotBuilt)
	require.Nil(t, handler)
}

// A path the client-side router owns gets the document. This is what makes `/join#TODI-…` work on
// a cold load: the browser asks the server for `/join`, and the server has no such file.
func TestHandler_AClientRoutedPath_GetsTheDocument(t *testing.T) {
	t.Parallel()
	handler := handlerOrSkip(t)

	for _, path := range []string{"/", "/join", "/board", "/board/01K3TGT8N9M4X0Q7R2VB6C5D1E", "/invites"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))

			require.Equal(t, http.StatusOK, rec.Code)
			require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
			require.Contains(t, rec.Body.String(), `<div id="root">`)
			// `index.html` names the hashed asset filenames, so a cached copy points at last
			// release's bundle and the console fails to load after every upgrade.
			require.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
		})
	}
}

// Everything under `assets/` carries a content hash in its filename, so it is immutable by
// construction: a change produces a different name rather than a different body at the same one.
func TestHandler_AHashedAsset_IsImmutable(t *testing.T) {
	t.Parallel()
	handler := handlerOrSkip(t)

	index := httptest.NewRecorder()
	handler.ServeHTTP(index, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	asset := assetPath(t, index.Body.String())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, asset, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "public, max-age=31536000, immutable", rec.Header().Get("Cache-Control"))
	require.NotEmpty(t, rec.Body.String())
}

// The console is static. A POST is a client that believes this path is an API operation, and
// answering 200 with the document would let it believe the write succeeded.
func TestHandler_APost_IsMethodNotAllowed(t *testing.T) {
	t.Parallel()
	handler := handlerOrSkip(t)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/board", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))
}

// A deployment with no outbound network at all is the target, so the shipped page must reference
// nothing it cannot serve itself.
func TestHandler_TheDocument_ReferencesNoExternalHost(t *testing.T) {
	t.Parallel()
	handler := handlerOrSkip(t)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	body := rec.Body.String()
	require.NotContains(t, body, "http://")
	require.NotContains(t, body, "https://")
	require.NotContains(t, body, "//cdn")
}

// assetPath pulls the first `/assets/…` reference out of the document, so the test follows what
// the page actually asks for rather than a filename it guessed.
func assetPath(t *testing.T, document string) string {
	t.Helper()
	const marker = `"/assets/`
	start := indexOf(document, marker)
	require.GreaterOrEqual(t, start, 0, "the document references no /assets/ file")
	rest := document[start+1:]
	end := indexOf(rest, `"`)
	require.Greater(t, end, 0)
	return rest[:end]
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

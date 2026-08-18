package apierr_test

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/canondoc"
)

const (
	apiDesignPath = "docs/design/02-api-design.md"
	errorsDir     = "docs/errors"
)

// documentedCode is one `code (NNN)` entry from the API design's error-code section.
type documentedCode struct {
	Code   string
	Status int
}

// documentedCodes reads the fenced blocks under "Error codes". Both blocks are read — the generic
// set and the domain set — because the catalogue holds both and a gate that read one would report
// success over half the vocabulary.
func documentedCodes(t *testing.T) []documentedCode {
	t.Helper()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)
	doc, err := canondoc.Load(filepath.Join(root, apiDesignPath))
	require.NoError(t, err)

	blocks, err := doc.BlocksUnder("Error codes")
	require.NoError(t, err)
	require.Len(t, blocks, 2, "expected the generic block and the domain block")

	pattern := regexp.MustCompile(`([a-z_]+) \((\d{3})\)`)
	var out []documentedCode
	for _, block := range blocks {
		for _, m := range pattern.FindAllStringSubmatch(block.Body, -1) {
			status, convErr := strconv.Atoi(m[2])
			require.NoError(t, convErr)
			out = append(out, documentedCode{Code: m[1], Status: status})
		}
	}
	require.Greater(t, len(out), 50, "only %d codes parsed; the parser is wrong", len(out))
	return out
}

// The catalogue, the normative document and the documentation tree are one fact seen from three
// places. Compared in every direction, because the copy that silently grows is the one nobody is
// checking.
func TestErrorCodes_Catalogue_MatchesTheAPIDesign(t *testing.T) {
	t.Parallel()
	documented := documentedCodes(t)

	want := make([]string, 0, len(documented))
	status := map[string]int{}
	for _, d := range documented {
		want = append(want, d.Code)
		status[d.Code] = d.Status
	}
	got := make([]string, 0, len(apierr.Codes()))
	for _, def := range apierr.Codes() {
		got = append(got, string(def.Code))
	}
	slices.Sort(want)
	slices.Sort(got)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("the error-code catalogue and %s disagree (-document +catalogue):\n%s",
			apiDesignPath, diff)
	}

	for _, def := range apierr.Codes() {
		require.Equal(t, status[string(def.Code)], def.Status,
			"%s: the document says %d and the catalogue says %d",
			def.Code, status[string(def.Code)], def.Status)
	}
}

// Every code has a page, and every page has a code. The `type` URL's last segment IS the code, so
// a code with no page ships a broken link; a page with no code is a page nobody will ever delete.
func TestErrorCodes_EveryCode_HasADocumentationPage(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	entries, err := os.ReadDir(filepath.Join(root, errorsDir))
	require.NoError(t, err)

	pages := map[string]bool{}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".md")
		if e.IsDir() || name == "README" || name == e.Name() {
			continue
		}
		pages[name] = true
	}
	require.NotEmpty(t, pages)

	for _, def := range apierr.Codes() {
		code := string(def.Code)
		require.True(t, pages[code], "%s/%s.md does not exist", errorsDir, code)
		delete(pages, code)

		body, readErr := os.ReadFile(filepath.Join(root, errorsDir, code+".md"))
		require.NoError(t, readErr)
		page := string(body)
		require.Contains(t, page, def.TypeURL(),
			"%s.md does not carry its own type URL", code)
		require.Contains(t, page, "HTTP "+strconv.Itoa(def.Status),
			"%s.md does not state its status", code)
	}
	for orphan := range pages {
		t.Errorf("%s/%s.md documents a code the catalogue does not hold", errorsDir, orphan)
	}
}

// The last segment of a `type` URL IS the code. A client that derives one from the other — which is
// the whole reason the URL is shaped this way — must not be able to be wrong.
func TestErrorCodes_TypeURL_EndsInTheCode(t *testing.T) {
	t.Parallel()
	for _, def := range apierr.Codes() {
		require.True(t, strings.HasSuffix(def.TypeURL(), "/"+string(def.Code)),
			"%s: type URL %s does not end in the code", def.Code, def.TypeURL())
		require.Equal(t, def.TypeURL(), def.Code.TypeURL())
	}
}

// A code outside the catalogue must not render: a response whose `type` URL 404s teaches its reader
// nothing, so it becomes an internal error with the offending code recorded as the cause.
func TestErrorCodes_UnknownCode_RendersAsAnInternalError(t *testing.T) {
	t.Parallel()
	e := apierr.New(apierr.Code("no_such_code"), "whatever")
	require.Equal(t, apierr.CodeInternalError, e.Code())
	require.Equal(t, http.StatusInternalServerError, e.GetStatus())
	require.ErrorIs(t, e, apierr.ErrUnknownCode)
}

// Every code the fallback can answer with is in the catalogue, and every mapped status agrees with
// the code's own status. A fallback that produced a code whose status differed from the response's
// would make the body disagree with the header on every framework error.
func TestCodeForStatus_EveryMappedStatus_AgreesWithItsCode(t *testing.T) {
	t.Parallel()
	require.NotEmpty(t, apierr.MappedStatuses())
	for _, status := range apierr.MappedStatuses() {
		code, ok := apierr.CodeForStatus(status)
		require.True(t, ok)
		def, found := apierr.Lookup(code)
		require.True(t, found, "the fallback for %d names %s, which is not in the catalogue",
			status, code)
		require.Equal(t, status, def.Status,
			"the fallback maps %d to %s, whose status is %d", status, code, def.Status)
		require.True(t, def.Generic,
			"the fallback maps %d to %s, which is a domain code the edge must not invent",
			status, code)
	}
}

// An unmapped status is reported as a failure we have no vocabulary for, rather than given the
// nearest code — which would be a confident mistake, and the failure mode this project is built
// against.
func TestCodeForStatus_AnUnmappedStatus_IsNotGuessedAt(t *testing.T) {
	t.Parallel()
	_, ok := apierr.CodeForStatus(http.StatusTeapot)
	require.False(t, ok)
}

package identity_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/canondoc"
	"github.com/prokopto-dev/tod-serve/internal/identity"
)

// apiDesignPath is where the closed error enum lives.
const apiDesignPath = "docs/design/02-api-design.md"

// documentedCodes reads `code (status)` out of the API design's own fenced block, so the Go and
// the document cannot drift. Two hand-maintained copies of one fact is the drift this repository
// gates against everywhere else.
func documentedCodes(t *testing.T) map[identity.Code]int {
	t.Helper()

	root, err := canondoc.RepoRoot()
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(root, apiDesignPath))
	require.NoError(t, err)

	pattern := regexp.MustCompile(`([a-z_]+) \((\d{3})\)`)
	out := map[identity.Code]int{}
	for _, match := range pattern.FindAllStringSubmatch(string(raw), -1) {
		status, err := strconv.Atoi(match[2])
		require.NoError(t, err)
		out[identity.Code(match[1])] = status
	}
	require.Greater(t, len(out), 20, "the parse of %s found almost no codes; the format moved", apiDesignPath)
	return out
}

// Every code this package returns is either one the API design lists, with the status it lists,
// or one of the named generic codes. A code invented here that the document never heard of would
// ship a `type` URL pointing at a page that does not exist.
func TestCodes_StatusesMatchTheAPIDesign(t *testing.T) {
	t.Parallel()

	documented := documentedCodes(t)
	// The generic set, which the API design's block deliberately does not repeat. Named here so
	// that "not in the document" cannot quietly become the escape hatch for a new code.
	generic := map[identity.Code]bool{identity.CodeValidationFailed: true}

	matched := 0
	for _, code := range identity.Codes() {
		status, ok := documented[code]
		if !ok {
			require.True(t, generic[code],
				"code %q is neither in %s nor one of the generic codes", code, apiDesignPath)
			continue
		}
		require.Equal(t, status, code.Status(), "code %q", code)
		matched++
	}
	require.Greater(t, matched, 15, "almost nothing was compared; the catalogue or the parse is wrong")
}

// The `type` URL's last segment IS the code, so an undocumented code ships a broken link.
// scripts/docs-check.sh checks the document's list; this checks the Go's.
func TestCodes_EveryCode_IsDocumented(t *testing.T) {
	t.Parallel()

	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	for _, code := range identity.Codes() {
		if code == identity.CodeValidationFailed {
			continue // generic; its page lands with the API's error envelope
		}
		page := filepath.Join(root, "docs", "errors", string(code)+".md")
		_, err := os.Stat(page)
		require.NoError(t, err, "code %q has no docs/errors/%s.md", code, code)
	}
}

func TestCodes_AreUnique(t *testing.T) {
	t.Parallel()

	seen := map[identity.Code]bool{}
	for _, code := range identity.Codes() {
		require.False(t, seen[code], "code %q is listed twice", code)
		seen[code] = true
	}
}

func TestError_CarriesItsCodeAndCause(t *testing.T) {
	t.Parallel()

	err := identity.NewError(identity.CodeIdentityBlocked, "blocked", os.ErrNotExist)

	require.Equal(t, 403, err.Status())
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Contains(t, err.Error(), "identity_blocked")

	got, ok := identity.CodeOf(err)
	require.True(t, ok)
	require.Equal(t, identity.CodeIdentityBlocked, got)

	_, ok = identity.CodeOf(os.ErrNotExist)
	require.False(t, ok, "an uncoded error must not be given a code by accident")
}

// An unknown code answers 500 rather than guessing 400. Presenting this instance's bug as the
// caller's fault sends somebody looking in the wrong place.
func TestCode_Unknown_Is500(t *testing.T) {
	t.Parallel()
	require.Equal(t, 500, identity.Code("not_a_code").Status())
}

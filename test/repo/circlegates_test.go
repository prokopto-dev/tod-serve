package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// enumeratingQueries are the two reads that return circles without naming one.
//
// Both carry a counted `-- tenancy:` waiver in db/queries/circle.sql, and both are safe for the
// same reason: they take no caller-supplied circle, and their results never reach a response body.
var enumeratingQueries = []string{"ListLiveCircles", "ListLiveCirclesOnServer"}

// TestCircleEnumeration_IsReachableOnlyFromTheProjection makes "this query must never back a
// caller-facing route" a mechanism rather than a comment.
//
// A circle's EXISTENCE is part of what it is hiding — canonical §7 is why cross-circle access is a
// `404` and never a `403`, and why there is no list-all-circles operation at any permission level.
// These two queries return every circle on the instance, so a handler that reached one would hand
// back exactly the enumeration the rest of the design spends its gates preventing. The waiver in
// the query file says so; this is what holds it to it.
//
// It is scoped to the projection rather than merely excluding `internal/api`, deliberately: a
// service reached by a handler would leak just as well as a handler, and "only the package that
// maintains the cache" is a rule with one obvious reading. `cmd/` is permitted because
// `rebuild-states` and `verify-states` are operator commands with no caller at all.
func TestCircleEnumeration_IsReachableOnlyFromTheProjection(t *testing.T) {
	t.Parallel()

	allowed := map[string]bool{
		"internal/projection": true,
		// Generated. sqlc writes the method itself, which is not a use of it.
		"internal/store/sqlitegen": true,
		// First-run setup, and the ONE caller-facing exception. It is admitted on a narrower
		// argument than "it is an operator": the enumeration reaches a caller only through a
		// route carrying `Auth: AuthSetupToken`, and such a route answers `404` without
		// `TOD_SETUP_TOKEN` and `409` once any identity administers the instance —
		// `api.SetupRoutes()` and the three refusals derived from it are what hold that, not this
		// comment. So the reader is somebody who could grant themselves ownership of every circle
		// listed, on an instance where nobody yet can. ADR-0016.
		//
		// It is here rather than replaced by a count because the wizard has to be able to finish
		// a setup that died half-way: an operator whose owner code was never redeemed needs to
		// name the circle a fresh one should admit them to, and cannot name what it will not show.
		"internal/setup": true,
	}

	found := 0
	err := filepath.WalkDir("../..", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel := filepath.ToSlash(strings.TrimPrefix(filepath.Clean(path), "../../"))
		dir := filepath.ToSlash(filepath.Dir(rel))
		for _, query := range enumeratingQueries {
			if !strings.Contains(string(body), "."+query+"(") {
				continue
			}
			found++
			require.True(t, allowed[dir] || strings.HasPrefix(dir, "cmd/"),
				"%s calls %s, which returns every circle on the instance. A circle's existence "+
					"is part of what it is hiding (canonical §7): there is no list-all-circles "+
					"operation at any permission level, and a caller-facing path that reached "+
					"this query would be one. If this is a maintenance sweep, it belongs in "+
					"internal/projection or in a cmd verb", rel, query)
		}
		return nil
	})
	require.NoError(t, err)
	require.Positive(t, found,
		"neither enumerating query was found anywhere; the scan is wrong and this gate is vacant")
}

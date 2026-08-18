package core_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/canondoc"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

const sampleULID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

func TestParseID_CanonicalEncoding_RoundTrips(t *testing.T) {
	t.Parallel()
	id, err := core.ParseID[core.Circle](sampleULID)
	require.NoError(t, err)
	require.Equal(t, sampleULID, id.String())
	require.Equal(t, "circle", id.Entity())
	require.False(t, id.IsZero())
	require.Equal(t, sampleULID, id.ULID().String())
	require.Equal(t, id, core.IDFromULID[core.Circle](id.ULID()))
}

func TestParseID_Invalid_NamesTheEntityInTheError(t *testing.T) {
	t.Parallel()
	_, err := core.ParseID[core.TodReport]("nope")
	require.ErrorIs(t, err, core.ErrInvalidULID)
	// "invalid ulid" alone tells whoever is reading the log nothing about which field was wrong.
	require.Contains(t, err.Error(), "tod_report")
}

func TestNewID_Generator_MintsAnIDForTheEntity(t *testing.T) {
	t.Parallel()
	g := core.NewGenerator(countingEntropy(0x5a))
	at := core.Micros(1_755_483_247_000_000)

	first, err := core.NewID[core.Membership](g, at)
	require.NoError(t, err)
	second, err := core.NewID[core.Membership](g, at)
	require.NoError(t, err)

	require.Equal(t, "membership", first.Entity())
	require.Equal(t, -1, first.Compare(second))
	require.Equal(t, at.Time().Truncate(1e6), first.ULID().Time().Time())
}

func TestNewID_EntropyFails_NamesTheEntityInTheError(t *testing.T) {
	t.Parallel()
	_, err := core.NewID[core.QuakeEvent](core.NewGenerator(failingEntropy{}), 1)
	require.ErrorIs(t, err, errNoEntropy)
	require.Contains(t, err.Error(), "quake_event")
}

// identified stands in for the request and response bodies that carry ids.
type identified struct {
	CircleID core.CircleID `json:"circle_id"`
	ReportID core.TodReportID
}

func TestID_JSON_RoundTripsAsAString(t *testing.T) {
	t.Parallel()
	circle, err := core.ParseID[core.Circle](sampleULID)
	require.NoError(t, err)
	report, err := core.ParseID[core.TodReport]("01ARZ3NDEMTSV4RRFFQ69G5FAV")
	require.NoError(t, err)
	want := identified{CircleID: circle, ReportID: report}

	encoded, err := json.Marshal(want)
	require.NoError(t, err)
	require.JSONEq(t,
		`{"circle_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","ReportID":"01ARZ3NDEMTSV4RRFFQ69G5FAV"}`,
		string(encoded))

	var got identified
	require.NoError(t, json.Unmarshal(encoded, &got))
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("round trip (-want +got):\n%s", diff)
	}
}

func TestID_UnmarshalJSON_Invalid_FailsAtTheEdge(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		given string
	}{
		// A bad id caught here is a 422 at the edge; caught later it is a foreign-key violation
		// three layers down, which reads as a 500.
		{"not an encoding", `{"circle_id":"circle-1"}`},
		{"lowercase", `{"circle_id":"01arz3ndektsv4rrffq69g5fav"}`},
		{"a number", `{"circle_id":12345}`},
		{"null", `{"circle_id":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got identified
			require.ErrorIs(t, json.Unmarshal([]byte(tc.given), &got), core.ErrInvalidULID)
		})
	}
}

// Every id names the table it belongs to, and every one of those names is a table in the domain
// model. The marker names are read out of the source rather than listed here, because a list here
// would be one more copy to forget.
func TestEntityMarkers_EveryName_IsATableInTheDomainModel(t *testing.T) {
	t.Parallel()

	tables := domainModelTables(t)
	markers := entityMarkerNames(t)
	require.NotEmpty(t, markers)

	for _, name := range markers {
		require.True(t, tables[name],
			"id marker names %q, which is not a table in %s", name, canondoc.DomainModelPath)
	}
}

// entityMarkerNames returns the table name every entity marker's `entity()` method returns.
func entityMarkerNames(t *testing.T) []string {
	t.Helper()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(root, "internal/core/id.go"), nil,
		parser.SkipObjectResolution)
	require.NoError(t, err)

	var names []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "entity" || fn.Recv == nil || fn.Body == nil {
			continue
		}
		require.Len(t, fn.Body.List, 1, "entity() should be a single return")
		ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
		require.True(t, ok)
		lit, ok := ret.Results[0].(*ast.BasicLit)
		require.True(t, ok, "entity() should return a string literal")
		unquoted, err := strconv.Unquote(lit.Value)
		require.NoError(t, err)
		names = append(names, unquoted)
	}
	return names
}

// domainModelTables reads the table names out of the domain model's tables.
func domainModelTables(t *testing.T) map[string]bool {
	t.Helper()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(root, canondoc.DomainModelPath))
	require.NoError(t, err)

	tables := map[string]bool{}
	row := regexp.MustCompile("^\\| `([a-z_]+)` \\|")
	for _, line := range strings.Split(string(raw), "\n") {
		if m := row.FindStringSubmatch(line); m != nil {
			tables[m[1]] = true
		}
	}
	require.NotEmpty(t, tables, "no tables parsed out of the domain model")
	return tables
}

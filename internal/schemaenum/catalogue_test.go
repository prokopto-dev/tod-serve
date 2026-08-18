package schemaenum_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/canondoc"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// docEnum is one `name: values` line of the canonical conventions §5 block.
type docEnum struct {
	Name   string
	Values []string
}

// canonicalEnums reads the catalogue out of the normative document. It is parsed rather than
// copied here: a copied list makes this test agree with the copy, and the pair that drifts is the
// catalogue and the document.
func canonicalEnums(t *testing.T) []docEnum {
	t.Helper()
	doc, err := canondoc.LoadCanonical()
	require.NoError(t, err)
	block, err := doc.BlockUnder("5. Enums", 0)
	require.NoError(t, err)

	var out []docEnum
	for _, line := range strings.Split(strings.TrimSpace(block.Body), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		name, values, found := strings.Cut(line, ":")
		require.True(t, found, "line %q is not `name: values`", line)
		out = append(out, docEnum{Name: strings.TrimSpace(name), Values: strings.Fields(values)})
	}
	require.NotEmpty(t, out, "the canonical enum block parsed to nothing")
	return out
}

func TestAll_Catalogue_MatchesCanonicalConventions(t *testing.T) {
	t.Parallel()

	var got []docEnum
	for _, e := range schemaenum.All() {
		got = append(got, docEnum{Name: e.Name, Values: e.Values})
	}

	// A whole-value comparison in document order, so an enum present on one side only, a value
	// added on one side only, and a reordering all fail here rather than three tests down.
	if diff := cmp.Diff(canonicalEnums(t), got); diff != "" {
		t.Errorf("catalogue differs from canonical conventions §5 (-document +code):\n%s", diff)
	}
}

func TestAll_EveryEnum_IsWellFormed(t *testing.T) {
	t.Parallel()
	valueShape := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	nameShape := regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)?$`)

	seenName := map[string]bool{}
	for _, e := range schemaenum.All() {
		require.Regexp(t, nameShape, e.Name)
		require.False(t, seenName[e.Name], "enum %s is declared twice", e.Name)
		seenName[e.Name] = true
		require.NotEmpty(t, e.Values, "enum %s has no values", e.Name)

		seenValue := map[string]bool{}
		for _, v := range e.Values {
			// The wire value IS the database value, so a capital letter or a hyphen here would
			// travel all the way to a CHECK constraint before anybody noticed.
			require.Regexp(t, valueShape, v, "enum %s", e.Name)
			require.False(t, seenValue[v], "enum %s repeats value %s", e.Name, v)
			seenValue[v] = true
		}
	}
}

// The ordered enums are ordered by a sentence in the canonical conventions, not by the order the
// values happen to be listed in. This reads that sentence.
func TestRank_OrderedEnums_MatchTheCanonicalOrdering(t *testing.T) {
	t.Parallel()
	doc, err := canondoc.LoadCanonical()
	require.NoError(t, err)

	chains := regexp.MustCompile("`([a-z_]+(?: < [a-z_]+)+)`").FindAllStringSubmatch(doc.Raw(), -1)
	require.Len(t, chains, 2, "canonical conventions should state exactly two orderings")

	for _, chain := range chains {
		ascending := strings.Split(chain[1], " < ")
		enum, ok := enumWithValues(ascending)
		require.True(t, ok, "no enum holds exactly %v", ascending)
		require.NotEqual(t, schemaenum.Unordered, enum.Order,
			"%s is ordered in the document and unordered in the catalogue", enum.Name)

		for want, value := range ascending {
			got, ok := enum.Rank(value)
			require.True(t, ok, "%s has no rank for %s", enum.Name, value)
			require.Equal(t, want, got, "%s: rank of %s", enum.Name, value)
		}
	}
}

// enumWithValues finds the enum whose value set is exactly values, in any order.
func enumWithValues(values []string) (schemaenum.Enum, bool) {
	for _, e := range schemaenum.All() {
		if len(e.Values) != len(values) {
			continue
		}
		all := true
		for _, v := range values {
			if !e.Contains(v) {
				all = false
				break
			}
		}
		if all {
			return e, true
		}
	}
	return schemaenum.Enum{}, false
}

func TestRank_UnorderedEnum_HasNoRank(t *testing.T) {
	t.Parallel()
	e, ok := schemaenum.Lookup(schemaenum.NameTodReportKind)
	require.True(t, ok)

	// An unordered enum refuses to invent a ranking rather than returning the position in the
	// list, which would look meaningful and be arbitrary.
	_, ranked := e.Rank(schemaenum.TodReportKindKill)
	require.False(t, ranked)
	require.True(t, e.Contains(schemaenum.TodReportKindKill))
}

func TestLookup_Name_FindsOrReportsMissing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		want  []string
		found bool
	}{
		{schemaenum.NameServer, []string{"blue", "green", "red"}, true},
		{schemaenum.NameCircleState, []string{"active", "archived"}, true},
		{"circle.status", nil, false},
		{"", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, ok := schemaenum.Lookup(tc.name)
			require.Equal(t, tc.found, ok)
			if diff := cmp.Diff(tc.want, e.Values); diff != "" {
				t.Errorf("values (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCheckSQL_Enum_RendersTheConstraint(t *testing.T) {
	t.Parallel()
	e, ok := schemaenum.Lookup(schemaenum.NameCircleState)
	require.True(t, ok)
	require.Equal(t, "CHECK (state IN ('active', 'archived'))", e.CheckSQL("state"))
}

func TestOrderBySQL_MembershipRole_RanksObserverLowest(t *testing.T) {
	t.Parallel()
	e, ok := schemaenum.Lookup(schemaenum.NameMembershipRole)
	require.True(t, ok)

	got, err := e.OrderBySQL("role")
	require.NoError(t, err)
	require.Equal(t,
		"CASE role WHEN 'owner' THEN 3 WHEN 'officer' THEN 2 "+
			"WHEN 'member' THEN 1 WHEN 'observer' THEN 0 END", got)
}

func TestOrderBySQL_UnorderedEnum_RefusesToInventAnOrder(t *testing.T) {
	t.Parallel()
	e, ok := schemaenum.Lookup(schemaenum.NameTodReportSource)
	require.True(t, ok)

	_, err := e.OrderBySQL("source")
	require.ErrorIs(t, err, schemaenum.ErrUnordered)
}

func TestOrder_String_NamesEveryOrder(t *testing.T) {
	t.Parallel()
	require.Equal(t, "unordered", schemaenum.Unordered.String())
	require.Equal(t, "ascending", schemaenum.Ascending.String())
	require.Equal(t, "descending", schemaenum.Descending.String())
}

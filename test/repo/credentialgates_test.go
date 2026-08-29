package repo

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/canondoc"
)

// bearerCredentialColumns maps a table holding a BEARER CREDENTIAL to the column that is the whole
// secret, and to the public prefix beside it where there is one.
//
// The rule these encode is written down in `db/queries/invite.sql` and enforced by nothing:
//
//	Looked up by hash on the unique index, NEVER by prefix: a prefix lookup is a brute-force
//	oracle.
//
// Every row here is a row whose HOLDER is the authorization. An invite code admits somebody to a
// circle; the owner grant in `tod_meta` makes its holder that circle's OWNER, which is the single
// most powerful thing a code can do in this product and the one with no membership behind it to
// revoke. A query that matched any of them on a prefix, a range or a pattern would turn an
// unguessable secret into one that can be walked.
//
// `tod_meta` is the one that matters most and is the least obvious. It is on the instance-scoped
// allowlist, so TEN001 skips the whole file, and it holds the owner grant under
// `owner_grant/<hex hash>` — a circle-scoped capability living on an instance-scoped table, for the
// same reason canonical §9 gives for `auth_flow` and `credential_ticket`: the row is addressed by
// an unguessable server-minted secret and never by circle. That reason is only true while nothing
// scans the table.
func bearerCredentialColumns() map[string]struct{ Secret, PublicPrefix string } {
	return map[string]struct{ Secret, PublicPrefix string }{
		"invite":            {Secret: "code_hash", PublicPrefix: "code_prefix"},
		"api_token":         {Secret: "token_hash", PublicPrefix: "token_prefix"},
		"credential_ticket": {Secret: "ticket_hash"},
		"auth_flow":         {Secret: "state"},
		"tod_meta":          {Secret: "key"},
	}
}

// patternMatch finds the SQL operators that turn an equality lookup into a walkable one.
var patternMatch = regexp.MustCompile(`(?i)\b(LIKE|GLOB|REGEXP)\b|\bsubstr\s*\(`)

// TestCredentialLookups_NoBearerCredential_IsAddressableByAPrefix is the gate behind the sentence
// `db/queries/invite.sql` already writes down.
//
// A prefix lookup against a credential column is a brute-force oracle: it turns "guess the whole
// secret" into "guess eight characters, then eight more". The public prefix exists precisely so a
// leaked token can be TRACED — it is loggable, it appears in `/me`, and it is in the token list
// every member can read — so a query that resolved a row from it would hand the tracing handle the
// authority of the secret.
//
// It reads every query file rather than the five named tables, because the failure is a query
// somebody adds later, in a file nobody thought was sensitive.
func TestCredentialLookups_NoBearerCredential_IsAddressableByAPrefix(t *testing.T) {
	t.Parallel()
	credentials := bearerCredentialColumns()

	checked := 0
	for _, q := range everyQuery(t) {
		for table, cols := range credentials {
			if !q.touches(table) {
				continue
			}
			checked++

			if cols.PublicPrefix != "" {
				require.NotContains(t, q.filter(), cols.PublicPrefix,
					"%s in %s filters %s on %s. That column is the PUBLIC half — it is logged, "+
						"and it is how a leaked credential is traced — so resolving a row from "+
						"it is a brute-force oracle. Match the whole secret, %s, on its unique "+
						"index", q.Name, q.File, table, cols.PublicPrefix, cols.Secret)
			}
			if match := patternMatch.FindString(q.filter()); match != "" {
				require.NotContains(t, q.filter(), cols.Secret,
					"%s in %s matches %s.%s with %q. A bearer credential is looked up by "+
						"equality on the whole secret or not at all; a pattern makes it walkable",
					q.Name, q.File, table, cols.Secret, strings.ToUpper(match))
			}
		}
	}

	// A gate that found nothing to look at is a gate reporting success over an empty search space,
	// which is how a rule quietly stops being enforced.
	require.Greater(t, checked, 10,
		"only %d queries touch a bearer-credential table; the table names in "+
			"bearerCredentialColumns have probably drifted from db/queries", checked)
}

// The columns named above still exist. Without this, renaming `code_hash` would leave the gate
// above green while checking a column nobody has.
func TestCredentialLookups_TheNamedColumns_AreInTheSchema(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(root, "db", "schema.hcl"))
	require.NoError(t, err)
	schema := string(raw)

	for table, cols := range bearerCredentialColumns() {
		body := tableBlock(t, schema, table)
		require.Contains(t, body, `column "`+cols.Secret+`"`,
			"bearerCredentialColumns says %s holds its secret in %s and the schema has no such "+
				"column; the gate is checking a name nobody uses", table, cols.Secret)
		if cols.PublicPrefix != "" {
			require.Contains(t, body, `column "`+cols.PublicPrefix+`"`,
				"bearerCredentialColumns names %s.%s as the public prefix and the schema has no "+
					"such column", table, cols.PublicPrefix)
		}
	}
}

// tableBlock returns one `table "name" { … }` block out of db/schema.hcl.
func tableBlock(t *testing.T, schema, name string) string {
	t.Helper()
	start := strings.Index(schema, "\ntable \""+name+"\" {")
	require.GreaterOrEqual(t, start, 0, "db/schema.hcl declares no table %q", name)
	end := strings.Index(schema[start+1:], "\n}\n")
	require.GreaterOrEqual(t, end, 0, "table %q is unterminated in db/schema.hcl", name)
	return schema[start : start+1+end]
}

// sqlQuery is one `-- name:` block out of db/queries.
type sqlQuery struct {
	File string
	Name string
	// SQL is the statement with its comment lines removed, so a comment SAYING code_prefix is not
	// read as a query that filters on it.
	SQL string
}

// touches reports whether the statement names the table.
func (q sqlQuery) touches(table string) bool {
	return regexp.MustCompile(`(?i)\b(?:FROM|JOIN|INTO|UPDATE)\s+` + table + `\b`).MatchString(q.SQL)
}

// filter returns the statement's WHERE clause, which is where a lookup happens. A column named in
// a projection or an INSERT's column list is not a way to address a row.
func (q sqlQuery) filter() string {
	at := strings.Index(strings.ToUpper(q.SQL), "WHERE")
	if at < 0 {
		return ""
	}
	return q.SQL[at:]
}

// everyQuery parses db/queries/*.sql into one block per `-- name:`.
func everyQuery(t *testing.T) []sqlQuery {
	t.Helper()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)
	files, err := filepath.Glob(filepath.Join(root, "db", "queries", "*.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "no query files; the parse is wrong")
	sort.Strings(files)

	var out []sqlQuery
	for _, path := range files {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)

		var current *sqlQuery
		for _, line := range strings.Split(string(raw), "\n") {
			if rest, ok := strings.CutPrefix(line, "-- name: "); ok {
				if current != nil {
					out = append(out, *current)
				}
				current = &sqlQuery{File: filepath.Base(path), Name: strings.Fields(rest)[0]}
				continue
			}
			if current == nil || strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			current.SQL += line + " "
		}
		if current != nil {
			out = append(out, *current)
		}
	}
	require.NotEmpty(t, out, "no queries parsed out of db/queries")
	return out
}

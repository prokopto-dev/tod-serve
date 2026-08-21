package apierr_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/canondoc"
)

// The two files that DECLARE codes. They are excluded from the search below, because a constant is
// obviously named where it is defined and finding it there would make every code look reachable.
var codeDeclarations = []string{
	filepath.Join("internal", "apierr", "codes.go"),
	filepath.Join("internal", "identity", "errors.go"),
}

// pendingCodes names every code in the closed enum that nothing emits yet, and says what will.
//
// It exists so that TestErrorCodes_EveryCode_IsEmittedOrExplicitlyPending can be a TWO-SIDED
// check. Without it, a code nobody can produce looks exactly like a code whose milestone has not
// landed, and `report_immutable` sat in the enum with no possible emitter — no PATCH or PUT on a
// report exists, so the framework answers `405` and the `409` was vocabulary a client could branch
// on and never see. DOC001 missed it because it compares the enum against the docs directory
// rather than against what is REACHABLE.
//
// A reason here is a promise about a milestone, not a shrug. A code with no emitter and no
// milestone should be deleted, which is what happened to `report_immutable`.
func pendingCodes() map[apierr.Code]string {
	return map[apierr.Code]string{
		// Produced by status rather than by name: the router answers some requests itself, and
		// `withFrameworkProblems` turns those into problems through `CodeForStatus`. They are
		// reachable — TestProblem_FrameworkErrors_AreRFC9457 drives each — and no Go line names
		// them, which is why this search cannot see them.
		apierr.CodeMethodNotAllowed:     "the edge maps the router's own 405 through CodeForStatus",
		apierr.CodeRequestTimeout:       "the edge maps a 408 through CodeForStatus",
		apierr.CodeUnsupportedMediaType: "the edge maps a 415 through CodeForStatus",

		// Emitted by internal/identity today, on paths whose ROUTES are in the registry and served
		// by nothing yet — `createAuthorizationURL`, `completeAuthorization`. They are named as
		// emitted rather than pending because the service produces them and a wiring is what is
		// missing, which is a different gap and one Server.Unimplemented already reports.
		apierr.CodeCredentialStale: "the OAuth flow milestone; the 60-second freshness rule " +
			"(ADR-0011) has no caller yet",

		// `provider_unverifiable` is the refusal to LINK a second identity through a provider with
		// no verifiable subject, and identity linking is a milestone of its own. `local` being
		// unverifiable is already visible everywhere else — the circle's revocation strength, the
		// invite preview, the membership — through `verifiable_subject` rather than through a
		// refusal, which is why nothing emits this one yet.
		apierr.CodeProviderUnverifiable: "the identity-linking milestone; linking through a " +
			"provider with no verifiable subject",
	}
}

// Every code in the closed enum is either emitted by this binary or named above with the milestone
// that will emit it.
//
// The search is over Go SOURCE rather than over behaviour, and that is a real limit: it proves a
// constant is referenced, not that the line referencing it can run. It is still the check that
// would have caught `report_immutable`, which no line named at all.
func TestErrorCodes_EveryCode_IsEmittedOrExplicitlyPending(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)

	names := codeConstants(t, root)
	used := identifiersInProductionSource(t, root)
	pending := pendingCodes()

	for _, def := range apierr.Codes() {
		emitted := false
		for _, ident := range names[def.Code] {
			if used[ident] {
				emitted = true
				break
			}
		}
		reason, named := pending[def.Code]
		if emitted == named {
			// Reported rather than fataled, so one run names every code that needs a decision
			// instead of the first one. `require` elsewhere in this repository stops at the first
			// failure on purpose; here the list IS the finding.
			t.Errorf("%q is emitted=%t and named-as-pending=%t: it must be exactly one. A code "+
				"nothing can produce is vocabulary a client may branch on and never see",
				def.Code, emitted, named)
		}
		if named {
			require.NotEmpty(t, reason, "%q is pending with no reason", def.Code)
		}
	}

	for code := range pending {
		_, ok := apierr.Lookup(code)
		require.True(t, ok, "%q is named as pending and is not in the enum", code)
	}
}

// codeConstants maps each code VALUE to every Go constant that holds it. There is more than one:
// internal/identity declares the subset it produces, so `conflict` is both `apierr.CodeConflict`
// and `identity.CodeConflict`, and either naming it is the code being emitted.
func codeConstants(t *testing.T, root string) map[apierr.Code][]string {
	t.Helper()
	out := map[apierr.Code][]string{}
	for _, rel := range codeDeclarations {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, 0)
		require.NoError(t, err, "parse %s", rel)

		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.ValueSpec)
			if !ok || len(spec.Values) != 1 {
				return true
			}
			lit, ok := spec.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			for _, name := range spec.Names {
				if strings.HasPrefix(name.Name, "Code") {
					out[apierr.Code(value)] = append(out[apierr.Code(value)], name.Name)
				}
			}
			return true
		})
	}
	require.NotEmpty(t, out, "no code constants parsed; the declaration list is wrong")
	return out
}

// identifierPattern matches a code constant's name as a whole word, so `CodeConflict` does not
// match `CodeConflictDetail` and a comment mentioning one does not count as emitting it.
var identifierPattern = regexp.MustCompile(`\bCode[A-Za-z0-9]+\b`)

// identifiersInProductionSource returns every `Code…` identifier named anywhere in this
// repository's non-test Go, outside the two files that declare them.
//
// Comments are stripped first. A constant named only in a comment is a constant nothing emits, and
// counting one would let a code stay in the enum on the strength of a sentence about it.
func identifiersInProductionSource(t *testing.T, root string) map[string]bool {
	t.Helper()
	declared := map[string]bool{}
	for _, rel := range codeDeclarations {
		declared[filepath.Join(root, rel)] = true
	}

	used := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules"):
			return filepath.SkipDir
		case d.IsDir() || !strings.HasSuffix(path, ".go"):
			return nil
		case strings.HasSuffix(path, "_test.go") || declared[path]:
			return nil
		}

		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, src, 0)
		if parseErr != nil {
			return parseErr
		}
		// Walk the tree rather than the bytes: an identifier in the AST is an identifier the
		// compiler saw, and comments are not in it.
		ast.Inspect(file, func(n ast.Node) bool {
			if ident, ok := n.(*ast.Ident); ok && identifierPattern.MatchString(ident.Name) {
				used[ident.Name] = true
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, used, "no code identifiers found; the walk is wrong")
	return used
}

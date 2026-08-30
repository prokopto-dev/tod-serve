// Package repogate holds the repository gates that a grep cannot express honestly.
//
// Most gates live in scripts/repo-gates.sh, where they run in CI without a Go toolchain and cost
// nothing. CLOCK001 is different: `time.Now` can be reached through an aliased import, and a gate
// that a two-character change defeats is a gate that will eventually be defeated by accident. The
// canonical conventions have always described CLOCK001 as an AST analyser; this is it.
//
// The analyser is run by TestCLOCK001_Repository_HasNoTimeNowOutsideClock in test/repo, so it
// fails the build rather than printing a warning nobody reads.
package repogate

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Rule bans a reference to named identifiers of an imported package, everywhere except an
// explicit set of directories.
type Rule struct {
	// ID is the gate's name, as it appears in docs/concepts/invariants.md.
	ID string
	// Import is the package path whose identifiers are banned, e.g. "time".
	Import string
	// Names are the banned identifiers within it, e.g. "Now".
	Names []string
	// AllowDirs are slash-separated directory prefixes, relative to the repository root, that may
	// use them. A prefix matches the directory itself and everything under it.
	AllowDirs []string
	// TestFilesOnly limits the rule to `*_test.go`. Some rules are about tests specifically: a
	// sleep in a test is a slow pass and a flaky failure, while a sleep in the running binary is
	// sometimes simply the right answer.
	TestFilesOnly bool
	// Reason is what a finding prints. It names the failure the rule prevents, because a gate
	// whose message is only its own name teaches nobody anything.
	Reason string
}

// ClockRule is CLOCK001: time.Now belongs to internal/clock alone.
//
// Test files are included. The canonical conventions say "outside internal/clock" with no
// exception for tests, and a test that reads the wall clock is a test that fails at midnight, on
// the last day of a month, in someone else's timezone.
func ClockRule() Rule {
	return Rule{
		ID:        "CLOCK001",
		Import:    "time",
		Names:     []string{"Now"},
		AllowDirs: []string{"internal/clock"},
		Reason:    "the clock is injected; time.Now belongs to internal/clock alone",
	}
}

// SleepRule is SLEEP001: a test may not sleep.
//
// Time-dependent tests use testing/synctest, which fakes the clock for the whole bubble, so a test
// that waits is a test that will be slow when it passes and flaky when the machine is busy.
func SleepRule() Rule {
	return Rule{
		ID:            "SLEEP001",
		Import:        "time",
		Names:         []string{"Sleep"},
		TestFilesOnly: true,
		Reason:        "time-dependent tests use testing/synctest; a sleeping test is a flaky test",
	}
}

// RouteRule is ROUTE001: an HTTP route is declared only in internal/api/register.go.
//
// AGENTS.md law 1 says routes live only in internal/api. This is stricter, and deliberately so:
// within that package there is exactly ONE file that may call the framework's registration
// functions, and it takes an operation id rather than a method and a path. A route registered any
// other way would not carry the permission, the scopes, the tenancy flag or the idempotency
// requirement the registry holds — and every architectural test that walks the registry would
// report success while missing it, which is the failure mode this repository exists to prevent.
//
// The allowance is a FILE rather than a directory. [Rule.allows] matches an exact path as well as a
// prefix, which is what makes that expressible.
//
// A grep cannot express this: `huma.Register` can be reached through an aliased import, and the
// convenience wrappers (`huma.Get`, `huma.Post`, …) register a route without the word `Register`
// appearing anywhere.
func RouteRule() Rule {
	return Rule{
		ID:     "ROUTE001",
		Import: "github.com/danielgtaylor/huma/v2",
		Names: []string{
			"Register", "AutoRegister",
			"Get", "Post", "Put", "Patch", "Delete", "Head", "Options",
		},
		AllowDirs: []string{"internal/api/register.go"},
		Reason: "a route is declared only through api.Register, which reads the route registry; " +
			"see AGENTS.md law 1",
	}
}

// skipDir reports whether a directory is outside what this module builds.
//
// The Go toolchain does not build `testdata` or `vendor`, so a rule about what this module
// compiles does not reach them. It is one function rather than the same two comparisons in each
// walker: three copies of a skip list is three chances for one of them to grow a third entry
// nobody else has, and a gate that walks MORE than its siblings reports findings they do not.
func skipDir(name string) bool { return name == "testdata" || name == "vendor" }

// Finding is one violation, with enough detail to click.
type Finding struct {
	// Rule is the gate ID.
	Rule string
	// File is the path relative to the repository root.
	File string
	// Line is 1-indexed.
	Line int
	// Ref is the offending reference as written, e.g. "t.Now" for an aliased import.
	Ref string
}

// String renders a finding as `file:line`-first, so an editor can jump to it.
func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %s uses %s (%s)", f.File, f.Line, f.Rule, f.Ref, f.Rule)
}

// Result is what one run of [Check] saw.
type Result struct {
	// Files is how many Go files were parsed. A caller checks it: a gate that reports success
	// over an empty search space is how a rule quietly stops being enforced.
	Files int
	// Findings are the violations, ordered by file and then line.
	Findings []Finding
}

// Check walks every Go file under each of dirs, relative to root, and reports every violation of
// every rule. Findings are ordered by file and then line, so a failure message is stable.
//
// Directories named `testdata` and `vendor` are skipped: the Go toolchain does not build them, so
// a rule about what this module compiles does not reach them.
func Check(root string, dirs []string, rules []Rule) (Result, error) {
	var result Result
	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return fmt.Errorf("walk %s: %w", p, err)
			}
			rel, relErr := filepath.Rel(root, p)
			if relErr != nil {
				return fmt.Errorf("relative path %s: %w", p, relErr)
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if skipDir(d.Name()) {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".go") {
				return nil
			}
			result.Files++
			src, readErr := os.ReadFile(p)
			if readErr != nil {
				return fmt.Errorf("read %s: %w", rel, readErr)
			}
			for _, rule := range rules {
				if rule.Allows(rel) || (rule.TestFilesOnly && !strings.HasSuffix(rel, "_test.go")) {
					continue
				}
				found, checkErr := CheckSource(rule, rel, string(src))
				if checkErr != nil {
					return checkErr
				}
				result.Findings = append(result.Findings, found...)
			}
			return nil
		})
		if err != nil {
			return Result{}, err
		}
	}
	slices.SortFunc(result.Findings, func(a, b Finding) int {
		if a.File != b.File {
			return strings.Compare(a.File, b.File)
		}
		return a.Line - b.Line
	})
	return result, nil
}

// CheckSource reports every violation of one rule in one file's source. It is exported so a test
// can prove the analyser catches an aliased import without committing a file that violates the
// rule it is testing.
func CheckSource(rule Rule, filename, src string) ([]Finding, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}

	local, dotImported := rule.localNames(file)
	if len(local) == 0 && !dotImported {
		return nil, nil
	}

	var findings []Finding
	record := func(pos token.Pos, ref string) {
		findings = append(findings, Finding{
			Rule: rule.ID,
			File: filename,
			Line: fset.Position(pos).Line,
			Ref:  ref,
		})
	}

	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			if pkg, ok := node.X.(*ast.Ident); ok &&
				slices.Contains(local, pkg.Name) && slices.Contains(rule.Names, node.Sel.Name) {
				record(node.Pos(), pkg.Name+"."+node.Sel.Name)
			}
			// Descend into the receiver only. Sel is a field name, never a reference to a
			// dot-imported identifier, and counting it would report `x.Now` on an unrelated type.
			ast.Inspect(node.X, visit)
			return false
		case *ast.KeyValueExpr:
			// A key is a field or map key, not a package-level reference.
			ast.Inspect(node.Value, visit)
			return false
		case *ast.Ident:
			if dotImported && slices.Contains(rule.Names, node.Name) {
				record(node.Pos(), node.Name)
			}
		}
		return true
	}
	ast.Inspect(file, visit)
	return findings, nil
}

// localNames returns the names by which the rule's import is reachable in this file, and whether
// it was dot-imported. A blank import contributes neither.
func (r Rule) localNames(file *ast.File) ([]string, bool) {
	var (
		names []string
		dot   bool
	)
	for _, spec := range file.Imports {
		if strings.Trim(spec.Path.Value, `"`) != r.Import {
			continue
		}
		switch {
		case spec.Name == nil:
			names = append(names, r.defaultLocalName())
		case spec.Name.Name == "_":
		case spec.Name.Name == ".":
			dot = true
		default:
			names = append(names, spec.Name.Name)
		}
	}
	return names, dot
}

// defaultLocalName returns the identifier an unaliased import of the rule's package binds.
//
// It is NOT always the last path segment. A module with a major-version suffix imports as
// `github.com/danielgtaylor/huma/v2` and binds `huma`, so reading the base would have made the
// analyser look for references to a package named `v2` and find none — a gate that reports success
// over nothing, which is the exact failure this package exists to prevent.
// TestRule_VersionedModulePath_BindsThePackageName pins it.
func (r Rule) defaultLocalName() string {
	base := path.Base(r.Import)
	if isMajorVersionSuffix(base) {
		return path.Base(path.Dir(r.Import))
	}
	return base
}

// isMajorVersionSuffix reports whether a path segment is `v2`, `v3` and so on — the Go module
// major-version convention.
func isMajorVersionSuffix(segment string) bool {
	if len(segment) < 2 || segment[0] != 'v' {
		return false
	}
	for _, c := range segment[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Allows reports whether a slash-separated path is inside one of the rule's permitted directories.
//
// An entry that names a FILE rather than a directory matches that file exactly, which is how
// ROUTE001 confines route registration to a single source file rather than to a package. It is
// exported so a test can assert the allowance without having to lay out a repository to walk.
func (r Rule) Allows(rel string) bool {
	for _, dir := range r.AllowDirs {
		if rel == dir || strings.HasPrefix(rel, dir+"/") {
			return true
		}
	}
	return false
}

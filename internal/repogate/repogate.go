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
				if d.Name() == "testdata" || d.Name() == "vendor" {
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
				if rule.allows(rel) || (rule.TestFilesOnly && !strings.HasSuffix(rel, "_test.go")) {
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
			names = append(names, path.Base(r.Import))
		case spec.Name.Name == "_":
		case spec.Name.Name == ".":
			dot = true
		default:
			names = append(names, spec.Name.Name)
		}
	}
	return names, dot
}

// allows reports whether a slash-separated path is inside one of the rule's permitted directories.
func (r Rule) allows(rel string) bool {
	for _, dir := range r.AllowDirs {
		if rel == dir || strings.HasPrefix(rel, dir+"/") {
			return true
		}
	}
	return false
}

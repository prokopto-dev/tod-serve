package repogate

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// EntropyRuleID is RAND001, as it appears in docs/concepts/invariants.md.
const EntropyRuleID = "RAND001"

// entropyImport is the one source a secret may be drawn from.
const entropyImport = "crypto/rand"

// entropyName is the identifier within it.
const entropyName = "Reader"

// EntropyField is a composite-literal field that carries an injected randomness source.
//
// Every constructor in this repository that mints a secret takes its randomness rather than
// reaching for it, and returns an error on a nil one rather than falling back to a default. That
// makes "a generator that quietly reaches for a weak source" a construction error instead of a
// review habit — but it does NOT force the caller to pass a cryptographic source, because the
// absence of a default only makes it a deliberate choice at the wiring site.
//
// RAND001 is what closes that. A deliberate choice nothing verifies is a wish.
type EntropyField string

// EntropyCall is a constructor whose argument at Arg is an injected randomness source.
type EntropyCall struct {
	// Package is the local package identifier as the call is written, e.g. `auth`.
	Package string
	// Func is the function name, e.g. `NewMinter`.
	Func string
	// Arg is the zero-based index of the randomness argument.
	Arg int
}

// EntropyFields are the struct fields RAND001 checks. Adding an entropy-taking config means adding
// its field name here, which is the point: the list is short, and a sink nobody added to it is a
// sink nobody is checking.
func EntropyFields() []EntropyField { return []EntropyField{"Entropy", "Random"} }

// EntropyCalls are the constructors RAND001 checks positionally.
func EntropyCalls() []EntropyCall {
	return []EntropyCall{
		{Package: "auth", Func: "NewMinter", Arg: 1},
		{Package: "core", Func: "NewGenerator", Arg: 0},
	}
}

// CheckEntropySource reports every place in one file that injects randomness from anything but
// `crypto/rand.Reader`.
//
// It is exported so a test can drive it with a deliberately wrong wiring, rather than the gate
// being proven only by never having fired.
func CheckEntropySource(filename, src string) ([]Finding, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}

	local := localNamesOf(file, entropyImport)
	var findings []Finding
	record := func(pos token.Pos, expr ast.Expr) {
		findings = append(findings, Finding{
			Rule: EntropyRuleID,
			File: filename,
			Line: fset.Position(pos).Line,
			Ref:  render(fset, expr),
		})
	}
	// isCryptoRandReader is the whole check. A named variable holding `rand.Reader`, a wrapper
	// function returning it, or an `io.Reader` parameter would all pass a "non-nil" test and fail
	// this one — which is the difference the brief this gate came from asked for.
	isCryptoRandReader := func(e ast.Expr) bool {
		sel, ok := e.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != entropyName {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		return ok && slices.Contains(local, pkg.Name)
	}

	fields := EntropyFields()
	calls := EntropyCalls()
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CompositeLit:
			for _, elt := range node.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || !slices.Contains(fields, EntropyField(key.Name)) {
					continue
				}
				if !isCryptoRandReader(kv.Value) {
					record(kv.Pos(), kv.Value)
				}
			}
		case *ast.CallExpr:
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			for _, want := range calls {
				if pkg.Name != want.Package || sel.Sel.Name != want.Func {
					continue
				}
				if want.Arg >= len(node.Args) {
					// A call with too few arguments does not compile, so this is a parse of
					// something that was never going to build. Reporting it is still right:
					// silence would be indistinguishable from a pass.
					record(node.Pos(), node)
					continue
				}
				if !isCryptoRandReader(node.Args[want.Arg]) {
					record(node.Args[want.Arg].Pos(), node.Args[want.Arg])
				}
			}
		}
		return true
	})
	return findings, nil
}

// CheckEntropy walks every non-test Go file under each of dirs and reports every violation.
//
// Test files are excluded, and that exclusion is the rule rather than an oversight: a test
// legitimately injects a deterministic reader so that an encoding can be asserted byte for byte,
// and forbidding that would make the encoding untestable. What RAND001 guards is the wiring the
// binary actually runs.
func CheckEntropy(root string, dirs []string) (Result, error) {
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
			if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
				return nil
			}
			result.Files++
			src, readErr := os.ReadFile(p)
			if readErr != nil {
				return fmt.Errorf("read %s: %w", rel, readErr)
			}
			found, checkErr := CheckEntropySource(rel, string(src))
			if checkErr != nil {
				return checkErr
			}
			result.Findings = append(result.Findings, found...)
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

// localNamesOf returns the names by which an import is reachable in a file. A dot import binds no
// name that could spell `rand.Reader`, so it contributes nothing here.
func localNamesOf(file *ast.File, importPath string) []string {
	var names []string
	for _, spec := range file.Imports {
		if strings.Trim(spec.Path.Value, `"`) != importPath {
			continue
		}
		switch {
		case spec.Name == nil:
			names = append(names, filepath.Base(importPath))
		case spec.Name.Name == "_", spec.Name.Name == ".":
		default:
			names = append(names, spec.Name.Name)
		}
	}
	return names
}

// render prints an expression back as source, so a finding quotes what was written rather than
// describing it.
func render(fset *token.FileSet, e ast.Expr) string {
	var b strings.Builder
	if err := printer.Fprint(&b, fset, e); err != nil {
		return "<unprintable expression>"
	}
	return b.String()
}

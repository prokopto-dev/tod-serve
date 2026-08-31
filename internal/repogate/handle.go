package repogate

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// HandleRuleID is SQL002: the analyser half of law 2, beside SQL001's grep. It answers whether a
// handle can be OBTAINED without importing database/sql, which the grep cannot see.
//
// This comment used to cite AGENTS.md as its authority, at a time when AGENTS.md law 2 said no
// SQL002 existed — two PRs raced and the prose lost. A comment naming a document that contradicts
// it is worse than no comment, so it states the rule instead of pointing at a copy of it.
const HandleRuleID = "SQL002"

// handleImport is the package whose handles must not leave internal/store.
const handleImport = "database/sql"

// HandleTypes are the `database/sql` types that ARE the connection.
//
// A `sql.Result` or a `sql.NullString` is a value that came back from a query and carries no
// ability to issue another one; these four are the handle itself, and holding one is the whole of
// what law 2 reserves to internal/store.
func HandleTypes() []string { return []string{"DB", "Tx", "Conn", "Stmt"} }

// CheckExportedHandles reports every exported declaration in one file that would let a
// `database/sql` handle out of the package.
//
// **This is the half SQL001 cannot see, and it is the half that matters.** SQL001 greps for the
// string `database/sql` in every non-test file outside internal/store, which answers "who imports
// it". It cannot answer "can a handle be obtained without importing it" — and a caller writing
//
//	db := store.Raw()
//
// names `database/sql` nowhere, compiles, and holds the connection. SQL001 stays green while every
// package in the repository can issue an untenanted query, which is the failure law 2 exists to
// prevent: ADR-0002 buys tenancy back with `circle_id` in every query's WHERE, and a raw handle
// goes around all of it.
//
// Exported declarations only. The store holds its handle in unexported fields — that is what
// holding it MEANS — so a rule that flagged those would flag the correct implementation.
//
// Checked: the results and parameters of exported functions and methods, exported struct fields,
// exported interface methods, and exported package-level variables and types. A handle can leave
// through any of them.
func CheckExportedHandles(filename, src string) ([]Finding, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}

	local, dotImported := handleLocalNames(file)
	if len(local) == 0 && !dotImported {
		return nil, nil
	}

	var findings []Finding
	record := func(pos token.Pos, ref string) {
		findings = append(findings, Finding{
			Rule: HandleRuleID, File: filename, Line: fset.Position(pos).Line, Ref: ref,
		})
	}

	// namesHandle reports whether a type expression mentions one of the handle types, however
	// deeply: `*sql.DB`, `[]*sql.Tx`, `map[string]*sql.Conn` and `func() *sql.DB` all hand one
	// over just as directly as the bare type does.
	namesHandle := func(expr ast.Expr) string {
		var ref string
		ast.Inspect(expr, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.SelectorExpr:
				if pkg, ok := node.X.(*ast.Ident); ok &&
					slices.Contains(local, pkg.Name) &&
					slices.Contains(HandleTypes(), node.Sel.Name) {
					ref = pkg.Name + "." + node.Sel.Name
					return false
				}
			case *ast.Ident:
				if dotImported && slices.Contains(HandleTypes(), node.Name) {
					ref = node.Name
					return false
				}
			}
			return true
		})
		return ref
	}

	checkFields := func(owner string, fields *ast.FieldList) {
		if fields == nil {
			return
		}
		for _, field := range fields.List {
			ref := namesHandle(field.Type)
			if ref == "" {
				continue
			}
			// An unnamed result — `func Open() (*sql.DB, error)` — has no field names, and it is
			// as much of a leak as a named one.
			if len(field.Names) == 0 {
				record(field.Pos(), owner+" -> "+ref)
				continue
			}
			for _, name := range field.Names {
				if name.IsExported() {
					record(name.Pos(), owner+"."+name.Name+" "+ref)
				}
			}
		}
	}

	for _, decl := range file.Decls {
		switch node := decl.(type) {
		case *ast.FuncDecl:
			if !node.Name.IsExported() || node.Type == nil {
				continue
			}
			// A method on an unexported type is unreachable from outside the package however
			// exported its own name is, so it cannot hand anything out.
			if node.Recv != nil && !exportedReceiver(node.Recv) {
				continue
			}
			checkSignature(node.Name.Name, node.Type, namesHandle, record)

		case *ast.GenDecl:
			for _, spec := range node.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if !s.Name.IsExported() {
						continue
					}
					switch t := s.Type.(type) {
					case *ast.StructType:
						checkFields(s.Name.Name, t.Fields)
					case *ast.InterfaceType:
						for _, m := range interfaceMethods(t) {
							if m.Name.IsExported() {
								checkSignature(s.Name.Name+"."+m.Name.Name, m.Type,
									namesHandle, record)
							}
						}
					default:
						if ref := namesHandle(s.Type); ref != "" {
							record(s.Name.Pos(), s.Name.Name+" "+ref)
						}
					}
				case *ast.ValueSpec:
					for _, name := range s.Names {
						if !name.IsExported() || s.Type == nil {
							continue
						}
						if ref := namesHandle(s.Type); ref != "" {
							record(name.Pos(), name.Name+" "+ref)
						}
					}
				}
			}
		}
	}
	return findings, nil
}

// checkSignature records a finding for a handle anywhere in a function's parameters or results.
//
// Parameters count as well as results. A package that ACCEPTS a `*sql.DB` is a package that holds
// one, and it is the same rule read from the other end.
func checkSignature(
	owner string, fn *ast.FuncType, namesHandle func(ast.Expr) string,
	record func(token.Pos, string),
) {
	for _, group := range []*ast.FieldList{fn.Params, fn.Results} {
		if group == nil {
			continue
		}
		for _, field := range group.List {
			if ref := namesHandle(field.Type); ref != "" {
				record(field.Pos(), owner+" "+ref)
			}
		}
	}
}

// interfaceMethods returns an interface's own method fields, skipping embedded interfaces, which
// have no name of their own to report.
func interfaceMethods(t *ast.InterfaceType) []struct {
	Name *ast.Ident
	Type *ast.FuncType
} {
	var out []struct {
		Name *ast.Ident
		Type *ast.FuncType
	}
	if t.Methods == nil {
		return out
	}
	for _, field := range t.Methods.List {
		fn, ok := field.Type.(*ast.FuncType)
		if !ok || len(field.Names) == 0 {
			continue
		}
		out = append(out, struct {
			Name *ast.Ident
			Type *ast.FuncType
		}{Name: field.Names[0], Type: fn})
	}
	return out
}

// exportedReceiver reports whether a method's receiver type is exported, looking through the
// pointer.
func exportedReceiver(recv *ast.FieldList) bool {
	if len(recv.List) == 0 {
		return false
	}
	expr := recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	ident, ok := expr.(*ast.Ident)
	return ok && ident.IsExported()
}

// handleLocalNames returns the names `database/sql` is reachable by in this file, and whether it
// was dot-imported. It mirrors [Rule.localNames]; the handle rule is about SIGNATURES rather than
// references, so it does not fit [Rule] and carries its own resolution.
func handleLocalNames(file *ast.File) ([]string, bool) {
	var (
		names []string
		dot   bool
	)
	for _, spec := range file.Imports {
		if strings.Trim(spec.Path.Value, `"`) != handleImport {
			continue
		}
		switch {
		case spec.Name == nil:
			names = append(names, "sql")
		case spec.Name.Name == "_":
		case spec.Name.Name == ".":
			dot = true
		default:
			names = append(names, spec.Name.Name)
		}
	}
	return names, dot
}

// HandleAllowDirs are the directories SQL002 does not walk.
//
// There is exactly one, and the exemption is earned rather than asserted. `internal/store/sqlitegen`
// is GENERATED by sqlc and never hand-edited (AGENTS.md), and what sqlc emits includes
//
//	type DBTX interface { PrepareContext(context.Context, string) (*sql.Stmt, error); … }
//	func (q *Queries) WithTx(tx *sql.Tx) *Queries
//
// Both are exported and both name a handle, so SQL002 reports them correctly — and neither can be
// changed here. What makes it safe is that no value of either type ever leaves the store:
// `Queries.db` is unexported, so a package cannot OBTAIN a `DBTX` to call `PrepareContext` on, and
// `WithTx` takes a `*sql.Tx` the caller would have to have imported `database/sql` to name, which
// is SQL001's finding.
//
// That reasoning is only true while nothing outside internal/store names `DBTX`, which is what
// [SqlitegenRule] checks. The exemption is a directory in one place with a rule beside it, rather
// than a comment.
func HandleAllowDirs() []string { return []string{"internal/store/sqlitegen"} }

// SqlitegenRule is the other half of [HandleAllowDirs]: `sqlitegen.DBTX` is named only inside
// internal/store.
//
// It is what keeps SQL002's one exemption honest. A package that got hold of a `DBTX` could call
// `PrepareContext` and hold a `*sql.Stmt` with `:=`, naming `database/sql` nowhere — SQL001 green,
// SQL002 not looking, and an untenanted statement in a package that should only ever reach the
// database through a generated, `circle_id`-carrying query.
//
// `Queries` and the generated parameter structs are deliberately NOT banned: every service takes a
// `*sqlitegen.Queries`, and that is the whole design — a query set whose statements are the ones
// TEN001 checks.
func SqlitegenRule() Rule {
	return Rule{
		ID:        "SQL002",
		Import:    "github.com/prokopto-dev/tod-serve/internal/store/sqlitegen",
		Names:     []string{"DBTX"},
		AllowDirs: []string{"internal/store"},
		Reason: "DBTX is the raw connection interface sqlc emits; holding one is holding a " +
			"handle, which internal/store reserves to itself (AGENTS.md law 2)",
	}
}

// CheckHandles walks every Go file under each of dirs, relative to root, and reports every
// exported declaration that would let a `database/sql` handle out of its package.
//
// It scans test files as well as source. A test helper in `internal/store` that exported the
// handle would put one within reach of every other package's tests, and a handle held in a test is
// a handle held.
func CheckHandles(root string, dirs []string) (Result, error) {
	var result Result
	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(root, dir),
			func(p string, d fs.DirEntry, err error) error {
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
				if allowedHandleDir(rel) {
					return nil
				}
				result.Files++
				src, readErr := os.ReadFile(p)
				if readErr != nil {
					return fmt.Errorf("read %s: %w", rel, readErr)
				}
				found, checkErr := CheckExportedHandles(rel, string(src))
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

// allowedHandleDir reports whether a path is inside one of [HandleAllowDirs].
func allowedHandleDir(rel string) bool {
	for _, dir := range HandleAllowDirs() {
		if rel == dir || strings.HasPrefix(rel, dir+"/") {
			return true
		}
	}
	return false
}

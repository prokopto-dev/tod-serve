// This directory is NOT part of the tod-serve Go module, and this file is what says so.
//
// Without it, `./...` walks into `web/node_modules`, and it finds Go source there: `flatted`, a
// transitive dependency of ESLint, ships a Go package. So `go build ./...`, `go vet ./...`,
// `go test ./...` and `golangci-lint run` would all compile whatever arbitrary Go code the
// JavaScript dependency tree happens to contain that week, and a broken one would be a red
// `make check` nobody could explain from the diff.
//
// A nested `go.mod` excludes the whole subtree from the parent module's package patterns, which is
// exactly the statement wanted. Nothing in here is ever imported: the console reaches Go as an
// embedded copy of `web/dist` under `internal/ui`, which `make build-web` writes.
//
// The Go version is the module's own and is deliberately not kept in step with the root one.
module github.com/prokopto-dev/tod-serve/web

go 1.26

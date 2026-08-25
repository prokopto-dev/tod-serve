# tod-serve — build and verification targets.
#
# Targets that cannot do real work yet call `notyet` with the roadmap phase that fills them in, and
# exit 0 so `make check` and CI stay green through Phase 0. `make status` derives the list of what is
# still stubbed straight from those call sites — there is no hand-maintained progress list, and
# adding one would be a second place to forget.
#
# A target that CAN do real work must do it unconditionally. A guard that skips the work when its
# inputs are missing turns into a guard that hides a broken toolchain the moment the inputs exist,
# and a green `make check` that ran nothing is worse than a red one.

SHELL := /bin/bash
.DEFAULT_GOAL := help

GO        ?= go
PKG       := ./...
BIN       := tod-serve
BUILD_DIR := ./bin
DB_PATH   ?= ./tod.db

# notyet <phase> <what> — a target that is declared but not yet implemented.
# No leading '@': call sites add it, so this also works inside shell if/else blocks.
define notyet
printf '\033[33m  not yet implemented\033[0m  %s\n  lands in: %s\n' "$(2)" "$(1)"
endef

## help: list every documented target
.PHONY: help
help:
	@printf 'tod-serve — pre-1.0, design phase. Run `make status` for what is implemented.\n\n'
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/^## //' | awk -F': ' '{printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

## status: what is still stubbed, derived from notyet call sites
.PHONY: status
status:
	@printf 'Implemented targets run real work. The following are declared and stubbed:\n\n'
	@grep -nE '^\t@?\$$\(call notyet,' $(MAKEFILE_LIST) \
	  | sed -E 's/^([0-9]+):\t@?\$$\(call notyet,([^,]+),(.*)\)$$/  \2\t\3/' \
	  | sort | awk -F'\t' '{printf "  \033[33m%-10s\033[0m %s\n", $$1, $$2}'
	@printf '\nSee ROADMAP.md for what each phase contains.\n'

## build: compile the binary, console included
.PHONY: build
build: build-web
	@mkdir -p $(BUILD_DIR)
	$(GO) build -trimpath -o $(BUILD_DIR)/$(BIN) ./cmd/$(BIN)

## build-web: build the console and stage it where go:embed reads it
# The staged copy lives at internal/ui/dist because `go:embed` cannot reach outside its own
# package directory. Only `.gitkeep` is committed there — it is what makes `//go:embed all:dist`
# compile on a clone where nobody has run this target, since an embed pattern matching no files is
# a COMPILE error and the Go package would otherwise stop building because a JavaScript toolchain
# had not been run.
#
# `ui.Available()` reports honestly when the placeholder is all there is, and `tod-serve serve`
# logs a warning rather than serving a blank page that looks like a broken console.
.PHONY: build-web
build-web:
	@if command -v npm >/dev/null 2>&1; then \
	  cd web && npm ci --silent --no-audit --no-fund && rm -rf dist && npm run build; \
	  cd .. && find internal/ui/dist -mindepth 1 ! -name .gitkeep -delete \
	    && cp -R web/dist/. internal/ui/dist/ \
	    && printf '\033[32m  built\033[0m     the console is staged in internal/ui/dist\n'; \
	 else printf '\033[33m  skipped\033[0m  npm is not on PATH; the binary will serve the API with no console\n'; fi

## fmt: format Go sources
.PHONY: fmt
fmt:
	$(GO) fmt $(PKG)

## vet: go vet over every package
.PHONY: vet
vet:
	$(GO) vet $(PKG)

## lint: run every linter in check mode
.PHONY: lint
lint: lint-repo lint-go lint-web

## lint-go: golangci-lint
# NOT a `notyet`: this target does real work the moment the tool is present, and CI always has it.
# A missing local binary is a missing local binary, not an unimplemented feature — conflating the
# two would put a permanent row in `make status` that never goes away and teach people to skim it.
.PHONY: lint-go
lint-go:
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint config verify && golangci-lint run; \
	 else printf '\033[33m  skipped\033[0m  golangci-lint is not on PATH; CI runs it (see .golangci.yml)\n'; fi

## lint-web: the console's ESLint run, its own rule's unit test, and the generated client
# Three things, because they fail differently:
#
#   1. `npm run lint` includes `tod/no-network-outside-api` — AGENTS.md law 7. WEB001 in
#      scripts/repo-gates.sh is the SECOND half of that rule and runs in the CI job with no npm,
#      because a lint rule is switched off by an `eslint-disable` comment and a grep is not.
#   2. `npm test` drives that rule against the shapes it exists to catch. A gate nobody has seen
#      fail is a gate nobody knows works.
#   3. `npm run gen:check` fails when web/src/api/generated.ts no longer matches
#      openapi/openapi.json. A console compiled against a spec the server has moved past is a
#      console that type-checks and 404s.
#
# NOT a `notyet`: like lint-go, this does real work the moment the toolchain is present, and CI
# always has it. A missing local npm is a missing local npm, not an unimplemented feature.
.PHONY: lint-web
lint-web:
	@if command -v npm >/dev/null 2>&1; then \
	  cd web && npm ci --silent --no-audit --no-fund && npm run lint && npm test && npm run gen:check; \
	 else printf '\033[33m  skipped\033[0m  npm is not on PATH; CI runs it (see web/eslint.config.js)\n'; fi

## lint-repo: repository gates (PIN001, TEN001, LOG001, MIG001, SQLC001, NET001, WEB001, CLOCK001, ...)
.PHONY: lint-repo
lint-repo:
	@bash scripts/repo-gates.sh

## test: the unit suite
.PHONY: test
test:
	$(GO) test -race $(PKG)

## test-golden: the consensus golden corpus
# Also reached by `make test`, since ./... covers it. This target exists so the corpus can be
# replayed on its own while working on internal/consensus, and so the thing docs/design/03-consensus.md
# calls the authority has a name at the command line.
.PHONY: test-golden
test-golden:
	$(GO) test ./test/golden/...

## test-integration: real SQLite in a temp dir
# Also run by `make test`, deliberately. A suite that only runs under its own target is a suite
# that stops running the first time somebody is in a hurry; this target is the focused re-run.
.PHONY: test-integration
test-integration:
	$(GO) test -race -count=1 ./internal/store/... ./internal/identity/identitysql/... ./db/... \
	  ./internal/tod/... ./internal/projection/...

## test-tenancy: cross-circle isolation over the route registry
# Walks the registry rather than a hand-written list, so a circle-scoped route added without
# coverage is a red test. It is PARTIAL: the circles, members, invites, ToD, quake and audit
# handlers are covered and the timer-override and event ones are not, so the test logs "N of 27" and
# enumerates the remainder with the milestone that owns each. `-v` is deliberate — that count is
# the whole point of running this on its own, and a green tick over a partial route set reported as
# "the tenancy gate passes" is exactly the failure this repository is built against.
.PHONY: test-tenancy
test-tenancy:
	$(GO) test -v -count=1 ./internal/api/ -run 'TestTenancy|TestPublicRoutes|TestInviteOracle'

## gen-openapi: regenerate openapi/openapi.json from the handlers
# Needs no external tool: the document is generated from the route registry and the handler types,
# so `make gen` can do this half on a machine with neither Atlas nor sqlc installed.
.PHONY: gen-openapi
gen-openapi:
	$(GO) test ./internal/api -run TestOpenAPISpec -update

## spec-diff: the OpenAPI document breaks no client against the base branch
# The change this exists for is a renamed `operationId`, which every other check here is blind to.
# BASE_REF=<ref> compares against something other than origin/main.
.PHONY: spec-diff
spec-diff:
	@bash scripts/spec-diff.sh

## gen-web: regenerate the console's TypeScript client from openapi/openapi.json
# The document is the contract, so the client is derived from it rather than written beside it —
# there is no hand-written request type anywhere in web/src, and a renamed field is a compile
# error in the console rather than a runtime `undefined` somebody meets on a raid night.
.PHONY: gen-web
gen-web:
	node web/scripts/generate-client.mjs

## gen: regenerate the enum locals, the migration and the sqlc bindings
# Needs Atlas and sqlc; the script says so by name if either is missing. ADR-0006 accepts that
# cost. The OpenAPI half lands with the API in Phase 1.
#
# NAME=<snake_case> names the migration when db/schema.hcl has changed.
.PHONY: gen
gen: gen-openapi gen-web
	@bash scripts/gen-schema.sh $(NAME)

## migrate: apply migrations to a local database
# Through the shipped binary rather than a migration CLI, because that is the path an operator
# takes and it is the only one worth having tested. TOD_DB_PATH overrides the target.
.PHONY: migrate
migrate:
	$(GO) run ./cmd/$(BIN) migrate --db $(DB_PATH)

## seed: load the embedded raid-target identity into a local database
# Identity only. Timers are NOT bundled and there is deliberately no target that loads them from
# this repository: they are community-derived, disputed, and live in tod-serve-p99-seed. Load them
# with `tod-serve seed timers --file <seed.json>`; canonical conventions section 15, SEED001.
.PHONY: seed
seed:
	$(GO) run ./cmd/$(BIN) seed targets --db $(DB_PATH)

## docs-check: every error code has a docs page, every ADR is within budget
.PHONY: docs-check
docs-check:
	@bash scripts/docs-check.sh

## verify-commands: every command named in AGENTS.md resolves to a real target
.PHONY: verify-commands
verify-commands:
	@fail=0; \
	for t in $$(grep -oE '^make [a-z-]+' AGENTS.md | awk '{print $$2}' | sort -u); do \
	  if ! grep -qE "^## $$t:" $(MAKEFILE_LIST); then \
	    printf '\033[31mAGENTS.md names `make %s`, which is not a documented target\033[0m\n' "$$t"; fail=1; \
	  fi; \
	done; \
	if [ $$fail -eq 0 ]; then printf 'every command in AGENTS.md resolves\n'; fi; \
	exit $$fail

## check: what CI runs
.PHONY: check
check: verify-commands docs-check lint vet build test

## clean: remove build output
.PHONY: clean
clean:
	rm -rf $(BUILD_DIR) web/dist
	@find internal/ui/dist -mindepth 1 ! -name .gitkeep -delete

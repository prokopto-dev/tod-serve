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
BUILD_DIR ?= ./bin

# GATE_CAPTURE — what the gates EMITTED during this run of `make check`, which is what DOC003
# verifies docs/concepts/invariants.md against. Written under $(BUILD_DIR), which is gitignored, and
# NEVER a checked-in artefact: a capture that can go stale is a second source of truth, and removing
# the second source of truth is the whole point of reading emissions rather than source text.
#
# Every target that emits a gate id appends to it. A new gate script must be teed here too — and if
# somebody forgets, DOC003 reports that gate as a phantom, loudly, rather than quietly not covering
# it. The failure lands in the right direction by construction.
GATE_CAPTURE ?= $(BUILD_DIR)/gate-emissions.txt
DB_PATH   ?= ./tod.db

# The version the binary reports, and the link flags that stamp it.
#
# `0.0.0-dev` is what main.go already defaults to, so a developer's `make build` and a bare
# `go build` disagree about nothing. deploy/Dockerfile overrides VERSION with the tag it is
# building, and overrides nothing else — so the image and a laptop are produced by the same
# recipe rather than by two spellings of it that drift.
VERSION   ?= 0.0.0-dev
LDFLAGS   ?= -s -w -X main.version=$(VERSION)

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
	$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BIN) ./cmd/$(BIN)

## build-web: build the console and stage it where go:embed reads it
# The staged copy lives at internal/ui/dist because `go:embed` cannot reach outside its own
# package directory. Only `.gitkeep` is committed there — it is what makes `//go:embed all:dist`
# compile on a clone where nobody has run this target, since an embed pattern matching no files is
# a COMPILE error and the Go package would otherwise stop building because a JavaScript toolchain
# had not been run.
#
# `ui.Available()` reports honestly when the placeholder is all there is, and `tod-serve serve`
# logs a warning rather than serving a blank page that looks like a broken console.
#
# BUILDING and STAGING are two steps, not one, because deploy/Dockerfile has npm in a DIFFERENT
# stage from the Go toolchain: `node:24` builds the console, the Go stage copies the result in as
# `web/dist`, and this target stages it. Folding the two together would mean the image describing
# how the binary is built a second time, in Dockerfile syntax, where it can drift from this file.
# The three outcomes are told apart out loud — a message saying the console was skipped when it was
# actually staged is exactly the confident mistake this repository is built against.
.PHONY: build-web
build-web:
	@if command -v npm >/dev/null 2>&1; then \
	  cd web && npm ci --silent --no-audit --no-fund && rm -rf dist && npm run build; \
	 elif [ -f web/dist/index.html ]; then \
	  printf '\033[33m  reusing\033[0m   npm is not on PATH; staging the console already built in web/dist\n'; \
	 else printf '\033[33m  skipped\033[0m  npm is not on PATH and web/dist holds no console; the binary will serve the API with no console\n'; fi
	@if [ -f web/dist/index.html ]; then \
	  find internal/ui/dist -mindepth 1 ! -name .gitkeep -delete \
	    && cp -R web/dist/. internal/ui/dist/ \
	    && printf '\033[32m  built\033[0m     the console is staged in internal/ui/dist\n'; fi

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
	@mkdir -p $(BUILD_DIR)
	@set -o pipefail; bash scripts/repo-gates.sh | tee -a $(GATE_CAPTURE)

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

## smoke: build the container image and drive a whole first deploy against it
# The executed version of docs/operations/deployment.md, and the only thing in this repository that
# runs the SHIPPED ARTEFACT rather than the source: migrate, init, seed, boot, redeem the one-time
# owner code over HTTP, report a ToD, read the board, take a backup and check it.
#
# NOT part of `make check`, which has to stay runnable on a machine with no Docker — and a gate that
# quietly skips when its toolchain is absent is a gate that reports success for a run that never
# happened. CI runs it as its own job (deploy / smoke).
.PHONY: smoke
smoke:
	@bash deploy/smoke.sh

## docs-check: every error code has a docs page, every ADR is within budget
.PHONY: docs-check
docs-check:
	@mkdir -p $(BUILD_DIR)
	@set -o pipefail; bash scripts/docs-check.sh | tee -a $(GATE_CAPTURE)

## verify-commands: every `make` target and `tod-serve` verb the docs name actually exists
# Two documents, two failure modes. A renamed Makefile target leaves stale prose; a runbook step
# written from memory leaves an operator mid-incident typing a verb that never existed. The second
# is resolved by ASKING THE BINARY rather than grepping for a `Use:` field, so nested verbs like
# `seed targets` are covered too.
.PHONY: verify-commands
verify-commands:
	@bash scripts/verify-commands.sh

## licence-check: no copyleft or source-available runtime dependency
# NOT in `lint-repo`, and the reason is the same one that keeps `verify-commands` out of it: this
# needs the Go toolchain to resolve the module graph, and the `lint / repo` CI job deliberately has
# none. It fails rather than skips when `go` is absent — a licence gate that quietly passed on the
# machine without a toolchain would be enforcing nothing on exactly the machines that skip the
# full suite.
.PHONY: licence-check
licence-check:
	@mkdir -p $(BUILD_DIR)
	@set -o pipefail; bash scripts/licence-gate.sh | tee -a $(GATE_CAPTURE)

## check: what CI runs
.PHONY: check
check: gate-capture-reset verify-commands licence-check docs-check lint vet build test doc-to-gate

## gate-capture-reset: start this run's gate capture empty
# `make check` must never verify against a capture left by an earlier run: that is precisely the
# stale second source of truth this design removes.
.PHONY: gate-capture-reset
gate-capture-reset:
	@mkdir -p $(BUILD_DIR)
	@: > $(GATE_CAPTURE)

## doc-to-gate: DOC003 — every mechanism invariants.md names actually exists
# It reads what the gates EMITTED rather than what their source contains, so no amount of quoting,
# escaping or heredoc nesting can fake a gate into existence. See scripts/doc-to-gate.sh.
#
# It needs the Go toolchain, because five gates -- CLOCK001, RAND001, ROUTE001, SLEEP001 and SQL002
# -- are internal/repogate rules that no shell run ever prints. `go test -list` is the toolchain's
# own answer to "what tests exist", and without it those five would be reported as phantoms. That is
# why this lives in CI's `build / test` job and NOT in `lint / repo`, which deliberately has no Go.
.PHONY: doc-to-gate
doc-to-gate:
	@mkdir -p $(BUILD_DIR)
	@$(GO) test -list '.*' $(PKG) >> $(GATE_CAPTURE)
	@TOD_GATE_CAPTURE=$(GATE_CAPTURE) bash scripts/doc-to-gate.sh

## clean: remove build output
.PHONY: clean
clean:
	rm -rf $(BUILD_DIR) web/dist
	@find internal/ui/dist -mindepth 1 ! -name .gitkeep -delete

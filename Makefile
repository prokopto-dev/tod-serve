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

## build: compile the binary
.PHONY: build
build:
	@mkdir -p $(BUILD_DIR)
	$(GO) build -trimpath -o $(BUILD_DIR)/$(BIN) ./cmd/$(BIN)

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
lint: lint-repo lint-go

## lint-go: golangci-lint
# NOT a `notyet`: this target does real work the moment the tool is present, and CI always has it.
# A missing local binary is a missing local binary, not an unimplemented feature — conflating the
# two would put a permanent row in `make status` that never goes away and teach people to skim it.
.PHONY: lint-go
lint-go:
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint config verify && golangci-lint run; \
	 else printf '\033[33m  skipped\033[0m  golangci-lint is not on PATH; CI runs it (see .golangci.yml)\n'; fi

## lint-repo: repository gates (PIN001, TEN001, NET001, CLOCK001, NOFLOAT001, PURE001/2)
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
.PHONY: test-integration
test-integration:
	@$(call notyet,Phase 1,boots a real SQLite database and exercises the store and API)

## test-tenancy: cross-circle isolation over the route registry
.PHONY: test-tenancy
test-tenancy:
	@$(call notyet,Phase 1,asserts a principal of circle A gets 404 on every circle-scoped operation of circle B)

## gen: regenerate enums, OpenAPI and sqlc bindings
.PHONY: gen
gen:
	@$(call notyet,Phase 1,writes the enum catalogue into the DDL CHECK and the OpenAPI schema, and runs sqlc)

## migrate: apply migrations to a local database
.PHONY: migrate
migrate:
	@$(call notyet,Phase 1,goose over db/migrations-sqlite via db/embed.go)

## seed: load the raid-target catalogue
.PHONY: seed
seed:
	@$(call notyet,Phase 3,embedded target identity; timers come from the separate tod-serve-p99-seed repo)

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
	rm -rf $(BUILD_DIR)

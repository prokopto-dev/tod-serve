# DOC003 — handover

**This branch is a snapshot of the abandoned approach, kept for its test corpus and its findings.
Do not continue it.** The mechanism is being replaced; see "The replacement" below.

## Why it was abandoned

Seven review findings, all one root cause: gate discovery read **source text**, so every round
handled one more lexical case and the next round found the next one.

| verdict sha | lexical case | direction |
|---|---|---|
| `ca50f7a` | the phantom-mechanism class itself | — |
| `ef00791` | comments | fail-open |
| `57569ad` | non-defining shell text | fail-open |
| `8b8283a` | unquoted prose (`echo report FOO001 …`) | fail-open |
| `dda5de0` | escaped double quotes (`echo "a\"; report FOO001 \"b"`) | fail-open |
| `e4bb280` | heredoc bodies | fail-open |
| `ee55e86` | mixed quoted/unquoted delimiter (`<<E"OF"`) | **fail-closed** |

Still unhandled when it was stopped: `$(...)`, backticks, line continuations.

Two of the seven were fail-CLOSED — a live gate reported as a phantom — which is the worse
direction and the one no reviewer was looking for:

- `s/#.*//` truncated at the `#` of `${x#prefix}` and lost any real call later on that line.
- `<<E"OF"` is delimiter `EOF` to bash, but the scanner records `E"OF"`, never closes the heredoc,
  and swallows every call after it. **This one is still live on this branch and is not fixed.**

## The replacement

Discover gates from what the scripts **emit when they run**, not from what they contain. A gate
that runs and prints its id definitionally exists, and no quoting, escaping or nesting can fake a
runtime pass line. The class closes in one change rather than one case per review round. It is also
what `internal/canondoc` already does for the normative documents: compare against the thing, not
against a copy of it.

## Measured constraints on that design

1. **Recursion.** `scripts/docs-check.sh` emits its own ids across 17 `report`/`pass` lines. If
   DOC003 discovers ids by running the gate scripts, it runs `docs-check.sh`, which runs DOC003.
2. **The scripts are not uniformly runnable.** `deploy-gates.sh` exits with a usage error without
   `<env|images|traefik> <deploy-directory> [root.go]`. `verify-commands.sh` emits no gate ids at
   all — its labels are `make` and `tod-serve`. So "run the gate scripts" means "run the Makefile's
   invocations of them".
3. **It covers shell only.** `CLOCK001`, `SLEEP001`, `ROUTE001`, `RAND001` and `SQL002` are Go —
   repogate rule ids and `test/repo` test names, which emit nothing from a shell run. `go test
   -list` is the emitted equivalent and is compiler-parsed rather than grepped, but it needs a Go
   toolchain, and the `lint / repo` job deliberately has none.

Constraints 1 and 2 both point the same way: **DOC003 should not run the gates itself.** `make
check` should capture the gate output once, and DOC003 verify against that capture — otherwise the
verification run is a second source of truth, which is the defect being removed.

## What is worth keeping

`test/repo/docgates_test.go` on this branch. The phantom corpus (a gate named only in a comment, a
string, an argument, a heredoc body) stays valid under any mechanism, and the NOT HELD / gitignore
escapes and the two vacancy checks are behaviour the replacement still needs. The shell-lexing
fixtures do not carry forward: under the new mechanism there is nothing for them to test.

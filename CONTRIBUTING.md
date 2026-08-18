# Contributing

Thanks for looking. This project is pre-1.0 and in its design phase — there is no working software
yet, which means **the most valuable contribution right now is an argument, not a patch.**

## Before you write code

Read [AGENTS.md](AGENTS.md). It is normative and it is short.

Then read the decision record for whatever you are about to change:
[docs/adr/](docs/adr/). If your change contradicts an accepted ADR, the change is a new ADR that
supersedes it — not an edit to the old one. Accepted ADRs are never rewritten; that is what makes
them worth reading in two years.

## The governing rule

**A rule without a gate is a wish.** If you add a rule, add the test, lint rule, CI gate or database
trigger that enforces it, and name it in [docs/concepts/invariants.md](docs/concepts/invariants.md).

If you cannot enforce it mechanically, say so in the PR. A rule documented as though it were enforced
is worse than an acknowledged review rule, because everyone downstream assumes it holds.

## What we especially want

| | |
|---|---|
| **P99 domain corrections** | If a log format, a raid target, or a piece of spawn mechanics is wrong here, that is the highest-value bug in the tracker. Use the `parser-bug` or `timer-dispute` template |
| **Attacks on the consensus rule** | [docs/design/03-consensus.md](docs/design/03-consensus.md) §9 lists the weaknesses we already know about. Finding one we do not is worth more than a feature |
| **A raid leader's opinion** on the early-bias tiebreak | Consensus §5 breaks a tie toward the earlier time on the theory that a missed spawn costs more than a wasted trip. That is a judgement call made by someone who is not running your raid |

## What we will push back on

- **Inventing a regex for an unverified log format.** Add a golden fixture marked `unverified` and
  open an issue. A guessed format produces silently wrong ToDs, which is worse than an error.
- **Bundling timer data.** Respawn and variance numbers are community-derived, disputed, and their
  most convenient source is a wiki whose licence we have not cleared. They load from a separate seed
  repository. See [canonical conventions §15](docs/design/00-canonical-conventions.md#15-data-provenance).
- **Weakening a test to go green.** Including regenerating a golden fixture.
- **A feature that implies more certainty than we have.** No probability curve over the variance
  band, no confidence score as a float. The failure mode designed against throughout is a
  *confident mistake*.

## Mechanics

```bash
make help      # every target
make check     # what CI runs
make status    # what is still stubbed
```

- **Sign off every commit**: `git commit -s`. Contributions are under the
  [DCO](https://developercertificate.org/). There is no CLA.
- **Conventional Commits on the PR title only.** Squash-merge makes the PR title the commit subject.
  Your WIP commits can say whatever you like.
- **Docs change in the same PR as the behaviour.** A PR that changes behaviour and not the document
  describing it is incomplete, not "docs to follow".
- Prose wraps at 100 columns. Tables may run over.

## Security

Do not open a public issue for a vulnerability. See [SECURITY.md](SECURITY.md).

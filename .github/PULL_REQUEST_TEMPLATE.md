## What and why

<!-- What changes, and the problem it solves. Link the issue. -->

## Checklist

- [ ] Signed off (`git commit -s`) — DCO, no CLA
- [ ] PR **title** follows Conventional Commits (squash-merge makes it the commit subject)
- [ ] `make check` passes
- [ ] Docs changed in this PR if behaviour changed
- [ ] **Any new rule names the gate that enforces it**, and the gate is in this PR

## If this touches an invariant

- [ ] `docs/concepts/invariants.md` updated with the mechanism
- [ ] No test was weakened or skipped to make this green
- [ ] No golden fixture was regenerated to make this green

## If this touches tenancy, identity or the consensus rule

- [ ] An ADR exists, or this PR adds one
- [ ] The negative consequences are stated, not just the benefits

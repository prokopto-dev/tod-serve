// Package setup is first-run setup: what a fresh instance needs before anybody can sign in, and
// the window during which a caller holding `TOD_SETUP_TOKEN` may write it.
//
// It exists because of a constraint that is not going away. On a fresh database nobody holds a
// credential and no circle exists, so no HTTP route can authorise anything from the domain — which
// is why [ADR-0012] put the first `instance_grant` at the console. Something has to write the first
// rows. This package is the second answer to "what, and authorised how": a token the operator sets
// in `.env` beside the pepper and the session key, and a window that closes the moment somebody
// can administer the instance.
//
// # The window is derived
//
// [Service.Available] is true exactly while no identity holds an administrator permission —
// [instancegrant.AdministratorExists]. There is no `setup_complete` row and there never will be:
// derived state is never authority here, and a stored flag is the thing that gets out of step with
// what is true.
//
// It is derived from the ADMINISTRATOR and not from the `instance` row, and the difference is the
// whole reason the derivation is written down. An instance row exists the moment the first step of
// setup succeeds; an administrator exists only once somebody has redeemed the owner code. Closing
// the window on the first fact leaves an operator whose browser died in between locked out of the
// instance AND out of the wizard that would fix it.
//
// # Setup is a sequence, not a transaction
//
// Every step is create-if-absent and the order is chosen so that **every prefix is a state
// [Service.Describe] can report and [Service.Run] can resume from**. The composing services each
// own a transaction of their own — [circle.Service.Create], [catalogue.Service.SeedTargets] and the
// rest — and one transaction across all of them would mean rewriting four packages while holding
// SQLite's only write lock across the whole catalogue seed. [ADR-0016] states that trade in full.
//
// # What it does not do
//
// It does not migrate. `tod-serve migrate` is a separate, deliberate step and the deploy pipeline
// runs it; a server that migrates on demand is the one rule this codebase will not break. And it
// removes nothing: `tod-serve init`, `circle create` and `instance grant` are still there, because
// they are the way back when nobody can sign in.
//
// [ADR-0012]: docs/adr/0012-instance-grants-are-a-capability-ledger.md
// [ADR-0016]: docs/adr/0016-first-run-setup-is-an-env-token-and-a-derived-window.md
package setup

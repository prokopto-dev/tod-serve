// Package catalogue owns raid-target identity, the per-server respawn timers hung off it, and the
// per-circle overrides that disagree with them.
//
// The split in this package mirrors the split canonical conventions §15 draws, and it is a licence
// boundary rather than a layering preference:
//
//   - Target IDENTITY — names, zones, expansions, categories, aliases — ships embedded, in
//     [Embedded], as our own literals. They are facts about the game.
//   - Timer DATA does not ship. Respawn and variance numbers are community-derived, genuinely
//     disputed, and load from the separate `tod-serve-p99-seed` repository through [ParseSeed] and
//     [Service.ApplySeed]. SEED001 in scripts/repo-gates.sh is the mechanism.
//
// An instance that has never been handed a seed file therefore holds a complete catalogue of
// targets and no timers at all, and that is a supported state rather than a broken one: every
// [Service.ResolveTimer] answers [TimerSourceNone], every board row reads `no_timer`, and reports
// are recorded exactly as they would be otherwise. It is the state the operator's VPS is in on the
// day they install it, so it is the default case the tests cover.
//
// Name matching lives here and only here. `name_norm` is [core.Normalise] applied in Go — core
// SQLite has no NFKC and its `lower()` is ASCII-only — and the resolve ladder in [Service.Resolve]
// is the single matcher: `createTodReport`'s `target_name`, the board's `q` filter and
// `resolveRaidTarget` all run the same rungs, so a plugin that sends a parsed mob name and the
// officer typing into a search box cannot disagree about which mob they meant.
package catalogue

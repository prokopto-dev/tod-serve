# `unknown_target`

**HTTP 422** · `type: https://docs.tod-serve.org/errors/unknown_target`

The `target_name` you sent did not resolve to any raid target, at any rung of the ladder — exact
`name`, `name_norm`, alias, `alias_norm`, prefix, substring.

## What causes it

- A misparse, or a mob that is not in the catalogue.
- A name whose punctuation differs from the canonical form. Normalisation strips `'`, `` ` `` and
  `-` and casefolds, so `` Vulak`Aerr ``, `Vulak'Aerr`, `VulakAerr` and `vulak aerr` all resolve —
  if yours did not, the difference is something else.

## What the client should do

Call `resolveRaidTarget` to see what the ladder does with the string, or send `target_id` instead.
An officer with `catalogue.manage` can add a missing target or an alias. **Do not invent a
client-side catalogue to work around this** — the resolve ladder exists precisely so the plugin
never has to hold one.

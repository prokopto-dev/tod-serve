# Glossary

Project 1999 raiding vocabulary, for contributors who do not play. Where a term drives a schema
decision, the decision is named.

## The world

| Term | Meaning |
|---|---|
| **Project 1999** | A volunteer-run EverQuest emulator, frozen at the Scars of Velious expansion. Level cap 60, no AA, no Bazaar. |
| **Blue / Green / Red** | The three servers — PvE, time-locked progression, and PvP. Separate worlds with separate spawn clocks. A Blue ToD says nothing about Green, which is why [a circle is pinned to one server](../adr/0009-circle-pinned-to-one-server.md). |
| **Eras** | Classic, Kunark, Velious. Determines which raid targets exist. |
| **Titanium client** | The 2005 client P99 runs on. It cannot be modded, so every tool is out-of-process and read-only. |
| **MacroQuest** | Bannable on P99. The reason this project only ever reads a log file. |

## Raid targets and spawns

| Term | Meaning |
|---|---|
| **ToD** | Time of death. When a raid target was killed — the input this entire system exists to record. |
| **Variance** | The randomised spawn window on a raid target, which is why a raid session can be six hours of waiting punctuated by a kill. Modelled as [two offsets from the ToD](../adr/0008-windows-are-offsets.md), never as `base ± variance`. |
| **Window** | The span between the earliest and latest possible spawn. The product's central output is not a countdown but *where now sits in the window*. |
| **Overdue** | Past the close of the window. Means the ToD is wrong, the timer is wrong, or someone killed it quietly. **Real intel, not an error state.** |
| **Repop / quake** | A GM-triggered earthquake respawns every raid target on the server at once. One `quake_event`, never N kill reports — see [domain model](../design/01-domain-model.md). |
| **Tracker** | One of two players per zone permitted to watch for a spawn. They may not pull, heal, buff, or do anything that would appear in an encounter log. |
| **Race line** | The physical start position defined per raid target, behind which guilds wait for a spawn. |
| **FTE** | First to engage. The server shouts which guild got aggro first; kill-stealing after an FTE is a petitionable offence. |
| **Rotation** | A scheduled sharing of a target between guilds. Most P99 targets are FTE races, but Plane of Sky runs a weekly rotation. Explicitly [out of scope at 1.0](../../ROADMAP.md). |

## Raid targets by era

Not exhaustive — the shipped catalogue is the authority.

| Era | Targets |
|---|---|
| Classic | Lord Nagafen, Lady Vox, Phinigel Autropos, Plane of Fear, Plane of Hate, Plane of Sky |
| Kunark | Trakanon, Gorenaire, Severilous, Talendor, Faydedar, Venril Sathir, Veeshan's Peak, the Chardok royals |
| Velious | Kael Drakkel and the Avatar of War, Temple of Veeshan and NToV ending at ``Vulak`Aerr``, Sleeper's Tomb, Plane of Growth, the Western Wastes dragons |

## Log lines

The client writes `eqlog_<Character>_<server>.txt`. Timestamps are `[Mon Aug 18 02:14:07 2026]` —
**the machine's local clock, with no timezone**, which is the single most likely source of bad data
in this system. See [consensus §9](../design/03-consensus.md#9-known-weaknesses).

| Line | Shape |
|---|---|
| **Slain** | `` Vulak`Aerr has been slain by Tankguy! `` — the automatic kill-credit trigger, and the primary ToD source. |
| **Backticks** | Many mob names contain one (``Vulak`Aerr``, ``N`Kotik``). `raid_target.name_norm` strips it, which is exactly why [the normalisation rule exists](../design/00-canonical-conventions.md#8-database-conventions). |

## This project's own vocabulary

| Term | Meaning |
|---|---|
| **Circle** | The tenant. Any set of people who agree to pool ToDs — a guild, or four friends. Pinned to one server. |
| **Destination** | What the plugin holds: `(endpoint, token, circle)`. Two destinations may be two circles on one host or two hosts entirely. |
| **Report** | One immutable claim that a target died at a time. Never updated, never deleted. |
| **Cluster** | The set of reports the consensus rule decides describe *the same kill*. |
| **Contested** | Reporters disagree in a way the derivation will not resolve silently. Surfaced with alternatives. |
| **Evidence** | The counts behind an answer — reporters, log lines, spread. The actual contract; `confidence` is a convenience derived from it. |
| **Revocation strength** | Whether banning someone from a circle sticks. A property of the circle *and* of the membership, because it depends on the identity provider. See [ADR-0003](../adr/0003-pluggable-identity-providers.md). |

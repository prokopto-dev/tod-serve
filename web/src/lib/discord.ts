// Reading a Discord channel out of whatever the officer pasted, and what a rebind costs.
//
// Pure — no React, no transport — for the same reason `./gate.ts` is: what decides the request
// this console makes is worth driving directly rather than through a renderer. `discord.test.ts`
// is that drive.
//
// **The parser is generous on purpose**, the same way `internal/invite`'s is for a hand-typed
// code. `docs/operations/discord-bot.md §5` tells an operator to turn on Developer Mode and use
// *Copy Channel ID*, which yields bare digits — but the thing already on somebody's clipboard is
// usually one of the other three: a channel mention lifted out of a message, a *Copy Link*, or a
// *Copy Message Link*. All four name the channel unambiguously, and refusing three of them teaches
// nothing except that this form is fussy.
//
// The two refusals are refusals because they are NOT channel ids and no amount of generosity makes
// them one: a `#name` is not stable and is not what an interaction carries, and a DM has no guild
// at all, so a binding on one could never resolve — `Resolve` compares the interaction's guild
// against the binding's, and a DM interaction carries none.

/** SNOWFLAKE_HINT is the server's own rule, in the server's own words. `internal/discord`. */
export const SNOWFLAKE_HINT = '1 to 20 digits'

const SNOWFLAKE = /^[0-9]{1,20}$/

/**
 * isSnowflake reports whether a string is shaped like a Discord id.
 *
 * It is a COURTESY and not an authority: `checkSnowflake` in `internal/discord` is what decides,
 * and a form that let a wrong id through would earn a `422` naming the field. What this buys is
 * that the officer finds out before the round trip — and, more usefully, that the form can tell a
 * channel NAME from an id and say something specific about it.
 */
export function isSnowflake(value: string): boolean {
  return SNOWFLAKE.test(value.trim())
}

/** ChannelReference is a channel, and the guild the input named alongside it if it named one. */
export interface ChannelReference {
  channel_id: string
  /**
   * guild_id is set only when the pasted form CARRIED one — a link does, a bare id does not.
   *
   * It is `null` rather than an empty string so a caller cannot fill the guild field with nothing
   * and believe it has been answered. The guild is stored and compared on every interaction, so a
   * wrong one is a channel that silently resolves to nothing.
   */
  guild_id: string | null
  /** form names which spelling was recognised, so the screen can show what it understood. */
  form: 'id' | 'mention' | 'link'
}

export type ChannelParse = { ok: true; ref: ChannelReference } | { ok: false; reason: string }

// A channel link, on any of the three Discord domains, with or without a scheme, and with or
// without the message id a *Copy Message Link* adds. `@me` is matched deliberately rather than
// falling through to the generic refusal: a DM link is the one wrong paste that looks right.
const CHANNEL_LINK =
  /^(?:https?:\/\/)?(?:(?:ptb|canary)\.)?discord(?:app)?\.com\/channels\/(@me|[0-9]{1,20})\/([0-9]{1,20})(?:\/[0-9]{1,20})?\/?$/

const CHANNEL_MENTION = /^<#([0-9]{1,20})>$/

/**
 * parseChannelReference reads a channel out of a bare id, a `<#…>` mention or a channel link.
 *
 * A link carries the guild too, which is the case worth having: the two ids have to agree, they
 * are twenty digits each, and transposing them produces a binding that is accepted and resolves to
 * nothing. One paste that fills both fields cannot transpose them.
 */
export function parseChannelReference(input: string): ChannelParse {
  const value = input.trim()
  if (value === '') return { ok: false, reason: 'Paste a channel id or a channel link.' }

  if (SNOWFLAKE.test(value)) {
    return { ok: true, ref: { channel_id: value, guild_id: null, form: 'id' } }
  }

  const mention = CHANNEL_MENTION.exec(value)?.[1]
  if (mention !== undefined) {
    return { ok: true, ref: { channel_id: mention, guild_id: null, form: 'mention' } }
  }

  const link = CHANNEL_LINK.exec(value)
  // Both groups are non-optional in the pattern, so a match has both. The check is what says so
  // to the compiler rather than an assertion that says it to nobody.
  const [, linkGuild, linkChannel] = link ?? []
  if (linkGuild !== undefined && linkChannel !== undefined) {
    if (linkGuild === '@me') {
      return {
        ok: false,
        reason:
          'That is a direct message, not a channel in a server. A binding stores the guild and ' +
          'compares it against every interaction, and a DM carries no guild — so a binding on ' +
          'one could never resolve.',
      }
    }
    return { ok: true, ref: { channel_id: linkChannel, guild_id: linkGuild, form: 'link' } }
  }

  if (value.startsWith('#')) {
    return {
      ok: false,
      reason:
        'That is the channel’s name, and a name is not what an interaction carries — renaming ' +
        'the channel would silently break the binding. Turn on User Settings → Advanced → ' +
        'Developer Mode, then right-click the channel → Copy Channel ID.',
    }
  }

  return {
    ok: false,
    reason:
      'Not a channel id (' +
      SNOWFLAKE_HINT +
      '), a <#…> mention or a channel link. Right-click the channel → Copy Link, or turn on ' +
      'Developer Mode and use Copy Channel ID.',
  }
}

/**
 * Disclosure is the part of a binding an officer is actually deciding about.
 *
 * `updated_at` is deliberately not in it. Any write bumps that column, so comparing it would stop a
 * flip because somebody re-asserted the same two values — a refusal over nothing, which is how a
 * check that matters gets clicked through.
 */
export interface Disclosure {
  allow_visible: boolean
  discord_guild_id: string
}

/** Drift is one field that moved between what an officer read and what is there now. */
export interface Drift {
  field: string
  seen: string
  now: string
}

/**
 * driftedSince reports what changed between the binding an officer acted on and the live one.
 *
 * **The server cannot make this check, and that is why it is here.** The flip reads the binding
 * immediately before writing it, so the entity tag it quotes back is always fresh — `If-Match`
 * therefore holds against a change landing during those two requests and against nothing else. The
 * window that matters is longer: it opens when the list is rendered and closes when somebody
 * presses a button, and a second officer's disclosure decision lands in the middle of it.
 *
 * That was a real defect in the first version of this screen, which unbound and re-bound with
 * `If-Match: *`. The wildcard reads as a precondition and is not one here — nothing exists at that
 * moment because the same flow deleted it one request earlier — so a colleague's change was
 * destroyed unseen and the ephemeral value from a stale row was written back over it. A visible
 * channel silently became ephemeral, or a corrected guild reverted, and the audit log recorded the
 * second officer as having decided it.
 *
 * So the fresh read is compared against what the officer SAW, and any difference stops the write.
 * "You cannot reverse a decision you have not read" is the rule the entity tag encodes; this is
 * the same rule over the window the tag cannot see.
 */
export function driftedSince(seen: Disclosure, fresh: Disclosure): Drift[] {
  const out: Drift[] = []
  if (seen.allow_visible !== fresh.allow_visible) {
    out.push({
      field: 'visible replies',
      seen: seen.allow_visible ? 'allowed' : 'off',
      now: fresh.allow_visible ? 'allowed' : 'off',
    })
  }
  if (seen.discord_guild_id !== fresh.discord_guild_id) {
    out.push({
      field: 'Discord server',
      seen: seen.discord_guild_id,
      now: fresh.discord_guild_id,
    })
  }
  return out
}

/**
 * widensDisclosure reports whether a change exposes more than the binding does now.
 *
 * It drives the confirmation, and only one direction gets one. Turning the switch off reduces what
 * the channel discloses, and a confirmation on the safe direction is what teaches people to click
 * through the one that matters.
 */
export function widensDisclosure(current: Disclosure, allowVisible: boolean): boolean {
  return allowVisible && !current.allow_visible
}

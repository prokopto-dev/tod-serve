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
 * Rebind is what changing a live binding's visible-reply switch actually costs, enumerated so the
 * screen states it rather than implying an in-place edit.
 *
 * **There is no in-place edit available to any browser.** `bindCircleDiscordChannel` is a
 * create-or-replace PUT: a create takes `If-Match: *`, and a replace takes the exact entity tag of
 * the binding — which only that PUT's own response carries. `listCircleDiscordChannels` answers no
 * `ETag`, per-item or otherwise, and there is no single-binding read, so a console that has merely
 * LISTED holds no tag it could send. Sending `*` at a binding that exists is refused with `412`,
 * which is the concurrency rule working exactly as intended: it stops an officer reversing a
 * disclosure decision they have not read.
 *
 * So the flip is `unbind` then `bind`, and every cost of that is listed here rather than hidden:
 * the switch is a disclosure decision, and the officer making one is owed the shape of it.
 */
export interface Rebind {
  /** allow_visible is the value the new binding is created with. */
  allow_visible: boolean
  /** widensDisclosure is true for the direction that exposes more, and drives the confirmation. */
  widensDisclosure: boolean
  /**
   * worstOutcome is what a failure between the two requests leaves behind.
   *
   * It is `unbound` in BOTH directions, and that is the property that makes the two-step
   * acceptable at all: a half-completed flip removes the channel's binding, and an unbound channel
   * discloses nothing — the bot answers ephemerally and asks which circle you meant. There is no
   * failure here whose residue is more disclosure than the officer asked for.
   */
  worstOutcome: 'unbound'
}

/** rebindFor describes the flip from a binding's current switch to the one an officer pressed. */
export function rebindFor(current: { allow_visible: boolean }, allowVisible: boolean): Rebind {
  return {
    allow_visible: allowVisible,
    widensDisclosure: allowVisible && !current.allow_visible,
    worstOutcome: 'unbound',
  }
}

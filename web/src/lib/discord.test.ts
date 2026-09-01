// What the binding form understands, driven.
//
// The scenario that made this a module rather than a regex inside a component: an officer has the
// channel on their clipboard already — Discord's own right-click → Copy Link — and the form
// refused it, because the documented path is Developer Mode → Copy Channel ID and nothing else was
// accepted. The link is the better paste of the two: it carries the guild as well, and the two ids
// are twenty digits each, so filling both fields from one paste is the only spelling that cannot
// be transposed.

import assert from 'node:assert/strict'
import test from 'node:test'

import { driftedSince, isSnowflake, parseChannelReference, widensDisclosure } from './discord.ts'

const channel = '1032123456789012345'
const guild = '987654321098765432'

test('a bare id is what Copy Channel ID produces, and it names no guild', () => {
  const got = parseChannelReference(`  ${channel} `)
  assert.deepEqual(got, {
    ok: true,
    ref: { channel_id: channel, guild_id: null, form: 'id' },
  })
})

test('a channel mention is what a paste out of a message looks like', () => {
  const got = parseChannelReference(`<#${channel}>`)
  assert.deepEqual(got, {
    ok: true,
    ref: { channel_id: channel, guild_id: null, form: 'mention' },
  })
})

test('a channel link fills BOTH ids, which is the paste that cannot be transposed', () => {
  for (const url of [
    `https://discord.com/channels/${guild}/${channel}`,
    `http://discord.com/channels/${guild}/${channel}/`,
    `discord.com/channels/${guild}/${channel}`,
    `https://ptb.discord.com/channels/${guild}/${channel}`,
    `https://canary.discord.com/channels/${guild}/${channel}`,
    `https://discordapp.com/channels/${guild}/${channel}`,
    // Copy Message Link, which is the same URL with the message id on the end.
    `https://discord.com/channels/${guild}/${channel}/1111111111111111111`,
  ]) {
    assert.deepEqual(
      parseChannelReference(url),
      { ok: true, ref: { channel_id: channel, guild_id: guild, form: 'link' } },
      url,
    )
  }
})

test('a DM link is refused for the reason it can never work, not as a bad paste', () => {
  const got = parseChannelReference(`https://discord.com/channels/@me/${channel}`)
  assert.equal(got.ok, false)
  assert.match(got.ok === false ? got.reason : '', /direct message/)
  assert.match(got.ok === false ? got.reason : '', /no guild/)
})

test('a channel name says why a name is not an id, rather than "invalid"', () => {
  const got = parseChannelReference('#raid-planning')
  assert.equal(got.ok, false)
  assert.match(got.ok === false ? got.reason : '', /Developer Mode/)
})

test('nothing else is guessed at', () => {
  for (const input of [
    '',
    '   ',
    'raid-planning',
    '123456789012345678901',
    'https://example.com',
  ]) {
    assert.equal(parseChannelReference(input).ok, false, JSON.stringify(input))
  }
})

test('isSnowflake is the server rule and nothing more generous', () => {
  assert.equal(isSnowflake(' 1 '), true)
  assert.equal(isSnowflake('1'.repeat(20)), true)
  assert.equal(isSnowflake('1'.repeat(21)), false)
  assert.equal(isSnowflake(''), false)
  assert.equal(isSnowflake('12a'), false)
})

// Only the widening direction is a disclosure decision, so only it is confirmed. A confirmation on
// the narrowing direction would be a dialog people learn to dismiss, which costs the one that
// matters its meaning.
test('only the widening direction is confirmed', () => {
  assert.equal(widensDisclosure({ allow_visible: false, discord_guild_id: guild }, true), true)
  assert.equal(widensDisclosure({ allow_visible: true, discord_guild_id: guild }, false), false)
  // Re-asserting the value it already has widens nothing, so it is not a confirmation either.
  assert.equal(widensDisclosure({ allow_visible: true, discord_guild_id: guild }, true), false)
})

// The check the entity tag cannot make. The flip reads immediately before it writes, so `If-Match`
// only ever covers the gap between those two requests; the window that matters opens when the list
// is rendered. A colleague's change landing in it is exactly what the first version of this screen
// destroyed unseen.
test('a binding that moved since it was read stops the write, and says what moved', () => {
  const seen = { allow_visible: false, discord_guild_id: guild }

  assert.deepEqual(driftedSince(seen, { allow_visible: false, discord_guild_id: guild }), [])

  assert.deepEqual(driftedSince(seen, { allow_visible: true, discord_guild_id: guild }), [
    { field: 'visible replies', seen: 'off', now: 'allowed' },
  ])

  assert.deepEqual(driftedSince(seen, { allow_visible: false, discord_guild_id: '111' }), [
    { field: 'Discord server', seen: guild, now: '111' },
  ])

  // Both at once are both reported: naming one and applying the other is the confident mistake.
  assert.equal(driftedSince(seen, { allow_visible: true, discord_guild_id: '111' }).length, 2)
})

// `updated_at` is deliberately NOT compared. Any write bumps it, so a colleague re-asserting the
// same two values would block a flip over nothing — and a check that fires on nothing is a check
// people learn to work around.
test('a rewrite that changed neither value is not drift', () => {
  const seen = { allow_visible: true, discord_guild_id: guild }
  assert.deepEqual(driftedSince(seen, { ...seen }), [])
})

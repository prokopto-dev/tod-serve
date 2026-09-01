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

import { isSnowflake, parseChannelReference, rebindFor } from './discord.ts'

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

// The direction matters and the residue does not. Turning the switch ON is the one that discloses
// more and is the one that gets a confirmation; turning it OFF is the safe direction and gets
// none. Both are an unbind followed by a bind, and a failure between the two leaves the channel
// UNBOUND either way — which is less disclosure than the officer started with, never more.
test('only the widening direction is a disclosure decision, and neither can fail open', () => {
  assert.deepEqual(rebindFor({ allow_visible: false }, true), {
    allow_visible: true,
    widensDisclosure: true,
    worstOutcome: 'unbound',
  })
  assert.deepEqual(rebindFor({ allow_visible: true }, false), {
    allow_visible: false,
    widensDisclosure: false,
    worstOutcome: 'unbound',
  })
  // Re-asserting the value it already has widens nothing, so it is not a confirmation either.
  assert.equal(rebindFor({ allow_visible: true }, true).widensDisclosure, false)
})

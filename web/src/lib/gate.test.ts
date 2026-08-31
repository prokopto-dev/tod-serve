// What a save actually sends, driven.
//
// The scenario that made this a module rather than three expressions inside a component: a circle
// accepts `discord` and `local`, the operator then disables `local` instance-wide, and the owner
// comes back to add a required role to the Discord gate. Every entry the form holds is "accepted",
// so the request carried `local` — and the server refused the WHOLE put with
// `409 provider_disabled`, leaving the owner unable to save a change that had nothing to do with
// it until they worked out that an unrelated row had to be unticked.

import assert from 'node:assert/strict'
import test from 'node:test'

import { choicesFor, gateState, saveSet, type Choice } from './gate.ts'

const choice = (over: Partial<Choice> & { key: string }): Choice => ({
  kind: 'discord',
  display_name: over.key,
  verifiable_subject: true,
  available: true,
  accepted: true,
  discord_guild_id: '',
  discord_required_role_ids: [],
  ...over,
})

test('a disabled provider is never sent, because sending it fails the whole put', () => {
  const { send, dropped } = saveSet([
    choice({ key: 'discord' }),
    choice({ key: 'local', kind: 'local', available: false, verifiable_subject: false }),
  ])

  assert.deepEqual(
    send.map((c) => c.key),
    ['discord'],
    'the request must not name a provider the instance has disabled',
  )
  assert.deepEqual(
    dropped.map((c) => c.key),
    ['local'],
    'and the one it cannot carry is reported, not quietly filtered away',
  )
})

test('an unticked provider is not "dropped" — dropped is only what the owner cannot keep', () => {
  const { send, dropped } = saveSet([
    choice({ key: 'discord' }),
    choice({ key: 'oidc', kind: 'oidc', accepted: false }),
  ])
  assert.deepEqual(send.map((c) => c.key), ['discord'])
  assert.deepEqual(dropped, [], 'a provider nobody ticked is a choice, not a loss')
})

test('the weak acknowledgement comes from what is SENT, not from everything ticked', () => {
  // The server applies its test to the providers in the request. A weak provider that is being
  // dropped is not one this owner is accepting, so claiming the acknowledgement for it would be a
  // claim about a decision nobody made.
  const dropping = saveSet([
    choice({ key: 'discord' }),
    choice({ key: 'local', kind: 'local', available: false, verifiable_subject: false }),
  ])
  assert.equal(dropping.acknowledgeWeak, false)

  const accepting = saveSet([
    choice({ key: 'discord' }),
    choice({ key: 'local', kind: 'local', verifiable_subject: false }),
  ])
  assert.equal(accepting.acknowledgeWeak, true, 'a weak provider actually being accepted needs it')
})

test('nothing accepted sends nothing and acknowledges nothing', () => {
  const { send, dropped, acknowledgeWeak } = saveSet([choice({ key: 'discord', accepted: false })])
  assert.deepEqual(send, [])
  assert.deepEqual(dropped, [])
  assert.equal(acknowledgeWeak, false)
})

test('a provider the instance disabled after acceptance is still merged onto the screen', () => {
  // `listIdentityProviders` is enabled-only, so this is the only thing keeping the row visible.
  const circle = {
    accepted_providers: [
      {
        key: 'local',
        kind: 'local',
        display_name: 'Local',
        provider_id: '01ARZ3NDEKTSV4RRFFQ69G5FAV',
        verifiable_subject: false,
        available: false,
        discord_required_role_ids: [],
      },
      {
        key: 'discord',
        kind: 'discord',
        display_name: 'Discord',
        provider_id: '01ARZ3NDEKTSV4RRFFQ69G5FAW',
        verifiable_subject: true,
        available: true,
        discord_guild_id: '123',
        discord_required_role_ids: ['raider'],
      },
    ],
  } as unknown as Parameters<typeof choicesFor>[0]

  const rows = choicesFor(circle, [
    { key: 'discord', kind: 'discord', display_name: 'Discord', verifiable_subject: true },
    { key: 'oidc', kind: 'oidc', display_name: 'Authentik', verifiable_subject: true },
  ] as unknown as Parameters<typeof choicesFor>[1])

  const byKey = new Map(rows.map((r) => [r.key, r]))
  assert.equal(byKey.size, 3, 'the enabled two, plus the accepted one that is no longer enabled')

  const local = byKey.get('local')
  assert.equal(local?.accepted, true)
  assert.equal(local?.available, false, 'and it is marked as the instance having disabled it')

  const discord = byKey.get('discord')
  assert.equal(discord?.accepted, true)
  assert.deepEqual(discord?.discord_required_role_ids, ['raider'], 'the gate survives the merge')

  const oidc = byKey.get('oidc')
  assert.equal(oidc?.accepted, false, 'an enabled provider this circle does not take is offered')

  // And what a save would do to that screen: keep the two enabled, give up the disabled one.
  const { send, dropped } = saveSet(rows)
  assert.deepEqual(send.map((c) => c.key), ['discord'])
  assert.deepEqual(dropped.map((c) => c.key), ['local'])
})

test('the three gate states are told apart by what admits, not by which fields are set', () => {
  assert.equal(gateState(choice({ key: 'd' })), 'none', 'no guild is no gate at all')
  assert.equal(
    gateState(choice({ key: 'd', discord_guild_id: '123' })),
    'guild',
    'a guild and no roles is anyone in the guild',
  )
  assert.equal(
    gateState(choice({ key: 'd', discord_guild_id: '123', discord_required_role_ids: ['r'] })),
    'roles',
    'a guild and roles is anyone holding any ONE of them',
  )
  // Roles with no guild is refused by the server (`internal/circle/providers.go` validateGate),
  // and it must not render as a role gate here either: a gate identified by its guild is not on.
  assert.equal(
    gateState(choice({ key: 'd', discord_required_role_ids: ['r'] })),
    'none',
    'required roles with no guild evaluate nothing, so this is no gate',
  )
})

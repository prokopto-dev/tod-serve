// What the Role cell offers, driven.
//
// The scenario that made this a module rather than one `slice` inside a component: an owner opened
// the Members page, opened the dropdown on THEIR OWN row, picked `officer`, and the server took it.
// The dropdown was capped at the caller's own role and at nothing else, so every option it showed
// on that row was one the caller could still reach — including the one that gave their circle away.

import assert from 'node:assert/strict'
import test from 'node:test'

import { ROLES, roleField } from './roles.ts'

const ME = '01ARZ3NDEKTSV4RRFFQ69G5FAV'
const THEM = '01ARZ3NDEKTSV4RRFFQ69G5FB0'

const field = (over: Partial<Parameters<typeof roleField>[0]> = {}) =>
  roleField({
    canManage: true,
    revoked: false,
    myRole: 'owner',
    myMembershipID: ME,
    member: { id: THEM, role: 'member' },
    ...over,
  })

test('an owner is offered nothing on their own row, and told why', () => {
  const own = field({ member: { id: ME, role: 'owner' } })

  assert.deepEqual(own.options, [], 'the reported bug: this dropdown offered officer, and it stuck')
  assert.match(own.reason, /not yours to change/)
  assert.match(own.reason, /promoting somebody else/, 'the reason has to name the way out')
  assert.equal(own.note, 'you', 'and the row says so on screen, not only on hover')
})

test('nobody is offered their own row — the rule is not owner-shaped', () => {
  for (const role of ROLES) {
    const own = field({ myRole: role, member: { id: ME, role } })
    assert.deepEqual(own.options, [], `${role} was offered a change to their own role`)
  }
})

test('an officer is offered nothing on an owner, and told why', () => {
  const outranked = field({ myRole: 'officer', member: { id: THEM, role: 'owner' } })

  assert.deepEqual(outranked.options, [])
  assert.equal(outranked.note, 'outranks you')
  assert.match(outranked.reason, /holds at least it/)
})

test('an owner still promotes somebody else to owner — that is the handover', () => {
  const other = field({ member: { id: THEM, role: 'officer' } })

  assert.deepEqual(other.options, ['observer', 'member', 'officer', 'owner'])
  assert.equal(other.reason, '', 'nothing is being withheld, so there is nothing to explain')
})

test('an owner may still change another OWNER, which is how a handover completes', () => {
  const peer = field({ member: { id: THEM, role: 'owner' } })

  assert.deepEqual(peer.options, ['observer', 'member', 'officer', 'owner'])
})

test('an officer is never offered owner — the escalation guard, unchanged', () => {
  const managed = field({ myRole: 'officer', member: { id: THEM, role: 'member' } })

  assert.deepEqual(managed.options, ['observer', 'member', 'officer'])
  assert.ok(!managed.options.includes('owner' as never))
})

test('the ordinary read-only cases explain nothing, because nothing is withheld', () => {
  assert.deepEqual(field({ canManage: false }), { options: [], reason: '', note: '' })
  assert.deepEqual(field({ revoked: true }), { options: [], reason: '', note: '' })
})

test('an unrecognised role offers nothing rather than guessing a ranking for it', () => {
  assert.deepEqual(field({ myRole: 'admiral' }), { options: [], reason: '', note: '' })
  assert.deepEqual(field({ member: { id: THEM, role: 'admiral' } }), {
    options: [],
    reason: '',
    note: '',
  })
})

test('a dropdown whose only entry is the role already showing is not offered', () => {
  const observer = field({ myRole: 'observer', member: { id: THEM, role: 'observer' } })

  assert.deepEqual(observer.options, [], 'a control that can only re-pick what is showing does nothing')
  assert.equal(observer.reason, '')
})

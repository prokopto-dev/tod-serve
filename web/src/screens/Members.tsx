// Members: roles, revocation, reinstatement.
//
// Two things are rendered rather than acted on, and both deliberately:
//
//   - `possible_duplicate` is SHOWN and nothing is done about it. It means two memberships look
//     like the same person; deciding they are is a judgement with consequences — merging identities
//     rewrites who reported what — so the console surfaces the flag and leaves the decision to a
//     human. A UI that merged them automatically would be quietly editing history.
//   - A revoked member stays in the list, with their reports still counting. Revocation controls
//     access, never history, and hiding the row would make the evidence counts stop adding up.
//
// The Role cell is the most dangerous field on this screen and what it offers is decided in
// `../lib/roles`, driven there rather than here.

import { useState } from 'react'

import { api, body, type Member, type RevokedResponse, toError } from '../api'
import { usePrincipal } from '../app/principal'
import { useResource } from '../app/useResource'
import { ProblemNotice, StaleNotice } from '../components/Problem'
import { Banner, Button, Card, Empty, Field, Input, Mono, Select, Spinner, Td, Th } from '../components/ui'
import { instant, plural } from '../lib/format'
import { roleField, type Role } from '../lib/roles'

export function Members() {
  const principal = usePrincipal()
  const circleID = principal.view.circle_id
  const [notice, setNotice] = useState<RevokedResponse | null>(null)
  const [error, setError] = useState<Error | null>(null)

  const members = useResource(
    (signal) => api.listMembers({ circle_id: circleID, limit: 200 }, { signal }).then((r) => r.data),
    [circleID],
  )

  const act = (promise: Promise<unknown>) => {
    setError(null)
    promise.then(() => members.reload()).catch((err: unknown) => setError(toError(err)))
  }

  const rows = members.data?.items ?? []

  return (
    <div className="space-y-3">
      {notice && (
        <Banner tone="warn" title={`${notice.display_name} is revoked`}>
          <p>
            {notice.invites_revoked > 0
              ? `${plural(notice.invites_revoked, 'live invite')} in this circle ${
                  notice.invites_revoked === 1 ? 'was' : 'were'
                } revoked at the same time.`
              : `This circle has ${plural(notice.active_invite_count, 'live invite')}. Revoking a member does not touch them unless the circle is set to.`}
          </p>
          <p className="mt-1">
            Their reports still count and their retractions still apply. Revocation controls access,
            never history.
          </p>
        </Banner>
      )}
      {error && <ProblemNotice error={error} />}

      <Card title="Members" subtitle={`${plural(rows.length, 'membership')} in this circle`}>
        <StaleNotice resource={members} />
        {members.error && (
          <div className="p-4">
            <ProblemNotice error={members.error} onRetry={members.reload} />
          </div>
        )}
        {members.loading && !members.data && <Spinner label="Reading members" />}
        {members.data && rows.length === 0 && <Empty title="No members yet." />}
        {rows.length > 0 && (
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-xs">
              <thead>
                <tr>
                  <Th>Name</Th>
                  <Th>Role</Th>
                  <Th>Kind</Th>
                  <Th>Provider</Th>
                  <Th>Joined</Th>
                  <Th>State</Th>
                  <Th />
                </tr>
              </thead>
              <tbody>
                {rows.map((member) => (
                  <MemberRow
                    key={member.id}
                    member={member}
                    circleID={circleID}
                    canManage={principal.can('member.manage')}
                    canRevoke={principal.can('member.revoke')}
                    myRole={principal.view.role}
                    myMembershipID={principal.view.membership_id}
                    onAct={act}
                    onRevoked={setNotice}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {principal.can('token.mint') && <ServiceMemberCard circleID={circleID} onDone={members.reload} />}
    </div>
  )
}

function MemberRow({
  member,
  circleID,
  canManage,
  canRevoke,
  myRole,
  myMembershipID,
  onAct,
  onRevoked,
}: {
  member: Member
  circleID: string
  canManage: boolean
  canRevoke: boolean
  myRole: string
  myMembershipID: string
  onAct: (promise: Promise<unknown>) => void
  onRevoked: (revoked: RevokedResponse) => void
}) {
  const revoked = Boolean(member.revoked_at)
  // What this caller may change this member's role to, and why not when they may not. The server
  // refuses all three cases; the console does not offer them, so nobody reaches for a control that
  // will always fail — and the role field is where that matters most, because the one it used to
  // offer took a circle's owner away from it.
  const role = roleField({ canManage, revoked, myRole, myMembershipID, member })

  return (
    <tr className={revoked ? 'opacity-60' : undefined}>
      <Td>
        <span className="text-ink-100">{member.display_name}</span>
        {member.possible_duplicate && (
          <span
            className="caps ml-1.5 rounded border border-warn/40 px-1 text-[10px] text-warn"
            title="Another membership in this circle looks like the same person. Deciding that they are is a judgement with consequences — it would rewrite who reported what — so this is shown and nothing is done about it."
          >
            possible duplicate
          </span>
        )}
      </Td>
      <Td>
        {role.options.length > 0 ? (
          <Select
            value={member.role}
            onChange={(e) => onAct(changeRole(circleID, member.id, e.target.value))}
          >
            {role.options.map((option) => (
              <option key={option} value={option}>
                {option}
              </option>
            ))}
          </Select>
        ) : (
          <>
            {member.role}
            {role.note && (
              <span
                className="caps ml-1.5 rounded border border-ink-700 px-1 text-[10px] text-ink-400"
                title={role.reason}
              >
                {role.note}
              </span>
            )}
          </>
        )}
      </Td>
      <Td className="text-ink-400">{member.kind}</Td>
      <Td className="text-ink-400">
        {member.provider_key ?? '—'}
        {member.revocation_strength === 'weak' && (
          <span
            className="caps ml-1.5 text-[10px] text-warn"
            title="Revoking this member cuts off their credentials but cannot stop the same person returning under a new identity."
          >
            weak
          </span>
        )}
      </Td>
      <Td className="tnum text-ink-400">{instant(member.joined_at)}</Td>
      <Td>
        {revoked ? (
          <span className="text-warn" title={member.revoke_reason || undefined}>
            revoked
          </span>
        ) : (
          <span className="text-[var(--color-status-inwindow)]">active</span>
        )}
      </Td>
      <Td className="text-right">
        {canRevoke &&
          (revoked ? (
            <Button
              onClick={() =>
                onAct(api.reinstateMember({ circle_id: circleID, member_id: member.id }))
              }
            >
              Reinstate
            </Button>
          ) : (
            <Button
              variant="danger"
              onClick={() => {
                const reason = window.prompt('Why? This goes in the audit log.') ?? ''
                onAct(
                  api
                    .revokeMember({
                      circle_id: circleID,
                      member_id: member.id,
                      body: reason ? { reason } : {},
                    })
                    .then((r) => onRevoked(body(r))),
                )
              }}
            >
              Revoke
            </Button>
          ))}
      </Td>
    </tr>
  )
}

/**
 * changeRole reads the member, then writes with the entity tag that read returned.
 *
 * It is two requests rather than one deliberately. `If-Match` is required on a state transition so
 * that a caller says which version they read, and `*` is accepted here — it means "any current
 * version" — but sending it would be this console overwriting another officer's change having read
 * nothing. The 412 the server answers instead carries the CURRENT representation, so the
 * read-merge-retry costs no extra round trip.
 */
function changeRole(circleID: string, memberID: string, role: string): Promise<unknown> {
  return api.getMember({ circle_id: circleID, member_id: memberID }).then((current) =>
    api.updateMember(
      {
        circle_id: circleID,
        member_id: memberID,
        body: { role: role as Role },
      },
      current.etag ? { ifMatch: current.etag } : {},
    ),
  )
}

/**
 * ServiceMemberCard mints the membership a bot uses.
 *
 * A PAT is bound to a MEMBERSHIP rather than to a service account, so a bot gets a `service`
 * membership with a responsible human behind it — which is what keeps "the audit names a human"
 * true while there is still exactly one principal kind in the authorization path.
 */
function ServiceMemberCard({ circleID, onDone }: { circleID: string; onDone: () => void }) {
  const [name, setName] = useState('')
  const [error, setError] = useState<Error | null>(null)
  const [secret, setSecret] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  return (
    <Card
      title="Service member"
      subtitle="A bot's membership, with you as the responsible human. Its token is shown once."
    >
      <div className="space-y-3 p-4">
        {error && <ProblemNotice error={error} />}
        {secret && (
          <Banner tone="accent" title="The bot's token — copy it now">
            <p className="mt-1 font-mono text-[11px] break-all select-all">{secret}</p>
            <p className="mt-1">
              This is the only time it exists in plaintext anywhere. Only the prefix is stored.
            </p>
          </Banner>
        )}
        <div className="flex items-end gap-3">
          <div className="flex-1">
            <Field label="Display name" hint="What the member list calls this bot.">
              <Input
                value={name}
                maxLength={64}
                placeholder="signup-bot"
                onChange={(e) => setName(e.target.value)}
              />
            </Field>
          </div>
          <Button
            variant="primary"
            disabled={!name.trim() || busy}
            onClick={() => {
              setBusy(true)
              setError(null)
              api
                .createServiceMember({ circle_id: circleID, body: { display_name: name.trim() } })
                .then((r) => {
                  setSecret(body(r).token.token)
                  setName('')
                  onDone()
                })
                .catch((err: unknown) => setError(toError(err)))
                .finally(() => setBusy(false))
            }}
          >
            Create
          </Button>
        </div>
        <p className="text-[11px] text-ink-500">
          A bot holding <Mono>invite.create</Mono> can post a signup link on request — that scope is
          deliberately outside the capability floor, because an invite minted by a token is capped
          at one use, 24 hours and a role no higher than <code>member</code>.
        </p>
      </div>
    </Card>
  )
}

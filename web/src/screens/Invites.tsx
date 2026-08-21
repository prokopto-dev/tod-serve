// Invites — the screen that produces the thing an officer pastes into a Discord channel.
//
// The link carries the code in the URL FRAGMENT: `https://tod.example.com/join#TODI-4KQ7M-9XPB2`.
// A fragment is never sent to any server — not to ours, not to a proxy, not in a `Referer` — which
// is the same reason the code travels in a POST body rather than a path segment, applied to the
// link somebody actually pastes.
//
// The code is shown ONCE, in the response that minted it, and this screen says so before it
// disappears. Everything after that is a `code_prefix`, which is enough to recognise an invite in
// this list and not enough to redeem it.
//
// A one-time login link is this and nothing more: an invite with `max_uses = 1`.

import { useState } from 'react'

import { api, body, type MintedInviteResponse, toError } from '../api'
import { usePrincipal } from '../app/principal'
import { useResource } from '../app/useResource'
import { ProblemNotice } from '../components/Problem'
import { Banner, Button, Card, Empty, Field, Input, Mono, Select, Spinner, Td, Th } from '../components/ui'
import { instant, plural } from '../lib/format'

const DAY = 24 * 60 * 60

const EXPIRIES: Array<{ label: string; seconds: number }> = [
  { label: '1 hour', seconds: 60 * 60 },
  { label: '24 hours', seconds: DAY },
  { label: '7 days', seconds: 7 * DAY },
  { label: '30 days', seconds: 30 * DAY },
]

export function Invites() {
  const principal = usePrincipal()
  const circleID = principal.view.circle_id
  const [minted, setMinted] = useState<MintedInviteResponse | null>(null)

  const invites = useResource(
    (signal) => api.listInvites({ circle_id: circleID, limit: 100 }, { signal }).then((r) => r.data),
    [circleID],
  )

  return (
    <div className="space-y-3">
      {minted && <MintedCard minted={minted} onDismiss={() => setMinted(null)} />}

      {principal.can('invite.create') && (
        <CreateInviteCard
          circleID={circleID}
          onMinted={(response) => {
            setMinted(response)
            invites.reload()
          }}
        />
      )}

      <Card
        title="Invites"
        subtitle="Only the prefix is stored. A live invite can be revoked; a redeemed one stays in the list."
      >
        {invites.error && (
          <div className="p-4">
            <ProblemNotice error={invites.error} onRetry={invites.reload} />
          </div>
        )}
        {invites.loading && !invites.data && <Spinner label="Reading invites" />}
        {invites.data && (invites.data.items ?? []).length === 0 && (
          <Empty title="No invites yet.">
            Mint one above, then paste the signup link into your Discord channel.
          </Empty>
        )}
        {(invites.data?.items ?? []).length > 0 && (
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-xs">
              <thead>
                <tr>
                  <Th>Code</Th>
                  <Th>Role</Th>
                  <Th>Uses</Th>
                  <Th>Expires</Th>
                  <Th>Note</Th>
                  <Th>State</Th>
                  <Th />
                </tr>
              </thead>
              <tbody>
                {(invites.data?.items ?? []).map((invite) => (
                  <tr key={invite.id}>
                    <Td>
                      <Mono>{invite.code_prefix}…</Mono>
                    </Td>
                    <Td>{invite.role}</Td>
                    <Td className="tnum">
                      {invite.uses}/{invite.max_uses}
                    </Td>
                    <Td className="tnum text-ink-400">{instant(invite.expires_at)}</Td>
                    <Td className="text-ink-400">{invite.note || '—'}</Td>
                    <Td>
                      {invite.live ? (
                        <span className="text-[var(--color-status-inwindow)]">live</span>
                      ) : (
                        <span className="text-ink-500">spent or revoked</span>
                      )}
                    </Td>
                    <Td className="text-right">
                      {invite.live && principal.can('invite.revoke') && (
                        <RevokeButton
                          circleID={circleID}
                          inviteID={invite.id}
                          onDone={invites.reload}
                        />
                      )}
                    </Td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  )
}

function CreateInviteCard({
  circleID,
  onMinted,
}: {
  circleID: string
  onMinted: (minted: MintedInviteResponse) => void
}) {
  const [role, setRole] = useState<'officer' | 'member' | 'observer'>('member')
  const [expires, setExpires] = useState(7 * DAY)
  const [maxUses, setMaxUses] = useState(1)
  const [note, setNote] = useState('')
  const [error, setError] = useState<Error | null>(null)
  const [busy, setBusy] = useState(false)

  const submit = () => {
    setBusy(true)
    setError(null)
    api
      .createInvite({
        circle_id: circleID,
        body: { role, expires_in_seconds: expires, max_uses: maxUses, note: note.trim() },
      })
      .then((r) => {
        onMinted(body(r))
        setNote('')
      })
      .catch((err: unknown) => setError(toError(err)))
      .finally(() => setBusy(false))
  }

  return (
    <Card title="Mint an invite" subtitle="An invite can never grant owner; that is a schema CHECK.">
      <div className="space-y-3 p-4">
        {error && <ProblemNotice error={error} />}
        <div className="grid gap-3 md:grid-cols-4">
          <Field label="Role">
            <Select
              value={role}
              className="w-full"
              onChange={(e) => setRole(e.target.value as typeof role)}
            >
              <option value="observer">observer — the board, without who reported</option>
              <option value="member">member</option>
              <option value="officer">officer</option>
            </Select>
          </Field>
          <Field label="Expires in">
            <Select
              value={expires}
              className="w-full"
              onChange={(e) => setExpires(Number(e.target.value))}
            >
              {EXPIRIES.map((e) => (
                <option key={e.seconds} value={e.seconds}>
                  {e.label}
                </option>
              ))}
            </Select>
          </Field>
          <Field
            label="Uses"
            hint={maxUses === 1 ? 'A one-time login link is exactly this.' : undefined}
          >
            <Input
              type="number"
              min={1}
              max={50}
              value={maxUses}
              onChange={(e) => setMaxUses(Math.max(1, Number(e.target.value) || 1))}
            />
          </Field>
          <Field label="Note" hint="Free text, shown in this list only.">
            <Input
              value={note}
              maxLength={500}
              placeholder="posted in #raid-signups"
              onChange={(e) => setNote(e.target.value)}
            />
          </Field>
        </div>
        <div className="flex justify-end">
          <Button variant="primary" onClick={submit} disabled={busy}>
            {busy ? 'Minting…' : 'Mint invite'}
          </Button>
        </div>
      </div>
    </Card>
  )
}

/**
 * MintedCard is the only place a code is ever visible, and it says so.
 *
 * `capped_by` is rendered when the server narrowed the request below what it asked for. An invite
 * minted BY A TOKEN is hard-narrowed — one use, 24 hours, role at most `member` — and a UI that
 * silently showed the clamped values would let somebody believe they had made a 30-day officer
 * invite.
 */
function MintedCard({
  minted,
  onDismiss,
}: {
  minted: MintedInviteResponse
  onDismiss: () => void
}) {
  const [copied, setCopied] = useState<'link' | 'code' | null>(null)
  const link = `${window.location.origin}/join#${minted.code}`

  const copy = (what: 'link' | 'code', value: string) => {
    navigator.clipboard
      .writeText(value)
      .then(() => setCopied(what))
      .catch(() => setCopied(null))
  }

  return (
    <Card
      title="Signup link"
      subtitle="Shown once. Close this and only the prefix remains — there is no way to read it back."
      actions={<Button onClick={onDismiss}>Done</Button>}
    >
      <div className="space-y-3 p-4">
        {minted.capped_by === 'pat' && (
          <Banner tone="warn" title="This invite was narrowed because a token minted it">
            An invite minted by a personal access token is capped at one use, 24 hours and a role no
            higher than <code>member</code>, whatever the request asked for. That cap is what makes{' '}
            <Mono>invite.create</Mono> safe to leave out of the capability floor, so a bot can post
            a link without a browser session — the values below are the real ones.
          </Banner>
        )}

        <div className="rounded border border-accent-600/40 bg-accent-600/5 p-3">
          <p className="text-[11px] tracking-wide text-ink-400 uppercase">Paste this into Discord</p>
          <p className="mt-1 font-mono text-sm break-all text-ink-100 select-all">{link}</p>
          <p className="mt-2 text-[11px] text-ink-500">
            The code is in the fragment, after the <code>#</code>. A fragment is never sent to any
            server, so the code does not reach our access logs, a proxy, or a <code>Referer</code>{' '}
            header — and the join page clears it from the address bar the moment it reads it.
          </p>
          <div className="mt-3 flex items-center gap-2">
            <Button variant="primary" onClick={() => copy('link', link)}>
              {copied === 'link' ? 'Copied' : 'Copy link'}
            </Button>
            <Button onClick={() => copy('code', minted.code)}>
              {copied === 'code' ? 'Copied' : 'Copy code only'}
            </Button>
          </div>
        </div>

        <dl className="grid gap-3 text-xs md:grid-cols-4">
          <div>
            <dt className="text-[11px] tracking-wide text-ink-400 uppercase">Role</dt>
            <dd className="mt-0.5 text-ink-100">{minted.role}</dd>
          </div>
          <div>
            <dt className="text-[11px] tracking-wide text-ink-400 uppercase">Uses</dt>
            <dd className="mt-0.5 text-ink-100">
              {minted.max_uses === 1 ? 'one — a login link' : plural(minted.max_uses, 'use')}
            </dd>
          </div>
          <div>
            <dt className="text-[11px] tracking-wide text-ink-400 uppercase">Expires</dt>
            <dd className="mt-0.5 text-ink-100 tnum">{instant(minted.expires_at)}</dd>
          </div>
          <div>
            <dt className="text-[11px] tracking-wide text-ink-400 uppercase">Prefix</dt>
            <dd className="mt-0.5">
              <Mono>{minted.code_prefix}</Mono>
            </dd>
          </div>
        </dl>
      </div>
    </Card>
  )
}

function RevokeButton({
  circleID,
  inviteID,
  onDone,
}: {
  circleID: string
  inviteID: string
  onDone: () => void
}) {
  const [error, setError] = useState<Error | null>(null)
  const [busy, setBusy] = useState(false)
  return (
    <>
      <Button
        variant="danger"
        disabled={busy}
        title="Revoking an invite is a capability-floor operation: it needs a browser session that has re-authenticated recently."
        onClick={() => {
          setBusy(true)
          setError(null)
          api
            .revokeInvite({ circle_id: circleID, invite_id: inviteID })
            .then(onDone)
            .catch((err: unknown) => setError(toError(err)))
            .finally(() => setBusy(false))
        }}
      >
        Revoke
      </Button>
      {error && (
        <div className="mt-2 text-left">
          <ProblemNotice error={error} />
        </div>
      )}
    </>
  )
}

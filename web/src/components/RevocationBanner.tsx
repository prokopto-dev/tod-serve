// Weak revocation is said PERSISTENTLY, not once at join.
//
// The damage from a weakly-revocable circle is not the re-entry — it is officers' false
// confidence that revoking somebody ended their access. `identity_provider.verifiable_subject` is
// a CHECK against `kind` rather than a toggle, and `local` being accepted is exactly when a circle
// most needs reminding: there is no third party who can tell us the account is gone.

import { Banner } from './ui'

export function RevocationBanner({
  strength,
  reasons,
  weakProviders,
}: {
  strength: string
  reasons?: string[] | null
  weakProviders?: string[] | null
}) {
  if (strength !== 'weak') return null
  return (
    <Banner tone="warn" title="Revocation in this circle is advisory, not durable">
      <p>
        This circle accepts an identity provider with no verifiable subject
        {weakProviders && weakProviders.length > 0 ? ` (${weakProviders.join(', ')})` : ''}, so
        revoking a member cuts off their credentials but cannot stop the same person coming back
        under a new identity. Revoked members’ reports still count either way.
      </p>
      {reasons && reasons.length > 0 && (
        <ul className="mt-1 list-disc space-y-0.5 pl-4">
          {reasons.map((reason) => (
            <li key={reason}>{reason}</li>
          ))}
        </ul>
      )}
    </Banner>
  )
}

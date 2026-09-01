package membership

import (
	"context"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// StepUpRequest re-proves an identity for a session that already exists.
//
// There is no circle id, no client name, no scopes and no idempotency key, and each absence is the
// point. The circle comes from the membership the caller's cookie is already bound to, so this
// route is not a second place a caller may name a circle. Nothing is minted, so there is nothing
// for a name or a scope to narrow and no response worth replaying.
type StepUpRequest struct {
	// MembershipID is the session's own membership, taken off the verified cookie by the edge.
	// A caller cannot name it.
	MembershipID core.MembershipID
	// ProviderKey is which identity provider is being re-proved through. It need not be the one
	// the membership was created with: a circle that accepts two providers accepts either as
	// proof, so long as the identity behind it is the SAME identity this membership belongs to.
	ProviderKey string
	// Credential is the same union `/join` and `/sessions` take.
	Credential identity.Credential
	// DisplayName is what a `local` provider asserts. It is used for verification only — a
	// step-up never renames anybody, because renaming is `member.manage` and this is not it.
	DisplayName string
}

// SteppedUp is what a successful re-proof reports.
//
// It carries no token and no membership representation. The caller already has both; what it did
// not have is a recent proof, and this says the proof landed and when it lapses.
type SteppedUp struct {
	// MembershipID is the membership whose session was re-proved: the caller's own, always.
	MembershipID core.MembershipID `json:"membership_id"`
	// CircleID is that membership's circle.
	CircleID core.CircleID `json:"circle_id"`
	// SteppedUpAt is the instant the identity was proved, which is now.
	SteppedUpAt core.Micros `json:"stepped_up_at"`
	// AsOf is the instant this answer was computed.
	AsOf core.Micros `json:"as_of"`
}

// StepUp re-proves the identity behind an existing session, and mints nothing.
//
// # Why this is not `/sessions` with a flag
//
// `/sessions` is "sign in on a device", and minting a personal access token is its job — a plugin
// with no browser gets its credential from exactly there. Re-proving a session that already exists
// is a different act with a different result, and running it through the sign-in route is what put
// a new row in somebody's device list every five minutes: a credential minted, handed to a browser
// that has no use for one, and never seen again. ADR-0024.
//
// # What is checked, and why each one
//
// Everything `/sessions` checks except the parts that only make sense when a credential is being
// handed out. In order:
//
//  1. The credential verifies. A ticket is consumed here, exactly as it is on the other two
//     routes, so a replayed ticket fails at `401 auth_ticket_invalid`.
//  2. The identity is not blocked.
//  3. The verified identity OWNS THE CALLER'S MEMBERSHIP. This is the check with no analogue on
//     `/sessions`, and it is what stops the route being a way to hand somebody else's session a
//     fresh proof: presenting your own valid credential against a cookie that is not yours must
//     not step that cookie up.
//  4. The membership is live and its circle still exists.
//  5. The circle still accepts this provider, read INSIDE the transaction — the live row, not the
//     snapshot. Re-authentication is exactly the moment a provider dropped yesterday has to bite.
//  6. The guild gate is evaluated against the facts this verification returned. A gate checked
//     only at join is a gate somebody walks around by re-authing, and that is as true here as it
//     is on `/sessions`.
func (s *Service) StepUp(ctx context.Context, req StepUpRequest) (SteppedUp, error) {
	now := s.clock.Now()

	provider, err := s.identity.Provider(ctx, req.ProviderKey)
	if err != nil {
		return SteppedUp{}, fromIdentity(err)
	}
	// A provider with no verifiable subject cannot re-prove an identity it already issued, and
	// this is where that is SAID rather than discovered. `local` mints a fresh server-side ULID
	// per verification — see internal/identity/local — so a `local` step-up would verify happily,
	// resolve to an identity nobody has ever seen, and answer `credential_invalid`: a code that
	// reads as "you got it wrong, try again" for a request that can never succeed. That is the
	// confident mistake, and this is the honest refusal.
	//
	// It is checked on `verifiable_subject` rather than on `kind == local` because that column is
	// a CHECK against the kind (04-identity §3), so the two cannot disagree — and the rule this
	// states is about the property, not about which provider happens to have it today.
	if !provider.VerifiableSubject {
		return SteppedUp{}, apierr.New(apierr.CodeProviderUnverifiable,
			"the "+provider.Key+" provider mints a new subject each time and cannot re-prove an "+
				"identity it already issued; there is no way to step up through it").
			WithField("body.provider", "cannot re-prove an existing identity")
	}

	existing, err := s.db.Queries().GetMembershipByID(ctx, req.MembershipID.String())
	if err != nil {
		if store.IsNotFound(err) {
			return SteppedUp{}, notFoundMembership()
		}
		return SteppedUp{}, coded(err)
	}
	circleID, err := core.ParseID[core.Circle](existing.CircleID)
	if err != nil {
		return SteppedUp{}, coded(err)
	}

	// The gate is read before verification for the same reason `/sessions` reads it there: the
	// `bearer_token` path needs a guild id to fetch facts for. Unlike `/sessions` the error is
	// NOT held back — the caller has already proved they hold a live session in this circle, so
	// the circle's existence is theirs to know and `provider_not_accepted` is the useful answer.
	accepted, err := circle.Accepted(ctx, s.db.Queries(), circleID, req.ProviderKey)
	if err != nil {
		return SteppedUp{}, err
	}

	verified, err := s.verify(ctx, provider, accepted, req.Credential, req.DisplayName)
	if err != nil {
		return SteppedUp{}, err
	}

	var out SteppedUp
	err = s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		stored, txErr := q.GetIdentityByProviderSubject(ctx,
			sqlitegen.GetIdentityByProviderSubjectParams{
				ProviderID: verified.ProviderID, Subject: verified.Subject,
			})
		if store.IsNotFound(txErr) {
			return notThisSession()
		}
		if txErr != nil {
			return txErr
		}
		if stored.BlockedAt != nil {
			return apierr.New(apierr.CodeIdentityBlocked,
				"this identity is blocked on this instance")
		}

		live, txErr := q.GetMembershipByID(ctx, req.MembershipID.String())
		if store.IsNotFound(txErr) {
			return notFoundMembership()
		}
		if txErr != nil {
			return txErr
		}
		// The identity behind the credential must be the identity behind the session. A service
		// membership has no identity at all, so a NULL here fails the comparison rather than
		// matching a NULL subject — which is the correct answer twice over, because a service
		// membership has no browser session to step up.
		if live.IdentityID == nil || *live.IdentityID != stored.ID {
			return notThisSession()
		}
		if live.RevokedAt != nil {
			return apierr.New(apierr.CodeMembershipRevoked,
				"this membership has been revoked; an officer has to reinstate it")
		}
		if _, txErr := circle.Read(ctx, q, circleID, now); txErr != nil {
			return notFoundMembership()
		}

		liveProvider, txErr := circle.Accepted(ctx, q, circleID, req.ProviderKey)
		if txErr != nil {
			return txErr
		}
		if txErr = identity.EvaluateGuildGate(
			liveProvider.Gate(), verified.GuildFacts); txErr != nil {
			return fromIdentity(txErr)
		}

		out = SteppedUp{
			MembershipID: req.MembershipID, CircleID: circleID,
			SteppedUpAt: now, AsOf: now,
		}
		return nil
	})
	if err != nil {
		return SteppedUp{}, coded(err)
	}
	return out, nil
}

// notThisSession is the answer when a credential verifies and belongs to somebody else.
//
// It is `credential_invalid` rather than `forbidden` or `404`, and the distinction is the fix it
// points at: the credential is real and the session is real, and what is wrong is that they are
// not each other's. A 404 would say the session is gone, which would send somebody to sign in
// again — the loop this whole route exists to end.
func notThisSession() error {
	return apierr.New(apierr.CodeCredentialInvalid,
		"that credential proves a different identity from the one this session belongs to")
}

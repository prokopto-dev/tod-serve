package membership

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/audit"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/invite"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// JoinRequest is `POST /join` after the edge has parsed it.
type JoinRequest struct {
	// Code is the invite code, exactly as the caller typed it. It is parsed here rather than at
	// the edge so that every spelling of one code resolves to one row — see internal/invite.
	Code        string
	ProviderKey string
	Credential  identity.Credential
	DisplayName string
	// ClientName is the device name the token is filed under, e.g. "nparse-plus-tod".
	ClientName string
	Scopes     []string
	// IdempotencyKey is required by the route registry and owned by this handler: there is no
	// membership principal in existence when the request arrives, and
	// `idempotency_record.principal_membership_id` is NOT NULL.
	IdempotencyKey string
	TokenTTL       time.Duration
}

// AuthenticateRequest is `POST /sessions`: the same shape minus the invite code, plus the circle.
type AuthenticateRequest struct {
	CircleID       core.CircleID
	ProviderKey    string
	Credential     identity.Credential
	DisplayName    string
	ClientName     string
	Scopes         []string
	IdempotencyKey string
	TokenTTL       time.Duration
}

// Joined is what a successful `/join` or `/sessions` returns.
type Joined struct {
	// Created says a membership was made. False on `/sessions`, and false on a `/join` by somebody
	// who is already a live member — which does NOT consume a use of their invite, because
	// spending one to tell somebody they are already in is a use nobody gets back.
	Created    bool          `json:"created"`
	Membership Member        `json:"membership"`
	Circle     circle.Circle `json:"circle"`
	Token      Token         `json:"token"`
	AsOf       core.Micros   `json:"as_of"`
	// Replayed marks a response served from `idempotency_record` rather than produced. Without it
	// a client cannot tell a retry that worked from a request that ran twice.
	Replayed bool `json:"replayed,omitempty"`
}

// Join redeems an invite: verify the credential, evaluate the guild gate, create the identity and
// the membership, and mint a token.
//
// The order is the rule:
//
//  1. Resolve the code. It names the circle; no caller-supplied circle id is accepted here at all.
//  2. Read the circle's accepted providers and its Discord gate — before verification, because the
//     gate names the guild whose facts the `bearer_token` path has to fetch.
//  3. Verify the credential. This is where a ticket is consumed and where `identity.blocked_at` is
//     refused.
//  4. **Evaluate the guild gate.** `/sessions` does the same thing with the same function; a gate
//     checked only at join is a gate somebody walks around by re-authing on a new device.
//  5. Re-resolve the invite inside the write transaction and redeem it there. Everything read in
//     step 1 is advisory: a user can sit on a consent screen for minutes, and the live row is the
//     authority — the same reason `target_state_cache` is not one.
func (s *Service) Join(ctx context.Context, req JoinRequest) (Joined, error) {
	now := s.clock.Now()

	resolved, err := invite.Resolve(ctx, s.db.Queries(), req.Code, now)
	if err != nil {
		return Joined{}, err
	}
	accepted, err := circle.Accepted(ctx, s.db.Queries(), resolved.CircleID, req.ProviderKey)
	if err != nil {
		return Joined{}, err
	}
	provider, err := s.identity.Provider(ctx, req.ProviderKey)
	if err != nil {
		return Joined{}, fromIdentity(err)
	}

	verified, err := s.verify(ctx, provider, accepted, req.Credential, req.DisplayName)
	if err != nil {
		return Joined{}, err
	}

	scopes, err := ParseScopes(req.Scopes)
	if err != nil {
		return Joined{}, err
	}
	name, err := tokenName(req.ClientName)
	if err != nil {
		return Joined{}, err
	}

	var out Joined
	err = s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		// Re-resolved under the write lock. The row read before the credential was verified is a
		// snapshot; this is the authority, and it is what decides which circle somebody joins.
		live, txErr := invite.Resolve(ctx, q, req.Code, now)
		if txErr != nil {
			return txErr
		}
		if live.CircleID != resolved.CircleID {
			// Unreachable while a code names one circle. Checked because the alternative to
			// checking is trusting a value read before a network round trip.
			return apierr.New(apierr.CodeInviteInvalid, "no such invite")
		}
		if _, txErr = circle.Accepted(ctx, q, live.CircleID, req.ProviderKey); txErr != nil {
			return txErr
		}

		identityID, txErr := s.upsertIdentity(ctx, q, verified, now)
		if txErr != nil {
			return txErr
		}

		existing, txErr := q.GetMembershipByIdentity(ctx, sqlitegen.GetMembershipByIdentityParams{
			CircleID: live.CircleID.String(), IdentityID: &identityID,
		})
		switch {
		case txErr == nil && existing.RevokedAt != nil:
			// The partial unique index made a second row unrepresentable, so this is the only
			// answer there is — and it is the whole revocation mechanism, seen from the door.
			return apierr.New(apierr.CodeMembershipRevoked,
				"this membership has been revoked; an officer has to reinstate it")
		case txErr == nil:
			membershipID, parseErr := core.ParseID[core.Membership](existing.ID)
			if parseErr != nil {
				return parseErr
			}
			out, txErr = s.finish(ctx, q, finishRequest{
				Key: req.IdempotencyKey, Hash: joinHash(req, live),
				MembershipID: membershipID, CircleID: live.CircleID,
				TokenName: name, Scopes: scopes, TTL: req.TokenTTL, Now: now, Created: false,
			})
			return txErr
		case !store.IsNotFound(txErr):
			return txErr
		}

		membershipID, txErr := core.NewID[core.Membership](s.ids, now)
		if txErr != nil {
			return txErr
		}
		var admittedBy *string
		if live.Kind == invite.KindInvite {
			id := live.InviteID.String()
			admittedBy = &id
		}
		displayName, txErr := validDisplayName(displayNameFor(verified, req.DisplayName))
		if txErr != nil {
			return txErr
		}
		if _, txErr = q.CreateMembership(ctx, sqlitegen.CreateMembershipParams{
			ID: membershipID.String(), CircleID: live.CircleID.String(),
			IdentityID: &identityID, Kind: schemaenum.MembershipKindHuman,
			DisplayName: displayName, DisplayNameNorm: core.Normalise(displayName),
			Role: string(live.Role), AdmittedByInviteID: admittedBy,
			JoinedAt: int64(now), CreatedAt: int64(now), UpdatedAt: int64(now),
		}); txErr != nil {
			return txErr
		}
		if txErr = invite.Redeem(
			ctx, q, live, membershipID, identityID, now, s.ids,
		); txErr != nil {
			return txErr
		}

		action := audit.ActionMemberJoined
		if live.Kind == invite.KindOwnerGrant {
			// A grant leaves no `invite_redemption` row — that table's `invite_id` is NOT NULL —
			// so the audit log is the only place the first owner's arrival is recorded.
			action = audit.ActionOwnerGrantRedeemed
		}
		if txErr = audit.Append(ctx, q, s.ids, now, audit.Entry{
			CircleID: live.CircleID, Actor: membershipID, Action: action,
			EntityType: "membership", EntityID: membershipID.String(),
			Detail: map[string]any{
				"role": string(live.Role), "provider": req.ProviderKey,
				"admitted_by": string(live.Kind),
			},
		}); txErr != nil {
			return txErr
		}

		out, txErr = s.finish(ctx, q, finishRequest{
			Key: req.IdempotencyKey, Hash: joinHash(req, live),
			MembershipID: membershipID, CircleID: live.CircleID,
			TokenName: name, Scopes: scopes, TTL: req.TokenTTL, Now: now, Created: true,
		})
		return txErr
	})
	if err != nil {
		return Joined{}, coded(err)
	}
	return out, nil
}

// Authenticate re-authenticates an existing membership on a new device.
//
// It takes a `circle_id` and answers `404` for a circle that does not exist, a circle this
// identity is not in, and a circle it never heard of — one answer, so the route confirms nothing
// about which. The circle is resolved only AFTER the credential verifies, which is what keeps a
// public route from becoming a circle-existence oracle.
func (s *Service) Authenticate(ctx context.Context, req AuthenticateRequest) (Joined, error) {
	now := s.clock.Now()

	provider, err := s.identity.Provider(ctx, req.ProviderKey)
	if err != nil {
		return Joined{}, fromIdentity(err)
	}
	// The circle's gate is read before verification because the `bearer_token` path needs the
	// guild id to fetch facts for. Nothing about the result reaches the caller: every failure
	// below this point answers 404, so a real circle and an invented one are indistinguishable.
	accepted, gateErr := circle.Accepted(ctx, s.db.Queries(), req.CircleID, req.ProviderKey)

	verified, err := s.verify(ctx, provider, accepted, req.Credential, req.DisplayName)
	if err != nil {
		return Joined{}, err
	}
	if gateErr != nil {
		return Joined{}, notFoundMembership()
	}

	scopes, err := ParseScopes(req.Scopes)
	if err != nil {
		return Joined{}, err
	}
	name, err := tokenName(req.ClientName)
	if err != nil {
		return Joined{}, err
	}

	var out Joined
	err = s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		stored, txErr := q.GetIdentityByProviderSubject(ctx,
			sqlitegen.GetIdentityByProviderSubjectParams{
				ProviderID: verified.ProviderID, Subject: verified.Subject,
			})
		if store.IsNotFound(txErr) {
			// A verified subject this instance has never seen has no membership anywhere, which
			// is the same answer as a circle they are not in.
			return notFoundMembership()
		}
		if txErr != nil {
			return txErr
		}
		if stored.BlockedAt != nil {
			return apierr.New(apierr.CodeIdentityBlocked,
				"this identity is blocked on this instance")
		}

		existing, txErr := q.GetMembershipByIdentity(ctx, sqlitegen.GetMembershipByIdentityParams{
			CircleID: req.CircleID.String(), IdentityID: &stored.ID,
		})
		if store.IsNotFound(txErr) {
			return notFoundMembership()
		}
		if txErr != nil {
			return txErr
		}
		if existing.RevokedAt != nil {
			return apierr.New(apierr.CodeMembershipRevoked,
				"this membership has been revoked; an officer has to reinstate it")
		}

		membershipID, txErr := core.ParseID[core.Membership](existing.ID)
		if txErr != nil {
			return txErr
		}
		out, txErr = s.finish(ctx, q, finishRequest{
			Key: req.IdempotencyKey, Hash: sessionHash(req),
			MembershipID: membershipID, CircleID: req.CircleID,
			TokenName: name, Scopes: scopes, TTL: req.TokenTTL, Now: now, Created: false,
		})
		return txErr
	})
	if err != nil {
		return Joined{}, coded(err)
	}
	return out, nil
}

// verify runs the credential through internal/identity and then evaluates the guild gate.
//
// The two steps are one function because they must not become two decisions in two places:
// [identity.EvaluateGuildGate] is the single evaluator, and this is the single call site both
// `/join` and `/sessions` reach it through.
func (s *Service) verify(
	ctx context.Context, provider identity.Provider, accepted circle.ProviderView,
	credential identity.Credential, displayName string,
) (identity.Verified, error) {
	gate := accepted.Gate()
	var guildIDs []string
	if !gate.IsZero() {
		// Only the gated guild. The flow never asks Discord for the subject's guild LIST, so it
		// never learns about guilds this circle has no business knowing.
		guildIDs = []string{gate.GuildID}
	}

	verified, err := s.identity.Verify(ctx, identity.VerifyRequest{
		Provider: provider, Credential: credential,
		GuildIDs: guildIDs, DisplayName: displayName,
	})
	if err != nil {
		return identity.Verified{}, fromIdentity(err)
	}
	if err := identity.EvaluateGuildGate(gate, verified.GuildFacts); err != nil {
		return identity.Verified{}, fromIdentity(err)
	}
	return verified, nil
}

// upsertIdentity finds the `(provider, subject)` row or writes it, and refuses a blocked one.
//
// The block is re-checked here as well as inside [identity.Service.Verify], because the two
// checks are at two different moments: Verify's is against the credential, this one is against the
// row the membership is about to point at.
func (s *Service) upsertIdentity(
	ctx context.Context, q *sqlitegen.Queries, verified identity.Verified, now core.Micros,
) (string, error) {
	stored, err := q.GetIdentityByProviderSubject(ctx, sqlitegen.GetIdentityByProviderSubjectParams{
		ProviderID: verified.ProviderID, Subject: verified.Subject,
	})
	switch {
	case err == nil && stored.BlockedAt != nil:
		return "", apierr.New(apierr.CodeIdentityBlocked,
			"this identity is blocked on this instance")
	case err == nil:
		return stored.ID, nil
	case !store.IsNotFound(err):
		return "", err
	}

	id, err := core.NewID[core.Identity](s.ids, now)
	if err != nil {
		return "", err
	}
	row, err := q.CreateIdentity(ctx, sqlitegen.CreateIdentityParams{
		ID: id.String(), ProviderID: verified.ProviderID, Subject: verified.Subject,
		DisplayName: verified.DisplayName, CreatedAt: int64(now), UpdatedAt: int64(now),
	})
	if err != nil {
		return "", err
	}
	return row.ID, nil
}

func displayNameFor(verified identity.Verified, supplied string) string {
	if strings.TrimSpace(supplied) != "" {
		return supplied
	}
	return verified.DisplayName
}

// notFoundMembership is the one answer `/sessions` gives to every "you are not in that circle"
// shape. Canonical §7: wrong tenant is 404, never 403, because a 403 confirms the circle exists.
func notFoundMembership() error {
	return apierr.New(apierr.CodeNotFound, "no membership for this identity in that circle")
}

// fromIdentity renders internal/identity's coded error as the problem the edge sends.
//
// The two vocabularies are one vocabulary — both are docs/design/02-api-design.md's closed enum —
// so this is a rename rather than a translation, and an unmapped code becomes an internal error
// rather than a guess. An error with no code at all is a bug or an infrastructure failure, and 500
// is the honest answer to either.
func fromIdentity(err error) error {
	var coded *identity.Error
	if !errors.As(err, &coded) {
		return apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	problem := apierr.Wrap(apierr.Code(coded.Code), err, coded.Message)
	if coded.Location != "" {
		problem = problem.WithField(coded.Location, coded.Message)
	}
	return problem
}

// joinHash identifies the request an `Idempotency-Key` was used for.
//
// It hashes what makes two requests the same LOGICAL request: the code, the provider, the
// credential kind, the name and the scopes. The credential's secret half is deliberately absent —
// a `provider_ticket` is single-use, so a genuine retry cannot carry the same one, and including
// it would make every retry look like a different request.
func joinHash(req JoinRequest, resolved invite.Resolved) []byte {
	return hashRequest(
		"join", resolved.CircleID.String(), resolved.InviteID.String(),
		req.ProviderKey, string(req.Credential.Kind), req.DisplayName, req.ClientName,
		strings.Join(req.Scopes, ","),
	)
}

func sessionHash(req AuthenticateRequest) []byte {
	return hashRequest(
		"sessions", req.CircleID.String(), "",
		req.ProviderKey, string(req.Credential.Kind), req.DisplayName, req.ClientName,
		strings.Join(req.Scopes, ","),
	)
}

func hashRequest(parts ...string) []byte {
	h := sha256.New()
	for _, p := range parts {
		// Length-prefixed, so two requests whose fields differ only in where one ends and the
		// next begins cannot hash the same.
		_, _ = fmt.Fprintf(h, "%d:%s", len(p), p)
	}
	return h.Sum(nil)
}

// finishRequest is what [Service.finish] needs to mint a token idempotently.
type finishRequest struct {
	Key          string
	Hash         []byte
	MembershipID core.MembershipID
	CircleID     core.CircleID
	TokenName    string
	Scopes       []authz.Scope
	TTL          time.Duration
	Now          core.Micros
	Created      bool
}

// idempotencyTTL is how long a `/join` or `/sessions` response stays replayable. A day matches the
// middleware's window for every other state-creating POST.
const idempotencyTTL = 24 * time.Hour

// finish mints the token and stores the response so a retry replays it.
//
// This is the half of `Idempotency-Key` the route registry marks `IdempotencyHandler`: the shared
// middleware cannot do it, because `idempotency_record.principal_membership_id` is NOT NULL and
// the membership does not exist until this transaction created it. So the record is written HERE,
// keyed on the membership this request just resolved.
//
// **What it stores is a credential, and that is stated rather than discovered.** A replayable
// token-minting response has to carry the token, so the response body in `idempotency_record`
// holds a live PAT for 24 hours — exactly as the middleware's own record does for
// `createServiceMember`. The alternative is a mint that cannot be retried, which turns one dropped
// response into a person who cannot join.
//
// **What it cannot cover is a `provider_ticket` retry.** A ticket is single-use by schema trigger,
// so a second `/join` with the same ticket fails at verification with `401 auth_ticket_invalid`
// and never reaches here. That is the design's answer, not a gap this function leaves.
func (s *Service) finish(
	ctx context.Context, q *sqlitegen.Queries, req finishRequest,
) (Joined, error) {
	if replayed, ok, err := s.replay(ctx, q, req); err != nil || ok {
		return replayed, err
	}

	token, err := s.mintToken(ctx, q, req.MembershipID, req.TokenName, req.Scopes, req.TTL, req.Now)
	if err != nil {
		return Joined{}, err
	}
	view, err := s.viewIn(ctx, q, req.CircleID, req.MembershipID)
	if err != nil {
		return Joined{}, err
	}
	circleView, err := circle.Read(ctx, q, req.CircleID, req.Now)
	if err != nil {
		return Joined{}, err
	}
	out := Joined{
		Created: req.Created, Membership: view, Circle: circleView,
		Token: token, AsOf: req.Now,
	}

	if err := s.record(ctx, q, req, out); err != nil {
		return Joined{}, err
	}
	return out, nil
}

// replay answers from `idempotency_record` when this key has already produced a response.
func (s *Service) replay(
	ctx context.Context, q *sqlitegen.Queries, req finishRequest,
) (Joined, bool, error) {
	if req.Key == "" {
		// Unreachable through the API: the route registry requires the header and the middleware
		// refuses a request without it. A caller inside the process — the CLI — has no retry to
		// replay, so no record is written for one either.
		return Joined{}, false, nil
	}
	existing, err := q.GetIdempotencyRecord(ctx, sqlitegen.GetIdempotencyRecordParams{
		PrincipalMembershipID: req.MembershipID.String(), Key: req.Key,
	})
	switch {
	case store.IsNotFound(err):
		return Joined{}, false, nil
	case err != nil:
		return Joined{}, false, err
	case core.Micros(existing.ExpiresAt).Before(req.Now):
		if _, delErr := q.DeleteIdempotencyRecord(ctx, existing.ID); delErr != nil {
			return Joined{}, false, delErr
		}
		return Joined{}, false, nil
	case !bytesEqual(existing.RequestHash, req.Hash):
		return Joined{}, false, apierr.New(apierr.CodeIdempotencyKeyReused,
			"this Idempotency-Key was used for a different request").
			WithField("header.Idempotency-Key", "already used for a different request")
	case existing.CompletedAt == nil || existing.ResponseBody == nil:
		return Joined{}, false, apierr.New(apierr.CodeIdempotencyConflict,
			"a request with this Idempotency-Key is still in flight; retry the same request")
	}

	var out Joined
	if err := json.Unmarshal([]byte(*existing.ResponseBody), &out); err != nil {
		return Joined{}, false, err
	}
	out.Replayed = true
	return out, true, nil
}

// record stores the response this key produced.
func (s *Service) record(
	ctx context.Context, q *sqlitegen.Queries, req finishRequest, out Joined,
) error {
	if req.Key == "" {
		return nil
	}
	body, err := json.Marshal(out)
	if err != nil {
		return err
	}
	id, err := core.NewID[core.IdempotencyRecord](s.ids, req.Now)
	if err != nil {
		return err
	}
	created, err := q.CreateIdempotencyRecord(ctx, sqlitegen.CreateIdempotencyRecordParams{
		ID: id.String(), PrincipalMembershipID: req.MembershipID.String(), Key: req.Key,
		RequestHash: req.Hash, ExpiresAt: int64(req.Now.Add(idempotencyTTL)),
		CreatedAt: int64(req.Now), UpdatedAt: int64(req.Now),
	})
	if err != nil {
		return err
	}
	stored := string(body)
	status := int64(200)
	completedAt := int64(req.Now)
	_, err = q.CompleteIdempotencyRecord(ctx, sqlitegen.CompleteIdempotencyRecordParams{
		ResponseStatus: &status, ResponseBody: &stored, CompletedAt: &completedAt,
		UpdatedAt: int64(req.Now), ID: created.ID,
	})
	return err
}

// viewIn builds a membership representation inside a transaction.
func (s *Service) viewIn(
	ctx context.Context, q *sqlitegen.Queries, circleID core.CircleID, id core.MembershipID,
) (Member, error) {
	row, err := q.GetMembership(ctx, sqlitegen.GetMembershipParams{
		CircleID: circleID.String(), ID: id.String(),
	})
	if store.IsNotFound(err) {
		return Member{}, apierr.New(apierr.CodeNotFound, "no such member")
	}
	if err != nil {
		return Member{}, err
	}
	return toView(ctx, q, row)
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

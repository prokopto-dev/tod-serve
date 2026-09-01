package discord

import (
	"context"
	"errors"
	"fmt"

	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// ErrNotAMember is returned when the invoking Discord account reaches no live membership in the
// circle this channel is bound to.
//
// **It is one error for four different situations**, and that is rule 5 in Discord's words: this
// instance has no `discord` provider, nobody has ever signed in with that account, the account's
// identity is in no membership of this circle, or the membership is revoked. Every one of them is
// answered as a stranger — a reply that distinguished them would tell somebody who has never been
// admitted that the circle exists and that a particular person is in it.
var ErrNotAMember = errors.New("this Discord account reaches no membership in that circle")

// Providers is the slice of `internal/identity` this package needs: which providers this instance
// has, so the `discord` one can be found. It is an interface so a test can drive the dispatcher
// without an OAuth application.
type Providers interface {
	EnabledProviders(ctx context.Context) ([]identity.Provider, error)
}

// Principal resolves the INVOKING Discord user to a principal in one circle.
//
// This is rule 2 of [04-identity §9] and the whole of the confused-deputy answer: the bot holds no
// credential, carries no permission of its own and has nothing to lend. What a command may do is
// what the person who typed it may do, read live from `membership` on this request — so a role
// change or a revocation takes effect on the next command rather than when something expires.
//
// The principal it returns is [auth.KindSession]. That is not a claim that a browser session
// exists: it is the narrowing that applies. A PAT is narrowed by its scopes, and there is no token
// here to carry any — narrowing as a token with an empty scope set would silently refuse every
// command. **It does NOT make the capability floor reachable:** [Principal.SteppedUpWithin] is
// false for a zero `SteppedUpAt`, so a step-up permission is refused, and no command in [Commands]
// names one anyway.
//
// [04-identity §9]: https://github.com/prokopto-dev/tod-serve/blob/main/docs/design/04-identity-and-revocation.md
func (s *Service) Principal(
	ctx context.Context, providers Providers, circleID core.CircleID, subject string,
) (auth.Principal, error) {
	if subject == "" {
		return auth.Principal{}, ErrNotAMember
	}
	providerID, err := discordProviderID(ctx, providers)
	if err != nil {
		return auth.Principal{}, err
	}

	q := s.db.Queries()
	person, err := q.GetIdentityByProviderSubject(ctx, sqlitegen.GetIdentityByProviderSubjectParams{
		ProviderID: providerID, Subject: subject,
	})
	if err != nil {
		if errors.Is(err, store.ErrNoRows) {
			return auth.Principal{}, ErrNotAMember
		}
		return auth.Principal{}, fmt.Errorf("read the identity behind a Discord subject: %w", err)
	}
	if person.BlockedAt != nil {
		// A blocked identity is refused everywhere else on this instance; refusing it here as a
		// stranger keeps that true without telling them they are blocked.
		return auth.Principal{}, ErrNotAMember
	}

	member, err := q.GetMembershipByIdentity(ctx, sqlitegen.GetMembershipByIdentityParams{
		CircleID: circleID.String(), IdentityID: &person.ID,
	})
	if err != nil {
		if errors.Is(err, store.ErrNoRows) {
			return auth.Principal{}, ErrNotAMember
		}
		return auth.Principal{}, fmt.Errorf("read the membership behind a Discord subject: %w", err)
	}
	if member.RevokedAt != nil {
		return auth.Principal{}, ErrNotAMember
	}

	membershipID, err := core.ParseID[core.Membership](member.ID)
	if err != nil {
		return auth.Principal{}, fmt.Errorf("read a membership id: %w", err)
	}
	identityID, err := core.ParseID[core.Identity](person.ID)
	if err != nil {
		return auth.Principal{}, fmt.Errorf("read an identity id: %w", err)
	}
	role, err := authz.ParseRole(member.Role)
	if err != nil {
		// A role outside the enum grants nothing. Failing open here would make a typo in a
		// migration into a privilege escalation, in a surface that answers strangers.
		return auth.Principal{}, fmt.Errorf("read the role of membership %s: %w", member.ID, err)
	}
	return auth.Principal{
		Kind:         auth.KindSession,
		MembershipID: membershipID,
		CircleID:     circleID,
		Role:         role,
		DisplayName:  member.DisplayName,
		IdentityID:   identityID,
		// No instance grants. An instance-realm permission is a decision about the whole instance
		// and reaches no command here; loading them would put the instance realm one slash command
		// away from anybody who compromised a Discord account.
	}, nil
}

// Memberships returns every live circle the invoking Discord account is a member of.
//
// It is what `/tod circles` answers with, and it is the ONLY place this package enumerates
// anything across circles. That is safe for the reason `ListCirclesForIdentity` is: it is keyed on
// a resolved identity and never on anything the payload carried, so it lists what this person
// already knows they are in.
func (s *Service) Memberships(
	ctx context.Context, providers Providers, subject string,
) ([]MemberCircle, error) {
	if subject == "" {
		return nil, nil
	}
	providerID, err := discordProviderID(ctx, providers)
	if err != nil {
		if errors.Is(err, ErrNotAMember) {
			return nil, nil
		}
		return nil, err
	}
	person, err := s.db.Queries().GetIdentityByProviderSubject(
		ctx, sqlitegen.GetIdentityByProviderSubjectParams{ProviderID: providerID, Subject: subject})
	if err != nil {
		if errors.Is(err, store.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("read the identity behind a Discord subject: %w", err)
	}
	rows, err := s.db.Queries().ListCirclesForIdentity(ctx, &person.ID)
	if err != nil {
		return nil, fmt.Errorf("list the circles behind a Discord subject: %w", err)
	}
	out := make([]MemberCircle, 0, len(rows))
	for _, row := range rows {
		id, err := core.ParseID[core.Circle](row.ID)
		if err != nil {
			return nil, fmt.Errorf("read a circle id: %w", err)
		}
		out = append(out, MemberCircle{ID: id, Name: row.Name, Server: row.Server})
	}
	return out, nil
}

// MemberCircle is one circle the invoker belongs to, as `/tod circles` reports it.
//
// It carries the server so a reply can PRINT it, never so anything can key on it. A person may
// hold memberships in several circles on one server — `membership` has no per-server uniqueness —
// and `ux_circle_name_norm_server` makes a name unique only within a server, so neither the name
// nor the server identifies a circle on its own. The id does, and that is what every comparison
// here uses.
type MemberCircle struct {
	ID     core.CircleID
	Name   string
	Server string
}

// discordProviderID returns the instance's `discord` provider.
//
// There is at most one — `ux_identity_provider_discord` is a partial unique index and
// `checkKindIsFree` refuses a second — so this is a lookup and not a choice. An instance with none
// answers every interaction as a stranger, which is correct: nobody on it has a Discord identity
// to be.
func discordProviderID(ctx context.Context, providers Providers) (string, error) {
	if providers == nil {
		return "", ErrNotAMember
	}
	enabled, err := providers.EnabledProviders(ctx)
	if err != nil {
		return "", fmt.Errorf("list the instance's enabled providers: %w", err)
	}
	for _, p := range enabled {
		if p.Kind == identity.KindDiscord {
			return p.ID, nil
		}
	}
	return "", ErrNotAMember
}

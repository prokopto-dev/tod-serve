package circle

import (
	"context"
	"encoding/json"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// AcceptedProvider is one entry of `setCircleProviders`.
type AcceptedProvider struct {
	// Key is the provider's wire identifier — `discord`, `authentik`, `local`. A key rather than
	// an id, because the client that renders this list got it from `listIdentityProviders`, which
	// is public and keyed.
	Key string
	// DiscordGuildID is the guild membership of which this circle requires. Empty means no gate.
	DiscordGuildID string
	// DiscordRequiredRoleIDs narrows the gate. EMPTY MEANS ANYONE IN THE GUILD.
	DiscordRequiredRoleIDs []string
}

// SetProvidersRequest replaces the set of providers a circle accepts.
type SetProvidersRequest struct {
	Providers []AcceptedProvider
	// AcknowledgeWeakRevocation is required to accept a provider with no verifiable subject.
	//
	// The failure mode it exists for: an officer revokes a leaker, the leaker redeems another
	// invite as "Tanky", and is reading the same ToDs a minute later — while the officers believe
	// the problem is handled. **The false confidence is the damage**, not the re-entry, and a
	// field the caller must set is the only thing that reliably reaches the person setting it.
	AcknowledgeWeakRevocation bool
}

// SetProviders replaces which of the instance's providers this circle accepts.
//
// Removing a provider stops NEW joins through it and revokes NO existing membership. That is the
// whole difference between this operation and `revokeMember`, and it is deliberate: mass-revoke on
// removal is a footgun that eventually deletes a guild's whole roster with one click.
func (s *Service) SetProviders(
	ctx context.Context, id core.CircleID, req SetProvidersRequest,
) (Circle, error) {
	now := s.clock.Now()
	err := s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		if _, txErr := q.GetCircle(ctx, id.String()); txErr != nil {
			if store.IsNotFound(txErr) {
				return apierr.New(apierr.CodeNotFound, "no such circle")
			}
			return txErr
		}

		wanted := map[string]AcceptedProvider{}
		for _, want := range req.Providers {
			provider, txErr := q.GetIdentityProviderByKey(ctx, want.Key)
			if store.IsNotFound(txErr) {
				return apierr.Newf(apierr.CodeValidationFailed,
					"no provider %q on this instance", want.Key).
					WithField("body.providers", "names a provider this instance does not have")
			}
			if txErr != nil {
				return txErr
			}
			if provider.Enabled != 1 {
				return apierr.Newf(apierr.CodeProviderDisabled,
					"this instance has disabled the %q provider", want.Key)
			}
			if provider.VerifiableSubject != 1 && !req.AcknowledgeWeakRevocation {
				return apierr.Newf(apierr.CodeAcknowledgementRequired,
					"%q has no verifiable subject, so revoking a member who joined through it "+
						"does not stop them rejoining; set acknowledge_weak_revocation to accept it anyway",
					want.Key).
					WithField("body.acknowledge_weak_revocation", "required to accept a weak provider")
			}
			if err := validateGate(provider, want); err != nil {
				return err
			}
			if _, duplicate := wanted[provider.ID]; duplicate {
				return apierr.Newf(apierr.CodeValidationFailed,
					"provider %q is listed twice", want.Key).
					WithField("body.providers", "lists one provider more than once")
			}
			wanted[provider.ID] = want

			roles, txErr := json.Marshal(roleIDsOrEmpty(want.DiscordRequiredRoleIDs))
			if txErr != nil {
				return txErr
			}
			_, txErr = q.PutCircleProvider(ctx, sqlitegen.PutCircleProviderParams{
				CircleID: id.String(), ProviderID: provider.ID,
				DiscordGuildID:             nullable(want.DiscordGuildID),
				DiscordRequiredRoleIdsJson: string(roles),
				CreatedAt:                  int64(now), UpdatedAt: int64(now),
			})
			if txErr != nil {
				return txErr
			}
		}

		existing, txErr := q.ListCircleProviders(ctx, id.String())
		if txErr != nil {
			return txErr
		}
		for _, row := range existing {
			if _, keep := wanted[row.ProviderID]; keep {
				continue
			}
			if _, txErr = q.DeleteCircleProvider(ctx, sqlitegen.DeleteCircleProviderParams{
				CircleID: id.String(), ProviderID: row.ProviderID,
			}); txErr != nil {
				return txErr
			}
		}
		return nil
	})
	if err != nil {
		if _, coded := apierr.From(err); coded {
			return Circle{}, err
		}
		return Circle{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}

	view, err := s.Get(ctx, id)
	if err != nil {
		return Circle{}, err
	}
	s.log.InfoContext(ctx, "circle providers set",
		"circle_id", id.String(),
		"accepted", len(view.AcceptedProviders),
		"revocation_strength", view.RevocationStrength)
	return view, nil
}

// validateGate refuses a guild gate on a provider that has no guilds.
//
// Reporting it rather than storing it is the point: `circle_provider.discord_guild_id` on an OIDC
// row would be a gate nothing evaluates, which reads to an owner as a gate that is on.
func validateGate(provider sqlitegen.IdentityProvider, want AcceptedProvider) error {
	if provider.Kind == string(identity.KindDiscord) {
		return nil
	}
	if want.DiscordGuildID != "" || len(want.DiscordRequiredRoleIDs) > 0 {
		return apierr.Newf(apierr.CodeValidationFailed,
			"provider %q is %s, and only discord has guilds to gate on",
			want.Key, provider.Kind).
			WithField("body.providers", "sets a Discord guild gate on a provider that has none")
	}
	return nil
}

// Accepted returns the circle's view of one provider, and refuses one it does not accept.
//
// It takes a query set so `/join` and `/sessions` can ask inside their own transaction: whether a
// circle accepts a provider is a fact that has to hold at the moment the membership is written,
// not a moment before it.
func Accepted(
	ctx context.Context, q *sqlitegen.Queries, id core.CircleID, providerKey string,
) (ProviderView, error) {
	accepted, err := acceptedProviders(ctx, q, id)
	if err != nil {
		return ProviderView{}, err
	}
	for _, p := range accepted {
		if p.Key != providerKey {
			continue
		}
		if !p.Available {
			return ProviderView{}, apierr.Newf(apierr.CodeProviderDisabled,
				"this instance has disabled the %q provider", providerKey)
		}
		return p, nil
	}
	return ProviderView{}, apierr.Newf(apierr.CodeProviderNotAccepted,
		"this circle does not accept %q", providerKey)
}

// Gate returns the Discord guild gate this circle applies to a provider.
//
// It is the value `/join` and `/sessions` both hand to [identity.EvaluateGuildGate]. There is one
// evaluator and one way to build its input, so the two call sites cannot diverge — a gate checked
// only at join is a gate somebody walks around by re-authing.
func (p ProviderView) Gate() identity.GuildGate {
	return identity.GuildGate{
		GuildID:         p.DiscordGuildID,
		RequiredRoleIDs: roleIDsOrEmpty(p.DiscordRequiredRoleIDs),
	}
}

// AcceptsWeakProvider reports whether the circle accepts any enabled provider with no verifiable
// subject — which is what forces `max_uses = 1` on its invites. It is the circle's fact, so it is
// answered here rather than by whoever mints the invite.
func AcceptsWeakProvider(
	ctx context.Context, q *sqlitegen.Queries, id core.CircleID,
) (bool, error) {
	accepted, err := acceptedProviders(ctx, q, id)
	if err != nil {
		return false, err
	}
	for _, p := range accepted {
		if p.Available && !p.VerifiableSubject {
			return true, nil
		}
	}
	return false, nil
}

// AcceptsWeakProvider is the same question outside a transaction.
func (s *Service) AcceptsWeakProvider(ctx context.Context, id core.CircleID) (bool, error) {
	return AcceptsWeakProvider(ctx, s.db.Queries(), id)
}

// Read returns a circle inside a caller's transaction.
func Read(
	ctx context.Context, q *sqlitegen.Queries, id core.CircleID, now core.Micros,
) (Circle, error) {
	return get(ctx, q, id, now)
}

func roleIDsOrEmpty(ids []string) []string {
	if ids == nil {
		// `[]` rather than `null`: the column is `NOT NULL DEFAULT '[]'` and an empty list is a
		// meaningful value here — "anyone in the guild" — rather than an absent one.
		return []string{}
	}
	return ids
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

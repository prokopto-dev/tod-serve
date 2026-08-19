package circle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/identity/discord"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// MaxNameLen bounds a circle name, in runes rather than bytes so the limit somebody hits is the
// one they can count.
const MaxNameLen = 80

// Config is what a [Service] needs. Every field is required.
type Config struct {
	Store *store.DB
	Clock clock.Clock
	IDs   *core.Generator
	Log   *slog.Logger
}

// Service reads and writes circles and the providers they accept.
type Service struct {
	db    *store.DB
	clock clock.Clock
	ids   *core.Generator
	log   *slog.Logger
}

// New returns a service.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("circle service: no store")
	case cfg.Clock == nil:
		return nil, errors.New("circle service: no clock")
	case cfg.IDs == nil:
		return nil, errors.New("circle service: no id generator")
	case cfg.Log == nil:
		return nil, errors.New("circle service: no logger")
	}
	return &Service{db: cfg.Store, clock: cfg.Clock, ids: cfg.IDs, log: cfg.Log}, nil
}

// ProviderView is one provider a circle accepts, as a client reads it.
//
// It carries no secret and never can: `client_secret` is a [core.Secret] and is not on this
// struct at all, which is a stronger statement than remembering to omit it.
type ProviderView struct {
	ProviderID  core.IdentityProviderID `json:"provider_id"`
	Key         string                  `json:"key"`
	Kind        string                  `json:"kind"`
	DisplayName string                  `json:"display_name"`
	// VerifiableSubject is a CHECK against `kind`, never a toggle. Everything about revocation
	// strength hangs off it, which is why a client is shown it rather than only the conclusion.
	VerifiableSubject bool `json:"verifiable_subject"`
	// Available says the INSTANCE still has this provider enabled. A circle that accepts a
	// provider the operator has since disabled reports `available: false` here and answers
	// `409 provider_disabled` at join — the row is not hidden, it is marked.
	Available bool `json:"available"`
	// DiscordGuildID is the guild this circle requires membership of, empty when it gates on none.
	DiscordGuildID string `json:"discord_guild_id,omitempty"`
	// DiscordRequiredRoleIDs narrows the gate. EMPTY MEANS ANYONE IN THE GUILD, and holding ANY
	// listed role admits.
	DiscordRequiredRoleIDs []string `json:"discord_required_role_ids"`
}

// Circle is the circle representation every circle operation returns.
type Circle struct {
	ID                       core.CircleID `json:"id"`
	Name                     string        `json:"name"`
	Description              string        `json:"description"`
	Server                   string        `json:"server"`
	Timezone                 string        `json:"timezone"`
	MinReportersToSupersede  int           `json:"min_reporters_to_supersede"`
	RevokeInvalidatesInvites bool          `json:"revoke_invalidates_invites"`
	State                    string        `json:"state"`
	// RevocationStrength is DERIVED on every read — see the package comment.
	RevocationStrength    string   `json:"revocation_strength"`
	RevocationWeakReasons []string `json:"revocation_weak_reasons"`
	WeakProviders         []string `json:"weak_providers"`
	// DisabledProviders names accepted providers the instance has since disabled. They are
	// excluded from the strength calculation because they admit nobody new, and they are reported
	// because a filter that drops a row silently is how an owner stops noticing it.
	DisabledProviders []string       `json:"disabled_providers"`
	AcceptedProviders []ProviderView `json:"accepted_providers"`
	CreatedAt         core.Micros    `json:"created_at"`
	UpdatedAt         core.Micros    `json:"updated_at"`
}

// CreateRequest is a new circle.
type CreateRequest struct {
	Name        string
	Description string
	Server      core.Server
	Timezone    string
}

// Create writes a circle and the providers it accepts.
//
// A new circle auto-accepts every enabled provider with a verifiable subject, and **never
// `local`** — [identity.AutoAccepted] is the one place that rule lives, so circle creation and
// "tell an owner what they would get" cannot disagree. A new circle that silently accepted the
// unverifiable provider would be a circle whose revocation is advisory before anybody chose that.
func (s *Service) Create(ctx context.Context, req CreateRequest) (Circle, error) {
	name, err := validName(req.Name)
	if err != nil {
		return Circle{}, err
	}
	if !req.Server.Valid() {
		return Circle{}, apierr.Newf(apierr.CodeValidationFailed,
			"server must be one of %s", core.Servers()).WithField("body.server", "not a server")
	}
	timezone := req.Timezone
	if timezone == "" {
		timezone = "UTC"
	}

	now := s.clock.Now()
	id, err := core.NewID[core.Circle](s.ids, now)
	if err != nil {
		return Circle{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}

	err = s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		_, txErr := q.CreateCircle(ctx, sqlitegen.CreateCircleParams{
			CircleID: id.String(), Name: name, NameNorm: core.Normalise(name),
			Description: req.Description, Server: string(req.Server), Timezone: timezone,
			MinReportersToSupersede: 1, RevokeInvalidatesInvites: 1,
			State:     schemaenum.CircleStateActive,
			CreatedAt: int64(now), UpdatedAt: int64(now),
		})
		if txErr != nil {
			return txErr
		}
		enabled, txErr := q.ListEnabledIdentityProviders(ctx)
		if txErr != nil {
			return txErr
		}
		for _, row := range identity.AutoAccepted(toProviders(enabled)) {
			_, txErr = q.PutCircleProvider(ctx, sqlitegen.PutCircleProviderParams{
				CircleID: id.String(), ProviderID: row.ID,
				DiscordRequiredRoleIdsJson: "[]",
				CreatedAt:                  int64(now), UpdatedAt: int64(now),
			})
			if txErr != nil {
				return txErr
			}
		}
		return nil
	})
	if err != nil {
		if store.IsUniqueViolation(err) {
			return Circle{}, apierr.Wrap(apierr.CodeConflict, err,
				"a circle with that name already exists on that server")
		}
		return Circle{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}

	s.log.InfoContext(ctx, "circle created",
		slog.String("circle_id", id.String()), slog.String("server", string(req.Server)))
	return s.Get(ctx, id)
}

// Get returns one circle. A circle that does not exist is 404, which is also the answer a
// principal of another circle gets — the tenancy middleware decides that before this runs.
func (s *Service) Get(ctx context.Context, id core.CircleID) (Circle, error) {
	return get(ctx, s.db.Queries(), id, s.clock.Now())
}

func get(
	ctx context.Context, q *sqlitegen.Queries, id core.CircleID, now core.Micros,
) (Circle, error) {
	row, err := q.GetCircle(ctx, id.String())
	if store.IsNotFound(err) {
		return Circle{}, apierr.New(apierr.CodeNotFound, "no such circle")
	}
	if err != nil {
		return Circle{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	accepted, err := acceptedProviders(ctx, q, id)
	if err != nil {
		return Circle{}, err
	}
	return toView(row, accepted), nil
}

// UpdateRequest is `updateCircle`. Every field is a pointer because PATCH means "the fields I
// sent", and a missing field and a zeroed one are different requests.
type UpdateRequest struct {
	Name                     *string
	Description              *string
	Timezone                 *string
	MinReportersToSupersede  *int
	RevokeInvalidatesInvites *bool
	State                    *string
	// Server is accepted only so that sending it can be REFUSED with the code that says why.
	// Ignoring it would let a client believe a circle had moved server, which is the confident
	// mistake [ADR-0009] exists to prevent.
	Server *string
}

// Update changes a circle's mutable fields.
func (s *Service) Update(
	ctx context.Context, id core.CircleID, req UpdateRequest,
) (Circle, error) {
	if req.Server != nil {
		return Circle{}, apierr.New(apierr.CodeFieldImmutable,
			"a circle is pinned to one server permanently; create a second circle for the other one").
			WithField("body.server", "immutable after creation")
	}

	current, err := s.Get(ctx, id)
	if err != nil {
		return Circle{}, err
	}
	name := current.Name
	if req.Name != nil {
		if name, err = validName(*req.Name); err != nil {
			return Circle{}, err
		}
	}
	description := current.Description
	if req.Description != nil {
		description = *req.Description
	}
	timezone := current.Timezone
	if req.Timezone != nil {
		if *req.Timezone == "" {
			return Circle{}, apierr.New(apierr.CodeValidationFailed, "timezone must not be empty").
				WithField("body.timezone", "must not be empty")
		}
		timezone = *req.Timezone
	}
	minReporters := current.MinReportersToSupersede
	if req.MinReportersToSupersede != nil {
		if *req.MinReportersToSupersede < 1 {
			return Circle{}, apierr.New(apierr.CodeValidationFailed,
				"min_reporters_to_supersede must be at least 1").
				WithField("body.min_reporters_to_supersede", "must be at least 1")
		}
		minReporters = *req.MinReportersToSupersede
	}
	revokeInvalidates := current.RevokeInvalidatesInvites
	if req.RevokeInvalidatesInvites != nil {
		revokeInvalidates = *req.RevokeInvalidatesInvites
	}
	state := current.State
	if req.State != nil {
		if *req.State != schemaenum.CircleStateActive && *req.State != schemaenum.CircleStateArchived {
			return Circle{}, apierr.Newf(apierr.CodeValidationFailed,
				"state must be %s or %s",
				schemaenum.CircleStateActive, schemaenum.CircleStateArchived).
				WithField("body.state", "not a circle state")
		}
		state = *req.State
	}

	now := s.clock.Now()
	_, err = s.db.Queries().UpdateCircle(ctx, sqlitegen.UpdateCircleParams{
		Name: name, NameNorm: core.Normalise(name), Description: description, Timezone: timezone,
		MinReportersToSupersede:  int64(minReporters),
		RevokeInvalidatesInvites: boolToInt(revokeInvalidates),
		State:                    state, UpdatedAt: int64(now), CircleID: id.String(),
	})
	if store.IsNotFound(err) {
		return Circle{}, apierr.New(apierr.CodeNotFound, "no such circle")
	}
	if err != nil {
		if store.IsUniqueViolation(err) {
			return Circle{}, apierr.Wrap(apierr.CodeConflict, err,
				"a circle with that name already exists on that server")
		}
		return Circle{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	return s.Get(ctx, id)
}

func validName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	switch {
	case name == "":
		return "", apierr.New(apierr.CodeValidationFailed, "a circle needs a name").
			WithField("body.name", "required")
	case utf8.RuneCountInString(name) > MaxNameLen:
		return "", apierr.Newf(apierr.CodeValidationFailed,
			"name is %d characters; the maximum is %d", utf8.RuneCountInString(name), MaxNameLen).
			WithField("body.name", "above the maximum length")
	case core.Normalise(name) == "":
		// A name of nothing but punctuation normalises to the empty string, which would collide
		// with every other such name on the unique index and read as blank in a member's list.
		return "", apierr.New(apierr.CodeValidationFailed,
			"a circle name needs at least one letter or digit").
			WithField("body.name", "must contain more than punctuation")
	}
	return name, nil
}

func toView(row sqlitegen.Circle, accepted []ProviderView) Circle {
	strength := identity.CircleStrength(providersOf(accepted))
	id, err := core.ParseID[core.Circle](row.ID)
	if err != nil {
		// Unreachable: the column is written from a minted id and read back. A zero id renders as
		// the zero ULID rather than panicking, which is the honest failure for a corrupt row.
		id = core.CircleID{}
	}
	return Circle{
		ID: id, Name: row.Name, Description: row.Description, Server: row.Server,
		Timezone:                 row.Timezone,
		MinReportersToSupersede:  int(row.MinReportersToSupersede),
		RevokeInvalidatesInvites: row.RevokeInvalidatesInvites == 1,
		State:                    row.State,
		RevocationStrength:       string(strength.Strength),
		RevocationWeakReasons:    strength.WeakReasons,
		WeakProviders:            strength.WeakProviders,
		DisabledProviders:        strength.DisabledProviders,
		AcceptedProviders:        accepted,
		CreatedAt:                core.Micros(row.CreatedAt),
		UpdatedAt:                core.Micros(row.UpdatedAt),
	}
}

// acceptedProviders reads `circle_provider` joined to the instance registry.
func acceptedProviders(
	ctx context.Context, q *sqlitegen.Queries, id core.CircleID,
) ([]ProviderView, error) {
	rows, err := q.ListCircleProviders(ctx, id.String())
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	out := make([]ProviderView, 0, len(rows))
	for _, row := range rows {
		provider, provErr := q.GetIdentityProvider(ctx, row.ProviderID)
		if store.IsNotFound(provErr) {
			// A foreign key makes this unreachable. It is checked because "unreachable" is a
			// claim about the schema that this function cannot verify, and skipping the row
			// silently would drop an accepted provider out of the strength calculation.
			return nil, apierr.Wrap(apierr.CodeInternalError,
				fmt.Errorf("circle %s accepts provider %s, which is not in the registry",
					id, row.ProviderID), "")
		}
		if provErr != nil {
			return nil, apierr.Wrap(apierr.CodeInternalError, provErr, "")
		}
		roleIDs, provErr := discord.ParseRoleIDs(row.DiscordRequiredRoleIdsJson)
		if provErr != nil {
			// Refused rather than defaulted to "no roles required": an unparseable list read as
			// an empty one opens the gate for everybody while appearing to enforce it.
			return nil, apierr.Wrap(apierr.CodeInternalError, provErr, "")
		}
		providerID, provErr := core.ParseID[core.IdentityProvider](provider.ID)
		if provErr != nil {
			return nil, apierr.Wrap(apierr.CodeInternalError, provErr, "")
		}
		out = append(out, ProviderView{
			ProviderID: providerID, Key: provider.Key, Kind: provider.Kind,
			DisplayName:       provider.DisplayName,
			VerifiableSubject: provider.VerifiableSubject == 1,
			Available:         provider.Enabled == 1,
			DiscordGuildID:    deref(row.DiscordGuildID),

			DiscordRequiredRoleIDs: roleIDs,
		})
	}
	return out, nil
}

// providersOf renders the accepted set as [identity.Provider] values, which is what
// [identity.CircleStrength] takes. Only the three fields the derivation reads are populated: a
// full row would carry a client secret into a calculation that has no use for one.
func providersOf(accepted []ProviderView) []identity.Provider {
	out := make([]identity.Provider, 0, len(accepted))
	for _, p := range accepted {
		out = append(out, identity.Provider{
			ID: p.ProviderID.String(), Key: p.Key, Kind: identity.Kind(p.Kind),
			Enabled: p.Available, VerifiableSubject: p.VerifiableSubject,
		})
	}
	return out
}

func toProviders(rows []sqlitegen.IdentityProvider) []identity.Provider {
	out := make([]identity.Provider, 0, len(rows))
	for _, row := range rows {
		out = append(out, identity.Provider{
			ID: row.ID, Key: row.Key, Kind: identity.Kind(row.Kind),
			DisplayName: row.DisplayName, Enabled: row.Enabled == 1,
			VerifiableSubject: row.VerifiableSubject == 1,
		})
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

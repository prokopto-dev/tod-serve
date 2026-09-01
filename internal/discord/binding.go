package discord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/audit"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// The audit actions a binding writes. Binding a channel is a DISCLOSURE decision and not a
// preference, so it is recorded the way a revocation is.
const (
	// ActionChannelBound is written when a channel is bound, and when an existing binding's
	// visible-reply switch moves. The detail says which, so the two are not one line in a log
	// somebody has to guess at.
	ActionChannelBound audit.Action = "discord_channel.bound"
	// ActionChannelUnbound is written when a binding is removed. It stops the next message and
	// unsays nothing already posted.
	ActionChannelUnbound audit.Action = "discord_channel.unbound"
)

// EntityDiscordChannel is the audit entity type a binding entry names.
const EntityDiscordChannel = "discord_channel"

// The audit detail keys.
const (
	// DetailAllowVisible is the switch itself: what an auditor is actually looking for.
	DetailAllowVisible = "allow_visible"
	// DetailGuildID is the guild the binding was made in.
	DetailGuildID = "discord_guild_id"
)

var (
	// ErrChannelNotBound is returned when no live binding names the channel. It is also what a
	// binding onto a TOMBSTONED circle produces: a deleted circle keeps its bindings, and one
	// that resolved to a tombstone would answer for a circle that no longer exists.
	ErrChannelNotBound = errors.New("this channel is not bound to a circle")
	// ErrChannelBoundElsewhere is returned when a bind names a channel that already belongs to a
	// live circle. It is refused rather than redirected, and the refusal NAMES NO CIRCLE: saying
	// which one holds the channel would confirm to an officer of circle A that circle B exists.
	ErrChannelBoundElsewhere = errors.New("this channel is already bound")
	// ErrGuildMismatch is returned when the interaction's guild is not the guild the binding was
	// made in. The signature proves who sent the payload, not that the channel id in it means
	// what the binding says.
	ErrGuildMismatch = errors.New("this channel is bound in a different guild")
	// ErrNotASnowflake is returned for a channel or guild id that is not one.
	ErrNotASnowflake = errors.New("a Discord id is 1 to 20 digits")
)

// Binding is one channel bound to one circle.
type Binding struct {
	// ChannelID is the Discord channel. It is the primary key: one channel resolves to at most
	// one circle, or a visible answer would have no single circle it could have come from.
	ChannelID string
	// GuildID is the guild the binding was made in. The resolve compares it against the
	// interaction's, so a channel id lifted from one guild into a payload from another resolves
	// to nothing.
	GuildID string
	// CircleID is the tenant every command in this channel is about.
	CircleID core.CircleID
	// AllowVisible says the channel's officer has decided a visible reply is acceptable here. It
	// is false on a binding created without saying otherwise, in the DDL rather than here.
	AllowVisible bool
	// CreatedBy is the membership that bound it. It is a composite key into `membership
	// (circle_id, id)`, which is what makes "an officer of circle A bound a channel to circle B"
	// unrepresentable.
	CreatedBy core.MembershipID
	CreatedAt core.Micros
	UpdatedAt core.Micros
}

// Config is what a [Service] needs. Every field is required, for the reason every other service
// here says: one that invents a clock behaves differently in a test than in production.
type Config struct {
	Store *store.DB
	Clock clock.Clock
	IDs   *core.Generator
	Log   *slog.Logger
}

// Service owns the channel bindings: the resolve an interaction depends on, and the three
// operations an officer reaches through the API.
type Service struct {
	db    *store.DB
	clock clock.Clock
	ids   *core.Generator
	log   *slog.Logger
}

// New returns a binding service.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("discord service: no store")
	case cfg.Clock == nil:
		return nil, errors.New("discord service: no clock")
	case cfg.IDs == nil:
		return nil, errors.New("discord service: no id generator")
	case cfg.Log == nil:
		return nil, errors.New("discord service: no logger")
	}
	return &Service{db: cfg.Store, clock: cfg.Clock, ids: cfg.IDs, log: cfg.Log}, nil
}

// Resolve answers "which circle is this interaction about", from the channel it arrived in.
//
// **It takes no circle id, and there is nowhere for one to enter.** That is rule 1 of
// [04-identity §9]: an interaction body is user-controlled, so a member of guild B passing circle
// A's id is the class [#29] closed. The channel is the key, the guild is a second fact the binding
// has to agree with, and a tombstoned circle resolves to nothing.
//
// [04-identity §9]: https://github.com/prokopto-dev/tod-serve/blob/main/docs/design/04-identity-and-revocation.md
// [#29]: https://github.com/prokopto-dev/tod-serve/pull/29
func (s *Service) Resolve(ctx context.Context, guildID, channelID string) (Binding, error) {
	if err := checkSnowflake(channelID); err != nil {
		return Binding{}, ErrChannelNotBound
	}
	row, err := s.db.Queries().GetCircleDiscordChannel(ctx, channelID)
	if err != nil {
		if errors.Is(err, store.ErrNoRows) {
			return Binding{}, ErrChannelNotBound
		}
		return Binding{}, fmt.Errorf("resolve the binding for channel %s: %w", channelID, err)
	}
	if row.CircleDeletedAt != nil {
		return Binding{}, ErrChannelNotBound
	}
	if row.DiscordGuildID != guildID {
		return Binding{}, ErrGuildMismatch
	}
	return bindingOf(sqlitegen.CircleDiscordChannel{
		DiscordChannelID:      row.DiscordChannelID,
		CircleID:              row.CircleID,
		DiscordGuildID:        row.DiscordGuildID,
		AllowVisible:          row.AllowVisible,
		CreatedByMembershipID: row.CreatedByMembershipID,
		CreatedAt:             row.CreatedAt,
		UpdatedAt:             row.UpdatedAt,
	})
}

// BindRequest is one channel being bound, or an existing binding's switch being moved.
type BindRequest struct {
	CircleID  core.CircleID
	ChannelID string
	GuildID   string
	// AllowVisible is the disclosure decision. False is the safe value and the DDL's default; a
	// caller that does not mention it gets it.
	AllowVisible bool
	// By is the officer's membership, and it must be a membership IN CircleID. The composite
	// foreign key is what enforces that; this field is what carries it.
	By core.MembershipID
}

// Bind creates or replaces a channel's binding, and audits it.
//
// The read and the write are one transaction, and the audit row is written through the same query
// set: an audit row that survives a rollback is worse than no row, because it is believed.
//
// A channel already bound to a LIVE circle is refused. Redirecting it silently would move a
// disclosure decision that a different circle's officer made, and the members reading that channel
// would be the last to know.
func (s *Service) Bind(ctx context.Context, req BindRequest) (Binding, error) {
	if err := checkSnowflake(req.ChannelID); err != nil {
		return Binding{}, apierr.Wrap(apierr.CodeValidationFailed, err,
			"discord_channel_id is not a Discord id").
			WithField("path.discord_channel_id", "1 to 20 digits")
	}
	if err := checkSnowflake(req.GuildID); err != nil {
		return Binding{}, apierr.Wrap(apierr.CodeValidationFailed, err,
			"discord_guild_id is not a Discord id").
			WithField("body.discord_guild_id", "1 to 20 digits")
	}

	var out Binding
	err := s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		existing, err := q.GetCircleDiscordChannel(ctx, req.ChannelID)
		switch {
		case err == nil:
			// A binding whose circle is tombstoned may be replaced; one whose circle is live may
			// not, and the refusal names no circle.
			if existing.CircleID != req.CircleID.String() && existing.CircleDeletedAt == nil {
				return apierr.Wrap(apierr.CodeConflict, ErrChannelBoundElsewhere,
					"this channel is already bound to a circle")
			}
		case !errors.Is(err, store.ErrNoRows):
			return fmt.Errorf("read the binding for channel %s: %w", req.ChannelID, err)
		}

		now := s.clock.Now()
		row, err := q.BindCircleDiscordChannel(ctx, sqlitegen.BindCircleDiscordChannelParams{
			DiscordChannelID:      req.ChannelID,
			CircleID:              req.CircleID.String(),
			DiscordGuildID:        req.GuildID,
			AllowVisible:          boolToInt(req.AllowVisible),
			CreatedByMembershipID: req.By.String(),
			CreatedAt:             int64(now),
			UpdatedAt:             int64(now),
		})
		if err != nil {
			return fmt.Errorf("bind channel %s: %w", req.ChannelID, err)
		}
		if out, err = bindingOf(row); err != nil {
			return err
		}
		return audit.Append(ctx, q, s.ids, now, audit.Entry{
			CircleID:   req.CircleID,
			Actor:      req.By,
			Action:     ActionChannelBound,
			EntityType: EntityDiscordChannel,
			EntityID:   req.ChannelID,
			Detail: map[string]any{
				DetailAllowVisible: req.AllowVisible,
				DetailGuildID:      req.GuildID,
			},
		})
	})
	if err != nil {
		return Binding{}, err
	}
	return out, nil
}

// Unbind removes a binding, and audits it.
//
// It is scoped to the circle that holds the binding, so an officer of another circle unbinding a
// channel is a `404` from the route's own tenancy check before this runs — and a channel this
// circle does not hold is a `404` here.
func (s *Service) Unbind(ctx context.Context, circleID core.CircleID, channelID string, by core.MembershipID) error {
	return s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		rows, err := q.UnbindCircleDiscordChannel(ctx, sqlitegen.UnbindCircleDiscordChannelParams{
			CircleID: circleID.String(), DiscordChannelID: channelID,
		})
		if err != nil {
			return fmt.Errorf("unbind channel %s: %w", channelID, err)
		}
		if rows == 0 {
			return apierr.New(apierr.CodeNotFound, "this circle has no binding for that channel")
		}
		return audit.Append(ctx, q, s.ids, s.clock.Now(), audit.Entry{
			CircleID:   circleID,
			Actor:      by,
			Action:     ActionChannelUnbound,
			EntityType: EntityDiscordChannel,
			EntityID:   channelID,
		})
	})
}

// List returns every channel this circle discloses into, oldest id first.
func (s *Service) List(ctx context.Context, circleID core.CircleID) ([]Binding, error) {
	rows, err := s.db.Queries().ListCircleDiscordChannels(ctx, circleID.String())
	if err != nil {
		return nil, fmt.Errorf("list the bindings for circle %s: %w", circleID, err)
	}
	out := make([]Binding, 0, len(rows))
	for _, row := range rows {
		b, err := bindingOf(row)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// Get returns one of this circle's bindings.
func (s *Service) Get(
	ctx context.Context, circleID core.CircleID, channelID string,
) (Binding, error) {
	row, err := s.db.Queries().GetCircleDiscordChannelInCircle(
		ctx, sqlitegen.GetCircleDiscordChannelInCircleParams{
			CircleID: circleID.String(), DiscordChannelID: channelID,
		})
	if err != nil {
		if errors.Is(err, store.ErrNoRows) {
			return Binding{}, apierr.New(apierr.CodeNotFound,
				"this circle has no binding for that channel")
		}
		return Binding{}, fmt.Errorf("read the binding for channel %s: %w", channelID, err)
	}
	return bindingOf(row)
}

func bindingOf(row sqlitegen.CircleDiscordChannel) (Binding, error) {
	circleID, err := core.ParseID[core.Circle](row.CircleID)
	if err != nil {
		return Binding{}, fmt.Errorf("read the circle of binding %s: %w", row.DiscordChannelID, err)
	}
	createdBy, err := core.ParseID[core.Membership](row.CreatedByMembershipID)
	if err != nil {
		return Binding{}, fmt.Errorf("read the author of binding %s: %w", row.DiscordChannelID, err)
	}
	return Binding{
		ChannelID:    row.DiscordChannelID,
		GuildID:      row.DiscordGuildID,
		CircleID:     circleID,
		AllowVisible: row.AllowVisible == 1,
		CreatedBy:    createdBy,
		CreatedAt:    core.Micros(row.CreatedAt),
		UpdatedAt:    core.Micros(row.UpdatedAt),
	}, nil
}

// maxSnowflake is the widest a Discord id gets: a 64-bit unsigned integer in decimal is 20 digits.
const maxSnowflake = 20

// checkSnowflake refuses anything that is not a Discord id.
//
// It is a shape check and never an existence one — this server cannot ask Discord whether a
// channel exists, and would not believe the answer about who can read it if it could. What it buys
// is that a channel id cannot be a path traversal, a ULID from another table, or a 4KB string
// stored for ever in a primary key.
func checkSnowflake(id string) error {
	if id == "" || len(id) > maxSnowflake {
		return fmt.Errorf("%w: got %d characters", ErrNotASnowflake, len(id))
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return fmt.Errorf("%w: %q is not a digit", ErrNotASnowflake, r)
		}
	}
	return nil
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

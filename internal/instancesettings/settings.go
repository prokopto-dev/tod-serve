package instancesettings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/audit"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// Setting is `instance_setting_change.setting`: which switch one ledger row is about.
type Setting string

// The settings an endpoint may move, initialised from the one enum catalogue so the wire value,
// the SQL CHECK and these constants cannot drift.
//
// `public_url` is deliberately not among them — see the package comment.
const (
	// SettingSelfServiceCircleCreation is the policy switch this ledger exists for: the instance's
	// stated answer to "who may create a circle here". It is what `/meta` publishes; it is not yet
	// what the `createCircle` route consults, and `internal/api` says so where a reader of the
	// field will meet it.
	SettingSelfServiceCircleCreation Setting = schemaenum.InstanceSettingSelfServiceCircleCreation
	// SettingName is the operator's name for the instance. Display only.
	SettingName Setting = schemaenum.InstanceSettingName
	// SettingTimezone is the instance's default IANA timezone. Display only: every timestamp on
	// the wire is `Micros`, and every countdown is a signed offset from a response's `as_of`.
	SettingTimezone Setting = schemaenum.InstanceSettingTimezone
)

// String returns the database and wire value.
func (s Setting) String() string { return string(s) }

var (
	// ErrNotConfigured is returned when there is no `instance` row to read or change. It is a
	// distinct error rather than a zero-valued answer because "self-service is off" and "nobody
	// has ever set this instance up" are different facts, and only one of them is fixed by
	// changing a setting.
	ErrNotConfigured = errors.New("this instance has no instance row")
	// ErrNoChange is returned when a request would record what the row already says. Appending it
	// would put a row in an audit record that documents nothing having happened — the same refusal
	// `instancegrant.Decide` makes, and the reason the table carries `old_value <> new_value` as a
	// CHECK as well.
	ErrNoChange = errors.New("the instance settings already say this")
	// ErrForkedChain is returned when more than one row satisfies "the row nothing chains onto".
	// Two unique indexes make that unrepresentable, so reaching it means a constraint was dropped
	// — and picking one of the two would be exactly the confident mistake this codebase is built
	// against.
	ErrForkedChain = errors.New("the instance setting ledger chain is forked")
)

// Settings is the instance row, as an administrator reads it.
type Settings struct {
	// Name is the operator's name for the instance.
	Name string
	// PublicURL is the origin this instance is reachable at. **Read-only here**: it must keep
	// matching every registered redirect URI, and it is resolved at boot from `$TOD_PUBLIC_URL`
	// before this row is consulted at all. It is reported because an administrator looking at a
	// broken sign-in needs to see it, not because anything on this path may write it.
	PublicURL string
	// Timezone is the instance's default IANA timezone. Display only.
	Timezone string
	// SelfServiceCircleCreation is the instance's stated policy on who may create a circle. See
	// [SettingSelfServiceCircleCreation] for what currently reads it and what does not.
	SelfServiceCircleCreation bool
	// UpdatedAt is when the row last moved.
	UpdatedAt core.Micros
}

// Change is one recorded move of one setting.
type Change struct {
	// ID is the row's own id.
	ID core.InstanceSettingChangeID
	// Setting is which switch moved.
	Setting Setting
	// OldValue and NewValue are rendered as the database renders them, so the boolean reads `0`
	// and `1` here exactly as it does in the `instance` row.
	OldValue string
	NewValue string
	// ChangedBy is the identity that decided, and is ZERO for a change written at the console.
	ChangedBy core.IdentityID
	// Reason is free text an administrator typed. It is shown in every listing, so it must carry
	// no secret.
	Reason string
	// ChangedAt is when.
	ChangedAt core.Micros
}

// ByConsole reports whether this change was written by an operator holding the database rather
// than by somebody presenting a credential.
func (c Change) ByConsole() bool { return c.ChangedBy.IsZero() }

// ChangeRequest is a change to apply. A nil field is one the caller did not mention, which is a
// different thing from a field set to its current value: the second is refused as no change and
// the first is simply not part of the request.
type ChangeRequest struct {
	Name                      *string
	Timezone                  *string
	SelfServiceCircleCreation *bool
	// ChangedBy is the identity making the change, and is ZERO for the console.
	ChangedBy core.IdentityID
	// Reason is free text, recorded in the ledger and shown in every listing.
	Reason string
}

// Config is what a [Service] needs. Every field is required: a service that invents a clock or an
// id generator behaves differently in a test than in production.
type Config struct {
	Store *store.DB
	Clock clock.Clock
	IDs   *core.Generator
	Log   *slog.Logger
}

// Service reads the instance settings and appends every change to the ledger.
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
		return nil, errors.New("instance settings service: no store")
	case cfg.Clock == nil:
		return nil, errors.New("instance settings service: no clock")
	case cfg.IDs == nil:
		return nil, errors.New("instance settings service: no id generator")
	case cfg.Log == nil:
		return nil, errors.New("instance settings service: no logger")
	}
	return &Service{db: cfg.Store, clock: cfg.Clock, ids: cfg.IDs, log: cfg.Log}, nil
}

// Current returns the instance settings as they stand.
func (s *Service) Current(ctx context.Context) (Settings, error) {
	row, err := s.db.Queries().GetInstance(ctx)
	if err != nil {
		return Settings{}, readInstance(err)
	}
	return settingsOf(row), nil
}

// History returns every change ever recorded, NEWEST first. Nothing prunes it.
func (s *Service) History(ctx context.Context) ([]Change, error) {
	rows, err := s.db.Queries().ListInstanceSettingChanges(ctx)
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError,
			fmt.Errorf("read the instance setting ledger: %w", err), "")
	}
	return convert(rows)
}

// Apply changes the instance row and appends one ledger row per setting that actually moved.
//
// **The read, the update and every append are ONE transaction**, and that is not a tidiness
// preference. The old value written into the ledger has to be the value the update replaced: read
// outside the transaction, two administrators toggling at once both record the same "from", and
// the chain then attests to a history that never happened. [store.DB.InTx] opens `IMMEDIATE`, so
// the two are serialised rather than racing.
//
// A request that would change nothing is refused with [ErrNoChange] rather than committing an
// empty transaction and answering 200. An audit record whose rows include ones where nothing
// happened is an audit record somebody has to filter before reading.
func (s *Service) Apply(ctx context.Context, req ChangeRequest) (Settings, []Change, error) {
	var (
		out      Settings
		recorded []Change
	)
	err := s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		row, err := q.GetInstance(ctx)
		if err != nil {
			return readInstance(err)
		}
		current := settingsOf(row)

		next, moves, err := plan(current, req)
		if err != nil {
			return err
		}
		if len(moves) == 0 {
			return apierr.Wrap(apierr.CodeConflict, ErrNoChange,
				"every field in this request already holds the value it asks for")
		}

		now := s.clock.Now()
		if _, err := q.UpdateInstance(ctx, sqlitegen.UpdateInstanceParams{
			Name: next.Name,
			// The public URL is written back exactly as it was read. It is in the UPDATE because
			// the query sets every column, and it is sourced from `current` rather than from the
			// request because no request may carry one.
			PublicUrl:                 current.PublicURL,
			Timezone:                  next.Timezone,
			SelfServiceCircleCreation: boolToInt(next.SelfServiceCircleCreation),
			UpdatedAt:                 int64(now),
		}); err != nil {
			return apierr.Wrap(apierr.CodeInternalError,
				fmt.Errorf("update the instance row: %w", err), "")
		}

		prevHash, err := chainTail(ctx, q)
		if err != nil {
			return err
		}
		for _, m := range moves {
			appended, err := s.append(ctx, q, m, req, prevHash, now)
			if err != nil {
				return err
			}
			// The next row chains onto the one just written, inside this transaction. Re-reading
			// the tail per row would work and would also be one query per setting for an answer
			// this loop already holds.
			prevHash = appended.hash
			recorded = append(recorded, appended.change)
		}
		next.UpdatedAt = now
		out = next
		return nil
	})
	if err != nil {
		return Settings{}, nil, err
	}
	for _, c := range recorded {
		s.log.InfoContext(ctx, "instance setting changed",
			slog.String("setting", c.Setting.String()),
			slog.String("old_value", c.OldValue),
			slog.String("new_value", c.NewValue),
			slog.Bool("by_console", c.ByConsole()))
	}
	return out, recorded, nil
}

// move is one setting the request actually changes.
type move struct {
	setting  Setting
	oldValue string
	newValue string
}

// plan validates the request and returns the settings it produces alongside the moves it makes.
//
// The moves are in catalogue order rather than request order, so two administrators submitting the
// same change get the same chain — a hash chain whose row order depends on JSON field order is one
// nobody can reproduce by hand.
func plan(current Settings, req ChangeRequest) (Settings, []move, error) {
	next := current
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return Settings{}, nil, apierr.New(apierr.CodeValidationFailed,
				"an instance name must not be empty").
				WithField("body.name", "must not be empty")
		}
		next.Name = name
	}
	if req.Timezone != nil {
		timezone := strings.TrimSpace(*req.Timezone)
		if timezone == "" {
			return Settings{}, nil, apierr.New(apierr.CodeValidationFailed,
				"a timezone must not be empty; it defaults to UTC").
				WithField("body.timezone", "must not be empty")
		}
		next.Timezone = timezone
	}
	if req.SelfServiceCircleCreation != nil {
		next.SelfServiceCircleCreation = *req.SelfServiceCircleCreation
	}

	var moves []move
	// Catalogue order. The switch this ledger exists for is first, which is also the order
	// internal/schemaenum lists them in.
	if next.SelfServiceCircleCreation != current.SelfServiceCircleCreation {
		moves = append(moves, move{
			setting:  SettingSelfServiceCircleCreation,
			oldValue: strconv.FormatInt(boolToInt(current.SelfServiceCircleCreation), 10),
			newValue: strconv.FormatInt(boolToInt(next.SelfServiceCircleCreation), 10),
		})
	}
	if next.Name != current.Name {
		moves = append(moves, move{
			setting: SettingName, oldValue: current.Name, newValue: next.Name,
		})
	}
	if next.Timezone != current.Timezone {
		moves = append(moves, move{
			setting: SettingTimezone, oldValue: current.Timezone, newValue: next.Timezone,
		})
	}
	return next, moves, nil
}

// appended is one written ledger row and the hash the next one chains onto.
type appended struct {
	change Change
	hash   []byte
}

func (s *Service) append(
	ctx context.Context, q *sqlitegen.Queries, m move, req ChangeRequest,
	prevHash []byte, now core.Micros,
) (appended, error) {
	id, err := core.NewID[core.InstanceSettingChange](s.ids, now)
	if err != nil {
		return appended{}, apierr.Wrap(apierr.CodeInternalError,
			fmt.Errorf("mint an instance setting change id: %w", err), "")
	}
	params := sqlitegen.AppendInstanceSettingChangeParams{
		ID:        id.String(),
		Setting:   m.setting.String(),
		OldValue:  m.oldValue,
		NewValue:  m.newValue,
		Reason:    req.Reason,
		PrevHash:  prevHash,
		ChangedAt: int64(now),
	}
	if !req.ChangedBy.IsZero() {
		by := req.ChangedBy.String()
		params.ChangedByIdentityID = &by
	}
	params.Hash = chainHash(params)

	row, err := q.AppendInstanceSettingChange(ctx, params)
	if err != nil {
		return appended{}, apierr.Wrap(apierr.CodeInternalError,
			fmt.Errorf("append instance setting change %q: %w", m.setting, err), "")
	}
	change, err := convertOne(row)
	if err != nil {
		return appended{}, err
	}
	return appended{change: change, hash: row.Hash}, nil
}

// chainTail returns the hash the next row must name as its predecessor: the hash of the row no
// other row already points at, or nil when the ledger is empty.
//
// It is derived from the CHAIN and never from `ORDER BY id`, for the reason
// `instancegrant.chainTail` spells out at length: an id-ordered head returns the wrong row as soon
// as two writers mint inside one millisecond, the next append reuses a `prev_hash` that is already
// claimed, and the unique index then refuses every further append — permanently.
func chainTail(ctx context.Context, q *sqlitegen.Queries) ([]byte, error) {
	tails, err := q.ListInstanceSettingChangeChainTail(ctx)
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError,
			fmt.Errorf("read the instance setting chain tail: %w", err), "")
	}
	switch len(tails) {
	case 1:
		return tails[0].Hash, nil
	case 0:
		// Empty ledger, or a chain that has no tail at all. The second needs a hand-written INSERT
		// forming a cycle, and answering it by starting a second chain beside the one already
		// there would hide exactly the tampering the chain exists to make visible.
		any, err := q.InstanceSettingChangeExists(ctx)
		if err != nil {
			return nil, apierr.Wrap(apierr.CodeInternalError,
				fmt.Errorf("read whether the instance setting ledger is empty: %w", err), "")
		}
		if any {
			return nil, apierr.Wrap(apierr.CodeInternalError,
				fmt.Errorf("the ledger holds rows and no unreferenced hash: %w", ErrForkedChain), "")
		}
		return nil, nil
	default:
		// Two unreferenced hashes is a forked chain, which `ux_instance_setting_change_chain`
		// makes unrepresentable — so reaching this means the index is gone, or two rows share a
		// hash.
		return nil, apierr.Wrap(apierr.CodeInternalError,
			fmt.Errorf("the ledger has %d chain tails: %w", len(tails), ErrForkedChain), "")
	}
}

// chainHash chains one instance_setting_change row, through the same function `audit_log` uses.
func chainHash(row sqlitegen.AppendInstanceSettingChangeParams) []byte {
	return audit.ChainHash(row.PrevHash,
		[]byte(row.ID),
		[]byte(row.Setting),
		[]byte(row.OldValue),
		[]byte(row.NewValue),
		[]byte(deref(row.ChangedByIdentityID)),
		[]byte(row.Reason),
		fmt.Appendf(nil, "%d", row.ChangedAt),
	)
}

// readInstance renders a failed `instance` read. A missing row is not an internal error: it is an
// instance first-run setup has never finished, and saying so names the fix.
func readInstance(err error) error {
	if store.IsNotFound(err) {
		return apierr.Wrap(apierr.CodeConflict, ErrNotConfigured,
			"this instance has never been set up, so it has no settings to read or change. "+
				"Run first-run setup, or `tod-serve init`")
	}
	return apierr.Wrap(apierr.CodeInternalError,
		fmt.Errorf("read the instance row: %w", err), "")
}

func settingsOf(row sqlitegen.Instance) Settings {
	return Settings{
		Name:                      row.Name,
		PublicURL:                 row.PublicUrl,
		Timezone:                  row.Timezone,
		SelfServiceCircleCreation: row.SelfServiceCircleCreation != 0,
		UpdatedAt:                 core.Micros(row.UpdatedAt),
	}
}

func convert(rows []sqlitegen.InstanceSettingChange) ([]Change, error) {
	out := make([]Change, 0, len(rows))
	for _, row := range rows {
		c, err := convertOne(row)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// convertOne reads one row, validating what the column types cannot.
//
// A `setting` outside the catalogue fails the whole read rather than being dropped: the row was
// written by a different binary, and quietly hiding it would let a downgrade decide what an
// administrator is shown about their own instance's history. It is the same refusal
// `instancegrant.convertOne` makes about a permission, and for the same reason — a partial answer
// to "what happened here" is the wrong shape of answer.
func convertOne(row sqlitegen.InstanceSettingChange) (Change, error) {
	id, err := core.ParseID[core.InstanceSettingChange](row.ID)
	if err != nil {
		return Change{}, apierr.Wrap(apierr.CodeInternalError,
			fmt.Errorf("read instance setting change id: %w", err), "")
	}
	enum, ok := schemaenum.Lookup(schemaenum.NameInstanceSettingChange)
	if !ok || !enum.Contains(row.Setting) {
		return Change{}, apierr.Wrap(apierr.CodeInternalError,
			fmt.Errorf("read instance setting change %s: unknown setting %q", row.ID, row.Setting),
			"")
	}
	c := Change{
		ID:        id,
		Setting:   Setting(row.Setting),
		OldValue:  row.OldValue,
		NewValue:  row.NewValue,
		Reason:    row.Reason,
		ChangedAt: core.Micros(row.ChangedAt),
	}
	if row.ChangedByIdentityID != nil {
		by, err := core.ParseID[core.Identity](*row.ChangedByIdentityID)
		if err != nil {
			return Change{}, apierr.Wrap(apierr.CodeInternalError,
				fmt.Errorf("read instance setting change %s changer: %w", row.ID, err), "")
		}
		c.ChangedBy = by
	}
	return c, nil
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

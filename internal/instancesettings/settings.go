package instancesettings

import (
	"context"
	"encoding/hex"
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
	// Revision is the ledger's chain head: the hash of the last change recorded, hex-encoded, and
	// empty on an instance nothing has changed since setup.
	//
	// **It exists because `updated_at` is a clock reading and not a revision, which is a weaker
	// thing than the entity tag over these settings needs.** Two commits can land in the same
	// microsecond, and if the second restores the values the first replaced, every other field
	// here returns to what it was — so a tag computed without this repeats, an `If-None-Match`
	// client is told `304`, and the two ledger rows it would have shown are invisible. A chain
	// hash cannot repeat: it covers the row's own ULID, and `ux_instance_setting_change_hash`
	// makes a duplicate unrepresentable.
	//
	// It is not a substitute for the other fields either. First-run setup writes the `instance`
	// row without appending here, so the values move while this does not — which is why the tag
	// covers both.
	Revision string
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

// Precondition is a caller's check against the settings as they stand, run INSIDE the transaction
// that is about to replace them.
//
// It is a callback rather than a field on [ChangeRequest] because the check that needs it is an
// HTTP one — `If-Match` against an entity tag — and this package must not learn how to compute an
// entity tag to enforce it. What it must do is guarantee WHEN the check runs, and that is what a
// callback invoked between the read and the write buys: the version compared is the version the
// `UPDATE` replaces, so two callers holding the same tag cannot both pass.
//
// A nil Precondition is no check, which is what `tod-serve` at the console has: an operator holding
// the database has read nothing to be stale against.
type Precondition func(Settings) error

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

// Describe returns the settings and the whole ledger behind them, from ONE read snapshot.
//
// **The three reads are a PAIR of pairs, and reading them as pooled statements gets both wrong.**
// The instance row and the chain head are one answer: a writer committing between them hands the
// caller the OLD settings beside the NEW revision, and the entity tag computed over that describes
// a state that never existed — so the very next conditional write is refused with `412` although
// nobody changed anything after the read. The settings and the ledger are the other: a change
// committing between them returns a revision that does not cover the rows beside it, which is the
// same confident mistake pointing the other way.
//
// It is [store.DB.InReadSnapshot] rather than [store.DB.InTx] for the reason ADR-0014 gives: `InTx`
// takes the write lock at `BEGIN`, and a read has no business serialising every writer on the
// instance behind it. This is the same shape issue #17 closed for the board.
func (s *Service) Describe(ctx context.Context) (Settings, []Change, error) {
	var (
		settings Settings
		changes  []Change
	)
	err := s.db.InReadSnapshot(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		var err error
		if settings, err = read(ctx, q); err != nil {
			return err
		}
		changes, err = history(ctx, q)
		return err
	})
	if err != nil {
		return Settings{}, nil, err
	}
	return settings, changes, nil
}

// Current returns the instance settings as they stand, including the ledger revision that makes
// them versionable. See [Settings.Revision] for why the clock reading alone is not one.
//
// It reads the row and the chain head from one snapshot, because they are one answer: see
// [Service.Describe]. A caller that also needs the ledger must use that rather than calling both,
// or the two are again two instants.
func (s *Service) Current(ctx context.Context) (Settings, error) {
	var out Settings
	err := s.db.InReadSnapshot(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		var err error
		out, err = read(ctx, q)
		return err
	})
	if err != nil {
		return Settings{}, err
	}
	return out, nil
}

// read returns the settings through the query set it is given, so the caller decides what instant
// they are read at. Inside [Service.Apply]'s transaction that is the row about to be replaced;
// inside a snapshot it is one consistent instant.
func read(ctx context.Context, q *sqlitegen.Queries) (Settings, error) {
	row, err := q.GetInstance(ctx)
	if err != nil {
		return Settings{}, readInstance(err)
	}
	head, err := chainTail(ctx, q)
	if err != nil {
		return Settings{}, err
	}
	return settingsOf(row, head), nil
}

// history returns every change ever recorded, NEWEST first, through the query set it is given.
// Nothing prunes it.
func history(ctx context.Context, q *sqlitegen.Queries) ([]Change, error) {
	rows, err := q.ListInstanceSettingChanges(ctx)
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError,
			fmt.Errorf("read the instance setting ledger: %w", err), "")
	}
	return convert(rows)
}

// Apply changes the instance row and appends one ledger row per setting that actually moved.
//
// **The precondition, the read, the update and every append are ONE transaction**, and that is not
// a tidiness preference. Two things depend on it, and both were wrong when the check ran outside:
//
//   - The old value written into the ledger has to be the value the update replaced. Read outside
//     the transaction, two administrators toggling at once both record the same "from", and the
//     chain then attests to a history that never happened.
//   - A conditional write has to compare the version the `UPDATE` replaces. Checked outside, two
//     callers holding the SAME entity tag both pass, then serialise here, and the loser commits
//     anyway — appending a ledger row on a precondition that no longer held, while the API
//     documents a `412`. That is worse than no precondition, because the row it writes is
//     believed.
//
// [store.DB.InTx] opens `IMMEDIATE`, so the two are serialised rather than racing, and the second
// caller re-reads inside its own transaction and fails `pre`.
//
// A request that would change nothing is refused with [ErrNoChange] rather than committing an
// empty transaction and answering 200. An audit record whose rows include ones where nothing
// happened is an audit record somebody has to filter before reading. The precondition runs BEFORE
// that check, so a stale caller is told their copy is out of date rather than told there is
// nothing to do — which is the more useful of the two and the only one that is true.
func (s *Service) Apply(
	ctx context.Context, req ChangeRequest, pre Precondition,
) (Settings, []Change, error) {
	var (
		out      Settings
		recorded []Change
	)
	err := s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		// Through the same helper the snapshot read uses, so "the settings" has one definition:
		// the row and the chain head together, because the entity tag covers both. Here they are
		// read inside the writing transaction, which is what makes the precondition below a check
		// against the version this UPDATE replaces.
		current, err := read(ctx, q)
		if err != nil {
			return err
		}
		prevHash, err := chainTail(ctx, q)
		if err != nil {
			return err
		}

		if pre != nil {
			if err := pre(current); err != nil {
				return err
			}
		}

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
		// The revision the caller is handed back is the chain head this call just wrote, so the
		// entity tag over the response is the tag the next conditional write must present.
		next.Revision = revisionOf(prevHash)
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

func settingsOf(row sqlitegen.Instance, chainHead []byte) Settings {
	return Settings{
		Name:                      row.Name,
		PublicURL:                 row.PublicUrl,
		Timezone:                  row.Timezone,
		SelfServiceCircleCreation: row.SelfServiceCircleCreation != 0,
		UpdatedAt:                 core.Micros(row.UpdatedAt),
		Revision:                  revisionOf(chainHead),
	}
}

// revisionOf renders the ledger's chain head for [Settings.Revision]. An empty ledger has no head
// and renders empty rather than as a zero hash, because "nothing has been changed here" is a real
// state and a fabricated hash for it would be indistinguishable from a real one.
func revisionOf(chainHead []byte) string {
	if len(chainHead) == 0 {
		return ""
	}
	return hex.EncodeToString(chainHead)
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

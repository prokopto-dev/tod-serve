package catalogue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// MaxNameLen and MaxZoneLen bound a target's two names, in runes rather than bytes so the limit
// somebody hits is the one they can count.
const (
	MaxNameLen = 120
	MaxZoneLen = 120
)

// MaxAliases bounds how many spellings one target may carry. It is generous because an alias is
// the cheapest thing in this package and the whole point of the ladder, and bounded because the
// alias set is replaced wholesale on update and an unbounded list is an unbounded write.
const MaxAliases = 32

// DefaultFixedGraceSeconds is `raid_target_timer.fixed_grace_seconds`' column default, repeated
// here because a timer resolved from nowhere still has to answer with one — see [TimerSourceNone].
const DefaultFixedGraceSeconds = 900

// Config is what a [Service] needs. Every field is required; a nil one is a construction error
// rather than a silent default, because a service that invented its own clock would behave
// differently in a test than in production and the difference is found in production.
type Config struct {
	Store *store.DB
	Clock clock.Clock
	IDs   *core.Generator
	Log   *slog.Logger
}

// Service reads and writes the raid-target catalogue, its per-server timers, and the per-circle
// overrides that disagree with them.
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
		return nil, errors.New("catalogue service: no store")
	case cfg.Clock == nil:
		return nil, errors.New("catalogue service: no clock")
	case cfg.IDs == nil:
		return nil, errors.New("catalogue service: no id generator")
	case cfg.Log == nil:
		return nil, errors.New("catalogue service: no logger")
	}
	return &Service{db: cfg.Store, clock: cfg.Clock, ids: cfg.IDs, log: cfg.Log}, nil
}

// Target is a raid target's identity: what the mob is, which is a fact about the game and the same
// fact for every circle on the instance.
//
// It carries no timer. The timer is the per-server half and lives in [TargetTimer], because a
// mob's existence and its respawn window have different provenance, different licences and
// different lifetimes — canonical §15.
type Target struct {
	ID core.RaidTargetID `json:"id"`
	// Name is the canonical spelling, including the backtick some mob names carry.
	Name string `json:"name" doc:"The canonical spelling, punctuation included"`
	// NameNorm is what matching compares. It is published rather than hidden so a client that
	// wants to pre-filter locally can use the same string the server does, instead of inventing a
	// second normalisation that disagrees at the first backtick.
	NameNorm      string      `json:"name_norm"`
	Zone          string      `json:"zone"`
	ZoneNorm      string      `json:"zone_norm"`
	Expansion     string      `json:"expansion" enum:"classic,kunark,velious"`
	Category      string      `json:"category" enum:"open_world,zone_boss,planar,ntov,sleeper,key_holder"`
	IsQuakeTarget bool        `json:"is_quake_target" doc:"Whether a server-wide repop resets this target"`
	State         string      `json:"state" enum:"active,retired"`
	Aliases       []string    `json:"aliases" doc:"Every spelling that resolves to this target"`
	CreatedAt     core.Micros `json:"created_at"`
	UpdatedAt     core.Micros `json:"updated_at"`
}

// TargetTimer is a target's CATALOGUE timer for one server.
//
// It is not the effective timer a circle sees: an override sits above it, and only
// [Service.ResolveTimer] applies that precedence. A type that conflated the two would let a caller
// read the catalogue and believe it had read the circle's answer.
type TargetTimer struct {
	Server     string `json:"server" enum:"blue,green,red"`
	WindowKind string `json:"window_kind" enum:"fixed,variance,unknown"`
	// WindowOpenOffsetSeconds and WindowCloseOffsetSeconds are seconds from the ToD, and are null
	// exactly when WindowKind is `unknown` — four CHECK constraints on the table make any other
	// combination unwritable.
	WindowOpenOffsetSeconds  *int64 `json:"window_open_offset_seconds"`
	WindowCloseOffsetSeconds *int64 `json:"window_close_offset_seconds"`
	FixedGraceSeconds        int64  `json:"fixed_grace_seconds"`
	ClusterEpsilonSeconds    *int64 `json:"cluster_epsilon_seconds"`
	// Source and Note are the provenance of numbers this repository does not ship. They are
	// required reading rather than decoration: two officers looking at a disputed window need to
	// know which seed said what.
	Source    string      `json:"source"`
	Note      string      `json:"note"`
	CreatedAt core.Micros `json:"created_at"`
	UpdatedAt core.Micros `json:"updated_at"`
}

// CatalogueEntry is one row of `listRaidTargets`: a target, and the INSTANCE-WIDE timer for the
// server the caller asked about.
//
// The field is called CatalogueTimer and not Timer on purpose, and this is the sharpest edge in
// this package. It is the catalogue's number with no circle override applied, so handing it to
// [consensus.Derive] would silently ignore every override a circle has set and produce a board
// that is confidently wrong. The type is [TargetTimer] rather than [consensus.Timer] so that
// mistake does not compile, and the name says why before anybody reaches for a conversion.
// [Service.ResolveTimer] is the one that answers "what window does THIS circle use".
//
// It is null when the caller named no server, and when the instance holds no timer for that target
// on that server — which is every row on an instance nobody has seeded, and is the honest answer
// rather than an invented window.
type CatalogueEntry struct {
	Target
	CatalogueTimer *TargetTimer `json:"catalogue_timer" doc:"The instance-wide timer for the requested server. NOT the circle's effective timer: a circle override sits above it"`
}

// ListFilter narrows a page of the catalogue.
type ListFilter struct {
	// Server folds that server's catalogue timer into each entry. The zero value folds none.
	Server core.Server
	// Expansion and Zone filter on exact catalogue values; Zone is compared normalised, so
	// "temple of veeshan" finds "Temple of Veeshan".
	Expansion string
	Zone      string
	// Query is the board's `q`. It runs the same normalisation and the same substring rung the
	// resolve ladder uses, so a search box and a plugin cannot disagree about what a name matches.
	Query string
	// IncludeRetired reads retired targets too. The board never does: a retired target is not a
	// thing anybody is waiting on.
	IncludeRetired bool
	// Cursor is the last id of the previous page. Canonical §4: every collection but
	// `/events/replay` pages on the opaque ULID cursor.
	Cursor core.RaidTargetID
	// Limit is the page size. Zero means the caller did not say; the API layer resolves it.
	Limit int
}

// Listing is one page of the catalogue.
type Listing struct {
	Entries    []CatalogueEntry
	NextCursor core.RaidTargetID
	HasMore    bool
	// Total is how many targets matched the filter before the page was cut, so a filter that drops
	// rows counts them somewhere visible.
	Total int
}

// Get returns one target by id.
func (s *Service) Get(ctx context.Context, id core.RaidTargetID) (Target, error) {
	row, err := s.db.Queries().GetRaidTarget(ctx, id.String())
	if store.IsNotFound(err) {
		return Target{}, apierr.New(apierr.CodeNotFound, "no such raid target")
	}
	if err != nil {
		return Target{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	aliases, err := s.db.Queries().ListRaidTargetAliases(ctx, id.String())
	if err != nil {
		return Target{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	names := make([]string, 0, len(aliases))
	for _, a := range aliases {
		names = append(names, a.Alias)
	}
	return toTarget(row, names)
}

// Timers returns every per-server catalogue timer a target has, ordered by server.
//
// A target with none is an empty slice and not an error: that is what an unseeded instance holds
// for every target it knows about.
func (s *Service) Timers(ctx context.Context, id core.RaidTargetID) ([]TargetTimer, error) {
	out := make([]TargetTimer, 0, len(core.Servers()))
	for _, server := range core.Servers() {
		row, err := s.db.Queries().GetRaidTargetTimer(ctx, sqlitegen.GetRaidTargetTimerParams{
			TargetID: id.String(), Server: string(server),
		})
		if store.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
		}
		out = append(out, toTargetTimer(row))
	}
	return out, nil
}

// List returns a page of the catalogue.
//
// The whole catalogue is read and filtered in Go rather than in SQL, and that is a deliberate
// choice this comment owns: it is tens of rows, `q` has to run [core.Normalise] which core SQLite
// cannot, and a `LIKE` beside the ladder would be a second matcher that disagrees with it at the
// first backtick. If the catalogue ever reaches a size where this is wrong, the fix is an index on
// `name_norm` and a prefix query, not a second normalisation.
func (s *Service) List(ctx context.Context, filter ListFilter) (Listing, error) {
	targets, err := s.loadTargets(ctx)
	if err != nil {
		return Listing{}, err
	}

	timers := map[string]TargetTimer{}
	if filter.Server != "" {
		if !filter.Server.Valid() {
			return Listing{}, apierr.Newf(apierr.CodeValidationFailed,
				"server must be one of %s", core.Servers()).
				WithField("query.server", "not a server")
		}
		rows, timerErr := s.db.Queries().ListRaidTargetTimersForServer(ctx, string(filter.Server))
		if timerErr != nil {
			return Listing{}, apierr.Wrap(apierr.CodeInternalError, timerErr, "")
		}
		for _, row := range rows {
			timers[row.TargetID] = toTargetTimer(row)
		}
	}

	matched := make([]Target, 0, len(targets))
	query := core.Normalise(filter.Query)
	zone := core.Normalise(filter.Zone)
	for _, t := range targets {
		switch {
		case !filter.IncludeRetired && t.State != schemaenum.RaidTargetStateActive:
		case filter.Expansion != "" && t.Expansion != filter.Expansion:
		case zone != "" && t.ZoneNorm != zone:
		case query != "" && !matchesQuery(t, query):
		default:
			matched = append(matched, t)
		}
	}

	// Ordered by id, which is ULID order, because the cursor is a ULID: a page ordered by name and
	// resumed by id would skip rows the moment a target was added. The embedded catalogue is
	// minted in expansion, then zone, then name order, so id order still reads as a list.
	slices.SortFunc(matched, func(a, b Target) int { return a.ID.ULID().Compare(b.ID.ULID()) })

	if !filter.Cursor.IsZero() {
		after := filter.Cursor.ULID()
		matched = slices.DeleteFunc(matched, func(t Target) bool {
			return t.ID.ULID().Compare(after) <= 0
		})
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = len(matched)
	}
	page := Listing{Total: len(matched)}
	if len(matched) > limit {
		page.HasMore = true
		matched = matched[:limit]
	}
	page.Entries = make([]CatalogueEntry, 0, len(matched))
	for _, t := range matched {
		entry := CatalogueEntry{Target: t}
		if timer, ok := timers[t.ID.String()]; ok {
			entry.CatalogueTimer = &timer
		}
		page.Entries = append(page.Entries, entry)
	}
	if len(page.Entries) > 0 && page.HasMore {
		page.NextCursor = page.Entries[len(page.Entries)-1].ID
	}
	return page, nil
}

// matchesQuery runs the board's `q` against a target's normalised name and aliases. It is the
// substring rung of the ladder and nothing else, so a search box narrows exactly the way a resolve
// would have.
func matchesQuery(t Target, query string) bool {
	if strings.Contains(t.NameNorm, query) {
		return true
	}
	for _, alias := range t.Aliases {
		if strings.Contains(core.Normalise(alias), query) {
			return true
		}
	}
	return false
}

// CreateRequest is a new raid target.
type CreateRequest struct {
	Name          string
	Zone          string
	Expansion     string
	Category      string
	IsQuakeTarget bool
	Aliases       []string
}

// Create adds a target to the instance-wide catalogue.
func (s *Service) Create(ctx context.Context, req CreateRequest) (Target, error) {
	fields, err := validateIdentity(req.Name, req.Zone, req.Expansion, req.Category)
	if err != nil {
		return Target{}, err
	}
	aliases, err := validateAliases(req.Aliases)
	if err != nil {
		return Target{}, err
	}

	now := s.clock.Now()
	id, err := core.NewID[core.RaidTarget](s.ids, now)
	if err != nil {
		return Target{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}

	err = s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		if txErr := checkNamespace(ctx, q, id, fields.nameNorm, aliases); txErr != nil {
			return txErr
		}
		_, txErr := q.CreateRaidTarget(ctx, sqlitegen.CreateRaidTargetParams{
			ID: id.String(), Name: fields.name, NameNorm: fields.nameNorm,
			Zone: fields.zone, ZoneNorm: fields.zoneNorm,
			Expansion: fields.expansion, Category: fields.category,
			IsQuakeTarget: boolToInt(req.IsQuakeTarget),
			State:         schemaenum.RaidTargetStateActive,
			CreatedAt:     int64(now), UpdatedAt: int64(now),
		})
		if txErr != nil {
			return txErr
		}
		return s.writeAliases(ctx, q, id, aliases, now)
	})
	if err != nil {
		return Target{}, s.writeError(err, fields.name)
	}

	s.log.InfoContext(ctx, "raid target created",
		slog.String("target_id", id.String()), slog.String("name", fields.name))
	return s.Get(ctx, id)
}

// UpdateRequest is `updateRaidTarget`. Every field is a pointer because PATCH means "the fields I
// sent", and a missing field and a zeroed one are different requests.
type UpdateRequest struct {
	Name          *string
	Zone          *string
	Expansion     *string
	Category      *string
	IsQuakeTarget *bool
	State         *string
	// Aliases REPLACES the set when present. An add-one verb would need a remove-one verb beside
	// it and a second route for a list of two strings; a replace is the whole set in one request,
	// which is also the only shape an `If-Match` can protect.
	Aliases *[]string
}

// Update changes a target's mutable fields.
func (s *Service) Update(
	ctx context.Context, id core.RaidTargetID, req UpdateRequest,
) (Target, error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return Target{}, err
	}

	name, zone := current.Name, current.Zone
	expansion, category := current.Expansion, current.Category
	if req.Name != nil {
		name = *req.Name
	}
	if req.Zone != nil {
		zone = *req.Zone
	}
	if req.Expansion != nil {
		expansion = *req.Expansion
	}
	if req.Category != nil {
		category = *req.Category
	}
	fields, err := validateIdentity(name, zone, expansion, category)
	if err != nil {
		return Target{}, err
	}

	state := current.State
	if req.State != nil {
		if *req.State != schemaenum.RaidTargetStateActive &&
			*req.State != schemaenum.RaidTargetStateRetired {
			return Target{}, apierr.Newf(apierr.CodeValidationFailed, "state must be %s or %s",
				schemaenum.RaidTargetStateActive, schemaenum.RaidTargetStateRetired).
				WithField("body.state", "not a raid target state")
		}
		state = *req.State
	}
	quake := current.IsQuakeTarget
	if req.IsQuakeTarget != nil {
		quake = *req.IsQuakeTarget
	}
	aliases := current.Aliases
	if req.Aliases != nil {
		if aliases, err = validateAliases(*req.Aliases); err != nil {
			return Target{}, err
		}
	}

	now := s.clock.Now()
	err = s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		if txErr := checkNamespace(ctx, q, id, fields.nameNorm, aliases); txErr != nil {
			return txErr
		}
		_, txErr := q.UpdateRaidTarget(ctx, sqlitegen.UpdateRaidTargetParams{
			Name: fields.name, NameNorm: fields.nameNorm,
			Zone: fields.zone, ZoneNorm: fields.zoneNorm,
			Expansion: fields.expansion, Category: fields.category,
			IsQuakeTarget: boolToInt(quake), State: state,
			UpdatedAt: int64(now), ID: id.String(),
		})
		if txErr != nil {
			return txErr
		}
		if req.Aliases == nil {
			return nil
		}
		if _, txErr = q.DeleteRaidTargetAliases(ctx, id.String()); txErr != nil {
			return txErr
		}
		return s.writeAliases(ctx, q, id, aliases, now)
	})
	if err != nil {
		return Target{}, s.writeError(err, fields.name)
	}
	return s.Get(ctx, id)
}

// writeAliases inserts one alias row per spelling. It takes the caller's transaction: a target
// whose aliases half-landed is a target the ladder answers differently on every retry.
func (s *Service) writeAliases(
	ctx context.Context, q *sqlitegen.Queries, target core.RaidTargetID,
	aliases []string, now core.Micros,
) error {
	for _, alias := range aliases {
		id, err := core.NewID[core.RaidTargetAlias](s.ids, now)
		if err != nil {
			return err
		}
		if _, err = q.CreateRaidTargetAlias(ctx, sqlitegen.CreateRaidTargetAliasParams{
			ID: id.String(), TargetID: target.String(),
			Alias: alias, AliasNorm: core.Normalise(alias),
			CreatedAt: int64(now), UpdatedAt: int64(now),
		}); err != nil {
			return err
		}
	}
	return nil
}

// checkNamespace enforces the one rule neither unique index can state: a spelling belongs to ONE
// target, whether it is that target's name or one of its aliases.
//
// `ux_raid_target_name_norm` makes names unique among names and `ux_raid_target_alias_norm` makes
// aliases unique among aliases. Neither says anything about the other table, so without this an
// alias `lordnagafen` could be hung on a different target — and the ladder would answer that
// spelling with the canonical-name target, because `name_norm` is rung two and `alias_norm` is
// rung four. The alias would resolve to somebody else's mob and its owner would never be told,
// which is the quiet version of the exact failure the ladder exists to prevent.
//
// The triggers in 000005_raid_target_name_namespace.sql are the enforcement, and they cover the
// paths this function cannot see — `tod-serve seed`, an importer, a hand-run `sqlite3`. This runs
// inside the write transaction so the API can answer `409` naming the collision instead of
// surfacing a constraint abort as a 500.
func checkNamespace(
	ctx context.Context, q *sqlitegen.Queries,
	self core.RaidTargetID, nameNorm string, aliases []string,
) error {
	targets, err := q.ListAllRaidTargets(ctx)
	if err != nil {
		return err
	}
	aliasRows, err := q.ListAllRaidTargetAliases(ctx)
	if err != nil {
		return err
	}

	claimed := make(map[string]string, len(targets)+len(aliasRows))
	for _, row := range targets {
		if row.ID != self.String() {
			claimed[row.NameNorm] = row.Name
		}
	}
	for _, row := range aliasRows {
		// An alias this target already owns is not a collision with itself: the update path
		// replaces the whole alias set, so every one of them is about to be rewritten.
		if row.TargetID != self.String() {
			claimed[row.AliasNorm] = row.Alias
		}
	}

	if owner, taken := claimed[nameNorm]; taken {
		return apierr.Newf(apierr.CodeConflict,
			"that name is already how %q is spelled; one spelling belongs to one target", owner).
			WithField("body.name", "already claimed by another target")
	}
	for i, alias := range aliases {
		norm := core.Normalise(alias)
		if owner, taken := claimed[norm]; taken {
			return apierr.Newf(apierr.CodeConflict,
				"the alias %q is already how %q is spelled; it would resolve to that target "+
					"instead of this one", alias, owner).
				WithField(fmt.Sprintf("body.aliases[%d]", i), "already claimed by another target")
		}
		if norm == nameNorm {
			// Not a collision with another row, but the same mistake: the name rung outranks the
			// alias rung, so this alias could never be the thing that matched.
			return apierr.Newf(apierr.CodeValidationFailed,
				"the alias %q is the target's own name; it would never be the rung that matched",
				alias).WithField(fmt.Sprintf("body.aliases[%d]", i), "same as the name")
		}
		claimed[norm] = alias
	}
	return nil
}

// writeError renders a failed write. A unique violation is the one a caller can act on: both
// `name_norm` and `alias_norm` are unique across the whole catalogue, so it means some spelling
// they sent already resolves somewhere else.
func (s *Service) writeError(err error, name string) error {
	if coded, ok := apierr.From(err); ok {
		return coded
	}
	if store.IsUniqueViolation(err) {
		return apierr.Wrap(apierr.CodeConflict, err,
			"a target or alias with that normalised spelling already exists; "+
				"resolve "+name+" to find it")
	}
	return apierr.Wrap(apierr.CodeInternalError, err, "")
}

// identityFields is a validated identity, normalised once so the caller cannot forget to.
type identityFields struct {
	name, nameNorm string
	zone, zoneNorm string
	expansion      string
	category       string
}

func validateIdentity(name, zone, expansion, category string) (identityFields, error) {
	f := identityFields{
		name: strings.TrimSpace(name), zone: strings.TrimSpace(zone),
		expansion: expansion, category: category,
	}
	switch {
	case f.name == "":
		return identityFields{}, apierr.New(apierr.CodeValidationFailed, "a target needs a name").
			WithField("body.name", "required")
	case utf8.RuneCountInString(f.name) > MaxNameLen:
		return identityFields{}, apierr.Newf(apierr.CodeValidationFailed,
			"name is %d characters; the maximum is %d",
			utf8.RuneCountInString(f.name), MaxNameLen).
			WithField("body.name", "above the maximum length")
	case f.zone == "":
		return identityFields{}, apierr.New(apierr.CodeValidationFailed, "a target needs a zone").
			WithField("body.zone", "required")
	case utf8.RuneCountInString(f.zone) > MaxZoneLen:
		return identityFields{}, apierr.Newf(apierr.CodeValidationFailed,
			"zone is %d characters; the maximum is %d",
			utf8.RuneCountInString(f.zone), MaxZoneLen).
			WithField("body.zone", "above the maximum length")
	}
	f.nameNorm = core.Normalise(f.name)
	f.zoneNorm = core.Normalise(f.zone)
	if f.nameNorm == "" {
		// A name of nothing but punctuation normalises to the empty string, which would collide
		// with every other such name on the unique index and match every query as a substring.
		return identityFields{}, apierr.New(apierr.CodeValidationFailed,
			"a target name needs at least one letter or digit").
			WithField("body.name", "must contain more than punctuation")
	}
	if !slices.Contains(expansions(), f.expansion) {
		return identityFields{}, apierr.Newf(apierr.CodeValidationFailed,
			"expansion must be one of %s", strings.Join(expansions(), ", ")).
			WithField("body.expansion", "not an expansion")
	}
	if !slices.Contains(categories(), f.category) {
		return identityFields{}, apierr.Newf(apierr.CodeValidationFailed,
			"category must be one of %s", strings.Join(categories(), ", ")).
			WithField("body.category", "not a category")
	}
	return f, nil
}

// validateAliases trims, de-duplicates by normalised form, and refuses the empty one.
//
// De-duplication happens here rather than at the unique index because two spellings of one alias
// in one request is a client being generous, not a conflict — `VS` and `vs.` are the same alias —
// and answering 409 to it would be a confusing way to say "you already told me".
func validateAliases(raw []string) ([]string, error) {
	if len(raw) > MaxAliases {
		return nil, apierr.Newf(apierr.CodeValidationFailed,
			"a target may carry at most %d aliases; %d were sent", MaxAliases, len(raw)).
			WithField("body.aliases", "too many")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for i, alias := range raw {
		alias = strings.TrimSpace(alias)
		norm := core.Normalise(alias)
		if norm == "" {
			return nil, apierr.New(apierr.CodeValidationFailed,
				"an alias needs at least one letter or digit").
				WithField(fmt.Sprintf("body.aliases[%d]", i), "must contain more than punctuation")
		}
		if seen[norm] {
			continue
		}
		seen[norm] = true
		out = append(out, alias)
	}
	return out, nil
}

// loadTargets reads the whole catalogue with its aliases attached, in one pass over each table.
func (s *Service) loadTargets(ctx context.Context) ([]Target, error) {
	rows, err := s.db.Queries().ListAllRaidTargets(ctx)
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	aliasRows, err := s.db.Queries().ListAllRaidTargetAliases(ctx)
	if err != nil {
		return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	byTarget := map[string][]string{}
	for _, a := range aliasRows {
		byTarget[a.TargetID] = append(byTarget[a.TargetID], a.Alias)
	}

	out := make([]Target, 0, len(rows))
	for _, row := range rows {
		target, convErr := toTarget(row, byTarget[row.ID])
		if convErr != nil {
			return nil, convErr
		}
		out = append(out, target)
	}
	return out, nil
}

func toTarget(row sqlitegen.RaidTarget, aliases []string) (Target, error) {
	id, err := core.ParseID[core.RaidTarget](row.ID)
	if err != nil {
		// Refused rather than zeroed. Every other id in this package is compared against this one,
		// and a target that rendered as the zero ULID would look like a different target to every
		// caller that held a real one.
		return Target{}, apierr.Wrap(apierr.CodeInternalError,
			fmt.Errorf("raid_target %q is not a ULID: %w", row.ID, err), "")
	}
	if aliases == nil {
		aliases = []string{}
	}
	return Target{
		ID: id, Name: row.Name, NameNorm: row.NameNorm,
		Zone: row.Zone, ZoneNorm: row.ZoneNorm,
		Expansion: row.Expansion, Category: row.Category,
		IsQuakeTarget: row.IsQuakeTarget == 1, State: row.State,
		Aliases:   aliases,
		CreatedAt: core.Micros(row.CreatedAt), UpdatedAt: core.Micros(row.UpdatedAt),
	}, nil
}

func toTargetTimer(row sqlitegen.RaidTargetTimer) TargetTimer {
	source := ""
	if row.Source != nil {
		source = *row.Source
	}
	return TargetTimer{
		Server: row.Server, WindowKind: row.WindowKind,
		WindowOpenOffsetSeconds:  row.WindowOpenOffsetSeconds,
		WindowCloseOffsetSeconds: row.WindowCloseOffsetSeconds,
		FixedGraceSeconds:        row.FixedGraceSeconds,
		ClusterEpsilonSeconds:    row.ClusterEpsilonSeconds,
		Source:                   source, Note: row.Note,
		CreatedAt: core.Micros(row.CreatedAt), UpdatedAt: core.Micros(row.UpdatedAt),
	}
}

// expansions and categories are the enum catalogue's values, read out of it rather than repeated,
// so a new expansion is one edit and not two.
func expansions() []string { return enumValues(schemaenum.NameRaidTargetExpansion) }
func categories() []string { return enumValues(schemaenum.NameRaidTargetCategory) }

func enumValues(name string) []string {
	e, ok := schemaenum.Lookup(name)
	if !ok {
		// Unreachable: the names above are constants from the catalogue this reads. An empty list
		// refuses every value rather than accepting every value, which is the direction that
		// fails closed.
		return nil
	}
	return e.Values
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

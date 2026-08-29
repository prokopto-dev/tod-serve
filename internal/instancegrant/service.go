package instancegrant

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/prokopto-dev/tod-serve/internal/audit"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// Decision is `instance_grant.decision`: what one row of the ledger recorded.
type Decision string

// The decisions, initialised from the one enum catalogue so the wire value, the SQL CHECK and
// these constants cannot drift.
const (
	// DecisionGranted means the identity holds the permission from this row until a later row
	// supersedes it.
	DecisionGranted Decision = schemaenum.InstanceGrantDecisionGranted
	// DecisionRevoked means it does not. It is a row rather than the absence of one, because the
	// decision to take an instance permission away is worth as much to a reader as the decision to
	// give it.
	DecisionRevoked Decision = schemaenum.InstanceGrantDecisionRevoked
)

// String returns the database and wire value.
func (d Decision) String() string { return string(d) }

var (
	// ErrNoChange is returned when a decision would record what the ledger already says. Appending
	// it would put a row in an audit record that documents nothing having happened.
	ErrNoChange = errors.New("the ledger already records this decision")
	// ErrUnknownIdentity is returned when the identity named does not exist. It is separate from a
	// foreign-key failure so that `tod-serve instance grant` can say which of the two things the
	// operator got wrong.
	ErrUnknownIdentity = errors.New("no such identity")
	// ErrForkedChain is returned when more than one row satisfies "the decision nothing
	// supersedes". Two unique indexes make that unrepresentable, so reaching it means a constraint
	// was dropped — and picking one of the two would be exactly the confident mistake this
	// codebase is built against.
	ErrForkedChain = errors.New("the instance grant chain is forked")
)

// Grant is one decision, as a caller reads it.
type Grant struct {
	// ID is the decision's own id, and the ledger's cursor.
	ID core.InstanceGrantID
	// IdentityID is who the decision is about.
	IdentityID core.IdentityID
	// Permission is the instance-realm key. It is validated on read: a row holding something
	// outside the catalogue grants nothing rather than everything.
	Permission authz.Permission
	// Decision is what was decided.
	Decision Decision
	// Supersedes is the decision this one replaced, and is zero for the first decision about a
	// permission.
	Supersedes core.InstanceGrantID
	// DecidedBy is the identity that decided, and is ZERO for a decision written at the console.
	// The console holds the database and needs no credential, which is how a fresh instance gets
	// its first instance owner at all.
	DecidedBy core.IdentityID
	// Reason is free text an operator typed. It is shown in every listing, so it must carry no
	// secret.
	Reason string
	// DecidedAt is when.
	DecidedAt core.Micros
}

// ByConsole reports whether this decision was written by the operator at the console rather than
// by a person holding a credential.
func (g Grant) ByConsole() bool { return g.DecidedBy.IsZero() }

// Config is what a [Service] needs. Every field is required: a service that invents a clock or an
// id generator behaves differently in a test than in production.
type Config struct {
	Store *store.DB
	Clock clock.Clock
	IDs   *core.Generator
	Log   *slog.Logger
}

// Service reads and appends to the instance grant ledger.
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
		return nil, errors.New("instance grant service: no store")
	case cfg.Clock == nil:
		return nil, errors.New("instance grant service: no clock")
	case cfg.IDs == nil:
		return nil, errors.New("instance grant service: no id generator")
	case cfg.Log == nil:
		return nil, errors.New("instance grant service: no logger")
	}
	return &Service{db: cfg.Store, clock: cfg.Clock, ids: cfg.IDs, log: cfg.Log}, nil
}

// Effective returns the instance-realm permissions this identity currently holds.
//
// A zero identity holds nothing and is not an error: a service membership has no identity, and
// asking about one is a normal question with a normal answer.
func (s *Service) Effective(ctx context.Context, identityID core.IdentityID) (authz.Set, error) {
	if identityID.IsZero() {
		return authz.Set{}, nil
	}
	decisions, err := s.decisionsFor(ctx, identityID)
	if err != nil {
		return authz.Set{}, err
	}
	var held []authz.Permission
	for _, d := range decisions {
		if d.Decision == DecisionGranted {
			held = append(held, d.Permission)
		}
	}
	return authz.NewSet(held...), nil
}

// DecisionsFor returns every current decision about one identity, granted and revoked alike.
func (s *Service) DecisionsFor(ctx context.Context, identityID core.IdentityID) ([]Grant, error) {
	return s.decisionsFor(ctx, identityID)
}

func (s *Service) decisionsFor(ctx context.Context, identityID core.IdentityID) ([]Grant, error) {
	rows, err := s.db.Queries().ListInstanceGrantDecisionsForIdentity(ctx, identityID.String())
	if err != nil {
		return nil, fmt.Errorf("read instance grants for identity %s: %w", identityID, err)
	}
	return convert(rows)
}

// Current returns every current decision on the instance, ordered by identity then permission.
func (s *Service) Current(ctx context.Context) ([]Grant, error) {
	rows, err := s.db.Queries().ListInstanceGrantDecisions(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the instance grants: %w", err)
	}
	return convert(rows)
}

// History returns every decision ever recorded, oldest first. Nothing prunes it.
func (s *Service) History(ctx context.Context) ([]Grant, error) {
	rows, err := s.db.Queries().ListInstanceGrantHistory(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the instance grant history: %w", err)
	}
	return convert(rows)
}

// DecideRequest is one decision to append.
type DecideRequest struct {
	// IdentityID is who the decision is about.
	IdentityID core.IdentityID
	// Permission must be instance-realm. A circle-realm key is refused here and is also
	// unrepresentable in the column, which is two mechanisms for one rule on purpose: the CHECK
	// protects the database and this protects the caller, who gets a sentence rather than a
	// constraint name.
	Permission authz.Permission
	// Decision is what to record.
	Decision Decision
	// DecidedBy is the identity deciding, and is ZERO for the console.
	DecidedBy core.IdentityID
	// Reason is free text, shown in every listing. It must carry no secret.
	Reason string
}

// Decide appends one decision to the ledger, and returns it.
//
// It refuses a decision the ledger already records ([ErrNoChange]): an audit record whose rows
// include ones where nothing happened is an audit record somebody has to filter before reading.
//
// The whole thing is one transaction. The tail it supersedes, the chain head it hashes against and
// the row it writes have to be consistent, and a chain built from a read outside the transaction
// that wrote it is a chain that can fork under concurrency.
func (s *Service) Decide(ctx context.Context, req DecideRequest) (Grant, error) {
	var out Grant
	err := s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		var txErr error
		out, txErr = s.DecideInTx(ctx, q, req)
		return txErr
	})
	if err != nil {
		return Grant{}, err
	}
	s.log.InfoContext(ctx, "instance grant decided",
		slog.String("identity_id", out.IdentityID.String()),
		slog.String("permission", string(out.Permission)),
		slog.String("decision", string(out.Decision)),
		slog.Bool("by_console", out.ByConsole()))
	return out, nil
}

// DecideInTx appends one decision through a transaction the caller already holds.
//
// It exists for the ONE caller that has to decide and write in the same breath: the join that
// admits the instance's first administrator reads "does anybody administer this instance" and
// appends `instance.owner` if nobody does, and a check on one side of a commit boundary from its
// own write is a check two concurrent redemptions both pass.
//
// It does not log. [Service.Decide] logs after its commit; a caller here has a transaction that
// may still roll back, and a log line about a decision that was undone is worse than none.
func (s *Service) DecideInTx(
	ctx context.Context, q *sqlitegen.Queries, req DecideRequest,
) (Grant, error) {
	if !authz.IsInstanceRealm(req.Permission) {
		return Grant{}, fmt.Errorf("decide %q for identity %s: %w",
			req.Permission, req.IdentityID, authz.ErrUnknownPermission)
	}
	if req.Decision != DecisionGranted && req.Decision != DecisionRevoked {
		return Grant{}, fmt.Errorf("decide %q for identity %s: unknown decision %q",
			req.Permission, req.IdentityID, req.Decision)
	}
	if req.IdentityID.IsZero() {
		return Grant{}, fmt.Errorf("decide %q: %w", req.Permission, ErrUnknownIdentity)
	}

	now := s.clock.Now()
	if _, err := q.GetIdentity(ctx, req.IdentityID.String()); err != nil {
		if store.IsNotFound(err) {
			return Grant{}, fmt.Errorf("decide %q for identity %s: %w",
				req.Permission, req.IdentityID, ErrUnknownIdentity)
		}
		return Grant{}, fmt.Errorf("read identity %s: %w", req.IdentityID, err)
	}

	current, err := s.tail(ctx, q, req.IdentityID, req.Permission)
	if err != nil {
		return Grant{}, err
	}
	if current != nil && current.Decision == req.Decision {
		return Grant{}, fmt.Errorf("decide %q for identity %s: %w",
			req.Permission, req.IdentityID, ErrNoChange)
	}
	if current == nil && req.Decision == DecisionRevoked {
		// Revoking something never granted would append a row asserting a removal that never
		// happened, which is a lie in the one table nobody is supposed to have to double-check.
		return Grant{}, fmt.Errorf("decide %q for identity %s: %w",
			req.Permission, req.IdentityID, ErrNoChange)
	}

	prevHash, err := chainTail(ctx, q)
	if err != nil {
		return Grant{}, err
	}

	id, err := core.NewID[core.InstanceGrant](s.ids, now)
	if err != nil {
		return Grant{}, fmt.Errorf("mint an instance grant id: %w", err)
	}
	params := sqlitegen.AppendInstanceGrantParams{
		ID:         id.String(),
		IdentityID: req.IdentityID.String(),
		Permission: string(req.Permission),
		Decision:   string(req.Decision),
		Reason:     req.Reason,
		PrevHash:   prevHash,
		DecidedAt:  int64(now),
	}
	if current != nil {
		superseded := current.ID.String()
		params.SupersedesID = &superseded
	}
	if !req.DecidedBy.IsZero() {
		by := req.DecidedBy.String()
		params.DecidedByIdentityID = &by
	}
	params.Hash = chainHash(params)

	row, err := q.AppendInstanceGrant(ctx, params)
	if err != nil {
		return Grant{}, fmt.Errorf("append instance grant %q for identity %s: %w",
			req.Permission, req.IdentityID, err)
	}
	return convertOne(row)
}

// Administers reports whether a held set of instance grants makes somebody an ADMINISTRATOR of
// this instance, as opposed to a holder of one narrow instance capability.
//
// The key is `instance.security.manage`, and it is one key rather than a list because
// [authz.ExpandInstance] already answers the rest: an identity granted `instance.owner` holds it,
// so ownership closes the same door without this function knowing that ownership exists. Asking
// through the expansion is also what stops this drifting from what the middleware would decide —
// `auth.Principal.Holds` asks the same question the same way.
//
// It is NOT "holds any instance-realm key". `ops.read` is a dashboard and `catalogue.manage` is
// timer curation; neither makes its holder able to configure who may sign in, which is what
// ADR-0012 calls being administrable over the API and what first-run setup exists to hand
// somebody.
func Administers(held authz.Set) bool {
	return authz.ExpandInstance(held).Has(authz.PermissionInstanceSecurityManage)
}

// AdministratorExists reports whether any identity on this instance currently administers it.
//
// **It is the first-run setup window, derived rather than stored** (ADR-0016): setup is open
// exactly while this is false. It takes the query set rather than reading the pool so that the
// join admitting the first administrator can ask it inside its own transaction — SQLite has one
// writer, so a check and an append in one transaction cannot both be taken by two redemptions.
//
// **A GRANT IS NOT ENOUGH, and that is the half that makes this a window rather than a latch.** An
// instance grant is on an identity, and an identity only reaches a request through a membership:
// `Authenticator.membership` reads one on every call and refuses a revoked one, or one in a deleted
// circle. The ledger outlives both — a revocation is a membership row, not a grant row — so an
// identity can hold `instance.owner` while every credential it could present is refused. Closing
// first-run setup on that would lock the operator out of the instance AND out of the one browser
// door back into it, which is the exact failure ADR-0016 exists to prevent.
//
// A REVOKED decision is not an administrator either, so an instance whose last owner was revoked is
// recoverable through this door as well as through the console.
//
// [CanAuthenticate] is the predicate rather than a second definition of "can act", and
// `checkAdministrable` in `tod-serve doctor` asks the same function: an operator whose report says
// nobody can administer this instance must not meet a wizard that says setup is over.
func AdministratorExists(ctx context.Context, q *sqlitegen.Queries) (bool, error) {
	rows, err := q.ListInstanceGrantDecisions(ctx)
	if err != nil {
		return false, fmt.Errorf("read the instance grants: %w", err)
	}
	grants, err := convert(rows)
	if err != nil {
		return false, err
	}
	held := map[core.IdentityID][]authz.Permission{}
	for _, g := range grants {
		if g.Decision == DecisionGranted {
			held[g.IdentityID] = append(held[g.IdentityID], g.Permission)
		}
	}
	for identityID, perms := range held {
		if !Administers(authz.NewSet(perms...)) {
			continue
		}
		live, err := CanAuthenticate(ctx, q, identityID)
		if err != nil {
			return false, err
		}
		if live {
			return true, nil
		}
	}
	return false, nil
}

// CanAuthenticate reports whether an identity has a membership it could actually present.
//
// `ListCirclesForIdentity` is the predicate rather than a query written for this, because it is
// already the one `listCircles` serves and the one the authenticator enforces:
// `revoked_at IS NULL AND deleted_at IS NULL`, joined on `identity_id` so a service membership —
// which has none; a bot has an owner rather than an identity — cannot qualify. Asking the question
// the API asks is what stops a second definition of "can act" existing.
func CanAuthenticate(
	ctx context.Context, q *sqlitegen.Queries, identityID core.IdentityID,
) (bool, error) {
	// The parameter is a pointer because `membership.identity_id` is nullable — a service
	// membership has none. A non-nil one is what asks about a person.
	id := identityID.String()
	rows, err := q.ListCirclesForIdentity(ctx, &id)
	if err != nil {
		return false, fmt.Errorf("memberships for identity %s are unreadable: %w", identityID, err)
	}
	return len(rows) > 0, nil
}

// chainTail returns the hash the next decision must name as its predecessor: the hash of the row
// no other row already points at, or nil when the ledger is empty.
//
// It is derived from the CHAIN and never from `ORDER BY id`. A ULID is monotonic within one
// generator, and `tod-serve instance grant` builds a fresh one per invocation — so two invocations
// inside one millisecond mint from random entropy and the later row can sort below the earlier
// one. An id-ordered head then returns the earlier row forever, the next append reuses a
// `prev_hash` that row's successor already claimed, and `ux_instance_grant_chain` refuses it: the
// ledger stops accepting decisions, permanently, on an instance nobody can then administer.
//
// The unique index makes that failure loud rather than a silent fork, which is the only reason the
// bug was a lockout and not a chain that quietly branched.
func chainTail(ctx context.Context, q *sqlitegen.Queries) ([]byte, error) {
	tails, err := q.ListInstanceGrantChainTail(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the instance grant chain tail: %w", err)
	}
	switch len(tails) {
	case 1:
		return tails[0].Hash, nil
	case 0:
		// Empty ledger, or a chain that has no tail at all. The second needs a hand-written INSERT
		// forming a cycle, and answering it by starting a second chain beside the one already
		// there would hide exactly the tampering the chain exists to make visible.
		any, err := q.InstanceGrantExists(ctx)
		if err != nil {
			return nil, fmt.Errorf("read whether the instance grant ledger is empty: %w", err)
		}
		if any {
			return nil, fmt.Errorf("the ledger holds rows and no unreferenced hash: %w",
				ErrForkedChain)
		}
		return nil, nil
	default:
		// Two unreferenced hashes is a forked chain, which `ux_instance_grant_chain` makes
		// unrepresentable — so reaching this means the index is gone, or two rows share a hash.
		return nil, fmt.Errorf("the ledger has %d chain tails: %w", len(tails), ErrForkedChain)
	}
}

// tail returns the decision nothing supersedes for one identity and one permission, or nil when
// there has never been one.
func (s *Service) tail(
	ctx context.Context, q *sqlitegen.Queries, identityID core.IdentityID, p authz.Permission,
) (*Grant, error) {
	rows, err := q.GetInstanceGrantDecision(ctx, sqlitegen.GetInstanceGrantDecisionParams{
		IdentityID: identityID.String(), Permission: string(p),
	})
	if err != nil {
		return nil, fmt.Errorf("read the current decision for %q on identity %s: %w",
			p, identityID, err)
	}
	switch len(rows) {
	case 0:
		return nil, nil
	case 1:
	default:
		// Two tails for one pair is a FORKED chain, which two unique indexes make unrepresentable
		// — so reaching this means one of them is gone. The query returns many rather than one
		// precisely so this is reachable: a `:one` query scans the first row and discards the
		// rest, which would resolve the fork by silently picking a branch.
		return nil, fmt.Errorf("%q on identity %s has %d current decisions: %w",
			p, identityID, len(rows), ErrForkedChain)
	}
	g, err := convertOne(rows[0])
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// chainHash chains one instance_grant row, through the same function `audit_log` uses.
func chainHash(row sqlitegen.AppendInstanceGrantParams) []byte {
	return audit.ChainHash(row.PrevHash,
		[]byte(row.ID),
		[]byte(row.IdentityID),
		[]byte(row.Permission),
		[]byte(row.Decision),
		[]byte(deref(row.SupersedesID)),
		[]byte(deref(row.DecidedByIdentityID)),
		[]byte(row.Reason),
		fmt.Appendf(nil, "%d", row.DecidedAt),
	)
}

func convert(rows []sqlitegen.InstanceGrant) ([]Grant, error) {
	out := make([]Grant, 0, len(rows))
	for _, row := range rows {
		g, err := convertOne(row)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

// convertOne reads one row, validating everything the column types cannot.
//
// A permission outside the catalogue is an error rather than a value that is silently dropped: the
// row was written by a different binary, and quietly ignoring it would let a downgrade change what
// somebody may do without saying so. It fails the whole read for the same reason
// `auth.ParseScopesJSON` does — a partial answer to "what may this person do" is the wrong shape of
// answer.
func convertOne(row sqlitegen.InstanceGrant) (Grant, error) {
	id, err := core.ParseID[core.InstanceGrant](row.ID)
	if err != nil {
		return Grant{}, fmt.Errorf("read instance grant id: %w", err)
	}
	identityID, err := core.ParseID[core.Identity](row.IdentityID)
	if err != nil {
		return Grant{}, fmt.Errorf("read instance grant %s identity: %w", row.ID, err)
	}
	permission, err := authz.ParsePermission(row.Permission)
	if err != nil {
		return Grant{}, fmt.Errorf("read instance grant %s: %w", row.ID, err)
	}
	if !authz.IsInstanceRealm(permission) {
		return Grant{}, fmt.Errorf("read instance grant %s: %q is not instance-realm: %w",
			row.ID, permission, authz.ErrUnknownPermission)
	}
	g := Grant{
		ID:         id,
		IdentityID: identityID,
		Permission: permission,
		Decision:   Decision(row.Decision),
		Reason:     row.Reason,
		DecidedAt:  core.Micros(row.DecidedAt),
	}
	if g.Decision != DecisionGranted && g.Decision != DecisionRevoked {
		return Grant{}, fmt.Errorf("read instance grant %s: unknown decision %q",
			row.ID, row.Decision)
	}
	if row.SupersedesID != nil {
		supersedes, err := core.ParseID[core.InstanceGrant](*row.SupersedesID)
		if err != nil {
			return Grant{}, fmt.Errorf("read instance grant %s supersedes: %w", row.ID, err)
		}
		g.Supersedes = supersedes
	}
	if row.DecidedByIdentityID != nil {
		by, err := core.ParseID[core.Identity](*row.DecidedByIdentityID)
		if err != nil {
			return Grant{}, fmt.Errorf("read instance grant %s decider: %w", row.ID, err)
		}
		g.DecidedBy = by
	}
	return g, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

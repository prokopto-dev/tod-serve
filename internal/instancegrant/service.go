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
	var out Grant
	err := s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		if _, err := q.GetIdentity(ctx, req.IdentityID.String()); err != nil {
			if store.IsNotFound(err) {
				return fmt.Errorf("decide %q for identity %s: %w",
					req.Permission, req.IdentityID, ErrUnknownIdentity)
			}
			return fmt.Errorf("read identity %s: %w", req.IdentityID, err)
		}

		current, err := s.tail(ctx, q, req.IdentityID, req.Permission)
		if err != nil {
			return err
		}
		if current != nil && current.Decision == req.Decision {
			return fmt.Errorf("decide %q for identity %s: %w",
				req.Permission, req.IdentityID, ErrNoChange)
		}
		if current == nil && req.Decision == DecisionRevoked {
			// Revoking something never granted would append a row asserting a removal that never
			// happened, which is a lie in the one table nobody is supposed to have to double-check.
			return fmt.Errorf("decide %q for identity %s: %w",
				req.Permission, req.IdentityID, ErrNoChange)
		}

		var prevHash []byte
		head, err := q.GetLatestInstanceGrant(ctx)
		switch {
		case err == nil:
			prevHash = head.Hash
		case !store.IsNotFound(err):
			return fmt.Errorf("read the instance grant chain head: %w", err)
		}

		id, err := core.NewID[core.InstanceGrant](s.ids, now)
		if err != nil {
			return fmt.Errorf("mint an instance grant id: %w", err)
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
			return fmt.Errorf("append instance grant %q for identity %s: %w",
				req.Permission, req.IdentityID, err)
		}
		out, err = convertOne(row)
		return err
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

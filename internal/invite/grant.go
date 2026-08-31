package invite

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// grantKeyPrefix namespaces owner grants inside `tod_meta`.
//
// The rest of the key is the hex of the code's hash, so a grant is looked up by an unguessable
// server-minted secret on the table's own primary key — the same shape as `invite.code_hash`, and
// never by circle. There is no scan and no prefix lookup here for the same reason there is none
// for an invite: it would be a brute-force oracle.
const grantKeyPrefix = "owner_grant/"

// DefaultGrantTTL is how long an owner grant lives when nobody says otherwise.
//
// A day: long enough for an operator to finish setting up the instance and open a browser, short
// enough that a code left in a terminal scrollback stops working before the week is out. Like an
// invite, a grant has no eternal form.
const DefaultGrantTTL = 24 * 60 * 60 * 1_000_000 // Micros

// ErrGrantConsumed is returned when a grant has already been redeemed.
var ErrGrantConsumed = errors.New("owner grant has already been redeemed")

// Grant is the one-time owner grant: the only way an owner reaches a circle that has none.
//
// It is NOT an invite, and the difference is a schema constraint rather than a preference:
// `invite` carries `CHECK (role <> 'owner')` so that a leaked invite — or one minted by a
// compromised bot token — can add a visible, revocable member and never seize the circle. A grant
// is printed once, on the operator's own terminal, by a command that already holds the database.
//
// Its single use is a compare-and-swap (`ConsumeMeta`), not a read-then-write: two browsers
// redeeming the same printed code must not both become owner.
type Grant struct {
	CircleID  string      `json:"circle_id"`
	ExpiresAt core.Micros `json:"expires_at"`
	CreatedAt core.Micros `json:"created_at"`
	// ConsumedAt is set by [ConsumeGrant] and is what the compare-and-swap swaps in.
	ConsumedAt core.Micros `json:"consumed_at,omitempty"`
}

// Role is what redeeming a grant makes somebody. It is always `owner`; the constant exists so the
// caller does not spell it and the reason travels with it.
func (Grant) Role() authz.Role { return authz.RoleOwner }

// Live reports whether the grant can still be redeemed at now.
func (g Grant) Live(now core.Micros) bool {
	return g.ConsumedAt.IsZero() && now.Before(g.ExpiresAt)
}

func grantKey(hash []byte) string { return grantKeyPrefix + hex.EncodeToString(hash) }

// MintGrant writes a one-time owner grant for a circle and returns the code to print.
//
// The code is returned exactly once and never stored: `tod_meta` holds the hash, so a database
// read yields no working credential — the same rule every other bearer credential in this schema
// obeys.
func MintGrant(
	ctx context.Context, q *sqlitegen.Queries, random io.Reader,
	circleID core.CircleID, now core.Micros, ttl core.Micros,
) (Code, error) {
	code, err := Mint(random)
	if err != nil {
		return "", fmt.Errorf("mint owner grant: %w", err)
	}
	grant := Grant{CircleID: circleID.String(), ExpiresAt: now + ttl, CreatedAt: now}
	value, err := json.Marshal(grant)
	if err != nil {
		return "", fmt.Errorf("mint owner grant: %w", err)
	}
	err = q.SetMeta(ctx, sqlitegen.SetMetaParams{
		Key: grantKey(Hash(code)), Value: string(value), UpdatedAt: int64(now),
	})
	if err != nil {
		return "", fmt.Errorf("record owner grant for circle %s: %w", circleID, err)
	}
	return code, nil
}

// ReadGrant returns the grant a code names, or [store.ErrNoRows] wrapped when there is none.
func ReadGrant(ctx context.Context, q *sqlitegen.Queries, code Code) (Grant, error) {
	return ReadGrantByHash(ctx, q, Hash(code))
}

// ReadGrantByHash is [ReadGrant] for a caller that already holds the hash and not the code.
//
// The browser OAuth flow is that caller: `auth_flow.invite_code_hash` is what a callback has,
// minutes after the code itself went out of scope. Both spellings go through one [grantKey] here,
// because a second derivation of that key elsewhere would be a second lookup path — which is
// exactly the defect this function was added to close.
func ReadGrantByHash(ctx context.Context, q *sqlitegen.Queries, hash []byte) (Grant, error) {
	row, err := q.GetMeta(ctx, grantKey(hash))
	if err != nil {
		if store.IsNotFound(err) {
			return Grant{}, fmt.Errorf("owner grant: %w", store.ErrNoRows)
		}
		return Grant{}, fmt.Errorf("read owner grant: %w", err)
	}
	var grant Grant
	if err := json.Unmarshal([]byte(row.Value), &grant); err != nil {
		return Grant{}, fmt.Errorf("read owner grant: %w", err)
	}
	return grant, nil
}

// GrantByCodeHash is the owner-grant lookup `identitysql.New` is handed.
//
// It is injected for the reason [HashCode] is, and against the same failure. `tod_meta`'s
// `owner_grant/` key is THIS package's, as the hash is, and a second spelling of it in the
// persistence half would let one path resolve a code and another refuse it. That is not
// hypothetical: until this existed, `createAuthorizationURL` resolved the `invite` table and
// nothing else, so every first-run owner code was answered `invite_invalid` after a perfectly
// successful Discord sign-in, while `previewInvite` — which falls back to [ReadGrant] — showed
// the same code as valid on the page the browser came from.
//
// It returns values rather than a [Grant] so that neither package has to import the other's
// types, and it returns `expires_at` and `consumed_at` rather than a verdict because the caller
// holds the clock — the same division `identitysql.InviteByCodeHash` already makes for an invite
// row. `consumed_at` is zero when the grant has not been redeemed.
//
// A hash naming no grant answers [store.ErrNoRows] wrapped, which is what an unissued invite
// answers. That must stay true: an owner grant a guesser can tell apart from an ordinary invite
// is the circle-existence oracle [Resolve] closes.
func GrantByCodeHash(
	ctx context.Context, q *sqlitegen.Queries, hash []byte,
) (circleID string, expiresAt, consumedAt core.Micros, err error) {
	grant, err := ReadGrantByHash(ctx, q, hash)
	if err != nil {
		return "", 0, 0, err
	}
	return grant.CircleID, grant.ExpiresAt, grant.ConsumedAt, nil
}

// ConsumeGrant marks a grant redeemed, and refuses a second redemption.
//
// The refusal is a compare-and-swap in SQL rather than a check in Go: `ConsumeMeta` updates only
// where the stored value is still the one that was read, so two concurrent redemptions of one
// printed code produce one owner and one [ErrGrantConsumed] rather than two owners.
func ConsumeGrant(
	ctx context.Context, q *sqlitegen.Queries, code Code, now core.Micros,
) (Grant, error) {
	grant, err := ReadGrant(ctx, q, code)
	if err != nil {
		return Grant{}, err
	}
	if !grant.ConsumedAt.IsZero() {
		return Grant{}, fmt.Errorf("consume owner grant: %w", ErrGrantConsumed)
	}
	before, err := json.Marshal(grant)
	if err != nil {
		return Grant{}, fmt.Errorf("consume owner grant: %w", err)
	}
	consumed := grant
	consumed.ConsumedAt = now
	after, err := json.Marshal(consumed)
	if err != nil {
		return Grant{}, fmt.Errorf("consume owner grant: %w", err)
	}

	_, err = q.ConsumeMeta(ctx, sqlitegen.ConsumeMetaParams{
		Value: string(after), UpdatedAt: int64(now),
		Key: grantKey(Hash(code)), Expected: string(before),
	})
	if store.IsNotFound(err) {
		// Somebody else redeemed it between the read above and this write. One grant, one owner.
		return Grant{}, fmt.Errorf("consume owner grant: %w", ErrGrantConsumed)
	}
	if err != nil {
		return Grant{}, fmt.Errorf("consume owner grant: %w", err)
	}
	return consumed, nil
}

// MintOwnerGrant writes a one-time owner grant for a circle and returns the code to print.
//
// It is the CLI's entry point and has no HTTP route, deliberately: an operation that makes an
// owner out of nothing must require the database rather than a credential, because on a fresh
// instance there is no credential to require.
func (s *Service) MintOwnerGrant(
	ctx context.Context, circleID core.CircleID,
) (Code, core.Micros, error) {
	now := s.clock.Now()
	code, err := MintGrant(ctx, s.db.Queries(), s.entropy, circleID, now, DefaultGrantTTL)
	if err != nil {
		return "", 0, err
	}
	// The prefix is loggable and is how the printed code is recognised later. The code is not.
	s.log.InfoContext(ctx, "owner grant minted",
		"circle_id", circleID.String(), "code_prefix", code.Prefix())
	return code, now + DefaultGrantTTL, nil
}

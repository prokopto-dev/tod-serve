// Package audit appends to the circle's hash-chained, append-only audit log.
//
// The table has a `BEFORE UPDATE OR DELETE … RAISE(ABORT)` trigger and `LOG001` refuses an
// `UPDATE` or `DELETE` against it anywhere in `db/queries`, so this package can only add. The hash
// chain is what makes a *removal* visible as well: every entry carries the previous entry's hash,
// so a row deleted by something that bypassed the trigger breaks the chain of everything after it.
//
// It is deliberately small. `listCircleAudit` — the read side — belongs to the milestone that
// lands the rest of the circle's read surface; writing the rows now is what makes "revocation is
// audited" a statement about the database rather than about an intention.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// Action is what happened. The vocabulary is `resource.past_tense_verb`, the same shape canonical
// §16 gives an SSE event, because the two describe the same moments from two sides.
type Action string

const (
	ActionCircleCreated       Action = "circle.created"
	ActionCircleUpdated       Action = "circle.updated"
	ActionCircleProvidersSet  Action = "circle.providers_set"
	ActionCircleDeleted       Action = "circle.deleted"
	ActionMemberJoined        Action = "member.joined"
	ActionMemberUpdated       Action = "member.updated"
	ActionMemberRevoked       Action = "member.revoked"
	ActionMemberReinstated    Action = "member.reinstated"
	ActionServiceMemberJoined Action = "member.service_created"
	ActionInviteCreated       Action = "invite.created"
	ActionInviteRevoked       Action = "invite.revoked"
	ActionInvitesRevoked      Action = "invite.revoked_in_bulk"
	ActionOwnerGrantRedeemed  Action = "owner_grant.redeemed"
	ActionTokenMinted         Action = "token.minted"
)

// The entity types an entry names. They are constants because an audit log read six months later
// is only searchable if the same thing is spelled the same way every time it happened.
const (
	EntityMembership = "membership"
	EntityInvite     = "invite"
	EntityCircle     = "circle"
)

// The detail keys that appear on more than one action, for the same reason.
const (
	DetailRole               = "role"
	DetailRevocationStrength = "revocation_strength"
)

// Entry is one thing that happened in one circle.
type Entry struct {
	CircleID core.CircleID
	// Actor is the membership that did it, and is zero for something the CLI or a redemption did
	// before any membership existed. The column is nullable for exactly that case.
	Actor      core.MembershipID
	Action     Action
	EntityType string
	EntityID   string
	// Detail is rendered to `detail_json`. It must carry no secret: an audit log is read by more
	// people than any other table here.
	Detail map[string]any
}

// Append writes one entry, chained to the circle's last one.
//
// It takes the caller's query set rather than the pool, and every caller passes a transaction's:
// an audit row written outside the transaction it describes is a row that survives a rollback,
// which is worse than no row because it is believed.
func Append(
	ctx context.Context, q *sqlitegen.Queries, ids *core.Generator, at core.Micros, e Entry,
) error {
	if e.Action == "" || e.EntityType == "" {
		return errors.New("append audit entry: action and entity type are required")
	}
	detail := e.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("append audit entry %s: %w", e.Action, err)
	}

	var prev []byte
	last, err := q.GetLatestAuditLogEntry(ctx, e.CircleID.String())
	switch {
	case err == nil:
		prev = last.Hash
	case !store.IsNotFound(err):
		return fmt.Errorf("read the audit chain head for circle %s: %w", e.CircleID, err)
	}

	id, err := core.NewID[core.AuditLog](ids, at)
	if err != nil {
		return fmt.Errorf("append audit entry %s: %w", e.Action, err)
	}
	row := sqlitegen.AppendAuditLogParams{
		ID:         id.String(),
		CircleID:   e.CircleID.String(),
		Action:     string(e.Action),
		EntityType: e.EntityType,
		DetailJson: string(detailJSON),
		PrevHash:   prev,
		CreatedAt:  int64(at),
	}
	if !e.Actor.IsZero() {
		actor := e.Actor.String()
		row.ActorMembershipID = &actor
	}
	if e.EntityID != "" {
		entity := e.EntityID
		row.EntityID = &entity
	}
	row.Hash = chainHash(row)

	if _, err := q.AppendAuditLog(ctx, row); err != nil {
		return fmt.Errorf("append audit entry %s: %w", e.Action, err)
	}
	return nil
}

// chainHash is SHA-256 over the previous hash and every field of this entry.
//
// Every field, deliberately: a chain over only the id and the timestamp would let the action, the
// actor or the detail be rewritten without breaking it, and the trigger that forbids the rewrite
// is a different mechanism guarding a different failure. Fields are length-prefixed rather than
// concatenated, so two entries whose fields differ only in where one string ends and the next
// begins cannot collide.
func chainHash(row sqlitegen.AppendAuditLogParams) []byte {
	h := sha256.New()
	write := func(b []byte) {
		// Deliberate waiver: hash.Hash.Write is documented never to return an error.
		_, _ = fmt.Fprintf(h, "%d:", len(b))
		_, _ = h.Write(b)
	}
	write(row.PrevHash)
	write([]byte(row.ID))
	write([]byte(row.CircleID))
	write([]byte(deref(row.ActorMembershipID)))
	write([]byte(row.Action))
	write([]byte(row.EntityType))
	write([]byte(deref(row.EntityID)))
	write([]byte(row.DetailJson))
	write(fmt.Appendf(nil, "%d", row.CreatedAt))
	return h.Sum(nil)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

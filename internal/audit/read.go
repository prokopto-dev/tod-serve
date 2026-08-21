package audit

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// Record is one audit entry as a client reads it.
//
// It publishes the chain: `hash` and `prev_hash` are what make a REMOVAL visible as well as an
// edit, and a reader who exports the log can verify the chain without a second endpoint. They are
// hex rather than raw bytes because the audit log is the one table an officer reads with their
// eyes.
type Record struct {
	ID       core.AuditLogID `json:"id"`
	CircleID core.CircleID   `json:"circle_id"`
	// Actor is null for something the CLI or a redemption did before any membership existed.
	Actor      *core.MembershipID `json:"actor_membership_id"`
	Action     string             `json:"action"`
	EntityType string             `json:"entity_type"`
	EntityID   string             `json:"entity_id,omitempty"`
	// Detail is the entry's structured extras. It carries no secret — an audit log is read by more
	// people than any other table here — and it is rendered rather than echoed as a string so a
	// client does not have to parse a string out of JSON.
	Detail map[string]any `json:"detail"`
	// PrevHash is empty on the first entry in a circle's chain.
	PrevHash  string      `json:"prev_hash,omitempty"`
	Hash      string      `json:"hash"`
	CreatedAt core.Micros `json:"created_at"`
}

// List returns a page of one circle's audit log, newest first, and whether another page follows.
//
// It reads one row more than asked for so `has_more` is a fact rather than a guess, and it takes
// the caller's query set so a read inside a transaction sees that transaction.
func List(
	ctx context.Context, q *sqlitegen.Queries, circleID core.CircleID, cursor string, limit int,
) ([]Record, bool, error) {
	rows, err := q.ListAuditLog(ctx, sqlitegen.ListAuditLogParams{
		CircleID: circleID.String(), AfterID: cursor, RowLimit: int64(limit) + 1,
	})
	if err != nil {
		return nil, false, fmt.Errorf("read the audit log for circle %s: %w", circleID, err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	records := make([]Record, 0, len(rows))
	for _, row := range rows {
		record, convErr := toRecord(row)
		if convErr != nil {
			return nil, false, convErr
		}
		records = append(records, record)
	}
	return records, hasMore, nil
}

func toRecord(row sqlitegen.AuditLog) (Record, error) {
	id, err := core.ParseID[core.AuditLog](row.ID)
	if err != nil {
		return Record{}, fmt.Errorf("parse audit entry id %s: %w", row.ID, err)
	}
	circleID, err := core.ParseID[core.Circle](row.CircleID)
	if err != nil {
		return Record{}, fmt.Errorf("parse audit circle id %s: %w", row.CircleID, err)
	}
	record := Record{
		ID: id, CircleID: circleID, Action: row.Action, EntityType: row.EntityType,
		EntityID: deref(row.EntityID), Detail: map[string]any{},
		PrevHash: hex.EncodeToString(row.PrevHash), Hash: hex.EncodeToString(row.Hash),
		CreatedAt: core.Micros(row.CreatedAt),
	}
	if row.ActorMembershipID != nil {
		actor, parseErr := core.ParseID[core.Membership](*row.ActorMembershipID)
		if parseErr != nil {
			return Record{}, fmt.Errorf("parse audit actor id %s: %w",
				*row.ActorMembershipID, parseErr)
		}
		record.Actor = &actor
	}
	if row.DetailJson != "" {
		if err := json.Unmarshal([]byte(row.DetailJson), &record.Detail); err != nil {
			return Record{}, fmt.Errorf("parse audit detail for entry %s: %w", row.ID, err)
		}
	}
	return record, nil
}

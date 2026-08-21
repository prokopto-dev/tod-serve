package core

import (
	"encoding/json"
	"fmt"
)

// Entity marks the table an [ID] belongs to. The method is unexported, so the set of id kinds is
// closed to this package: an id for a new table is added here, next to every other one, rather
// than appearing in whichever package happened to need it first.
type Entity interface{ entity() string }

// The entity markers. Each is an empty struct whose only job is to make ids of different tables
// different types to the compiler. They are never values in the domain.
type (
	// Circle marks a circle id — the tenant.
	Circle struct{}
	// Membership marks a membership id, which is the principal everything else is attributed to.
	Membership struct{}
	// Invite marks an invite id.
	Invite struct{}
	// InviteRedemption marks an invite_redemption id.
	InviteRedemption struct{}
	// Identity marks an identity id — a person, instance-wide.
	Identity struct{}
	// IdentityProvider marks an identity_provider id.
	IdentityProvider struct{}
	// IdentityLink marks an identity_link id.
	IdentityLink struct{}
	// InstanceGrant marks an instance_grant id — one instance-level authorization decision.
	InstanceGrant struct{}
	// AuthFlow marks an auth_flow id — one in-flight browser OAuth authorization.
	AuthFlow struct{}
	// CredentialTicket marks a credential_ticket id. It is not the ticket secret; see [Secret].
	CredentialTicket struct{}
	// TodReport marks a tod_report id, which is also the report log's cursor.
	TodReport struct{}
	// QuakeEvent marks a quake_event id.
	QuakeEvent struct{}
	// RaidTarget marks a raid_target id.
	RaidTarget struct{}
	// RaidTargetAlias marks a raid_target_alias id.
	RaidTargetAlias struct{}
	// APIToken marks an api_token id. It is not the token secret; see [Secret].
	APIToken struct{}
	// IdempotencyRecord marks an idempotency_record id. The record is keyed on
	// `(principal_membership_id, key)`; this is its own surrogate.
	IdempotencyRecord struct{}
	// AuditLog marks an audit_log id.
	AuditLog struct{}
	// EventOutbox marks an event_outbox id. Delivery ordering is `event_seq`, not this.
	EventOutbox struct{}
)

func (Circle) entity() string            { return "circle" }
func (Membership) entity() string        { return "membership" }
func (Invite) entity() string            { return "invite" }
func (InviteRedemption) entity() string  { return "invite_redemption" }
func (Identity) entity() string          { return "identity" }
func (IdentityProvider) entity() string  { return "identity_provider" }
func (IdentityLink) entity() string      { return "identity_link" }
func (InstanceGrant) entity() string     { return "instance_grant" }
func (AuthFlow) entity() string          { return "auth_flow" }
func (CredentialTicket) entity() string  { return "credential_ticket" }
func (TodReport) entity() string         { return "tod_report" }
func (QuakeEvent) entity() string        { return "quake_event" }
func (RaidTarget) entity() string        { return "raid_target" }
func (RaidTargetAlias) entity() string   { return "raid_target_alias" }
func (APIToken) entity() string          { return "api_token" }
func (IdempotencyRecord) entity() string { return "idempotency_record" }
func (AuditLog) entity() string          { return "audit_log" }
func (EventOutbox) entity() string       { return "event_outbox" }

// ID is a ULID that knows which table it belongs to. See the package comment for why ids are
// generic rather than a named string type per table.
type ID[E Entity] struct {
	// ulid is unexported so that the only routes in are [ParseID], [NewID] and UnmarshalJSON,
	// all of which validate. A convertible string type would let an unvalidated id in by
	// conversion, which is what happens when the deadline is close.
	ulid ULID
}

// The id types. Downstream code names these, never `ID[Circle]`.
type (
	CircleID            = ID[Circle]
	MembershipID        = ID[Membership]
	InviteID            = ID[Invite]
	InviteRedemptionID  = ID[InviteRedemption]
	IdentityID          = ID[Identity]
	IdentityProviderID  = ID[IdentityProvider]
	IdentityLinkID      = ID[IdentityLink]
	InstanceGrantID     = ID[InstanceGrant]
	AuthFlowID          = ID[AuthFlow]
	CredentialTicketID  = ID[CredentialTicket]
	TodReportID         = ID[TodReport]
	QuakeEventID        = ID[QuakeEvent]
	RaidTargetID        = ID[RaidTarget]
	RaidTargetAliasID   = ID[RaidTargetAlias]
	APITokenID          = ID[APIToken]
	IdempotencyRecordID = ID[IdempotencyRecord]
	AuditLogID          = ID[AuditLog]
	EventOutboxID       = ID[EventOutbox]
)

// NewID mints an id for entity E, stamped with at — see [Generator.New] for what at must be.
func NewID[E Entity](g *Generator, at Micros) (ID[E], error) {
	u, err := g.New(at)
	if err != nil {
		var e E
		return ID[E]{}, fmt.Errorf("mint %s id: %w", e.entity(), err)
	}
	return ID[E]{ulid: u}, nil
}

// ParseID reads an id from its canonical encoding. The entity appears in the error because
// "invalid ulid" on its own tells whoever is reading the log nothing about which field was wrong.
func ParseID[E Entity](s string) (ID[E], error) {
	u, err := ParseULID(s)
	if err != nil {
		var e E
		return ID[E]{}, fmt.Errorf("parse %s id: %w", e.entity(), err)
	}
	return ID[E]{ulid: u}, nil
}

// IDFromULID wraps an already-validated ULID. The store uses it after decoding a column; nothing
// on a request path should, because a request path has a string and [ParseID] is how strings get
// checked.
func IDFromULID[E Entity](u ULID) ID[E] { return ID[E]{ulid: u} }

// String renders the canonical encoding.
func (i ID[E]) String() string { return i.ulid.String() }

// ULID returns the underlying identifier, for storage and for ordering.
func (i ID[E]) ULID() ULID { return i.ulid }

// Entity returns the name of the table this id belongs to.
func (i ID[E]) Entity() string {
	var e E
	return e.entity()
}

// IsZero reports whether the id is unset.
func (i ID[E]) IsZero() bool { return i.ulid.IsZero() }

// Equal reports whether two ids are the same id. An ID is comparable with ==; this exists so that
// go-cmp can compare a whole request or response value containing one without being handed
// permission to read the unexported field.
func (i ID[E]) Equal(other ID[E]) bool { return i.ulid == other.ulid }

// Compare orders ids as their encodings sort — by minting time, then entropy. Ids are cursors.
func (i ID[E]) Compare(other ID[E]) int { return i.ulid.Compare(other.ulid) }

// MarshalJSON renders the id as a string.
func (i ID[E]) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(i.String())
	if err != nil {
		return nil, fmt.Errorf("marshal %s id: %w", i.Entity(), err)
	}
	return b, nil
}

// UnmarshalJSON reads and validates an id. An id that is not 26 characters of Crockford base32
// fails here, at the edge, rather than as a foreign-key violation three layers down.
func (i *ID[E]) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		var e E
		return fmt.Errorf("unmarshal %s id %s: %w: %w", e.entity(), b, ErrInvalidULID, err)
	}
	parsed, err := ParseID[E](s)
	if err != nil {
		return err
	}
	*i = parsed
	return nil
}

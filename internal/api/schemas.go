package api

import (
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

// ULID documents a 26-character Crockford base32 identifier.
//
// It exists because [core.ID] keeps its value in an unexported field — the only routes in are the
// validating constructors — so a schema generator reflecting over it sees an empty object. Rather
// than open the type up for the benefit of a document, the document is told what the type is.
type ULID struct{}

// Schema returns the identifier schema. Implementing this interface is what makes the alias below
// produce a string rather than an object.
func (ULID) Schema(huma.Registry) *huma.Schema {
	length := 26
	return &huma.Schema{
		Type:        huma.TypeString,
		Description: "A ULID: 26 characters of Crockford base32, lexicographically time-ordered.",
		MinLength:   &length,
		MaxLength:   &length,
		Pattern:     "^[0-9A-HJKMNP-TV-Z]{26}$",
		Examples:    []any{"01K3TGT8N9M4X0Q7R2VB6C5D1E"},
	}
}

// Timestamp documents a [core.Micros] on the wire: RFC 3339 with microsecond precision, always Z.
type Timestamp struct{}

// Schema returns the timestamp schema.
func (Timestamp) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{
		Type:        huma.TypeString,
		Format:      "date-time",
		Description: "RFC 3339 with microsecond precision, always UTC.",
		Examples:    []any{"2026-08-18T02:14:07.000000Z"},
	}
}

// ErrorCode documents the closed set of machine-readable error codes.
//
// Publishing the enum is the point: a client that branches on `code` can generate that branch, and
// a code outside the list is a code this server does not emit. The list comes from the catalogue,
// so the document cannot fall behind it.
type ErrorCode struct{}

// Schema returns the error-code schema, with every code in the catalogue.
func (ErrorCode) Schema(huma.Registry) *huma.Schema {
	values := make([]any, 0, len(apierr.Codes()))
	for _, def := range apierr.Codes() {
		values = append(values, string(def.Code))
	}
	return &huma.Schema{
		Type:        huma.TypeString,
		Description: "A stable machine-readable error code. The `type` URL's last segment is this value.",
		Enum:        values,
	}
}

// idTypes are the typed identifiers that appear on the wire.
//
// A new typed id needs a line here. Forgetting one is not silent:
// TestSpec_NoSchema_IsAnEmptyObject fails on any schema that reflection could not describe, which
// is exactly the shape an unaliased id produces.
func idTypes() []reflect.Type {
	return []reflect.Type{
		reflect.TypeFor[core.CircleID](),
		reflect.TypeFor[core.MembershipID](),
		reflect.TypeFor[core.InviteID](),
		reflect.TypeFor[core.InviteRedemptionID](),
		reflect.TypeFor[core.IdentityID](),
		reflect.TypeFor[core.IdentityProviderID](),
		reflect.TypeFor[core.IdentityLinkID](),
		reflect.TypeFor[core.TodReportID](),
		reflect.TypeFor[core.QuakeEventID](),
		reflect.TypeFor[core.RaidTargetID](),
		reflect.TypeFor[core.RaidTargetAliasID](),
		reflect.TypeFor[core.APITokenID](),
		reflect.TypeFor[core.AuditLogID](),
		reflect.TypeFor[core.EventOutboxID](),
		reflect.TypeFor[core.IdempotencyRecordID](),
	}
}

// registerSchemaAliases tells the document what the types it cannot reflect over actually are.
//
// [core.Secret] is deliberately NOT here. If a secret ever reaches a response body its schema will
// be an empty object, and TestSpec_NoSchema_IsAnEmptyObject will fail — which is a better outcome
// than a document that renders it as a perfectly ordinary string.
func registerSchemaAliases(registry huma.Registry) {
	for _, t := range idTypes() {
		registry.RegisterTypeAlias(t, reflect.TypeFor[ULID]())
	}
	registry.RegisterTypeAlias(reflect.TypeFor[core.Micros](), reflect.TypeFor[Timestamp]())
	// The problem type keeps its body in an unexported field so that the cause it carries can
	// never be marshalled. The document describes the body.
	registry.RegisterTypeAlias(
		reflect.TypeFor[apierr.Error](), reflect.TypeFor[apierr.Problem]())
	registry.RegisterTypeAlias(reflect.TypeFor[apierr.Code](), reflect.TypeFor[ErrorCode]())
}

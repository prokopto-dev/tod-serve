package apierr

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
)

// Code is a stable machine-readable error code. It is the `code` member of every problem response
// and the last segment of that response's `type` URL.
type Code string

// docsBase is the prefix of every `type` URL. The last segment is the code itself, which is what
// makes a code and its documentation page impossible to drift apart without DOC001 noticing.
const docsBase = "https://docs.tod-serve.org/errors/"

// The generic set: the failures the edge itself produces, before any domain code runs.
const (
	CodeMalformedRequest       Code = "malformed_request"
	CodeUnauthenticated        Code = "unauthenticated"
	CodeTokenInvalid           Code = "token_invalid"
	CodeTokenExpired           Code = "token_expired"
	CodeForbidden              Code = "forbidden"
	CodeInsufficientScope      Code = "insufficient_scope"
	CodeSessionRequired        Code = "session_required"
	CodeStepUpRequired         Code = "step_up_required"
	CodeNotFound               Code = "not_found"
	CodeMethodNotAllowed       Code = "method_not_allowed"
	CodeNotAcceptable          Code = "not_acceptable"
	CodeRequestTimeout         Code = "request_timeout"
	CodeConflict               Code = "conflict"
	CodePreconditionFailed     Code = "precondition_failed"
	CodePayloadTooLarge        Code = "payload_too_large"
	CodeUnsupportedMediaType   Code = "unsupported_media_type"
	CodeValidationFailed       Code = "validation_failed"
	CodePreconditionRequired   Code = "precondition_required"
	CodeIdempotencyKeyRequired Code = "idempotency_key_required"
	CodeIdempotencyKeyReused   Code = "idempotency_key_reused"
	CodeIdempotencyConflict    Code = "idempotency_conflict"
	CodeRateLimited            Code = "rate_limited"
	CodeInternalError          Code = "internal_error"
	CodeServiceUnavailable     Code = "service_unavailable"
)

// The domain set, exactly as docs/design/02-api-design.md lists it.
const (
	CodeMembershipRevoked           Code = "membership_revoked"
	CodeInviteInvalid               Code = "invite_invalid"
	CodeInviteExpired               Code = "invite_expired"
	CodeInviteExhausted             Code = "invite_exhausted"
	CodeInviteRevoked               Code = "invite_revoked"
	CodeProviderNotAccepted         Code = "provider_not_accepted"
	CodeProviderDisabled            Code = "provider_disabled"
	CodeProviderUnverifiable        Code = "provider_unverifiable"
	CodeCredentialInvalid           Code = "credential_invalid"
	CodeCredentialExpired           Code = "credential_expired"
	CodeCredentialStale             Code = "credential_stale"
	CodeIdentityProviderUnreachable Code = "identity_provider_unreachable"
	CodeAcknowledgementRequired     Code = "acknowledgement_required"
	CodeServerMismatch              Code = "server_mismatch"
	CodeDiedAtInFuture              Code = "died_at_in_future"
	CodeDiedAtTooOld                Code = "died_at_too_old"
	CodeAlreadyRetracted            Code = "already_retracted"
	CodeRetractNotPermitted         Code = "retract_not_permitted"
	CodeUnknownTarget               Code = "unknown_target"
	CodeAmbiguousTarget             Code = "ambiguous_target"
	CodeLastOwner                   Code = "last_owner"
	CodeFieldImmutable              Code = "field_immutable"
	CodeLinkRequiresVerifiableID    Code = "link_requires_verifiable_identity"
	CodeGuildMembershipRequired     Code = "guild_membership_required"
	CodeGuildRoleRequired           Code = "guild_role_required"
	CodeAuthTicketInvalid           Code = "auth_ticket_invalid"
	CodeAuthTicketExpired           Code = "auth_ticket_expired"
	CodeAuthFlowExpired             Code = "auth_flow_expired"
	CodeIdentityBlocked             Code = "identity_blocked"
	CodeCredentialAudienceMismatch  Code = "credential_audience_mismatch"
	CodeProviderScopeDeclined       Code = "provider_scope_declined"
)

// ErrUnknownCode is returned for a code outside the catalogue.
var ErrUnknownCode = errors.New("unknown error code")

// Def is one code and everything derived from it.
type Def struct {
	// Code is the code itself, and the last segment of its `type` URL.
	Code Code
	// Status is the HTTP status this code is always returned with. It is a property of the code
	// rather than of the call site: a code that could arrive as either a 403 or a 404 would be a
	// code a client cannot branch on.
	Status int
	// Title is the RFC 9457 `title` — a short summary of the problem TYPE, identical on every
	// occurrence. What is specific to one occurrence goes in `detail`.
	Title string
	// Generic marks the edge's own failures, as opposed to the domain set. The distinction is what
	// lets the status-to-code fallback consider only codes the edge is entitled to invent.
	Generic bool
}

// TypeURL returns the RFC 9457 `type` for the code.
func (d Def) TypeURL() string { return docsBase + string(d.Code) }

// String returns the code.
func (c Code) String() string { return string(c) }

// codes is the catalogue, in the order docs/design/02-api-design.md lists it: the generic set
// first, then the domain set.
//
// It is a function rather than a package-level slice for the same reason the permission catalogue
// is: a slice is mutable, and the error vocabulary is not something any package should be able to
// edit from a distance.
func codes() []Def {
	return []Def{
		{CodeMalformedRequest, http.StatusBadRequest, "Malformed request", true},
		{CodeUnauthenticated, http.StatusUnauthorized, "Not authenticated", true},
		{CodeTokenInvalid, http.StatusUnauthorized, "Token is not valid", true},
		{CodeTokenExpired, http.StatusUnauthorized, "Token has expired", true},
		{CodeForbidden, http.StatusForbidden, "Insufficient permission", true},
		{CodeInsufficientScope, http.StatusForbidden, "Insufficient token scope", true},
		{CodeSessionRequired, http.StatusForbidden, "A browser session is required", true},
		{CodeStepUpRequired, http.StatusForbidden, "Re-authentication is required", true},
		{CodeNotFound, http.StatusNotFound, "Not found", true},
		{CodeMethodNotAllowed, http.StatusMethodNotAllowed, "Method not allowed", true},
		{CodeNotAcceptable, http.StatusNotAcceptable, "Not acceptable", true},
		{CodeRequestTimeout, http.StatusRequestTimeout, "Request timed out", true},
		{CodeConflict, http.StatusConflict, "Conflict", true},
		{CodePreconditionFailed, http.StatusPreconditionFailed, "Precondition failed", true},
		{CodePayloadTooLarge, http.StatusRequestEntityTooLarge, "Payload too large", true},
		{CodeUnsupportedMediaType, http.StatusUnsupportedMediaType, "Unsupported media type", true},
		{CodeValidationFailed, http.StatusUnprocessableEntity, "Validation failed", true},
		{CodePreconditionRequired, http.StatusPreconditionRequired, "Precondition required", true},
		{CodeIdempotencyKeyRequired, http.StatusBadRequest, "Idempotency-Key is required", true},
		{CodeIdempotencyKeyReused, http.StatusUnprocessableEntity, "Idempotency-Key reused", true},
		{CodeIdempotencyConflict, http.StatusConflict, "A request with this key is in flight", true},
		{CodeRateLimited, http.StatusTooManyRequests, "Rate limited", true},
		{CodeInternalError, http.StatusInternalServerError, "Internal error", true},
		{CodeServiceUnavailable, http.StatusServiceUnavailable, "Service unavailable", true},

		{CodeMembershipRevoked, http.StatusForbidden, "Membership revoked", false},
		{CodeInviteInvalid, http.StatusNotFound, "Invite invalid", false},
		{CodeInviteExpired, http.StatusConflict, "Invite expired", false},
		{CodeInviteExhausted, http.StatusConflict, "Invite exhausted", false},
		{CodeInviteRevoked, http.StatusConflict, "Invite revoked", false},
		{CodeProviderNotAccepted, http.StatusConflict, "Provider not accepted by this circle", false},
		{CodeProviderDisabled, http.StatusConflict, "Provider disabled on this instance", false},
		{CodeProviderUnverifiable, http.StatusUnprocessableEntity, "Provider cannot verify a subject", false},
		{CodeCredentialInvalid, http.StatusUnauthorized, "Credential invalid", false},
		{CodeCredentialExpired, http.StatusUnauthorized, "Credential expired", false},
		{CodeCredentialStale, http.StatusUnauthorized, "Credential stale", false},
		{CodeIdentityProviderUnreachable, http.StatusServiceUnavailable, "Identity provider unreachable", false},
		{CodeAcknowledgementRequired, http.StatusUnprocessableEntity, "Acknowledgement required", false},
		{CodeServerMismatch, http.StatusUnprocessableEntity, "Server does not match the circle", false},
		{CodeDiedAtInFuture, http.StatusUnprocessableEntity, "died_at is in the future", false},
		{CodeDiedAtTooOld, http.StatusUnprocessableEntity, "died_at is too old", false},
		{CodeAlreadyRetracted, http.StatusConflict, "Already retracted", false},
		{CodeRetractNotPermitted, http.StatusForbidden, "Retraction not permitted", false},
		{CodeUnknownTarget, http.StatusUnprocessableEntity, "Unknown raid target", false},
		{CodeAmbiguousTarget, http.StatusUnprocessableEntity, "Ambiguous raid target", false},
		{CodeLastOwner, http.StatusConflict, "The circle would have no owner", false},
		{CodeFieldImmutable, http.StatusUnprocessableEntity, "Field is immutable", false},
		{CodeLinkRequiresVerifiableID, http.StatusUnprocessableEntity, "Link requires a verifiable identity", false},
		{CodeGuildMembershipRequired, http.StatusForbidden, "Guild membership required", false},
		{CodeGuildRoleRequired, http.StatusForbidden, "Guild role required", false},
		{CodeAuthTicketInvalid, http.StatusUnauthorized, "Auth ticket invalid", false},
		{CodeAuthTicketExpired, http.StatusUnauthorized, "Auth ticket expired", false},
		{CodeAuthFlowExpired, http.StatusConflict, "Auth flow expired", false},
		{CodeIdentityBlocked, http.StatusForbidden, "Identity blocked on this instance", false},
		{CodeCredentialAudienceMismatch, http.StatusUnauthorized, "Credential was minted for another application", false},
		{CodeProviderScopeDeclined, http.StatusForbidden, "A required provider scope was declined", false},
	}
}

// Codes returns the catalogue, in document order.
func Codes() []Def { return slices.Clone(codes()) }

// Lookup returns the definition of c.
func Lookup(c Code) (Def, bool) {
	for _, def := range codes() {
		if def.Code == c {
			return def, true
		}
	}
	return Def{}, false
}

// Parse validates a code read from a stored response or a test fixture.
func Parse(s string) (Code, error) {
	if _, ok := Lookup(Code(s)); !ok {
		return "", fmt.Errorf("parse error code %q: %w", s, ErrUnknownCode)
	}
	return Code(s), nil
}

// Status returns the HTTP status a code is always returned with, and 0 for a code outside the
// catalogue — which is a programming error the renderer turns into an internal error rather than
// a response with a status nobody can predict.
func (c Code) Status() int {
	def, ok := Lookup(c)
	if !ok {
		return 0
	}
	return def.Status
}

// TypeURL returns the RFC 9457 `type` for the code, whether or not it is in the catalogue: a
// broken link is easier to diagnose than a missing field.
func (c Code) TypeURL() string { return docsBase + string(c) }

// statusFallback maps an HTTP status onto the generic code that describes it. It exists for the
// errors the HTTP framework produces on its own — a router 404, a negotiation failure, a body over
// the size limit — which never pass through [New] and would otherwise ship with no code at all.
//
// It is deliberately partial. An unmapped status is not guessed at: [CodeForStatus] reports that it
// could not describe the failure, and the renderer says `internal_error` rather than inventing a
// code that means something else. TestCodeForStatus_EveryStatusHumaProduces_IsMapped drives the
// real framework through each of its own failure paths, so this map is checked against what the
// framework actually emits rather than against what somebody guessed it emits.
func statusFallback() map[int]Code {
	return map[int]Code{
		http.StatusBadRequest:            CodeMalformedRequest,
		http.StatusUnauthorized:          CodeUnauthenticated,
		http.StatusForbidden:             CodeForbidden,
		http.StatusNotFound:              CodeNotFound,
		http.StatusMethodNotAllowed:      CodeMethodNotAllowed,
		http.StatusNotAcceptable:         CodeNotAcceptable,
		http.StatusRequestTimeout:        CodeRequestTimeout,
		http.StatusConflict:              CodeConflict,
		http.StatusPreconditionFailed:    CodePreconditionFailed,
		http.StatusRequestEntityTooLarge: CodePayloadTooLarge,
		http.StatusUnsupportedMediaType:  CodeUnsupportedMediaType,
		http.StatusUnprocessableEntity:   CodeValidationFailed,
		http.StatusPreconditionRequired:  CodePreconditionRequired,
		http.StatusTooManyRequests:       CodeRateLimited,
		http.StatusInternalServerError:   CodeInternalError,
		http.StatusServiceUnavailable:    CodeServiceUnavailable,
	}
}

// CodeForStatus returns the generic code for an HTTP status the framework chose, and whether the
// status is one this catalogue can describe.
func CodeForStatus(status int) (Code, bool) {
	c, ok := statusFallback()[status]
	return c, ok
}

// MappedStatuses returns every status [CodeForStatus] answers for, ascending. A test asserts this
// covers what the HTTP framework can emit; without it the map could quietly stop covering a path
// the framework added.
func MappedStatuses() []int {
	out := make([]int, 0, len(statusFallback()))
	for status := range statusFallback() {
		out = append(out, status)
	}
	slices.Sort(out)
	return out
}

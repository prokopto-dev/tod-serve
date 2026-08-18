package identity

import (
	"errors"
	"fmt"
	"net/http"
)

// Code is one value of the closed error enum in docs/design/02-api-design.md. The `type` URL's
// last segment IS the code, so a code with no documentation page ships a broken link — which
// `docs-check.sh` refuses and `TestCodes_EveryCode_IsDocumented` pins from this side.
type Code string

// The codes this subsystem returns. Every one has a page under docs/errors/.
const (
	// Generic. Not in the API design's "beyond the generic set" list, and named here because the
	// credential union is validated in the service rather than purely in the schema
	// ([ADR-0007]'s stated cost), so this package is where those errors come from.
	//
	// [ADR-0007]: docs/adr/0007-one-join-endpoint.md
	CodeValidationFailed Code = "validation_failed"

	CodeCredentialInvalid          Code = "credential_invalid"
	CodeCredentialExpired          Code = "credential_expired"
	CodeCredentialStale            Code = "credential_stale"
	CodeCredentialAudienceMismatch Code = "credential_audience_mismatch"

	CodeAuthTicketInvalid Code = "auth_ticket_invalid"
	CodeAuthTicketExpired Code = "auth_ticket_expired"
	CodeAuthFlowExpired   Code = "auth_flow_expired"

	CodeProviderDisabled        Code = "provider_disabled"
	CodeProviderNotAccepted     Code = "provider_not_accepted"
	CodeProviderUnverifiable    Code = "provider_unverifiable"
	CodeProviderScopeDeclined   Code = "provider_scope_declined"
	CodeProviderUnreachable     Code = "identity_provider_unreachable"
	CodeAcknowledgementRequired Code = "acknowledgement_required"

	CodeGuildMembershipRequired Code = "guild_membership_required"
	CodeGuildRoleRequired       Code = "guild_role_required"
	CodeIdentityBlocked         Code = "identity_blocked"

	CodeInviteInvalid   Code = "invite_invalid"
	CodeInviteExpired   Code = "invite_expired"
	CodeInviteRevoked   Code = "invite_revoked"
	CodeInviteExhausted Code = "invite_exhausted"

	CodeLinkRequiresVerifiableIdentity Code = "link_requires_verifiable_identity"
)

// Codes returns every code this package can produce, in declaration order.
//
// It builds the slice on each call rather than sharing one: a package-level slice is mutable, and
// a caller that sorted or appended to it in place would change what every later caller sees.
func Codes() []Code {
	return []Code{
		CodeValidationFailed,
		CodeCredentialInvalid, CodeCredentialExpired, CodeCredentialStale, CodeCredentialAudienceMismatch,
		CodeAuthTicketInvalid, CodeAuthTicketExpired, CodeAuthFlowExpired,
		CodeProviderDisabled, CodeProviderNotAccepted, CodeProviderUnverifiable,
		CodeProviderScopeDeclined, CodeProviderUnreachable, CodeAcknowledgementRequired,
		CodeGuildMembershipRequired, CodeGuildRoleRequired, CodeIdentityBlocked,
		CodeInviteInvalid, CodeInviteExpired, CodeInviteRevoked, CodeInviteExhausted,
		CodeLinkRequiresVerifiableIdentity,
	}
}

// Status returns the HTTP status the API design pairs with this code.
//
// A switch rather than a map, because a package-level map is mutable state and this is a closed
// fact about the wire contract. `TestCodes_StatusesMatchTheAPIDesign` reads the document's own
// fenced block and compares, so the two copies cannot drift.
func (c Code) Status() int {
	switch c {
	case CodeCredentialInvalid, CodeCredentialExpired, CodeCredentialStale,
		CodeCredentialAudienceMismatch, CodeAuthTicketInvalid, CodeAuthTicketExpired:
		return http.StatusUnauthorized
	case CodeProviderScopeDeclined, CodeGuildMembershipRequired,
		CodeGuildRoleRequired, CodeIdentityBlocked:
		return http.StatusForbidden
	case CodeInviteInvalid:
		return http.StatusNotFound
	case CodeAuthFlowExpired, CodeProviderDisabled, CodeProviderNotAccepted,
		CodeInviteExpired, CodeInviteRevoked, CodeInviteExhausted:
		return http.StatusConflict
	case CodeValidationFailed, CodeProviderUnverifiable, CodeAcknowledgementRequired,
		CodeLinkRequiresVerifiableIdentity:
		return http.StatusUnprocessableEntity
	case CodeProviderUnreachable:
		return http.StatusServiceUnavailable
	default:
		// An unknown code is a programming error, and 500 is the honest answer to one. Guessing
		// 400 would present a bug as the caller's fault.
		return http.StatusInternalServerError
	}
}

// Error is a failure with a wire code. internal/api turns it into RFC 9457 problem+json; this
// package never renders one, because rendering is the edge's job and a service that formats HTTP
// is a service that cannot be called from a job.
type Error struct {
	Code Code
	// Message is for a human. It never carries a secret, a token, or text an untrusted party
	// supplied — those reach logs.
	Message string
	// Location points into the request body for a validation failure —
	// `body.credential.token` — and is empty otherwise. [ADR-0007] accepts that part of the
	// union's validation lives in the service rather than the schema; this is what keeps the
	// resulting error specific rather than "body invalid".
	//
	// [ADR-0007]: docs/adr/0007-one-join-endpoint.md
	Location string
	cause    error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the provider's own error, so a caller that wants to know whether Discord was
// unreachable rather than merely that the credential failed can ask with errors.Is.
func (e *Error) Unwrap() error { return e.cause }

// Status is the HTTP status for this error.
func (e *Error) Status() int { return e.Code.Status() }

// NewError builds a coded error.
func NewError(code Code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, cause: cause}
}

// NewValidationError builds a `validation_failed` pointing at one field of the request body.
func NewValidationError(location, message string) *Error {
	return &Error{Code: CodeValidationFailed, Message: message, Location: location}
}

// CodeOf reports the wire code of err, and whether it had one. A caller that has no coded error
// is looking at a bug or an infrastructure failure, and either way the answer is a 500 rather
// than a guess.
func CodeOf(err error) (Code, bool) {
	var coded *Error
	if errors.As(err, &coded) {
		return coded.Code, true
	}
	return "", false
}

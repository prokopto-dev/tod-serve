package apierr

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

// ContentType is the media type every problem response carries — RFC 9457, not `application/json`,
// so a proxy or a generic client can tell a problem from a representation without parsing it.
const ContentType = "application/problem+json"

// Field is one entry in a problem's `errors[]`: which part of the request was wrong, and why.
//
// It carries no copy of the offending VALUE, deliberately. A validation failure on
// `body.credential.token` would otherwise echo a bearer credential back into a response body, an
// access log and whatever the client prints — and the field that most often fails validation is
// exactly the field that must never be rendered.
type Field struct {
	// Location is a dotted path into the request: `body.credential.token`, `query.limit`,
	// `header.If-Match`.
	Location string `json:"location" doc:"Dotted path to the part of the request that was rejected"`
	// Message says what was wrong with it.
	Message string `json:"message" doc:"What was wrong with it"`
}

// Meta carries the structured extras a particular problem needs. It is a struct rather than a map
// because a `map[string]any` in a response is a shape no client can generate against and no test
// can typecheck.
type Meta struct {
	// RequestID correlates the response with the instance's log line. It is the only thing that
	// finds the log for an internal error, whose detail is deliberately empty.
	RequestID string `json:"request_id,omitempty" doc:"Correlates this response with the instance log"`
	// Current is the resource's current representation, sent with a 412 so the read-merge-retry
	// round trip costs no extra request.
	Current json.RawMessage `json:"current,omitempty" doc:"The resource as it is now, on a 412"`
	// Candidates are the raid targets a name resolved to ambiguously.
	Candidates json.RawMessage `json:"candidates,omitempty" doc:"Ambiguous resolve candidates"`
	// RetryAfterSeconds mirrors the `Retry-After` header in the body, for clients that read one
	// and not the other.
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty" doc:"Seconds to wait before retrying"`
	// StepUpWindowSeconds is how recently a session must have re-authenticated.
	StepUpWindowSeconds int `json:"step_up_window_seconds,omitempty" doc:"Required re-authentication recency"`
	// CappedBy names what narrowed a request's values below what it asked for — `pat` for an
	// invite minted by a token. Never hide a row silently: if a value was clamped, say so.
	CappedBy string `json:"capped_by,omitempty" doc:"What narrowed the request below what it asked for"`
}

// Problem is the RFC 9457 body. `code` is the extension member every client branches on; `type`
// resolves to the documentation page for that code and its last segment IS the code.
type Problem struct {
	Type     string  `json:"type" doc:"Documentation for this error code; the last segment is the code"`
	Title    string  `json:"title" doc:"Short summary of the problem type, identical on every occurrence"`
	Status   int     `json:"status" doc:"HTTP status code"`
	Code     Code    `json:"code" doc:"Stable machine-readable code from a closed enum"`
	Detail   string  `json:"detail,omitempty" doc:"Explanation specific to this occurrence"`
	Instance string  `json:"instance,omitempty" doc:"The request this problem is about"`
	Errors   []Field `json:"errors,omitempty" doc:"Per-field detail, where the failure has any"`
	Meta     *Meta   `json:"meta,omitempty" doc:"Structured extras this problem needs"`
}

// Error is a problem in flight: the response body, plus the cause, which is logged and never sent.
//
// It satisfies the HTTP framework's status-carrying error interface, so a handler returns one and
// the edge renders it — there is no translation switch between a service's error and its status,
// because a switch is the thing nobody can enumerate.
type Error struct {
	problem Problem
	cause   error
}

// New returns a problem with the given code and detail.
//
// An unknown code is not silently rendered: it becomes [CodeInternalError] with the offending code
// recorded as the cause, because a response whose `type` URL 404s teaches the reader nothing.
func New(code Code, detail string) *Error {
	def, ok := Lookup(code)
	if !ok {
		return &Error{
			problem: problemFor(CodeInternalError, ""),
			cause:   fmt.Errorf("render problem %q: %w", code, ErrUnknownCode),
		}
	}
	p := problemFor(def.Code, detail)
	return &Error{problem: p}
}

// Newf returns a problem whose detail is formatted. It takes the same shape as [fmt.Errorf]
// because that is the shape every caller already knows.
func Newf(code Code, format string, a ...any) *Error {
	return New(code, fmt.Sprintf(format, a...))
}

// Wrap returns a problem carrying a cause. The cause is for the log; it never reaches the client,
// because the detail an unexpected failure carries is exactly the detail that leaks internals.
func Wrap(code Code, cause error, detail string) *Error {
	e := New(code, detail)
	if e.cause == nil {
		e.cause = cause
		return e
	}
	e.cause = errors.Join(e.cause, cause)
	return e
}

func problemFor(code Code, detail string) Problem {
	def, _ := Lookup(code)
	return Problem{
		Type:   def.TypeURL(),
		Title:  def.Title,
		Status: def.Status,
		Code:   def.Code,
		Detail: detail,
	}
}

// WithField appends a per-field failure. Chainable, so a validator reads as a list.
func (e *Error) WithField(location, message string) *Error {
	e.problem.Errors = append(e.problem.Errors, Field{Location: location, Message: message})
	return e
}

// WithFields appends several per-field failures at once.
func (e *Error) WithFields(fields ...Field) *Error {
	e.problem.Errors = append(e.problem.Errors, fields...)
	return e
}

// WithInstance records which request this problem is about.
func (e *Error) WithInstance(instance string) *Error {
	e.problem.Instance = instance
	return e
}

// WithRequestID records the log correlation id.
func (e *Error) WithRequestID(id string) *Error {
	e.meta().RequestID = id
	return e
}

// WithCurrent attaches the resource's current representation, which is what a `412` owes its
// caller: without it the client has to guess whether to re-read or give up.
func (e *Error) WithCurrent(raw json.RawMessage) *Error {
	e.meta().Current = raw
	return e
}

// WithCandidates attaches the ambiguous resolve candidates.
func (e *Error) WithCandidates(raw json.RawMessage) *Error {
	e.meta().Candidates = raw
	return e
}

// WithRetryAfter records how long to wait, in both the body and the `Retry-After` header.
func (e *Error) WithRetryAfter(seconds int) *Error {
	e.meta().RetryAfterSeconds = seconds
	return e
}

// WithStepUpWindow records how recently a session must have re-authenticated.
func (e *Error) WithStepUpWindow(seconds int) *Error {
	e.meta().StepUpWindowSeconds = seconds
	return e
}

// WithCappedBy records what narrowed the request below what it asked for.
func (e *Error) WithCappedBy(what string) *Error {
	e.meta().CappedBy = what
	return e
}

func (e *Error) meta() *Meta {
	if e.problem.Meta == nil {
		e.problem.Meta = &Meta{}
	}
	return e.problem.Meta
}

// Code returns the machine-readable code, which is what a caller branches on.
func (e *Error) Code() Code { return e.problem.Code }

// Problem returns the response body.
func (e *Error) Problem() Problem { return e.problem }

// GetStatus returns the HTTP status. The HTTP framework calls this to set the response code.
func (e *Error) GetStatus() int { return e.problem.Status }

// GetHeaders returns the headers this problem needs on the response. `Retry-After` is a header a
// client library honours automatically and a body field it would have to be taught to read, so a
// rate limit sends both.
func (e *Error) GetHeaders() http.Header {
	if e.problem.Meta == nil || e.problem.Meta.RetryAfterSeconds == 0 {
		return nil
	}
	return http.Header{"Retry-After": []string{strconv.Itoa(e.problem.Meta.RetryAfterSeconds)}}
}

// ContentType switches a JSON response to `application/problem+json`. The HTTP framework calls it
// on the response body, which is how the media type follows the error rather than the route.
func (e *Error) ContentType(ct string) string {
	if ct == "application/json" {
		return ContentType
	}
	return ct
}

// Error renders the problem for a log line. The cause is included here and never in the body.
func (e *Error) Error() string {
	msg := string(e.problem.Code)
	if e.problem.Detail != "" {
		msg += ": " + e.problem.Detail
	}
	if e.cause != nil {
		msg += ": " + e.cause.Error()
	}
	return msg
}

// Unwrap returns the cause, so errors.Is and errors.As reach through a problem to what produced it.
func (e *Error) Unwrap() error { return e.cause }

// MarshalJSON renders the RFC 9457 body. The cause is unexported and has no tag, so there is no
// path by which it reaches a client.
func (e *Error) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(e.problem)
	if err != nil {
		return nil, fmt.Errorf("marshal problem %s: %w", e.problem.Code, err)
	}
	return b, nil
}

// From extracts a problem from an error chain.
func From(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// HasCode reports whether the error chain carries a problem with the given code. It is the
// comparison callers want: an error is identified by its code, never by pointer equality.
func HasCode(err error, code Code) bool {
	e, ok := From(err)
	return ok && e.Code() == code
}

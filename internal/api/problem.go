package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
)

// RequestIDHeader carries the log correlation id back to the caller. It is the only thing that
// finds the log line behind an `internal_error`, whose detail is deliberately empty.
const RequestIDHeader = "X-Request-Id"

type requestIDKey struct{}

// WithRequestID returns a context carrying the request id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFrom returns the request id, or the empty string outside a request.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// init makes every error the HTTP framework produces on its own an RFC 9457 problem with a code
// from the closed enum.
//
// It assigns two package-level variables in a dependency, which is exactly the shared mutable state
// AGENTS.md bans — and there is no other seam. The framework builds its own errors for a router
// 404, a negotiation failure and a body over the size limit, long before any code here runs, and
// those are precisely the responses a client is most likely to meet first. Doing it in init rather
// than in a constructor is deliberate: a constructor would leave a window in which the process
// answered in two different vocabularies depending on wiring order.
//
// TestProblem_FrameworkErrors_AreRFC9457 drives the real framework through each of its own failure
// paths and asserts the result, so this is checked rather than asserted.
func init() {
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		return newProblem(nil, status, msg, errs...)
	}
	huma.NewErrorWithContext = func(
		ctx huma.Context, status int, msg string, errs ...error,
	) huma.StatusError {
		return newProblem(ctx, status, msg, errs...)
	}
}

// newProblem turns whatever the framework was about to say into a problem.
//
// An *apierr.Error anywhere in errs wins: it already carries a code chosen by the code that knows
// what went wrong, and flattening it to a status-derived generic would throw that away.
func newProblem(ctx huma.Context, status int, msg string, errs ...error) huma.StatusError {
	for _, err := range errs {
		if e, ok := apierr.From(err); ok {
			return stampRequestID(ctx, e)
		}
	}

	code, ok := apierr.CodeForStatus(status)
	if !ok {
		// A status this catalogue cannot describe is reported as what it is — a failure we have no
		// vocabulary for — rather than given the nearest code, which would be a confident mistake.
		return stampRequestID(ctx, apierr.Wrap(apierr.CodeInternalError,
			errors.New("no error code describes HTTP status "+http.StatusText(status)), ""))
	}

	problem := apierr.New(code, detailFor(code, msg))
	for _, err := range errs {
		if err == nil {
			continue
		}
		problem = problem.WithField(locationOf(err), messageOf(err))
	}
	return stampRequestID(ctx, problem)
}

// detailFor decides what reaches the client. A 5xx detail is dropped: the detail an unexpected
// failure carries is exactly the detail that leaks internals, and the cause is in the log.
func detailFor(code apierr.Code, msg string) string {
	if def, ok := apierr.Lookup(code); ok && def.Status >= http.StatusInternalServerError {
		return ""
	}
	return msg
}

// locationOf reads the request path out of a framework validation error, so `errors[].location`
// says `body.credential.token` rather than repeating the message.
func locationOf(err error) string {
	var detailer huma.ErrorDetailer
	if errors.As(err, &detailer) {
		if d := detailer.ErrorDetail(); d != nil && d.Location != "" {
			return d.Location
		}
	}
	return ""
}

// messageOf reads the human half of a framework validation error.
//
// The offending VALUE is deliberately not carried across. The framework offers it, and the field
// that most often fails validation is a credential — echoing it into a response body, an access log
// and whatever the client prints is not a trade worth making for a better error message.
func messageOf(err error) string {
	var detailer huma.ErrorDetailer
	if errors.As(err, &detailer) {
		if d := detailer.ErrorDetail(); d != nil && d.Message != "" {
			return d.Message
		}
	}
	return err.Error()
}

func stampRequestID(ctx huma.Context, e *apierr.Error) *apierr.Error {
	if ctx == nil {
		return e
	}
	if id := RequestIDFrom(ctx.Context()); id != "" {
		return e.WithRequestID(id)
	}
	return e
}

// writeProblem sends an error as a problem response and stops the request.
//
// It goes through the framework's own writer so that content negotiation, the status and the
// `Retry-After` header are handled in one place rather than three.
func (b *Builder) writeProblem(ctx huma.Context, err error) {
	e, ok := apierr.From(err)
	if !ok {
		e = apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	if id := RequestIDFrom(ctx.Context()); id != "" {
		e = e.WithRequestID(id)
	}
	if e.Code() == apierr.CodeInternalError {
		b.cfg.Log.ErrorContext(ctx.Context(), "request failed",
			"error", err, "operation", ctx.Operation().OperationID)
	}
	// The headers a problem carries are applied here rather than left to the framework: it applies
	// them on the handler path only, and a `Retry-After` that appears on some rate-limit responses
	// and not others is a rate limit clients hammer.
	for name, values := range e.GetHeaders() {
		for _, v := range values {
			ctx.AppendHeader(name, v)
		}
	}
	// Deliberate waiver: WriteErr already reports its own failure to stderr, and there is no
	// second response to send when writing the first one failed.
	_ = huma.WriteErr(b.api, ctx, e.GetStatus(), e.Error(), e)
}

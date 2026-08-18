package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/danielgtaylor/huma/v2"

	"github.com/prokopto-dev/tod-serve/internal/authz"
)

// ErrUnknownOperation is returned when a handler is registered for an operation id that is not in
// the registry. It is the mechanism behind "a route cannot be invented": there is no path from a
// handler to a served path that does not go through [Lookup].
var ErrUnknownOperation = errors.New("operation is not in the route registry")

// ErrAlreadyRegistered is returned when one operation id is registered twice. Two handlers on one
// operation is not a merge; it is a bug that would silently keep whichever ran last.
var ErrAlreadyRegistered = errors.New("operation is already registered")

// The security scheme names, as they appear in the OpenAPI document. They are constants because
// they are part of the published contract: a generated SDK names them.
const (
	// SchemeBearer is `Authorization: Bearer tods_pat_…`. The only token transport there is.
	SchemeBearer = "patBearer"
	// SchemeSession is the `__Host-tod_session` cookie.
	SchemeSession = "sessionCookie"
	// SchemeMetricsToken is `TOD_METRICS_TOKEN` on the separate metrics listener. It is never a
	// PAT scope and never appears on an API operation.
	SchemeMetricsToken = "metricsToken"
)

// The OpenAPI extensions each operation carries. Everything the route registry knows reaches the
// document, so a client, an SDK generator and a reviewer all read the same facts the middleware
// enforces — and TestSpec_Extensions_MatchTheRouteRegistry asserts they still agree.
const (
	ExtPermission     = "x-tod-permission"
	ExtScopes         = "x-tod-scopes"
	ExtAnyScope       = "x-tod-any-scope"
	ExtSessionOnly    = "x-tod-session-only"
	ExtCircleScoped   = "x-tod-circle-scoped"
	ExtCreatesState   = "x-tod-creates-state"
	ExtIdempotency    = "x-tod-idempotency"
	ExtIfMatch        = "x-tod-if-match-required"
	ExtETag           = "x-tod-etag"
	ExtOperationalURL = "x-tod-unversioned"
)

// PermissionExtension is the machine-readable half of what an operation requires. It is a struct
// rather than a map because a generated document nobody can typecheck is a document that drifts.
type PermissionExtension struct {
	// Kind is the auth kind: `public`, `self`, `permission` or `metrics_token`.
	Kind string `json:"kind"`
	// AnyOf are the catalogue permissions, ANY of which reaches the operation. Empty for every
	// kind except `permission`.
	AnyOf []authz.OpenAPIPermission `json:"any_of,omitempty"`
	// RequiresStepUp marks a capability-floor operation: session only, recently re-authenticated,
	// and reachable by no token at any scope.
	RequiresStepUp bool `json:"requires_step_up"`
}

// Register attaches a handler to the operation named by id.
//
// It is the ONLY way to serve a route. The method, path, security, extensions and middleware all
// come from the registry row, so a handler cannot disagree with the document about what it is:
// there is no method parameter to get wrong and no path string to typo.
//
// It is a function rather than a method because Go has no generic methods, and the input and
// output types have to be generic for the framework to derive schemas from them.
func Register[I, O any](
	b *Builder, id OperationID, handler func(context.Context, *I) (*O, error),
) error {
	if b == nil {
		return errors.New("register: builder is nil")
	}
	route, err := MustLookup(id)
	if err != nil {
		return err
	}
	if b.registered[id] {
		return fmt.Errorf("register %q: %w", id, ErrAlreadyRegistered)
	}

	op := b.operation(route)
	huma.Register(b.api, op, handler)
	b.registered[id] = true
	b.order = append(b.order, id)
	return nil
}

// operation renders a registry row as an operation the framework can serve and document.
func (b *Builder) operation(r Route) huma.Operation {
	op := huma.Operation{
		OperationID: string(r.ID),
		Method:      r.Method,
		Path:        r.FullPath(),
		Summary:     r.Summary,
		Hidden:      r.Hidden,
		Security:    securityFor(r),
		Middlewares: huma.Middlewares{b.routeMiddleware(r)},
		Extensions:  extensionsFor(r),
		Errors:      errorStatusesFor(r),
	}
	return op
}

// securityFor renders the security requirement for a route. It is where "no token reaches a
// capability-floor operation" becomes a published fact rather than only a runtime check: a floor
// operation offers the session scheme and does not offer the bearer scheme at all.
func securityFor(r Route) []map[string][]string {
	switch r.Auth {
	case AuthPublic:
		// One empty requirement object is OpenAPI for "security is optional here". An omitted
		// `security` would inherit the document-level default, which is not the same statement.
		return []map[string][]string{{}}
	case AuthMetricsToken:
		return []map[string][]string{{SchemeMetricsToken: {}}}
	case AuthSelf, AuthPermission:
		if r.SessionOnly() {
			return []map[string][]string{{SchemeSession: {}}}
		}
		scopes := make([]string, 0, len(r.Scopes))
		for _, s := range r.Scopes {
			scopes = append(scopes, string(s))
		}
		return []map[string][]string{
			{SchemeBearer: scopes},
			{SchemeSession: {}},
		}
	default:
		// An unknown auth kind offers nothing. Failing open here would publish a route as public
		// because somebody added an enum value and forgot this switch.
		return []map[string][]string{{SchemeSession: {}}}
	}
}

// extensionsFor renders everything the registry knows about a route into the document.
func extensionsFor(r Route) map[string]any {
	perms := make([]authz.OpenAPIPermission, 0, len(r.Permissions))
	for _, p := range r.Permissions {
		for _, def := range authz.OpenAPIPermissions() {
			if def.Key == string(p) {
				perms = append(perms, def)
			}
		}
	}
	scopes := make([]string, 0, len(r.Scopes))
	for _, s := range r.Scopes {
		scopes = append(scopes, string(s))
	}

	ext := map[string]any{
		ExtPermission: PermissionExtension{
			Kind:           string(r.Auth),
			AnyOf:          perms,
			RequiresStepUp: r.RequiresStepUp(),
		},
		ExtScopes:       scopes,
		ExtAnyScope:     r.AnyScope,
		ExtSessionOnly:  r.SessionOnly(),
		ExtCircleScoped: r.CircleScoped,
		ExtCreatesState: r.CreatesState,
		ExtIdempotency:  string(r.Idempotency),
		ExtIfMatch:      r.IfMatch,
		ExtETag:         r.ETag,
	}
	if !r.Versioned {
		ext[ExtOperationalURL] = true
	}
	return ext
}

// errorStatusesFor lists the problem responses an operation documents. Every operation can answer
// with the edge's own failures; the route's shape adds the rest.
//
// A `403` is listed only where one is reachable. On a circle-scoped route the wrong tenant is a
// `404`, so documenting a `403` for it would publish exactly the confusion canonical §7 removes.
func errorStatusesFor(r Route) []int {
	statuses := []int{400, 422, 500}
	if r.Authenticated() {
		statuses = append(statuses, 401, 403)
	}
	if r.CircleScoped || len(r.PathParams()) > 0 {
		statuses = append(statuses, 404)
	}
	if r.IfMatch {
		statuses = append(statuses, 412, 428)
	}
	if r.RequiresIdempotencyKey() {
		statuses = append(statuses, 409)
	}
	return statuses
}

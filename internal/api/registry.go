package api

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/prokopto-dev/tod-serve/internal/authz"
)

// BasePath prefixes every API operation. Within v1 the surface is additive only.
const BasePath = "/api/v1"

// CirclePathParam is the path parameter that names the tenant. Every circle-scoped route contains
// it, spelled exactly this way, and TestRouteRegistry_CircleScoped_MatchesThePath asserts the
// declared flag and the path agree — a route whose path says `{circle_id}` and whose registry row
// says it is not circle-scoped would skip the tenancy middleware entirely.
const CirclePathParam = "{circle_id}"

// OperationID is an operation's stable identifier. It is `lowerCamelCase`, explicit on every
// operation, and **never renamed**: generated SDK method names come from it, so a rename breaks
// clients even when the HTTP surface is unchanged. `oasdiff` fails a rename as a breaking change.
type OperationID string

// String returns the operation id.
func (o OperationID) String() string { return string(o) }

// The operation ids, in the order docs/design/02-api-design.md lists them.
const (
	OpGetServerMeta          OperationID = "getServerMeta"
	OpGetSetupState          OperationID = "getSetupState"
	OpRunSetup               OperationID = "runSetup"
	OpListIdentityProviders  OperationID = "listIdentityProviders"
	OpCreateAuthorizationURL OperationID = "createAuthorizationURL"
	OpCompleteAuthorization  OperationID = "completeAuthorization"
	OpGetCurrentPrincipal    OperationID = "getCurrentPrincipal"
	OpPreviewInvite          OperationID = "previewInvite"
	OpRedeemInvite           OperationID = "redeemInvite"
	OpAuthenticateIdentity   OperationID = "authenticateIdentity"
	OpListMyTokens           OperationID = "listMyTokens"
	OpRevokeToken            OperationID = "revokeToken"

	OpListCircles        OperationID = "listCircles"
	OpCreateCircle       OperationID = "createCircle"
	OpGetCircle          OperationID = "getCircle"
	OpUpdateCircle       OperationID = "updateCircle"
	OpSetCircleProviders OperationID = "setCircleProviders"
	OpDeleteCircle       OperationID = "deleteCircle"

	OpListMembers         OperationID = "listMembers"
	OpGetMember           OperationID = "getMember"
	OpUpdateMember        OperationID = "updateMember"
	OpRevokeMember        OperationID = "revokeMember"
	OpReinstateMember     OperationID = "reinstateMember"
	OpCreateServiceMember OperationID = "createServiceMember"
	OpListInvites         OperationID = "listInvites"
	OpCreateInvite        OperationID = "createInvite"
	OpRevokeInvite        OperationID = "revokeInvite"

	OpCreateTodReport      OperationID = "createTodReport"
	OpListTodReports       OperationID = "listTodReports"
	OpGetTodReport         OperationID = "getTodReport"
	OpRetractTodReport     OperationID = "retractTodReport"
	OpListTargetStates     OperationID = "listTargetStates"
	OpGetTargetState       OperationID = "getTargetState"
	OpReportQuake          OperationID = "reportQuake"
	OpListQuakes           OperationID = "listQuakes"
	OpSubscribeCircleEvent OperationID = "subscribeCircleEvents"
	OpReplayCircleEvents   OperationID = "replayCircleEvents"
	OpListCircleAudit      OperationID = "listCircleAudit"

	OpListRaidTargets           OperationID = "listRaidTargets"
	OpGetRaidTarget             OperationID = "getRaidTarget"
	OpResolveRaidTarget         OperationID = "resolveRaidTarget"
	OpCreateRaidTarget          OperationID = "createRaidTarget"
	OpUpdateRaidTarget          OperationID = "updateRaidTarget"
	OpPutRaidTargetTimer        OperationID = "putRaidTargetTimer"
	OpListCircleTimerOverrides  OperationID = "listCircleTimerOverrides"
	OpPutCircleTimerOverride    OperationID = "putCircleTimerOverride"
	OpDeleteCircleTimerOverride OperationID = "deleteCircleTimerOverride"

	OpListAdminIdentityProviders OperationID = "listAdminIdentityProviders"
	OpCreateIdentityProvider     OperationID = "createIdentityProvider"
	OpUpdateIdentityProvider     OperationID = "updateIdentityProvider"
	OpDeleteIdentityProvider     OperationID = "deleteIdentityProvider"
	OpGetDoctorReport            OperationID = "getDoctorReport"
	OpListJobs                   OperationID = "listJobs"
	OpGetLiveness                OperationID = "getLiveness"
	OpGetReadiness               OperationID = "getReadiness"
	OpGetMetrics                 OperationID = "getMetrics"
)

// Auth says what may authenticate a caller for an operation. It is the `Permission` column of the
// API design's tables, read as a kind rather than as a string.
type Auth string

const (
	// AuthPublic is reachable with no credential at all.
	AuthPublic Auth = "public"
	// AuthSelf is any authenticated principal acting on itself: no permission is consulted,
	// because the resource IS the caller.
	AuthSelf Auth = "self"
	// AuthPermission requires one of the route's permissions, from the authz catalogue.
	AuthPermission Auth = "permission"
	// AuthMetricsToken is `TOD_METRICS_TOKEN` on the separate metrics listener. It is never gated
	// by a PAT scope and never reaches the authz catalogue at all — canonical §13.
	AuthMetricsToken Auth = "metrics_token"
	// AuthSetupToken is `TOD_SETUP_TOKEN`, and it reaches first-run setup and nothing else.
	//
	// It is a kind here rather than a check inside a handler for the same reason every other kind
	// is: the middleware resolves it before any handler runs, the OpenAPI document publishes a
	// scheme for it rather than calling the operation public, and [SetupRoutes] is what the three
	// refusal tests walk — so a second setup route cannot be added uncovered. ADR-0016.
	//
	// It authenticates NO PRINCIPAL. There is nobody to be on a fresh database, which is the
	// whole problem first-run setup exists to solve, so a route carrying this kind reaches no
	// permission, no circle and no membership.
	AuthSetupToken Auth = "setup_token"
)

// Idempotency says who replays a retry of a state-creating POST.
//
// It is an enum rather than a boolean because the two cases have genuinely different mechanisms,
// and flattening them would have hidden that `redeemInvite` cannot use the shared table: uniqueness
// is `(principal_id, key)` where principal is the MEMBERSHIP, and a join is the request that
// creates the membership.
type Idempotency string

const (
	// IdempotencyNone marks an operation that creates no domain state. The header is not required
	// and sending one changes nothing.
	IdempotencyNone Idempotency = ""
	// IdempotencyMembership is the ordinary case: `(membership, key)` in `idempotency_record`,
	// replayed by the middleware before the handler runs. Keyed on the membership rather than the
	// token, so a rotation mid-retry still replays.
	IdempotencyMembership Idempotency = "membership"
	// IdempotencyHandler marks an operation that creates domain state before any membership
	// principal exists — `redeemInvite` and `authenticateIdentity`, and the instance-realm creates
	// whose principal is a session with no membership at all. The header is still REQUIRED, and
	// the middleware makes it available to the handler, which owns the replay because only it
	// knows what to key on. `idempotency_record.principal_membership_id` is NOT NULL, so this is a
	// property of the schema rather than a preference.
	IdempotencyHandler Idempotency = "handler"
)

// Route is one operation, as data. Everything the architectural tests need to walk the API surface
// is a field here, because a rule that has to read a handler's body is a rule with no gate.
type Route struct {
	// ID is the operation id, and the registry's key.
	ID OperationID
	// Method is the HTTP method.
	Method string
	// Path is the path exactly as docs/design/02-api-design.md writes it, without the base path.
	Path string
	// Versioned says whether the route is served under [BasePath]. Only the operational endpoints
	// — `/healthz`, `/readyz`, `/metrics` — are not: a container health check and a scrape config
	// are configured once and must not need editing when the API version moves.
	Versioned bool
	// Auth is what may authenticate a caller.
	Auth Auth
	// Permissions are the permissions that reach the operation, ANY of which suffices. More than
	// one appears exactly where the API design writes `a / b`: `retractTodReport` is reachable by
	// `tod.retract` for one's own report and `tod.retract.any` for somebody else's.
	Permissions []authz.Permission
	// Scopes are the PAT scopes that reach the operation. Empty, with AnyScope false, means no
	// token reaches it at any scope — the capability floor, or a `self` operation that alters
	// authentication state.
	Scopes []authz.Scope
	// AnyScope marks the API design's `any`: the operation is about the caller's own principal, so
	// any live token reaches it. `getCurrentPrincipal` must answer for a token with no scopes at
	// all, or a client cannot discover that it has none.
	AnyScope bool
	// CircleScoped says the operation addresses one circle's resources, and therefore that a
	// principal of another circle must get 404 rather than 403.
	CircleScoped bool
	// CreatesState says the operation appends to the domain — a report, a membership, an invite,
	// a circle. It is what makes `Idempotency-Key` required.
	CreatesState bool
	// Idempotency says who replays a retry. Non-empty exactly when CreatesState is true.
	Idempotency Idempotency
	// ETag says the operation returns an entity tag, so a client can revalidate with
	// `If-None-Match` and a writer can quote it back in `If-Match`.
	ETag bool
	// IfMatch says `If-Match` is REQUIRED: the operation overwrites state a previous read
	// supplied, so a request without one is refused with 428 rather than silently racing.
	IfMatch bool
	// InviteOracle marks a public route that reveals whether an invite code is live.
	//
	// Every such route is metered from ONE shared bucket keyed on the caller — never a bucket each,
	// which would simply hand a code-guesser twice the guessing budget. The flag is what joins the
	// bucket, so adding a third route that accepts a code is a one-word decision rather than a
	// second limiter somebody has to remember to wire.
	InviteOracle bool
	// ConditionalRead marks a read that actually PERFORMS revalidation: it compares the caller's
	// `If-None-Match` against the tag it would have returned and answers `304` with no body.
	//
	// It is separate from ETag, and the gap between them is deliberate rather than redundant.
	// `ETag: true` says the operation returns a tag, which is what an `If-Match` writer needs.
	// Revalidating is a second thing a handler has to actually do, and a route that advertises
	// `If-None-Match` without doing it is worse than one that offers neither: a client that
	// implements conditional requests against it pays for a full body on every poll while
	// believing it does not.
	//
	// The flag drives the documented `304` — huma cannot infer one from a dynamic `Status` field,
	// and a real response absent from the contract is one a generated client treats as an
	// undocumented error. TestSpec_EveryConditionalRead_Documents304 compares the two in both
	// directions, so the flag cannot be set on a route that does not revalidate, and a documented
	// 304 cannot appear without it.
	ConditionalRead bool
	// InvalidatesTimer marks an operation that MOVES A RESPAWN WINDOW, and therefore changes every
	// derived answer hanging off it with no row appended anywhere.
	//
	// The other four `target_state.change_reason` values are read back off the report log, which
	// is what lets invalidation be a DELETE inside the writing transaction. `timer_change` is the
	// fifth and the log cannot show it: a window moved and nothing was reported. It has to be
	// PUSHED by whoever moved it.
	//
	// The flag is on the route rather than in the handler because a wiring that nothing enforces
	// is one refactor from being gone, and this is the second push-based invalidation in this
	// project to have had no mechanism behind it.
	// TestRouteRegistry_EveryTimerWritingRoute_PushesTheInvalidation drives every route carrying
	// it and asserts the invalidator fired; TestRouteRegistry_EveryWindowWritingPath_CarriesTheFlag
	// closes the other direction, so the flag cannot be quietly dropped to silence the first.
	InvalidatesTimer bool
	// Hidden keeps the operation out of the OpenAPI document. Permitted only on `/healthz`,
	// `/readyz`, `/metrics` and the OAuth callback — canonical §7, asserted by
	// TestRouteRegistry_Hidden_OnlyTheOperationalEndpointsAndTheCallback.
	Hidden bool
	// Summary is the one-line description that reaches the OpenAPI document.
	Summary string
}

// FullPath returns the path the route is actually served at.
func (r Route) FullPath() string {
	if r.Versioned {
		return BasePath + r.Path
	}
	return r.Path
}

// RequiresStepUp reports whether the operation is in the capability floor: session-only, recently
// re-authenticated, and reachable by no token at any scope.
//
// It is derived from internal/authz rather than declared, so the floor has exactly one definition.
// TestRouteRegistry_StepUp_MatchesTheAPIDesign compares the derivation against the `step-up`
// annotation in the document, so the document cannot quietly disagree with the catalogue either.
func (r Route) RequiresStepUp() bool {
	for _, p := range r.Permissions {
		if authz.RequiresStepUp(p) {
			return true
		}
	}
	return false
}

// SessionOnly reports whether no personal access token reaches the operation at any scope.
func (r Route) SessionOnly() bool {
	switch r.Auth {
	case AuthPermission, AuthSelf:
		return !r.AnyScope && len(r.Scopes) == 0
	case AuthPublic, AuthMetricsToken, AuthSetupToken:
		return false
	default:
		return false
	}
}

// RequiresIdempotencyKey reports whether `Idempotency-Key` is required on the request.
func (r Route) RequiresIdempotencyKey() bool { return r.Idempotency != IdempotencyNone }

// Authenticated reports whether the operation needs a principal at all.
func (r Route) Authenticated() bool { return r.Auth == AuthSelf || r.Auth == AuthPermission }

// PathParams returns the route's path parameter names, in path order.
func (r Route) PathParams() []string {
	var out []string
	for _, segment := range strings.Split(r.Path, "/") {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			out = append(out, strings.Trim(segment, "{}"))
		}
	}
	return out
}

// routes is the registry: every operation in docs/design/02-api-design.md, in document order.
//
// TestRouteRegistry_MatchesTheAPIDesign parses that document's operation tables and compares them
// to this list in BOTH directions, so an operation in one and not the other is a red test. It is a
// function rather than a package-level slice for the same reason the permission catalogue is: the
// route surface is the last thing that should be editable from a distance.
func routes() []Route {
	return []Route{
		// --- Discovery and identity ---
		{
			ID: OpGetServerMeta, Method: http.MethodGet, Path: "/meta", Versioned: true,
			Auth:    AuthPublic,
			Summary: "Version, API versions, feature flags, and whether self-service circle creation is on",
		},
		{
			ID: OpGetSetupState, Method: http.MethodGet, Path: "/setup", Versioned: true,
			Auth:    AuthSetupToken,
			Summary: "What first-run setup has to work with: the instance row, providers, circles and catalogue",
		},
		{
			ID: OpRunSetup, Method: http.MethodPost, Path: "/setup", Versioned: true,
			Auth: AuthSetupToken, CreatesState: true, Idempotency: IdempotencyHandler,
			// `Idempotency-Key` is required and validated at the edge, like every other operation
			// that creates state: `routeMiddleware` reaches its handler through one tail, so the
			// branch that resolves no principal cannot skip it.
			//
			// `IdempotencyHandler` says what happens after that, and it is NOT a replay. There is
			// no `idempotency_record` to key on — that table's `principal_membership_id` is NOT
			// NULL and setup has no principal at all — and the response could not be reproduced
			// from the database anyway, because the owner code is held only as a hash. What the
			// handler owns instead is convergence: every step is create-if-absent and a second
			// circle is refused outright, so a retry of a lost response mints nothing and is told
			// which field resumes the run.
			// `TestRunSetup_ARepeatedRequest_MintsNoSecondOwnerCode` is that promise.
			Summary: "Create the instance, its first provider and its first circle, and return a one-time owner code",
		},
		{
			ID: OpListIdentityProviders, Method: http.MethodGet, Path: "/identity-providers",
			Versioned: true, Auth: AuthPublic,
			Summary: "The enabled identity providers, and never a secret. Needed before auth",
		},
		{
			ID: OpCreateAuthorizationURL, Method: http.MethodPost, Path: "/auth/authorization-url",
			Versioned: true, Auth: AuthPublic, InviteOracle: true,
			// Creates an `auth_flow` row, which is not domain state: it is a pre-authentication
			// artefact swept on expiry, written only past the shared invite rate limit, and there
			// is no principal in existence to key `(principal, key)` on.
			Summary: "Start a browser OAuth flow. Takes no circle_id, by design",
		},
		{
			ID: OpCompleteAuthorization, Method: http.MethodGet,
			Path: "/auth/callback/{provider_key}", Versioned: true, Auth: AuthPublic, Hidden: true,
			Summary: "The OAuth redirect target. Redirects to the SPA with the ticket in the fragment",
		},
		{
			ID: OpGetCurrentPrincipal, Method: http.MethodGet, Path: "/me", Versioned: true,
			Auth: AuthSelf, AnyScope: true,
			Summary: "The calling principal: membership, circle, role, effective permissions, token prefix, scopes, expiry",
		},
		{
			ID: OpPreviewInvite, Method: http.MethodPost, Path: "/invites/preview", Versioned: true,
			Auth: AuthPublic, InviteOracle: true,
			Summary: "Read an invite by code. The code travels in the body, never the path",
		},
		{
			ID: OpRedeemInvite, Method: http.MethodPost, Path: "/join", Versioned: true,
			Auth: AuthPublic, CreatesState: true, Idempotency: IdempotencyHandler,
			Summary: "Redeem an invite: verify the credential, create the identity and membership, mint a token",
		},
		{
			ID: OpAuthenticateIdentity, Method: http.MethodPost, Path: "/sessions", Versioned: true,
			Auth: AuthPublic, CreatesState: true, Idempotency: IdempotencyHandler,
			Summary: "Re-authenticate an existing membership on a new device, with no invite",
		},
		{
			ID: OpListMyTokens, Method: http.MethodGet, Path: "/tokens", Versioned: true,
			Auth: AuthSelf, AnyScope: true,
			Summary: "My own devices. Officers see nobody's",
		},
		{
			ID: OpRevokeToken, Method: http.MethodDelete, Path: "/tokens/{token_id}",
			Versioned: true, Auth: AuthSelf,
			Summary: "Revoke one of my own devices",
		},

		// --- Circles ---
		{
			ID: OpListCircles, Method: http.MethodGet, Path: "/circles", Versioned: true,
			Auth: AuthSelf, Scopes: []authz.Scope{authz.ScopeCircleRead},
			Summary: "The circles I am a member of. There is no list-all operation at any permission level",
		},
		{
			ID: OpCreateCircle, Method: http.MethodPost, Path: "/circles", Versioned: true,
			Auth:         AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionInstanceCircleCreate},
			CreatesState: true, Idempotency: IdempotencyHandler,
			Summary: "Create a circle on this instance",
		},
		{
			ID: OpGetCircle, Method: http.MethodGet, Path: "/circles/{circle_id}", Versioned: true,
			Auth:         AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionCircleRead},
			Scopes:       []authz.Scope{authz.ScopeCircleRead},
			CircleScoped: true, ETag: true, ConditionalRead: true,
			Summary: "Read the circle",
		},
		{
			ID: OpUpdateCircle, Method: http.MethodPatch, Path: "/circles/{circle_id}",
			Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionCircleManage},
			CircleScoped: true, IfMatch: true, ETag: true,
			Summary: "Rename the circle or change its settings. `server` is immutable",
		},
		{
			ID: OpSetCircleProviders, Method: http.MethodPut,
			Path: "/circles/{circle_id}/providers", Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionCircleSecurityManage},
			CircleScoped: true, IfMatch: true, ETag: true,
			Summary: "Set which identity providers the circle accepts, which changes its revocation strength",
		},
		{
			ID: OpDeleteCircle, Method: http.MethodDelete, Path: "/circles/{circle_id}",
			Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionCircleDelete},
			CircleScoped: true,
			Summary:      "Delete the circle and every report in it",
		},

		// --- Members and invites ---
		{
			ID: OpListMembers, Method: http.MethodGet, Path: "/circles/{circle_id}/members",
			Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionMemberRead},
			Scopes:       []authz.Scope{authz.ScopeMemberRead},
			CircleScoped: true,
			Summary:      "List the circle's members",
		},
		{
			ID: OpGetMember, Method: http.MethodGet,
			Path: "/circles/{circle_id}/members/{member_id}", Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionMemberRead},
			Scopes:       []authz.Scope{authz.ScopeMemberRead},
			CircleScoped: true, ETag: true, ConditionalRead: true,
			Summary: "Read one member",
		},
		{
			ID: OpUpdateMember, Method: http.MethodPatch,
			Path: "/circles/{circle_id}/members/{member_id}", Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionMemberManage},
			CircleScoped: true, IfMatch: true, ETag: true,
			Summary: "Change a member's role or display name",
		},
		{
			ID: OpRevokeMember, Method: http.MethodPost,
			Path: "/circles/{circle_id}/members/{member_id}/revoke", Versioned: true,
			Auth:         AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionMemberRevoke},
			CircleScoped: true, IfMatch: true,
			// Not a state CREATION: the membership row already exists and revocation is a
			// transition on it. `If-Match` rather than `Idempotency-Key` is the right guard,
			// because the failure to defend against is two officers disagreeing, not a retry.
			Summary: "Revoke a membership. Their reports still count",
		},
		{
			ID: OpReinstateMember, Method: http.MethodPost,
			Path: "/circles/{circle_id}/members/{member_id}/reinstate", Versioned: true,
			Auth:         AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionMemberRevoke},
			CircleScoped: true, IfMatch: true,
			Summary: "Reinstate a revoked membership. The only way back in",
		},
		{
			ID: OpCreateServiceMember, Method: http.MethodPost,
			Path: "/circles/{circle_id}/service-members", Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionTokenMint},
			CircleScoped: true, CreatesState: true, Idempotency: IdempotencyMembership,
			Summary: "Create a service membership and mint its token, owned by a named human",
		},
		{
			ID: OpListInvites, Method: http.MethodGet, Path: "/circles/{circle_id}/invites",
			Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionInviteRead},
			Scopes:       []authz.Scope{authz.ScopeInviteRead},
			CircleScoped: true,
			Summary:      "List the circle's invites",
		},
		{
			ID: OpCreateInvite, Method: http.MethodPost, Path: "/circles/{circle_id}/invites",
			Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionInviteCreate},
			Scopes:       []authz.Scope{authz.ScopeInviteCreate},
			CircleScoped: true, CreatesState: true, Idempotency: IdempotencyMembership,
			Summary: "Mint an invite code. One minted by a token is hard-narrowed to one use, 24 hours and a role below owner",
		},
		{
			ID: OpRevokeInvite, Method: http.MethodDelete,
			Path: "/circles/{circle_id}/invites/{invite_id}", Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionInviteRevoke},
			CircleScoped: true,
			Summary:      "Revoke an invite before it expires",
		},

		// --- ToD reports and derived state ---
		{
			ID: OpCreateTodReport, Method: http.MethodPost,
			Path: "/circles/{circle_id}/tod-reports", Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionTodReport},
			Scopes:       []authz.Scope{authz.ScopeTodReport},
			CircleScoped: true, CreatesState: true, Idempotency: IdempotencyMembership,
			Summary: "Append one immutable time-of-death report",
		},
		{
			ID: OpListTodReports, Method: http.MethodGet,
			Path: "/circles/{circle_id}/tod-reports", Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionTodRead},
			Scopes:       []authz.Scope{authz.ScopeTodRead},
			CircleScoped: true,
			Summary:      "The report log, newest first, cursor-paginated",
		},
		{
			ID: OpGetTodReport, Method: http.MethodGet,
			Path: "/circles/{circle_id}/tod-reports/{report_id}", Versioned: true,
			Auth:         AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionTodRead},
			Scopes:       []authz.Scope{authz.ScopeTodRead},
			CircleScoped: true,
			Summary:      "One report. Reports are immutable, so this representation never changes",
		},
		{
			ID: OpRetractTodReport, Method: http.MethodPost,
			Path: "/circles/{circle_id}/tod-reports/{report_id}/retract", Versioned: true,
			Auth: AuthPermission,
			Permissions: []authz.Permission{
				authz.PermissionTodRetract, authz.PermissionTodRetractAny,
			},
			Scopes:       []authz.Scope{authz.ScopeTodRetract},
			CircleScoped: true, CreatesState: true, Idempotency: IdempotencyMembership,
			Summary: "Retract a report by appending a retraction row. The original stays visible",
		},
		{
			ID: OpListTargetStates, Method: http.MethodGet, Path: "/circles/{circle_id}/tods",
			Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionTodRead},
			Scopes:       []authz.Scope{authz.ScopeTodRead},
			CircleScoped: true, ETag: true, ConditionalRead: true,
			Summary: "The board: every target's derived state, window and evidence",
		},
		{
			ID: OpGetTargetState, Method: http.MethodGet,
			Path: "/circles/{circle_id}/tods/{target_id}", Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionTodRead},
			Scopes:       []authz.Scope{authz.ScopeTodRead},
			CircleScoped: true, ETag: true, ConditionalRead: true,
			Summary: "One target: state, window, evidence and alternatives",
		},
		{
			ID: OpReportQuake, Method: http.MethodPost, Path: "/circles/{circle_id}/quakes",
			Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionTodQuakeReport},
			CircleScoped: true, CreatesState: true, Idempotency: IdempotencyMembership,
			Summary: "Record a server-wide earthquake. A false one wipes the whole board",
		},
		{
			ID: OpListQuakes, Method: http.MethodGet, Path: "/circles/{circle_id}/quakes",
			Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionTodRead},
			Scopes:       []authz.Scope{authz.ScopeTodRead},
			CircleScoped: true,
			Summary:      "The quake log",
		},
		{
			ID: OpSubscribeCircleEvent, Method: http.MethodGet,
			Path: "/circles/{circle_id}/events", Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionTodRead},
			Scopes:       []authz.Scope{authz.ScopeEventsSubscribe},
			CircleScoped: true,
			Summary:      "Server-sent events for the circle",
		},
		{
			ID: OpReplayCircleEvents, Method: http.MethodGet,
			Path: "/circles/{circle_id}/events/replay", Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionTodRead},
			Scopes:       []authz.Scope{authz.ScopeEventsSubscribe},
			CircleScoped: true,
			Summary:      "Replay the event stream from a sequence. The only place `since_seq` is legal",
		},
		{
			ID: OpListCircleAudit, Method: http.MethodGet, Path: "/circles/{circle_id}/audit",
			Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionAuditRead},
			CircleScoped: true,
			Summary:      "The circle's audit log",
		},

		// --- Raid-target catalogue ---
		{
			ID: OpListRaidTargets, Method: http.MethodGet, Path: "/raid-targets", Versioned: true,
			Auth:        AuthPermission,
			Permissions: []authz.Permission{authz.PermissionCatalogueRead},
			Scopes:      []authz.Scope{authz.ScopeCatalogueRead},
			Summary:     "The raid-target catalogue. Instance-wide: a mob's existence is a game fact",
		},
		{
			ID: OpGetRaidTarget, Method: http.MethodGet, Path: "/raid-targets/{target_id}",
			Versioned: true, Auth: AuthPermission,
			Permissions: []authz.Permission{authz.PermissionCatalogueRead},
			Scopes:      []authz.Scope{authz.ScopeCatalogueRead},
			ETag:        true, ConditionalRead: true,
			Summary: "One raid target",
		},
		{
			ID: OpResolveRaidTarget, Method: http.MethodPost, Path: "/raid-targets/resolve",
			Versioned: true, Auth: AuthPermission,
			Permissions: []authz.Permission{authz.PermissionCatalogueRead},
			Scopes:      []authz.Scope{authz.ScopeCatalogueRead},
			// A POST that reads: the name being resolved is user input of unbounded length and a
			// query string is the wrong place for it. It creates nothing.
			Summary: "Resolve a target name through the ladder: exact, normalised, alias, prefix, substring",
		},
		{
			ID: OpCreateRaidTarget, Method: http.MethodPost, Path: "/raid-targets", Versioned: true,
			Auth:         AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionCatalogueManage},
			CreatesState: true, Idempotency: IdempotencyHandler,
			Summary: "Add a raid target, for every circle on the instance",
		},
		{
			ID: OpUpdateRaidTarget, Method: http.MethodPatch, Path: "/raid-targets/{target_id}",
			Versioned: true, Auth: AuthPermission,
			Permissions: []authz.Permission{authz.PermissionCatalogueManage},
			IfMatch:     true, ETag: true,
			Summary: "Change a raid target",
		},
		{
			ID: OpPutRaidTargetTimer, Method: http.MethodPut,
			Path: "/raid-targets/{target_id}/timers/{server}", Versioned: true,
			Auth:        AuthPermission,
			Permissions: []authz.Permission{authz.PermissionCatalogueManage},
			IfMatch:     true, ETag: true, InvalidatesTimer: true,
			Summary: "Set a target's respawn timer for one server",
		},
		{
			ID: OpListCircleTimerOverrides, Method: http.MethodGet,
			Path: "/circles/{circle_id}/timer-overrides", Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionCircleManage},
			CircleScoped: true,
			Summary:      "The circle's timer overrides",
		},
		{
			ID: OpPutCircleTimerOverride, Method: http.MethodPut,
			Path: "/circles/{circle_id}/timer-overrides/{target_id}", Versioned: true,
			Auth:         AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionCircleManage},
			CircleScoped: true, IfMatch: true, ETag: true, InvalidatesTimer: true,
			Summary: "Override one target's timer for this circle",
		},
		{
			ID: OpDeleteCircleTimerOverride, Method: http.MethodDelete,
			Path: "/circles/{circle_id}/timer-overrides/{target_id}", Versioned: true,
			Auth:        AuthPermission,
			Permissions: []authz.Permission{authz.PermissionCircleManage},
			// Removing an override moves the window too: the circle falls back to the catalogue
			// timer, or to `unknown` if there is none. A board that kept serving the overridden
			// window after the override was deleted is the same bug as one that never saw it set.
			CircleScoped: true, InvalidatesTimer: true,
			Summary: "Remove a circle's timer override",
		},

		// --- Instance administration ---
		{
			ID: OpListAdminIdentityProviders, Method: http.MethodGet,
			Path: "/admin/identity-providers", Versioned: true, Auth: AuthPermission,
			Permissions: []authz.Permission{authz.PermissionInstanceSecurityManage},
			Summary:     "The instance's identity providers, secrets excluded",
		},
		{
			ID: OpCreateIdentityProvider, Method: http.MethodPost,
			Path: "/admin/identity-providers", Versioned: true, Auth: AuthPermission,
			Permissions:  []authz.Permission{authz.PermissionInstanceSecurityManage},
			CreatesState: true, Idempotency: IdempotencyHandler,
			Summary: "Add an identity provider",
		},
		{
			ID: OpUpdateIdentityProvider, Method: http.MethodPatch,
			Path: "/admin/identity-providers/{provider_id}", Versioned: true, Auth: AuthPermission,
			Permissions: []authz.Permission{authz.PermissionInstanceSecurityManage},
			IfMatch:     true, ETag: true,
			Summary: "Change an identity provider",
		},
		{
			ID: OpDeleteIdentityProvider, Method: http.MethodDelete,
			Path: "/admin/identity-providers/{provider_id}", Versioned: true, Auth: AuthPermission,
			Permissions: []authz.Permission{authz.PermissionInstanceSecurityManage},
			Summary:     "Remove an identity provider",
		},
		{
			ID: OpGetDoctorReport, Method: http.MethodGet, Path: "/admin/doctor", Versioned: true,
			Auth:        AuthPermission,
			Permissions: []authz.Permission{authz.PermissionOpsRead},
			Summary:     "Instance diagnostics",
		},
		{
			ID: OpListJobs, Method: http.MethodGet, Path: "/admin/jobs", Versioned: true,
			Auth:        AuthPermission,
			Permissions: []authz.Permission{authz.PermissionOpsRead},
			Summary:     "Background job status",
		},
		{
			ID: OpGetLiveness, Method: http.MethodGet, Path: "/healthz", Auth: AuthPublic,
			Hidden:  true,
			Summary: "Liveness. Touches no database, so a container is not killed mid-migration",
		},
		{
			ID: OpGetReadiness, Method: http.MethodGet, Path: "/readyz", Auth: AuthPublic,
			Hidden:  true,
			Summary: "Readiness: the database is reachable and the migrations are at the expected version",
		},
		{
			ID: OpGetMetrics, Method: http.MethodGet, Path: "/metrics", Auth: AuthMetricsToken,
			Hidden:  true,
			Summary: "Prometheus metrics, on a separate listener, behind TOD_METRICS_TOKEN",
		},
	}
}

// Routes returns the registry, in the order the API design lists it.
func Routes() []Route { return slices.Clone(routes()) }

// Lookup returns the route with the given operation id.
func Lookup(id OperationID) (Route, bool) {
	for _, r := range routes() {
		if r.ID == id {
			return r, true
		}
	}
	return Route{}, false
}

// MustLookup returns the route with the given operation id, or an error naming it.
//
// It is what [Builder.Register] calls, so registering a handler for an operation that is not in
// the registry is impossible rather than merely discouraged: there is no path from a handler to a
// route that does not pass through this lookup.
func MustLookup(id OperationID) (Route, error) {
	r, ok := Lookup(id)
	if !ok {
		return Route{}, fmt.Errorf("register %q: %w", id, ErrUnknownOperation)
	}
	return r, nil
}

// CircleScopedRoutes returns every route that addresses one circle's resources — the set
// TestTenancy_CrossCircle_EveryOperationDenies has to cover.
func CircleScopedRoutes() []Route {
	var out []Route
	for _, r := range routes() {
		if r.CircleScoped {
			out = append(out, r)
		}
	}
	return out
}

// InviteOracleRoutes returns every route metered from the shared invite-code bucket — the set
// TestInviteOracle_PreviewAndAuthorizationURL_ShareOneBucket covers.
func InviteOracleRoutes() []Route {
	var out []Route
	for _, r := range routes() {
		if r.InviteOracle {
			out = append(out, r)
		}
	}
	return out
}

// SetupRoutes returns every route authorised by `TOD_SETUP_TOKEN` — the set the three first-run
// refusals are driven over, so a second setup route cannot be added without being covered by all
// of them. It is the same shape as [CircleScopedRoutes] and the tenancy gate over it.
func SetupRoutes() []Route {
	var out []Route
	for _, r := range routes() {
		if r.Auth == AuthSetupToken {
			out = append(out, r)
		}
	}
	return out
}

// PublicRoutes returns every route reachable with no credential — the set
// TestPublicRoutes_ResolveNoCircleFromCallerSuppliedId has to cover.
func PublicRoutes() []Route {
	var out []Route
	for _, r := range routes() {
		if r.Auth == AuthPublic {
			out = append(out, r)
		}
	}
	return out
}

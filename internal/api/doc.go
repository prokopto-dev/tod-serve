// Package api is the only place in this repository where an HTTP route is declared.
//
// The rule has a mechanism rather than a convention behind it: ROUTE001 in internal/repogate is an
// AST analyser that fails any call to the HTTP framework's registration functions outside
// internal/api/register.go, so a route cannot be attached anywhere else — not from a domain
// package, and not from another file in this one.
//
// # The route registry
//
// [Routes] is the whole API surface as data: method, path, operation id, permission, scopes,
// whether the route is circle-scoped, and whether it creates domain state. It is the substrate the
// architectural tests walk, and it is what makes "a new uncovered route is a red test" possible
// rather than aspirational:
//
//   - TestRouteRegistry_MatchesTheAPIDesign compares it to the operation tables in
//     docs/design/02-api-design.md, in both directions.
//   - TestTenancy_CrossCircle_EveryOperationDenies drives every circle-scoped route from it.
//   - TestRoutes_EveryStateCreatingPost_RequiresIdempotencyKey reads it rather than a list.
//   - The OpenAPI document is generated from it, so the spec cannot describe a route that is not
//     there or omit one that is.
//
// [Register] is the only way to attach a handler, and it takes an [OperationID] rather than a
// method and a path. There is no way to invent a route: an operation id outside the registry is an
// error at wiring time, and the method, path, security and extensions all come from the registry
// row rather than from the call site.
//
// # Where the failures are decided
//
// Authentication, tenancy and authorization all happen in one middleware, before any handler runs
// — see [routeMiddleware]. That is deliberate. A handler that resolved its own circle could
// forget, and a handler that returned 403 for the wrong tenant would leak a circle's existence,
// which canonical §7 exists to prevent. The order the checks run in IS the rule: wrong tenant is
// 404, right tenant and insufficient permission is 403.
package api

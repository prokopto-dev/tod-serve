// Package authz is the single source for permissions, scopes, roles and the capability floor.
//
// Everything about authorization is generated from the catalogue in this package: the `permission`
// and `role_permission` table seeds, the OpenAPI `x-tod-permission` metadata, the PAT scope enum
// and docs/reference/permissions.md. Hand-written permission lists are forbidden — `role_permission`
// is FK-constrained to `permission(key)`, so a list that has drifted is a boot failure rather than
// a style disagreement.
//
// # The shape of an authorization decision
//
// Effective capability is role permissions ∩ token scopes. A permission narrows a role; a scope
// narrows a token. There is no `admin:*` scope and no all-powerful token, and
// TestScopes_NoScopeGrants_ACapabilityFloorPermission is what keeps it that way.
//
// The two are asked separately on purpose. [EffectiveForSession] and [EffectiveForToken] are
// distinct functions rather than one function taking a possibly-empty scope list, because the
// empty list has to mean "this token may do nothing" — and the same call shape would then read as
// "no scopes, so no narrowing" to the next person writing a session path.
//
// # Two realms
//
// A permission is granted either by a circle membership's role or by an instance-level grant.
// [RolePermissions] covers the first. The second — `catalogue.manage`, `ops.read` and the
// `instance.*` keys — has no role matrix here, because there is no instance role enum in the
// canonical conventions and inventing one would put a second authorization model in the codebase.
// TestPermissions_InstanceRealm_IsNotGrantedByAnyRole pins that boundary so the hole is visible
// rather than assumed; the mechanism that fills it lands with the auth subsystem.
package authz

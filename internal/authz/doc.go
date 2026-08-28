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
// `instance.*` keys — has no role matrix here and never will: there is no instance role enum in
// the canonical conventions, and inventing one would put a second authorization model in the
// codebase. TestPermissions_InstanceRealm_IsNotGrantedByAnyRoleMatrix keeps that boundary.
//
// What grants them instead is `instance_grant`, an append-only ledger of decisions keyed on an
// IDENTITY rather than a membership (ADR-0012). This package owns the value set — see
// [InstancePermissions] and [InstancePermissionEnum], which generate the column's CHECK — and does
// not read the table: which grants an identity holds is a question for the store, and
// [EffectiveForSession] takes the answer as an argument.
//
// # One expansion
//
// `instance.owner` grants every other instance-realm key, through [Implies] and [ExpandInstance]
// (ADR-0015). It is the only implication in the catalogue and it runs in one direction from one
// key, which is what keeps it short of the instance role enum ADR-0012 rejected: every narrower
// key is still separately grantable, so handing somebody `ops.read` for a dashboard hands over
// nothing else.
//
// The expansion is applied in [EffectiveForSession] rather than at the ledger. What was DECIDED
// and what it reaches are two different facts, and a ledger that stored the expansion would leave
// four rows standing when the one that caused them was revoked.
//
// A token reaches none of them at any scope, expansion included. That is not a rule stated here so
// much as an arithmetic consequence: no scope in [Scopes] grants an instance-realm key, so the
// intersection in [EffectiveForToken] is empty however the ledger reads — and that function takes
// no instance set to expand in the first place.
package authz

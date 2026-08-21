package store

import (
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// insertGrant is the shape every test below writes: a well-formed row with one field varied, so
// what fails is the field under test and nothing else.
const insertGrant = `INSERT INTO instance_grant
	(id, identity_id, permission, decision, supersedes_id, decided_by_identity_id, reason,
	 prev_hash, hash, decided_at)
	VALUES (?, ?, ?, ?, ?, NULL, '', ?, ?, ?)`

// `instance_grant.permission` holds instance-realm keys and nothing else, and the value list is
// GENERATED from internal/authz into db/enums.hcl.
//
// This is the database half of internal/instancegrant's own refusal. Without it that test would
// prove only that a Go branch exists, and the CHECK could be dropped from the schema with nothing
// going red — the shape of gate the invariants document calls a tautology. It walks the whole
// catalogue rather than a chosen key, because "true of the whole realm" is the claim.
func TestInstanceGrant_ACircleRealmPermission_IsRefusedByTheSchema(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	f := seed(t, ctx, db)
	id := newIDs(rand.Reader)

	for _, def := range authz.Permissions() {
		err := exec(t, ctx, db, insertGrant,
			id.next(t), f.IdentityID, string(def.Key), schemaenum.InstanceGrantDecisionGranted,
			nil, nil, []byte(string(def.Key)), int64(now))
		if def.Realm == authz.RealmInstance {
			require.NoError(t, err,
				"%q is instance-realm and the column refused it, so nothing can grant it", def.Key)
			continue
		}
		require.Error(t, err, "%q is circle-realm and the column accepted it", def.Key)
		require.Contains(t, err.Error(), "ck_instance_grant_permission")
	}

	// And a key outside the catalogue entirely.
	err := exec(t, ctx, db, insertGrant,
		id.next(t), f.IdentityID, "instance.destroy", schemaenum.InstanceGrantDecisionGranted,
		nil, nil, []byte("destroy"), int64(now))
	require.Error(t, err)
	require.Contains(t, err.Error(), "ck_instance_grant_permission")
}

// Which decision is current is a CONSTRAINT, not a sort: two unique indexes make each
// (identity, permission) pair one chain with exactly one tail. That is what lets the effective
// grant be read with no ORDER BY and no tie-break, so a clock step or two ULIDs minted in one
// millisecond by two processes cannot make two rows both look latest.
func TestInstanceGrant_AForkedChain_IsRefusedByTheSchema(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	f := seed(t, ctx, db)
	id := newIDs(rand.Reader)
	permission := string(authz.PermissionOpsRead)

	root := id.next(t)
	mustExec(t, ctx, db, insertGrant,
		root, f.IdentityID, permission, schemaenum.InstanceGrantDecisionGranted,
		nil, nil, []byte("h1"), int64(now))

	// A second head for the same pair.
	err := exec(t, ctx, db, insertGrant,
		id.next(t), f.IdentityID, permission, schemaenum.InstanceGrantDecisionGranted,
		nil, []byte("h1"), []byte("h2"), int64(now))
	require.Error(t, err, "two chains started for one identity and permission")
	// SQLite names the COLUMNS of a violated unique index rather than the index, so this is what
	// `ux_instance_grant_head` looks like from the driver.
	require.Contains(t, err.Error(), "instance_grant.identity_id, instance_grant.permission")

	// One revocation superseding the root is fine.
	mustExec(t, ctx, db, insertGrant,
		id.next(t), f.IdentityID, permission, schemaenum.InstanceGrantDecisionRevoked,
		root, []byte("h1"), []byte("h3"), int64(now))

	// A second one naming the same predecessor is not: the pair would have two tails.
	err = exec(t, ctx, db, insertGrant,
		id.next(t), f.IdentityID, permission, schemaenum.InstanceGrantDecisionRevoked,
		root, []byte("h3"), []byte("h4"), int64(now))
	require.Error(t, err, "two decisions superseded the same one")
	require.Contains(t, err.Error(), "instance_grant.supersedes_id")

	// And two rows cannot name the same predecessor HASH. `audit_log` leaves that to a single
	// writer; here a forked chain is unrepresentable, because a chain that branches proves nothing
	// — verification would follow one branch and never see the other.
	err = exec(t, ctx, db, insertGrant,
		id.next(t), f.OIDCIdentID, permission, schemaenum.InstanceGrantDecisionGranted,
		nil, []byte("h1"), []byte("h5"), int64(now))
	require.Error(t, err, "two decisions named the same predecessor hash")
	require.Contains(t, err.Error(), "instance_grant.prev_hash")
}

// A row cannot supersede itself, which is the one cycle a single INSERT could create.
func TestInstanceGrant_ARowSupersedingItself_IsRefusedByTheSchema(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	f := seed(t, ctx, db)
	id := newIDs(rand.Reader)

	self := id.next(t)
	err := exec(t, ctx, db, insertGrant,
		self, f.IdentityID, string(authz.PermissionOpsRead),
		schemaenum.InstanceGrantDecisionRevoked, self, nil, []byte("h1"), int64(now))
	require.Error(t, err)
	require.Contains(t, err.Error(), "ck_instance_grant_supersedes_another_row")
}

// `decision` is an enum with two values, from the one catalogue.
func TestInstanceGrant_AnUnknownDecision_IsRefusedByTheSchema(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	f := seed(t, ctx, db)
	id := newIDs(rand.Reader)

	err := exec(t, ctx, db, insertGrant,
		id.next(t), f.IdentityID, string(authz.PermissionOpsRead), "maybe",
		nil, nil, []byte("h1"), int64(now))
	require.Error(t, err)
	require.Contains(t, err.Error(), "ck_instance_grant_decision")
}

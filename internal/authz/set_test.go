package authz_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/authz"
)

func TestNewSet_Input_IsDeduplicatedAndSorted(t *testing.T) {
	t.Parallel()
	got := authz.NewSet(
		authz.PermissionTodReport,
		authz.PermissionCircleRead,
		authz.PermissionTodReport,
	)
	want := []authz.Permission{authz.PermissionCircleRead, authz.PermissionTodReport}
	if diff := cmp.Diff(want, got.Slice()); diff != "" {
		t.Errorf("set (-want +got):\n%s", diff)
	}
	require.Equal(t, 2, got.Len())
	require.Equal(t, "{circle.read tod.report}", got.String())
}

func TestSet_Slice_IsACopy(t *testing.T) {
	t.Parallel()
	set := authz.NewSet(authz.PermissionCircleRead, authz.PermissionTodRead)

	// A set a caller can reslice and overwrite is not a set anybody can reason about, and this one
	// is handed out on every authorization decision.
	slice := set.Slice()
	slice[0] = authz.PermissionInstanceOwner
	require.True(t, set.Has(authz.PermissionCircleRead))
	require.False(t, set.Has(authz.PermissionInstanceOwner))
}

func TestSet_Operations_BehaveAsSets(t *testing.T) {
	t.Parallel()
	a := authz.NewSet(authz.PermissionCircleRead, authz.PermissionTodRead)
	b := authz.NewSet(authz.PermissionTodRead, authz.PermissionTodReport)

	require.Equal(t, authz.NewSet(authz.PermissionTodRead), a.Intersect(b))
	require.Equal(t, authz.NewSet(authz.PermissionCircleRead, authz.PermissionTodRead,
		authz.PermissionTodReport), a.Union(b))
	require.Equal(t, authz.NewSet(), a.Intersect(authz.NewSet()))
	require.True(t, a.Equal(authz.NewSet(authz.PermissionTodRead, authz.PermissionCircleRead)))
	require.False(t, a.Equal(b))
	require.Equal(t, 0, authz.Set{}.Len())
	require.False(t, authz.Set{}.Has(authz.PermissionTodRead))
}

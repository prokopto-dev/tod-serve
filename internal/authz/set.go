package authz

import (
	"slices"
	"strings"
)

// Set is an immutable set of permissions, sorted so that anything generated from it — a seed, a
// documentation table, a test failure — comes out the same way every time.
//
// It is a type rather than a []Permission because the interesting operation on permissions is
// intersection, and a slice invites a caller to append to a set it did not build.
type Set struct {
	perms []Permission
}

// NewSet returns the set of the given permissions, deduplicated and sorted.
func NewSet(perms ...Permission) Set {
	sorted := slices.Clone(perms)
	slices.Sort(sorted)
	return Set{perms: slices.Compact(sorted)}
}

// Has reports whether the set contains p. This is the question every request asks.
func (s Set) Has(p Permission) bool {
	_, found := slices.BinarySearch(s.perms, p)
	return found
}

// Len returns the number of permissions in the set.
func (s Set) Len() int { return len(s.perms) }

// Slice returns the permissions, sorted. The copy is deliberate: a Set that a caller can reslice
// and mutate is not a set anybody can reason about.
func (s Set) Slice() []Permission { return slices.Clone(s.perms) }

// Intersect returns the permissions in both sets. This is the whole authorization rule: role
// permissions ∩ token scopes.
func (s Set) Intersect(other Set) Set {
	var out []Permission
	for _, p := range s.perms {
		if other.Has(p) {
			out = append(out, p)
		}
	}
	return Set{perms: out}
}

// Union returns the permissions in either set.
func (s Set) Union(other Set) Set {
	return NewSet(append(slices.Clone(s.perms), other.perms...)...)
}

// Equal reports whether two sets hold the same permissions. go-cmp uses this, so a failure prints
// the sets rather than complaining about an unexported field.
func (s Set) Equal(other Set) bool { return slices.Equal(s.perms, other.perms) }

// String renders the set for a test failure or a log line.
func (s Set) String() string {
	parts := make([]string, 0, len(s.perms))
	for _, p := range s.perms {
		parts = append(parts, string(p))
	}
	return "{" + strings.Join(parts, " ") + "}"
}

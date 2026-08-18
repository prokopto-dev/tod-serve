package dbschema_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/dbschema"
)

func TestCheckConstraints_StoredDDL_YieldsEveryNamedCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ddl  string
		want map[string]string
	}{
		{
			name: "no constraints",
			ddl:  "CREATE TABLE t (id text NOT NULL, PRIMARY KEY (id)) STRICT",
			want: map[string]string{},
		},
		{
			name: "an enum check, whose value list contains the parentheses",
			ddl: "CREATE TABLE circle (state text NOT NULL, " +
				"CONSTRAINT ck_circle_state CHECK (state IN ('active', 'archived'))) STRICT",
			want: map[string]string{"ck_circle_state": "state IN ('active', 'archived')"},
		},
		{
			name: "a nested predicate, whose outer pair is not the first close",
			ddl: "CREATE TABLE t (a text, b text, " +
				"CONSTRAINT ck_t_pair CHECK ((a = 'x') = (b IS NOT NULL))) STRICT",
			want: map[string]string{"ck_t_pair": "(a = 'x') = (b IS NOT NULL)"},
		},
		{
			name: "several constraints alongside a named foreign key",
			ddl: "CREATE TABLE t (a text, b integer, " +
				"CONSTRAINT fk_t_other FOREIGN KEY (a) REFERENCES other (id), " +
				"CONSTRAINT ck_t_a CHECK (a IN ('one')), " +
				"CONSTRAINT ck_t_b CHECK (b >= 0)) STRICT",
			want: map[string]string{"ck_t_a": "a IN ('one')", "ck_t_b": "b >= 0"},
		},
		{
			// A parenthesis inside a value must not end the expression: without the quote
			// tracking this returns a truncated predicate that still compares as "present".
			name: "a parenthesis inside a quoted value",
			ddl: "CREATE TABLE t (a text, " +
				"CONSTRAINT ck_t_a CHECK (a IN ('left(', 'right)'))) STRICT",
			want: map[string]string{"ck_t_a": "a IN ('left(', 'right)')"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(tc.want, dbschema.CheckConstraints(tc.ddl)); diff != "" {
				t.Errorf("constraints (-want +got):\n%s", diff)
			}
		})
	}
}

func TestConstraintName_Binding_MirrorsTheGeneratedLocal(t *testing.T) {
	t.Parallel()
	for _, b := range dbschema.Bindings() {
		require.Equal(t, "ck_"+b.Table+"_"+b.Column, b.ConstraintName())
		// The two names differ only in their prefix, so a reader who finds one can find the other.
		require.Equal(t, "check_"+b.Table+"_"+b.Column, b.LocalName())
	}
}

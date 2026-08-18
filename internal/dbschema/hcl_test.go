package dbschema_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/dbschema"
)

func TestHCLTables_AtlasShapedSource_YieldsTablesAndColumns(t *testing.T) {
	t.Parallel()

	const src = `// a leading comment, and a table "commented_out" mention inside it
schema "main" {}

table "circle" {
  schema = schema.main
  column "id" {
    null = false
    type = text
  }
  column "server" {
    null = false
    type = text
  }
  primary_key {
    columns = [column.id]
  }
  index "ux_circle_server" {
    unique  = true
    columns = [column.server]
  }
  strict = true
}

table "membership" {
  schema = schema.main
  column "circle_id" {
    null = false
    type = text
  }
  foreign_key "fk_membership_circle" {
    columns     = [column.circle_id]
    ref_columns = [table.circle.column.id]
  }
  strict = true
}
`

	got, err := dbschema.HCLTables(src)
	require.NoError(t, err)

	want := map[string][]string{
		"circle":     {"id", "server"},
		"membership": {"circle_id"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("tables (-want +got):\n%s", diff)
	}
}

// A parse that silently yields nothing would make every comparison against it pass.
func TestHCLTables_EmptySource_IsAnError(t *testing.T) {
	t.Parallel()
	_, err := dbschema.HCLTables("// nothing but a comment\n")
	require.Error(t, err)
}

func TestHCLTables_ATableDeclaredTwice_IsAnError(t *testing.T) {
	t.Parallel()
	const src = `table "circle" {
  column "id" {
    type = text
  }
}
table "circle" {
  column "id" {
    type = text
  }
}
`
	_, err := dbschema.HCLTables(src)
	require.ErrorContains(t, err, "declared twice")
}

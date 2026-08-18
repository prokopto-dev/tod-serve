package core_test

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// The typed constants and the enum catalogue are two copies of the same three strings, and the
// catalogue is the one that reaches the CHECK constraint and the OpenAPI schema. This compares
// them in both directions, so neither can gain a value the other does not have.
func TestServers_Catalogue_MatchesSchemaEnum(t *testing.T) {
	t.Parallel()
	enum, ok := schemaenum.Lookup(schemaenum.NameServer)
	require.True(t, ok)

	var fromCore []string
	for _, s := range core.Servers() {
		fromCore = append(fromCore, s.String())
	}
	if diff := cmp.Diff(enum.Values, fromCore); diff != "" {
		t.Errorf("core.Servers differs from the enum catalogue (-catalogue +core):\n%s", diff)
	}
	for _, v := range enum.Values {
		require.True(t, core.Server(v).Valid(), "catalogue has %q and core does not", v)
	}
}

func TestParseServer_Value_IsAcceptedOrRefused(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		given string
		want  core.Server
		valid bool
	}{
		{"blue", "blue", core.ServerBlue, true},
		{"green", "green", core.ServerGreen, true},
		{"red", "red", core.ServerRed, true},
		{"uppercase", "Blue", "", false},
		{"unknown server", "teal", "", false},
		{"empty", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := core.ParseServer(tc.given)
			if !tc.valid {
				require.ErrorIs(t, err, core.ErrInvalidServer)
				require.Empty(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
			require.True(t, got.Valid())
		})
	}
}

// pinned stands in for the request bodies that echo the server back — the guard against a fan-out
// client reporting a Blue kill into a Green circle.
type pinned struct {
	Server core.Server `json:"server"`
}

func TestServer_UnmarshalJSON_UnknownServer_FailsAtTheEdge(t *testing.T) {
	t.Parallel()
	var got pinned
	require.NoError(t, json.Unmarshal([]byte(`{"server":"blue"}`), &got))
	require.Equal(t, core.ServerBlue, got.Server)

	// Without this the bad value reaches the CHECK constraint and surfaces as a 500 at write time
	// instead of a 422 at the edge.
	require.ErrorIs(t, json.Unmarshal([]byte(`{"server":"teal"}`), &got), core.ErrInvalidServer)
	require.ErrorIs(t, json.Unmarshal([]byte(`{"server":7}`), &got), core.ErrInvalidServer)

	encoded, err := json.Marshal(pinned{Server: core.ServerGreen})
	require.NoError(t, err)
	require.JSONEq(t, `{"server":"green"}`, string(encoded))
}

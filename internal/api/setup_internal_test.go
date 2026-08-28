package api

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/core"
)

// TestSetupConfigAuthorises_TheWholeTruthTable is the unit half of the three refusals.
//
// The HTTP tests in `setup_test.go` prove the route refuses; this proves the decision itself, over
// every combination there is — because the case that matters most has no interesting HTTP shape at
// all. An unset token and a caller who presents nothing are two empty strings, and a comparison
// that only asked "are these equal" would hand the instance to the first stranger who loaded the
// page on a deployment whose operator never armed setup.
func TestSetupConfigAuthorises_TheWholeTruthTable(t *testing.T) {
	t.Parallel()
	const configured = "setup-token-value"

	tests := []struct {
		name      string
		token     core.Secret
		presented string
		want      bool
	}{
		{"unset and nothing presented", "", "", false},
		{"unset and something presented", "", configured, false},
		{"unset and whitespace presented", "", " ", false},
		{"configured and matching", core.Secret(configured), configured, true},
		{"configured and nothing presented", core.Secret(configured), "", false},
		// Same length, one character different: a shorter wrong value would exercise
		// ConstantTimeCompare's length short-circuit instead of the comparison.
		{"configured and one character off", core.Secret(configured), "setup-token-valuE", false},
		{"configured and a prefix presented", core.Secret(configured), "setup-token", false},
		{"configured and a superstring presented", core.Secret(configured), configured + "x", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := SetupConfig{Token: tc.token}
			require.Equal(t, tc.want, cfg.authorises(tc.presented))
		})
	}
}

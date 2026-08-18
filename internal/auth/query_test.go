package auth_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/auth"
)

// Canonical §7: `Authorization: Bearer` only, and a query-string token is rejected with no
// exception at all. There is no compat shim, so this is the whole rule and it is checked here as a
// function and again over HTTP in internal/api.
func TestAuth_TokenInAQueryString_IsRejected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		query    string
		rejected bool
	}{
		{"no query at all", "", false},
		{"an ordinary filter", "status=in_window&limit=50", false},
		{"a parameter called token", "token=anything", true},
		{"a parameter called access_token", "access_token=x", true},
		{"a parameter called api_key", "api_key=x", true},
		{"upper case", "TOKEN=x", true},
		{"a real token under an innocent name", "q=tods_pat_ABCD1234_secret", true},
		{"a cursor that merely looks long", "cursor=01K3TGT8N9M4X0Q7R2VB6C5D1E", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q, err := url.ParseQuery(tc.query)
			require.NoError(t, err)

			err = auth.RejectTokenInURL(q)
			if tc.rejected {
				require.ErrorIs(t, err, auth.ErrTokenInURL)
				return
			}
			require.NoError(t, err)
		})
	}
}

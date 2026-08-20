package api

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCreateRaidTarget_TheIdempotencyHash_CoversEveryFieldOfTheBody.
//
// The hash is what turns a replayed key carrying a DIFFERENT request into `idempotency_key_reused`
// rather than a silent replay of the first one. A field left out of it is a field a client can
// change without being told their second request did nothing — and `aliases` is exactly the field
// somebody retries after noticing they forgot one.
//
// It hashes the marshalled body rather than a hand-listed set of fields, which is the encoding
// that cannot fall behind the struct: this test drives the same `json.Marshal` the handler does,
// so a field added to the request body is covered the moment it exists.
//
// It is an internal test because `hashBody` is unexported and should stay that way — exporting a
// test hook to reach it would put a function in the package's surface that exists for no caller.
func TestCreateRaidTarget_TheIdempotencyHash_CoversEveryFieldOfTheBody(t *testing.T) {
	t.Parallel()

	var base createRaidTargetInput
	base.Body.Name = "Overking Bathezid"
	base.Body.Zone = "Chardok"
	base.Body.Expansion = "kunark"
	base.Body.Category = "zone_boss"
	base.Body.IsQuakeTarget = true
	base.Body.Aliases = []string{"Bathezid"}

	tests := []struct {
		name   string
		mutate func(*createRaidTargetInput)
	}{
		{"the name", func(in *createRaidTargetInput) { in.Body.Name = "Prince Selrach Di`Zok" }},
		{"the zone", func(in *createRaidTargetInput) { in.Body.Zone = "Sebilis" }},
		{"the expansion", func(in *createRaidTargetInput) { in.Body.Expansion = "velious" }},
		{"the category", func(in *createRaidTargetInput) { in.Body.Category = "open_world" }},
		{"the quake flag", func(in *createRaidTargetInput) { in.Body.IsQuakeTarget = false }},
		{
			"an alias added",
			func(in *createRaidTargetInput) { in.Body.Aliases = []string{"Bathezid", "OKB"} },
		},
		{"every alias removed", func(in *createRaidTargetInput) { in.Body.Aliases = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			changed := base
			tt.mutate(&changed)
			require.NotEqual(t, hashCreateBody(t, base), hashCreateBody(t, changed),
				"changing %s leaves the idempotency hash identical, so retrying the same key with "+
					"this body replays the first request instead of reporting the conflict",
				tt.name)
		})
	}

	// The same body twice is the same hash, or every legitimate retry would answer 422 instead of
	// replaying — which is the failure in the other direction and just as bad.
	require.Equal(t, hashCreateBody(t, base), hashCreateBody(t, base))
}

func hashCreateBody(t *testing.T, in createRaidTargetInput) string {
	t.Helper()
	raw, err := json.Marshal(in.Body)
	require.NoError(t, err)
	return string(hashBody("createRaidTarget", string(raw)))
}

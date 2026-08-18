package core_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/core"
)

func TestMicros_String_IsRFC3339WithSixDigitsAndZ(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		given core.Micros
		want  string
	}{
		{"epoch", 0, "1970-01-01T00:00:00.000000Z"},
		{"whole second", 1_755_483_247_000_000, "2025-08-18T02:14:07.000000Z"},
		{"one microsecond", 1_755_483_247_000_001, "2025-08-18T02:14:07.000001Z"},
		{"trailing zeros kept", 1_755_483_247_100_000, "2025-08-18T02:14:07.100000Z"},
		{"before the epoch", -1_000_000, "1969-12-31T23:59:59.000000Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.given.String())
		})
	}
}

func TestParseMicros_ValidTimestamp_NormalisesToUTC(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		given string
		want  core.Micros
	}{
		{"microseconds", "2025-08-18T02:14:07.000001Z", 1_755_483_247_000_001},
		{"no fraction", "2025-08-18T02:14:07Z", 1_755_483_247_000_000},
		{"milliseconds", "2025-08-18T02:14:07.100Z", 1_755_483_247_100_000},
		// A client that sends an offset is unambiguous, so it is accepted and normalised. Only
		// the rendering is strict.
		{"positive offset", "2025-08-18T04:14:07+02:00", 1_755_483_247_000_000},
		{"negative offset", "2025-08-18T00:14:07-02:00", 1_755_483_247_000_000},
		// Storage is microseconds; a Go client marshals time.Time with nanoseconds by default, so
		// refusing this would refuse every Go client.
		{"nanoseconds truncated", "2025-08-18T02:14:07.0000019Z", 1_755_483_247_000_001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := core.ParseMicros(tc.given)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestParseMicros_Malformed_ReturnsErrInvalidTimestamp(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		given string
	}{
		{"empty", ""},
		{"date only", "2025-08-18"},
		{"no offset", "2025-08-18T02:14:07"},
		{"unix seconds", "1755483247"},
		{"prose", "yesterday"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := core.ParseMicros(tc.given)
			require.ErrorIs(t, err, core.ErrInvalidTimestamp)
		})
	}
}

// stamped is a whole-value stand-in for the many request and response bodies that carry a time.
type stamped struct {
	DiedAt core.Micros `json:"died_at"`
}

func TestMicros_JSON_RoundTripsThroughTheWireFormat(t *testing.T) {
	t.Parallel()
	want := stamped{DiedAt: 1_755_483_247_000_001}

	encoded, err := json.Marshal(want)
	require.NoError(t, err)
	require.JSONEq(t, `{"died_at":"2025-08-18T02:14:07.000001Z"}`, string(encoded))

	var got stamped
	require.NoError(t, json.Unmarshal(encoded, &got))
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("round trip (-want +got):\n%s", diff)
	}
}

func TestMicros_UnmarshalJSON_Number_IsRejected(t *testing.T) {
	t.Parallel()
	// Two accepted wire representations means two client behaviours, and only one gets tested.
	var got stamped
	err := json.Unmarshal([]byte(`{"died_at":1755483247000001}`), &got)
	require.ErrorIs(t, err, core.ErrInvalidTimestamp)
}

func TestMicrosFromTime_SubMicrosecond_IsDiscarded(t *testing.T) {
	t.Parallel()
	at := time.Date(2025, 8, 18, 2, 14, 7, 1999, time.UTC)
	require.Equal(t, core.Micros(1_755_483_247_000_001), core.MicrosFromTime(at))
	require.Equal(t, at.Truncate(time.Microsecond), core.MicrosFromTime(at).Time())
}

func TestMicros_Arithmetic_IsMicrosecondTruncating(t *testing.T) {
	t.Parallel()
	base := core.Micros(1_755_483_247_000_000)

	require.Equal(t, core.Micros(1_755_483_307_000_000), base.Add(time.Minute))
	require.Equal(t, core.Micros(1_755_483_187_000_000), base.Add(-time.Minute))
	require.Equal(t, base, base.Add(999*time.Nanosecond), "sub-microsecond offsets are discarded")
	require.Equal(t, time.Minute, base.Add(time.Minute).Sub(base))
	require.Equal(t, -time.Minute, base.Sub(base.Add(time.Minute)))

	require.True(t, base.Before(base.Add(time.Microsecond)))
	require.True(t, base.After(base.Add(-time.Microsecond)))
	require.False(t, base.Before(base))
	require.True(t, core.Micros(0).IsZero())
	require.False(t, base.IsZero())
}

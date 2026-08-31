package instancesettings_test

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/audit"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/instancesettings"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// The switch this package exists for: an operator who answered the wizard one way is no longer
// stuck with that answer, and the change is recorded rather than merely applied.
func TestApply_SelfServiceCircleCreation_MovesTheRowAndRecordsIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedInstance(false)

	f.clock.Advance(time.Hour)
	updated, recorded, err := f.service.Apply(t.Context(), instancesettings.ChangeRequest{
		SelfServiceCircleCreation: ptr(true),
		ChangedBy:                 f.alice,
		Reason:                    "the guild grew",
	})
	require.NoError(t, err)
	require.True(t, updated.SelfServiceCircleCreation)

	// The row moved, read back through the query every authorization check uses rather than
	// through the answer this call returned.
	current, err := f.service.Current(t.Context())
	require.NoError(t, err)
	require.True(t, current.SelfServiceCircleCreation)
	require.Equal(t, fixtureNow+core.Micros(time.Hour/time.Microsecond), current.UpdatedAt)

	require.Len(t, recorded, 1)
	change := recorded[0]
	require.Equal(t, instancesettings.SettingSelfServiceCircleCreation, change.Setting)
	// `0` and `1`, exactly as the `instance` row holds it. Translating to `false`/`true` here
	// would put a second spelling of the value in front of whoever is checking what happened.
	require.Equal(t, "0", change.OldValue)
	require.Equal(t, "1", change.NewValue)
	require.Equal(t, f.alice, change.ChangedBy)
	require.False(t, change.ByConsole())
	require.Equal(t, "the guild grew", change.Reason)
	require.Equal(t, current.UpdatedAt, change.ChangedAt)
}

// A change written with no identity is the operator at the console — a different fact from a
// person having decided it, which is why it is a NULL column rather than a self-reference.
func TestApply_NoChanger_ReadsAsTheConsole(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedInstance(false)

	_, recorded, err := f.service.Apply(t.Context(), instancesettings.ChangeRequest{
		SelfServiceCircleCreation: ptr(true),
	})
	require.NoError(t, err)
	require.Len(t, recorded, 1)
	require.True(t, recorded[0].ByConsole())
	require.True(t, recorded[0].ChangedBy.IsZero())
}

// The whole point of the ledger: every row chains onto the one before it, through the same
// function `audit_log` uses. A row removed by something that bypassed the trigger is visible in
// every row after it, and this walks the chain to say so.
func TestApply_EveryRow_ChainsOntoTheOneBeforeIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedInstance(false)

	// Three writes, so the chain has a beginning, a middle and an end. A two-row chain passes a
	// check that only ever looks at the tail.
	for _, req := range []instancesettings.ChangeRequest{
		{SelfServiceCircleCreation: ptr(true), ChangedBy: f.alice},
		{Name: ptr("Riot"), Timezone: ptr("Europe/London")},
		{SelfServiceCircleCreation: ptr(false), Reason: "too many circles"},
	} {
		f.clock.Advance(time.Minute)
		_, _, err := f.service.Apply(t.Context(), req)
		require.NoError(t, err)
	}

	rows := f.rows()
	// Four rows: the two toggles, plus the name and the timezone from the middle request.
	require.Len(t, rows, 4)

	var prev []byte
	for i, row := range rows {
		require.Equal(t, prev, row.PrevHash,
			"row %d does not chain onto its predecessor", i)
		want := audit.ChainHash(row.PrevHash,
			[]byte(row.ID), []byte(row.Setting), []byte(row.OldValue), []byte(row.NewValue),
			[]byte(deref(row.ChangedByIdentityID)), []byte(row.Reason),
			[]byte(formatInt(row.ChangedAt)))
		require.Equal(t, want, row.Hash, "row %d's hash does not cover its own fields", i)
		prev = row.Hash
	}
}

// Two settings changed in one request are two rows, in CATALOGUE order rather than in whatever
// order the caller's struct happened to be read. A chain whose row order depends on the caller is
// one nobody can reproduce by hand when they need to verify it.
func TestApply_SeveralSettingsAtOnce_AreRecordedInCatalogueOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedInstance(false)

	_, recorded, err := f.service.Apply(t.Context(), instancesettings.ChangeRequest{
		Timezone:                  ptr("America/New_York"),
		Name:                      ptr("Riot"),
		SelfServiceCircleCreation: ptr(true),
	})
	require.NoError(t, err)

	var got []string
	for _, c := range recorded {
		got = append(got, c.Setting.String())
	}
	want := []string{
		schemaenum.InstanceSettingSelfServiceCircleCreation,
		schemaenum.InstanceSettingName,
		schemaenum.InstanceSettingTimezone,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("recorded settings are not in catalogue order (-want +got):\n%s", diff)
	}
}

// A field sent at the value it already holds is not a change, and an audit record whose rows
// include ones where nothing happened is one somebody has to filter before reading. The database
// says the same thing from the other side: `old_value <> new_value` is a CHECK.
func TestApply_AFieldAtItsCurrentValue_RecordsNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedInstance(true)

	_, _, err := f.service.Apply(t.Context(), instancesettings.ChangeRequest{
		SelfServiceCircleCreation: ptr(true),
		Name:                      ptr("Test Instance"),
	})
	require.ErrorIs(t, err, instancesettings.ErrNoChange)
	require.Equal(t, apierr.CodeConflict, codeOf(t, err))
	require.Empty(t, f.rows(), "a refused change appended a row")
}

// A request naming only the fields that did not move still applies the one that did, and records
// only that one.
func TestApply_OneMovedFieldAmongUnchangedOnes_RecordsOnlyTheMove(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedInstance(false)

	_, recorded, err := f.service.Apply(t.Context(), instancesettings.ChangeRequest{
		Name:                      ptr("Test Instance"),
		Timezone:                  ptr("UTC"),
		SelfServiceCircleCreation: ptr(true),
	})
	require.NoError(t, err)
	require.Len(t, recorded, 1)
	require.Equal(t, instancesettings.SettingSelfServiceCircleCreation, recorded[0].Setting)
}

// The public URL has no field to arrive in, which is the design rather than an omission: it must
// keep matching every registered redirect URI, and `instance_setting_change.setting` cannot hold
// it either. This asserts the second half — the column refuses the value outright, so a row
// claiming the public URL changed is unrepresentable rather than merely unwritten.
func TestSchema_ASettingRowNamingThePublicURL_IsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedInstance(false)

	_, err := f.store.Queries().AppendInstanceSettingChange(t.Context(),
		f.appendParams("public_url", "https://old.example.com", "https://new.example.com"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "CHECK")

	// And the enum the CHECK is generated from is the same list the service moves, so the two
	// cannot drift into disagreeing about which settings exist.
	enum, ok := schemaenum.Lookup(schemaenum.NameInstanceSettingChange)
	require.True(t, ok)
	require.False(t, enum.Contains("public_url"))
}

// A change to an instance nobody has set up is refused with the fix in the message, not with a
// 500: "self-service is off" and "nobody has ever set this instance up" are different facts.
func TestApply_NoInstanceRow_SaysSoRatherThanFailing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	_, _, err := f.service.Apply(t.Context(), instancesettings.ChangeRequest{
		SelfServiceCircleCreation: ptr(true),
	})
	require.ErrorIs(t, err, instancesettings.ErrNotConfigured)
	require.Equal(t, apierr.CodeConflict, codeOf(t, err))

	_, err = f.service.Current(t.Context())
	require.ErrorIs(t, err, instancesettings.ErrNotConfigured)
}

// An empty name or timezone is refused rather than written: the first would leave `/meta` naming
// nothing, and the second is what every rendered date on the console falls back to.
func TestApply_EmptyValues_AreRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		req   instancesettings.ChangeRequest
		field string
	}{
		{"name", instancesettings.ChangeRequest{Name: ptr("   ")}, "body.name"},
		{"timezone", instancesettings.ChangeRequest{Timezone: ptr("")}, "body.timezone"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.seedInstance(false)

			_, _, err := f.service.Apply(t.Context(), tc.req)
			require.Equal(t, apierr.CodeValidationFailed, codeOf(t, err))
			require.Equal(t, tc.field, firstFieldLocation(t, err))
			require.Empty(t, f.rows())
		})
	}
}

// History is what an administrator reads to answer "who turned this on", so it is newest first and
// nothing prunes it.
func TestHistory_IsNewestFirst_AndKeepsEveryRow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedInstance(false)

	for _, on := range []bool{true, false, true} {
		f.clock.Advance(time.Minute)
		_, _, err := f.service.Apply(t.Context(), instancesettings.ChangeRequest{
			SelfServiceCircleCreation: ptr(on),
		})
		require.NoError(t, err)
	}

	history, err := f.service.History(t.Context())
	require.NoError(t, err)
	require.Len(t, history, 3)
	for i := 1; i < len(history); i++ {
		require.Greater(t, history[i-1].ChangedAt, history[i].ChangedAt,
			"history is not newest first")
	}
	// The instance is back where it started and the ledger still remembers the round trip. That
	// is the whole difference between a log and a current-state column.
	current, err := f.service.Current(t.Context())
	require.NoError(t, err)
	require.True(t, current.SelfServiceCircleCreation)
	require.Equal(t, "0", history[len(history)-1].OldValue)
}

// codeOf returns the problem code a service error renders as. Every error this package returns is
// an [apierr.Error], so a plain error here is a failure rather than a fallback: it would reach the
// edge as a 500 and tell the administrator nothing about what they got wrong.
func codeOf(t *testing.T, err error) apierr.Code {
	t.Helper()
	var problem *apierr.Error
	require.True(t, errors.As(err, &problem), "not an apierr.Error: %v", err)
	return problem.Code()
}

// firstFieldLocation returns the request path a validation failure points at, so a test asserts
// the caller is told WHICH field was wrong rather than only that something was.
func firstFieldLocation(t *testing.T, err error) string {
	t.Helper()
	var problem *apierr.Error
	require.True(t, errors.As(err, &problem), "not an apierr.Error: %v", err)
	require.NotEmpty(t, problem.Problem().Errors, "the problem names no field")
	return problem.Problem().Errors[0].Location
}

// deref and formatInt spell the two conversions the chain hash makes, so the test computes the
// hash the same way the code does without importing an unexported helper to do it.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func formatInt(v int64) string { return strconv.FormatInt(v, 10) }

// appendParams builds a ledger row for a test that writes one directly, so the CHECK rather than
// the service is what refuses it.
func (f *fixture) appendParams(
	setting, oldValue, newValue string,
) sqlitegen.AppendInstanceSettingChangeParams {
	f.t.Helper()
	id, err := core.NewID[core.InstanceSettingChange](f.ids, f.clock.Now())
	require.NoError(f.t, err)
	return sqlitegen.AppendInstanceSettingChangeParams{
		ID:        id.String(),
		Setting:   setting,
		OldValue:  oldValue,
		NewValue:  newValue,
		Hash:      []byte("hash-0001"),
		ChangedAt: int64(fixtureNow),
	}
}

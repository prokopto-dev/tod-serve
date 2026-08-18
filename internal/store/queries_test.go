package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

func TestQueries_Circle_RoundTripsThroughTheGeneratedBindings(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	id := newIDs(rand.Reader)
	circleID := id.next(t)

	created, err := db.Queries().CreateCircle(ctx, sqlitegen.CreateCircleParams{
		CircleID:                 circleID,
		Name:                     "Riot Blue",
		NameNorm:                 "riotblue",
		Description:              "",
		Server:                   schemaenum.ServerBlue,
		Timezone:                 "UTC",
		MinReportersToSupersede:  1,
		RevokeInvalidatesInvites: 1,
		State:                    schemaenum.CircleStateActive,
		CreatedAt:                int64(now),
		UpdatedAt:                int64(now),
	})
	require.NoError(t, err)
	require.Equal(t, circleID, created.ID)

	read, err := db.Queries().GetCircle(ctx, circleID)
	require.NoError(t, err)
	require.Equal(t, created, read)
}

// A circle-scoped read names circle_id, so a principal of circle A finds nothing in circle B. This
// is the store half of the rule the API turns into a 404 rather than a 403: the query returning no
// row is what makes "wrong tenant looks like absent" true underneath.
func TestQueries_CrossCircleRead_FindsNothing(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	f := seed(t, ctx, db)
	reportID := insertReport(t, ctx, db, f, now)

	other := newIDs(rand.Reader).next(t)
	_, err := db.Queries().CreateCircle(ctx, sqlitegen.CreateCircleParams{
		CircleID: other, Name: "Rival Blue", NameNorm: "rivalblue",
		Server: schemaenum.ServerBlue, Timezone: "UTC", MinReportersToSupersede: 1,
		RevokeInvalidatesInvites: 1, State: schemaenum.CircleStateActive,
		CreatedAt: int64(now), UpdatedAt: int64(now),
	})
	require.NoError(t, err)

	mine, err := db.Queries().GetTodReport(ctx,
		sqlitegen.GetTodReportParams{CircleID: f.CircleID, ID: reportID})
	require.NoError(t, err)
	require.Equal(t, reportID, mine.ID)

	_, err = db.Queries().GetTodReport(ctx,
		sqlitegen.GetTodReportParams{CircleID: other, ID: reportID})
	require.True(t, errors.Is(err, sql.ErrNoRows),
		"a report was readable from another circle: %v", err)
}

// The natural key is the second line of defence behind Idempotency-Key: the same reporter cannot
// lodge the same kill twice even if the header is botched.
func TestQueries_TodReport_NaturalKeyRefusesADuplicateKill(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	f := seed(t, ctx, db)
	insertReport(t, ctx, db, f, now)

	err := exec(t, ctx, db, `
		INSERT INTO tod_report (id, circle_id, target_id, kind, died_at, reported_at,
			reporter_membership_id, source, self_confidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newIDs(rand.Reader).next(t), f.CircleID, f.TargetID, schemaenum.TodReportKindKill,
		int64(now), int64(now), f.MembershipID, schemaenum.TodReportSourceManual,
		schemaenum.TodReportSelfConfidenceCertain)
	require.Error(t, err)

	// A correction by the same reporter has a different died_at, so it is unaffected. If the index
	// ever lost its died_at column this would start failing, and a legitimate correction would be
	// rejected as a duplicate.
	insertReport(t, ctx, db, f, now.Add(-3_600_000_000))
}

// A died_at in the future is the only hard rejection on a time, because it is impossible
// independent of any derivation. The tolerance is 120 seconds of clock skew.
func TestQueries_TodReport_DiedAtBeyondTheSkewTolerance_IsRefused(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	f := seed(t, ctx, db)

	// Inside the tolerance: accepted, because a client clock a minute fast is ordinary.
	insertReportAt(t, ctx, db, f, now+60*1_000_000, now)

	err := exec(t, ctx, db, `
		INSERT INTO tod_report (id, circle_id, target_id, kind, died_at, reported_at,
			reporter_membership_id, source, self_confidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newIDs(rand.Reader).next(t), f.CircleID, f.TargetID, schemaenum.TodReportKindKill,
		int64(now)+300*1_000_000, int64(now), f.MembershipID, schemaenum.TodReportSourceLogLine,
		schemaenum.TodReportSelfConfidenceCertain)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ck_tod_report_died_at_not_in_future")
}

func TestInTx_FunctionReturnsAnError_RollsBackEverythingItWrote(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	circleID := newIDs(rand.Reader).next(t)
	boom := errors.New("the caller changed its mind")

	err := db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		_, err := q.CreateCircle(ctx, sqlitegen.CreateCircleParams{
			CircleID: circleID, Name: "Riot Blue", NameNorm: "riotblue",
			Server: schemaenum.ServerBlue, Timezone: "UTC", MinReportersToSupersede: 1,
			RevokeInvalidatesInvites: 1, State: schemaenum.CircleStateActive,
			CreatedAt: int64(now), UpdatedAt: int64(now),
		})
		require.NoError(t, err)
		return boom
	})
	require.True(t, errors.Is(err, boom), "the cause must survive the rollback: %v", err)

	_, err = db.Queries().GetCircle(ctx, circleID)
	require.True(t, errors.Is(err, sql.ErrNoRows), "the rolled-back circle is still there")
}

func TestInTx_FunctionReturnsNil_Commits(t *testing.T) {
	t.Parallel()
	ctx, db := openMigrated(t)
	circleID := newIDs(rand.Reader).next(t)

	require.NoError(t, db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		_, err := q.CreateCircle(ctx, sqlitegen.CreateCircleParams{
			CircleID: circleID, Name: "Riot Blue", NameNorm: "riotblue",
			Server: schemaenum.ServerBlue, Timezone: "UTC", MinReportersToSupersede: 1,
			RevokeInvalidatesInvites: 1, State: schemaenum.CircleStateActive,
			CreatedAt: int64(now), UpdatedAt: int64(now),
		})
		return err
	}))

	_, err := db.Queries().GetCircle(ctx, circleID)
	require.NoError(t, err)
}

// insertReportAt puts one kill report in with an explicit died_at and reported_at.
func insertReportAt(t *testing.T, ctx context.Context, db *DB, f fixture, died, reported core.Micros) {
	t.Helper()
	mustExec(t, ctx, db, `
		INSERT INTO tod_report (id, circle_id, target_id, kind, died_at, reported_at,
			reporter_membership_id, source, self_confidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newIDs(rand.Reader).next(t), f.CircleID, f.TargetID, schemaenum.TodReportKindKill,
		int64(died), int64(reported), f.MembershipID, schemaenum.TodReportSourceLogLine,
		schemaenum.TodReportSelfConfidenceCertain)
}

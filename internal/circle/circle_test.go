package circle_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// A new circle auto-accepts every enabled provider with a verifiable subject, and NEVER `local`.
// A new circle that silently accepted the unverifiable provider would be a circle whose revocation
// is advisory before anybody chose that.
func TestCreate_AutoAcceptsVerifiableProviders_AndNeverLocal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.provider("discord", schemaenum.IdentityProviderKindDiscord, true)
	f.provider("authentik", schemaenum.IdentityProviderKindOIDC, true)
	f.provider("local", schemaenum.IdentityProviderKindLocal, true)
	disabled := f.provider("keycloak", schemaenum.IdentityProviderKindOIDC, true)
	f.disableProvider(disabled)

	view := f.create("Riot Blue", schemaenum.ServerBlue)

	keys := make([]string, 0, len(view.AcceptedProviders))
	for _, p := range view.AcceptedProviders {
		keys = append(keys, p.Key)
	}
	require.ElementsMatch(t, []string{"discord", "authentik"}, keys)
	require.NotContains(t, keys, "local", "an owner must reach for local; it is never auto-added")
	require.NotContains(t, keys, "keycloak", "a disabled provider admits nobody, so it is not accepted")
}

// Derived on every read, never stored. Storing it would let it drift the moment a provider is
// added — and drift in the SAFE-LOOKING direction, because the stored value would still say
// `durable` while the new provider quietly made it false.
func TestCreate_RevocationStrength_IsDerivedFromWhatTheCircleAccepts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.provider("discord", schemaenum.IdentityProviderKindDiscord, true)
	local := f.provider("local", schemaenum.IdentityProviderKindLocal, true)

	view := f.create("Riot Blue", schemaenum.ServerBlue)
	require.Equal(t, string(identity.StrengthDurable), view.RevocationStrength)
	require.Empty(t, view.WeakProviders)
	require.Empty(t, view.RevocationWeakReasons)

	// Accepting `local` makes the circle weak, on the next read, with no write to the circle row.
	updated, err := f.service.SetProviders(t.Context(), view.ID, circle.SetProvidersRequest{
		Providers:                 []circle.AcceptedProvider{{Key: "discord"}, {Key: "local"}},
		AcknowledgeWeakRevocation: true,
	})
	require.NoError(t, err)
	require.Equal(t, string(identity.StrengthWeak), updated.RevocationStrength)
	require.Equal(t, []string{"local"}, updated.WeakProviders)
	require.Equal(t, []string{identity.WeakReasonUnverifiableProvider}, updated.RevocationWeakReasons)

	// Disabling it at the INSTANCE makes the circle durable again — it admits nobody new — and the
	// row is counted somewhere visible rather than dropped from the answer.
	f.disableProvider(local)
	after, err := f.service.Get(t.Context(), view.ID)
	require.NoError(t, err)
	require.Equal(t, string(identity.StrengthDurable), after.RevocationStrength)
	require.Equal(t, []string{"local"}, after.DisabledProviders,
		"a provider excluded from the calculation must still be reported; never hide a row silently")
	for _, p := range after.AcceptedProviders {
		if p.Key == "local" {
			require.False(t, p.Available)
		}
	}
}

// A circle that accepts nothing is durable, vacuously and correctly: nobody can join it at all.
func TestCreate_WithNoProvidersOnTheInstance_IsDurableAndUnjoinable(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.create("Riot Blue", schemaenum.ServerBlue)
	require.Empty(t, view.AcceptedProviders)
	require.Equal(t, string(identity.StrengthDurable), view.RevocationStrength)
}

// ADR-0009: a circle is pinned to one server permanently. Sending `server` is REFUSED with the code
// that says why rather than ignored, because ignoring it would let a client believe a circle had
// moved.
func TestUpdate_Server_IsFieldImmutable(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.create("Riot Blue", schemaenum.ServerBlue)

	green := schemaenum.ServerGreen
	_, err := f.service.Update(t.Context(), view.ID, circle.UpdateRequest{Server: &green})
	require.True(t, apierr.HasCode(err, apierr.CodeFieldImmutable), "got %v", err)

	// Even naming the server it already has: the field is immutable, not merely unchangeable, and
	// a client that could send the current value would eventually send a different one.
	same := schemaenum.ServerBlue
	_, err = f.service.Update(t.Context(), view.ID, circle.UpdateRequest{Server: &same})
	require.True(t, apierr.HasCode(err, apierr.CodeFieldImmutable), "got %v", err)

	after, err := f.service.Get(t.Context(), view.ID)
	require.NoError(t, err)
	require.Equal(t, schemaenum.ServerBlue, after.Server)
}

func TestUpdate_OnlyTheFieldsSent_Change(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	view := f.create("Riot Blue", schemaenum.ServerBlue)

	name := "Riot"
	updated, err := f.service.Update(t.Context(), view.ID, circle.UpdateRequest{Name: &name})
	require.NoError(t, err)
	require.Equal(t, "Riot", updated.Name)
	require.Equal(t, view.Description, updated.Description)
	require.Equal(t, view.Timezone, updated.Timezone)
	require.Equal(t, view.MinReportersToSupersede, updated.MinReportersToSupersede)
	require.Equal(t, view.RevokeInvalidatesInvites, updated.RevokeInvalidatesInvites)
	require.Equal(t, view.State, updated.State)
}

// Names arrive with the punctuation, spacing and case a person typed. Two circles whose names
// differ only in those are the same name to `ux_circle_name_norm_server`, and the answer is a
// conflict rather than a second circle nobody can tell apart in a list.
func TestCreate_TwoNamesThatNormaliseTheSame_OnOneServer_Conflict(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.service.Create(t.Context(), circle.CreateRequest{
		Name: "Riot Blue", Server: core.Server(schemaenum.ServerBlue),
	})
	require.NoError(t, err)

	for _, name := range []string{"riot blue", "RiotBlue", "Riot-Blue", "  Riot   Blue  "} {
		_, err := f.service.Create(t.Context(), circle.CreateRequest{
			Name: name, Server: core.Server(schemaenum.ServerBlue),
		})
		require.True(t, apierr.HasCode(err, apierr.CodeConflict), "%q: got %v", name, err)
	}

	// Per server, not per instance: one guild's Blue circle and Green circle share a name by
	// design, and that is the whole reason the index is composite.
	_, err = f.service.Create(t.Context(), circle.CreateRequest{
		Name: "Riot Blue", Server: core.Server(schemaenum.ServerGreen),
	})
	require.NoError(t, err)
}

func TestCreate_AnUnusableName_IsRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		given string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"punctuation only, which normalises to nothing", "---"},
		{"longer than the maximum", string(make([]byte, 0)) + longName()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			_, err := f.service.Create(t.Context(), circle.CreateRequest{
				Name: tt.given, Server: core.Server(schemaenum.ServerBlue),
			})
			require.True(t, apierr.HasCode(err, apierr.CodeValidationFailed), "got %v", err)
		})
	}
}

func TestCreate_AServerOutsideTheEnum_IsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, server := range []string{"", "BLUE", "purple", "blue "} {
		_, err := f.service.Create(t.Context(), circle.CreateRequest{
			Name: "Riot", Server: core.Server(server),
		})
		require.True(t, apierr.HasCode(err, apierr.CodeValidationFailed), "%q: got %v", server, err)
	}
}

func TestGet_ACircleThatDoesNotExist_IsNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, err := core.ParseID[core.Circle]("01K3TGT8N9M4X0Q7R2VB6C5D1E")
	require.NoError(t, err)
	_, err = f.service.Get(t.Context(), id)
	require.True(t, apierr.HasCode(err, apierr.CodeNotFound), "got %v", err)
}

func longName() string {
	out := ""
	for range circle.MaxNameLen + 1 {
		out += "a"
	}
	return out
}

// `deleteCircle` writes a TOMBSTONE. It cannot remove rows: `tod_report`, `quake_event`,
// `invite_redemption` and `audit_log` are append-only by trigger, and with `foreign_keys` ON a
// circle holding any of them cannot be deleted at all. The evidence outliving the circle is the
// report log's whole trust argument, not a compromise.
func TestDelete_TombstonesTheCircle_AndLeavesTheEvidence(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.provider("discord", schemaenum.IdentityProviderKindDiscord, true)
	view := f.create("Riot Blue", schemaenum.ServerBlue)
	actor := f.seedMember(view.ID)

	deleted, err := f.service.Delete(t.Context(), view.ID, actor)
	require.NoError(t, err)
	require.Equal(t, "Riot Blue", deleted.Name, "the representation of what went is returned once")

	// It stops existing to every reader.
	_, err = f.service.Get(t.Context(), view.ID)
	require.True(t, apierr.HasCode(err, apierr.CodeNotFound), "got %v", err)

	// The audit trail is still there and still resolves to the circle. `audit_log.circle_id`
	// references `circle`, so reading this back at all is the assertion that the row survived: a
	// real delete would have had to take these append-only rows with it, and a trigger refuses.
	head, err := f.store.Queries().GetLatestAuditLogEntry(t.Context(), view.ID.String())
	require.NoError(t, err, "the audit trail went with the circle")
	require.Equal(t, "circle.deleted", head.Action)
	require.Equal(t, view.ID.String(), head.CircleID)

	// Deleting twice is 404 rather than moving the timestamp: the moment a circle stopped existing
	// is the first one.
	_, err = f.service.Delete(t.Context(), view.ID, actor)
	require.True(t, apierr.HasCode(err, apierr.CodeNotFound), "got %v", err)
}

// A deleted circle releases its name. An operator who deleted "Riot Blue" by mistake has to be
// able to make it again, and a unique index over dead rows would tell them the name is taken by
// something they cannot see.
func TestDelete_ReleasesTheName_SoItCanBeCreatedAgain(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first := f.create("Riot Blue", schemaenum.ServerBlue)
	actor := f.seedMember(first.ID)

	_, err := f.service.Create(t.Context(), circle.CreateRequest{
		Name: "Riot Blue", Server: core.Server(schemaenum.ServerBlue),
	})
	require.True(t, apierr.HasCode(err, apierr.CodeConflict), "the name is taken while it is live")

	_, err = f.service.Delete(t.Context(), first.ID, actor)
	require.NoError(t, err)

	second, err := f.service.Create(t.Context(), circle.CreateRequest{
		Name: "Riot Blue", Server: core.Server(schemaenum.ServerBlue),
	})
	require.NoError(t, err, "the deleted circle is still holding its name")
	require.NotEqual(t, first.ID, second.ID)
}

// TestCreateFirst_ConcurrentCallers_CreateExactlyOneCircle is the gate on first-run setup's
// promise that an instance does not get a second circle it never asked for.
//
// The wizard reads the instance's circles to decide whether to create one, and every write after
// that read is made from a snapshot. Two runs describing the same empty instance would each see no
// circle and each create one — so the condition is re-counted inside the transaction that inserts,
// which the writing pool's `_txlock=immediate` turns into a queue rather than a race: the second
// caller cannot BEGIN until the first has committed.
//
// It races real callers, so a single round can miss a real defect: `database/sql` keeps two idle
// connections by default and opens the rest on demand, which staggers callers that were released
// together. Hence ROUNDS, each on its own database — the check outside the transaction was caught
// in roughly half of single rounds and in every run of this shape.
//
// Every caller asks for a DIFFERENT name, so the unique index on `(name_norm, server)` refuses
// nothing here. If it did, this would pass while proving something else entirely.
func TestCreateFirst_ConcurrentCallers_CreateExactlyOneCircle(t *testing.T) {
	t.Parallel()

	const (
		rounds  = 8
		callers = 16
	)
	for round := range rounds {
		f := newFixture(t)
		ctx := t.Context()

		// A real barrier, not a bare WaitGroup released before the others have started: every
		// caller reports that it is running, and only then is any of them let go. Without it the
		// scheduler can finish one caller before the next has read anything, and the test passes
		// over a race it never ran.
		var ready, done sync.WaitGroup
		ready.Add(callers)
		done.Add(callers)
		start := make(chan struct{})
		results := make(chan error, callers)
		for i := range callers {
			go func() {
				defer done.Done()
				ready.Done()
				<-start
				_, err := f.service.CreateFirst(ctx, circle.CreateRequest{
					Name: fmt.Sprintf("Racer %d", i), Server: core.Server(schemaenum.ServerBlue),
				})
				results <- err
			}()
		}
		ready.Wait()
		close(start)
		done.Wait()
		close(results)

		created, refused := 0, 0
		for err := range results {
			switch {
			case err == nil:
				created++
			case errors.Is(err, circle.ErrNotFirst):
				refused++
			default:
				require.NoError(t, err, "a caller failed for a reason that is not losing the race")
			}
		}
		require.Equalf(t, 1, created,
			"round %d: %d callers were told they created the first circle", round, created)
		require.Equal(t, callers-1, refused)

		live, err := f.store.Queries().CountLiveCircles(ctx)
		require.NoError(t, err)
		require.EqualValuesf(t, 1, live,
			"round %d: the database holds %d circles; the count and the insert are not one "+
				"transaction", round, live)
	}
}

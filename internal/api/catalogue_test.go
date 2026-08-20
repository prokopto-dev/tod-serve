package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

// catalogueHarness is a harness whose instance has been seeded with target IDENTITY and no timers.
//
// That combination is not a convenience: it is what `tod-serve seed targets` produces and what an
// operator's VPS looks like the day they install the binary. Timer data ships from nowhere —
// canonical §15 — so any test here that wants a window has to write one, which keeps "we have no
// timers" the default rather than the special case.
func (h *harness) seedCatalogue() {
	h.t.Helper()
	_, err := h.catalogue.SeedTargets(h.t.Context())
	require.NoError(h.t, err)
}

// catalogueReader mints a principal that can read the catalogue, and the circle it belongs to.
func (h *harness) catalogueReader() (core.Secret, core.CircleID) {
	h.t.Helper()
	circleID := h.seedCircle("Riot")
	member := h.seedMember(circleID, authz.RoleMember)
	return h.seedToken(member, authz.ScopeCatalogueRead), circleID
}

// TestListRaidTargets_AnUnseededInstance_ServesEveryTargetWithNoTimer is the acceptance criterion
// the milestone names, driven over HTTP rather than through the service.
//
// An instance that has run `seed targets` and never been handed a timer file must serve a complete
// catalogue and say, on every row, that it has no window. Not a 500, not an empty list, not a
// guess. The board an officer opens on day one is this, and it has to be legible.
func TestListRaidTargets_AnUnseededInstance_ServesEveryTargetWithNoTimer(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedCatalogue()
	token, _ := h.catalogueReader()

	got := h.do(request{
		Method: http.MethodGet,
		Path:   api.BasePath + "/raid-targets?server=blue&limit=200",
		Token:  token,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)

	var page api.Page[catalogue.CatalogueEntry]
	require.NoError(t, json.Unmarshal([]byte(got.Body), &page))
	require.Len(t, page.Items, len(catalogue.Embedded()))
	require.False(t, page.HasMore)

	for _, entry := range page.Items {
		require.Nil(t, entry.CatalogueTimer,
			"%s carries a timer on an instance nobody seeded; timer data does not ship",
			entry.Name)
		require.NotEmpty(t, entry.Name)
		require.NotEmpty(t, entry.Zone)
	}
}

// TestListRaidTargets_TheFilters_NarrowByExpansionZoneAndName covers the board's three filters,
// including `q`, which runs the same normalisation the resolve ladder does.
func TestListRaidTargets_TheFilters_NarrowByExpansionZoneAndName(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedCatalogue()
	token, _ := h.catalogueReader()

	tests := []struct {
		name    string
		query   string
		expect  string
		atLeast int
	}{
		{name: "by expansion", query: "expansion=classic", expect: "Lord Nagafen", atLeast: 5},
		{
			// The zone is compared normalised, so an officer typing it in lower case with no
			// punctuation finds it.
			name: "by zone, typed carelessly", query: "zone=temple%20of%20veeshan",
			expect: "Vulak`Aerr", atLeast: 10,
		},
		{name: "by a name substring", query: "q=nag", expect: "Lord Nagafen", atLeast: 1},
		{
			// `q` matches aliases too. `VA` is Vulak's, and nothing in the catalogue is NAMED va.
			name: "by an alias", query: "q=VA", expect: "Vulak`Aerr", atLeast: 1,
		},
		{
			name:  "by a name substring with the backtick typed as an apostrophe",
			query: "q=vulak'aerr", expect: "Vulak`Aerr", atLeast: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := h.do(request{
				Method: http.MethodGet,
				Path:   api.BasePath + "/raid-targets?limit=200&" + tt.query,
				Token:  token,
			})
			require.Equal(t, http.StatusOK, got.Status, got.Body)

			var page api.Page[catalogue.CatalogueEntry]
			require.NoError(t, json.Unmarshal([]byte(got.Body), &page))
			require.GreaterOrEqual(t, len(page.Items), tt.atLeast)
			require.Less(t, len(page.Items), len(catalogue.Embedded()),
				"the filter narrowed nothing")

			names := make([]string, 0, len(page.Items))
			for _, entry := range page.Items {
				names = append(names, entry.Name)
			}
			require.Contains(t, names, tt.expect)
		})
	}
}

// TestListRaidTargets_TheCursor_WalksTheWholeCatalogueOnce. Canonical §4: every collection but
// `/events/replay` pages on the opaque ULID cursor, and a page that repeated or skipped a row would
// give the board a target twice or not at all.
func TestListRaidTargets_TheCursor_WalksTheWholeCatalogueOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedCatalogue()
	token, _ := h.catalogueReader()

	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		require.Less(t, pages, 50, "the cursor is not advancing")
		path := api.BasePath + "/raid-targets?limit=7"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		got := h.do(request{Method: http.MethodGet, Path: path, Token: token})
		require.Equal(t, http.StatusOK, got.Status, got.Body)

		var page api.Page[catalogue.CatalogueEntry]
		require.NoError(t, json.Unmarshal([]byte(got.Body), &page))
		for _, entry := range page.Items {
			require.False(t, seen[entry.ID.String()], "%s came back on two pages", entry.Name)
			seen[entry.ID.String()] = true
		}
		if !page.HasMore {
			require.Empty(t, page.NextCursor, "the last page still offered a cursor")
			break
		}
		require.NotEmpty(t, page.NextCursor)
		cursor = page.NextCursor
	}
	require.Len(t, seen, len(catalogue.Embedded()))
}

// TestResolveRaidTarget_TheMiddleOfTheRange is the ladder over HTTP: the spellings a real client
// actually sends, not the two ends everybody remembers to test.
func TestResolveRaidTarget_TheMiddleOfTheRange(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedCatalogue()
	token, _ := h.catalogueReader()

	tests := []struct {
		name  string
		typed string
		want  string
		kind  catalogue.MatchKind
	}{
		{"the canonical spelling", "Vulak`Aerr", "Vulak`Aerr", catalogue.MatchName},
		{"typed with an apostrophe", "Vulak'Aerr", "Vulak`Aerr", catalogue.MatchNameNormalised},
		{"typed with no punctuation", "VulakAerr", "Vulak`Aerr", catalogue.MatchNameNormalised},
		{"typed in lower case with a space", "vulak aerr", "Vulak`Aerr", catalogue.MatchNameNormalised},
		{"an alias", "Naggy", "Lord Nagafen", catalogue.MatchAlias},
		{"an alias in the wrong case", "naggy", "Lord Nagafen", catalogue.MatchAliasNormalised},
		{"the long name as an alias", "Nagafen", "Lord Nagafen", catalogue.MatchAlias},
		{"an alias that is also a prefix of another mob", "Sev", "Severilous", catalogue.MatchAlias},
		{"a prefix of exactly one", "Trakan", "Trakanon", catalogue.MatchPrefix},
		{"a substring of exactly one", "orenaire", "Gorenaire", catalogue.MatchSubstring},
		{"stray whitespace on both ends", "  Naggy  ", "Lord Nagafen", catalogue.MatchAlias},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := h.do(request{
				Method: http.MethodPost, Path: api.BasePath + "/raid-targets/resolve",
				Token: token,
				Body:  `{"name": ` + mustJSON(t, tt.typed) + `}`,
			})
			require.Equal(t, http.StatusOK, got.Status, got.Body)

			var body api.ResolutionResponse
			require.NoError(t, json.Unmarshal([]byte(got.Body), &body))
			require.Equal(t, tt.want, body.Target.Name, "%q resolved wrong", tt.typed)
			require.Equal(t, tt.kind, body.MatchKind, "%q matched at the wrong rung", tt.typed)
			require.NotZero(t, body.AsOf)
		})
	}
}

// TestResolveRaidTarget_AnAmbiguousName_Is422WithCandidatesOnTheWire. The plugin holds no
// catalogue: `meta.candidates[]` is the only thing that tells it what it might have meant, so its
// presence is part of the contract and not a nicety.
func TestResolveRaidTarget_AnAmbiguousName_Is422WithCandidatesOnTheWire(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedCatalogue()
	token, _ := h.catalogueReader()

	got := h.do(request{
		Method: http.MethodPost, Path: api.BasePath + "/raid-targets/resolve",
		Token: token, Body: `{"name": "Lord"}`,
	})
	h.requireProblem(got, apierr.CodeAmbiguousTarget)
	require.NotNil(t, got.Problem.Meta)
	require.NotEmpty(t, got.Problem.Meta.Candidates)

	var candidates []catalogue.Target
	require.NoError(t, json.Unmarshal(got.Problem.Meta.Candidates, &candidates))
	require.Greater(t, len(candidates), 1)
	for _, candidate := range candidates {
		require.NotEmpty(t, candidate.ID, "a candidate with no id cannot be sent back as target_id")
	}
}

// TestResolveRaidTarget_AnUnknownName_Is422AndNotAGuess. A resolver that answered with its nearest
// match would be the confident mistake this whole project is designed against.
func TestResolveRaidTarget_AnUnknownName_Is422AndNotAGuess(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedCatalogue()
	token, _ := h.catalogueReader()

	got := h.do(request{
		Method: http.MethodPost, Path: api.BasePath + "/raid-targets/resolve",
		Token: token, Body: `{"name": "Emperor Ssraeshza"}`,
	})
	h.requireProblem(got, apierr.CodeUnknownTarget)
}

// TestGetRaidTarget_AnUnseededTarget_HasAnEmptyTimersArray. Not null, not absent, not an error:
// `[]` is what "we know this mob and no window for it" looks like, and a client that special-cased
// null is a client somebody had to debug.
func TestGetRaidTarget_AnUnseededTarget_HasAnEmptyTimersArray(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedCatalogue()
	token, _ := h.catalogueReader()

	id := h.resolveTargetID(token, "Vulak`Aerr")
	got := h.do(request{
		Method: http.MethodGet, Path: api.BasePath + "/raid-targets/" + id,
		Token: token,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)
	require.Contains(t, got.Body, `"timers":[]`)
	require.NotEmpty(t, got.Header.Get(api.ETagHeader))

	// The tag is stable across reads: it covers the target and its timers, and `as_of` is on the
	// response outside it. A tag that moved every second would make If-Match unusable.
	again := h.do(request{
		Method: http.MethodGet, Path: api.BasePath + "/raid-targets/" + id,
		Token: token,
	})
	require.Equal(t, got.Header.Get(api.ETagHeader), again.Header.Get(api.ETagHeader))
}

// TestRaidTargetWrites_AreUnreachableUntilInstanceGrantsExist is a gap made into a gate.
//
// `catalogue.manage` is REALM-INSTANCE: canonical §6 has no instance role enum, so no circle role
// grants it and `TestPermissions_InstanceRealm_IsNotGrantedByAnyRole` in internal/authz pins that.
// These three handlers are written, correct and covered at the service level — and reachable by
// nobody, including an owner on a stepped-up session, until the auth subsystem lands instance
// grants.
//
// Registering them anyway is the right direction: the route registry IS the surface, the handlers
// are already under the architectural tests that walk it, and the day a grant exists they work
// with no further change. What would NOT be right is leaving that unsaid, so this test asserts the
// 403 rather than skipping the routes. When instance grants land it goes red, and whoever lands
// them is sent here to write the tests these three operations then deserve.
func TestRaidTargetWrites_AreUnreachableUntilInstanceGrantsExist(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedCatalogue()
	reader, circleID := h.catalogueReader()
	owner := h.seedMember(circleID, authz.RoleOwner)
	session := h.session(owner, true)
	id := h.resolveTargetID(reader, "Lord Nagafen")

	tests := []struct {
		name    string
		method  string
		path    string
		body    string
		headers map[string]string
	}{
		{
			name: "createRaidTarget", method: http.MethodPost,
			path: api.BasePath + "/raid-targets",
			body: `{"name": "A Mob", "zone": "Somewhere", "expansion": "kunark",
			        "category": "zone_boss"}`,
			headers: map[string]string{api.IdempotencyKeyHeader: "unreachable"},
		},
		{
			name: "updateRaidTarget", method: http.MethodPatch,
			path:    api.BasePath + "/raid-targets/" + id,
			body:    `{"zone": "Somewhere Else"}`,
			headers: map[string]string{api.IfMatchHeader: "*"},
		},
		{
			name: "putRaidTargetTimer", method: http.MethodPut,
			path:    api.BasePath + "/raid-targets/" + id + "/timers/blue",
			body:    `{"window_kind": "unknown"}`,
			headers: map[string]string{api.IfMatchHeader: "*"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := h.do(request{
				Method: tt.method, Path: tt.path, Session: session,
				Body: tt.body, Headers: tt.headers,
			})
			h.requireProblem(got, apierr.CodeForbidden)
		})
	}

	// And the reads beside them are reachable, so the 403 above is about the permission and not
	// about the routes being broken.
	read := h.do(request{
		Method: http.MethodGet, Path: api.BasePath + "/raid-targets/" + id, Token: reader,
	})
	require.Equal(t, http.StatusOK, read.Status, read.Body)
}

// TestGetRaidTarget_ASeededTimer_FoldsIntoTheReadAndOnlyForItsServer covers the read side of a
// timer over HTTP.
//
// The timer is written through the service rather than through `putRaidTargetTimer`, because that
// route is unreachable — see the test above. What is under test here is the READ: that a window
// reaches `timers[]` on the target and `catalogue_timer` on the list, and that blue's window never
// appears on green's board.
func TestGetRaidTarget_ASeededTimer_FoldsIntoTheReadAndOnlyForItsServer(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedCatalogue()
	reader, _ := h.catalogueReader()
	id := h.resolveTargetID(reader, "Lord Nagafen")

	targetID, err := core.ParseID[core.RaidTarget](id)
	require.NoError(t, err)
	_, err = h.catalogue.PutTimer(t.Context(), targetID, core.ServerBlue,
		catalogue.WindowRequest{
			WindowKind:               "variance",
			WindowOpenOffsetSeconds:  ptrInt64Test(60),
			WindowCloseOffsetSeconds: ptrInt64Test(120),
			Source:                   "a test, not the wiki",
		})
	require.NoError(t, err)

	got := h.do(request{
		Method: http.MethodGet, Path: api.BasePath + "/raid-targets/" + id, Token: reader,
	})
	require.Equal(t, http.StatusOK, got.Status, got.Body)
	var body api.TargetResponse
	require.NoError(t, json.Unmarshal([]byte(got.Body), &body))
	require.Len(t, body.Timers, 1)
	require.Equal(t, "blue", body.Timers[0].Server)
	require.Equal(t, int64(60), *body.Timers[0].WindowOpenOffsetSeconds)
	require.Equal(t, "a test, not the wiki", body.Timers[0].Source,
		"a window with no provenance is a number nobody can dispute")

	blue := h.listEntry(reader, id, "blue")
	require.NotNil(t, blue.CatalogueTimer)
	require.Equal(t, int64(60), *blue.CatalogueTimer.WindowOpenOffsetSeconds)

	green := h.listEntry(reader, id, "green")
	require.Nil(t, green.CatalogueTimer, "blue's window appeared on green's board")

	// Writing a timer changes the target's representation, so its ETag moves. A tag that did not
	// would let a client hold a cached target with a stale window in it.
	after := h.do(request{
		Method: http.MethodGet, Path: api.BasePath + "/raid-targets/" + id, Token: reader,
	})
	require.NotEqual(t, "", after.Header.Get(api.ETagHeader))
}

// TestCircleTimerOverride_TheLifecycle_IsWriteReadAndRemove, over HTTP, with the precondition rule
// on a resource that may not exist yet.
func TestCircleTimerOverride_TheLifecycle_IsWriteReadAndRemove(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedCatalogue()
	reader, circleID := h.catalogueReader()
	owner := h.seedMember(circleID, authz.RoleOwner)
	session := h.session(owner, true)

	id := h.resolveTargetID(reader, "Venril Sathir")
	base := api.BasePath + "/circles/" + circleID.String() + "/timer-overrides"

	empty := h.do(request{Method: http.MethodGet, Path: base, Session: session})
	require.Equal(t, http.StatusOK, empty.Status, empty.Body)
	require.Contains(t, empty.Body, `"items":[]`)

	// Creating one has no prior tag to send, so `*` is the only legal precondition and a bare
	// omission is still refused.
	missing := h.do(request{
		Method: http.MethodPut, Path: base + "/" + id, Session: session,
		Body: `{"window_kind": "variance", "window_open_offset_seconds": 300,
		        "window_close_offset_seconds": 400}`,
	})
	h.requireProblem(missing, apierr.CodePreconditionRequired)

	created := h.do(request{
		Method: http.MethodPut, Path: base + "/" + id, Session: session,
		Headers: map[string]string{api.IfMatchHeader: "*"},
		Body: `{"window_kind": "variance", "window_open_offset_seconds": 300,
		        "window_close_offset_seconds": 400,
		        "note": "we have tracked this for two years"}`,
	})
	require.Equal(t, http.StatusOK, created.Status, created.Body)

	var override api.OverrideResponse
	require.NoError(t, json.Unmarshal([]byte(created.Body), &override))
	require.Equal(t, "Venril Sathir", override.TargetName)
	require.Equal(t, owner, override.CreatedByMembershipID)
	require.NotZero(t, override.AsOf)

	listed := h.do(request{Method: http.MethodGet, Path: base, Session: session})
	require.Equal(t, http.StatusOK, listed.Status)
	require.Contains(t, listed.Body, "Venril Sathir")

	removed := h.do(request{
		Method: http.MethodDelete, Path: base + "/" + id, Session: session,
	})
	require.Equal(t, http.StatusOK, removed.Status, removed.Body)
	require.Contains(t, removed.Body, "Venril Sathir",
		"a delete that answers with nothing cannot be told from one that removed nothing")

	// Removing it twice is a 404: "removed" and "there was nothing there" are different answers.
	again := h.do(request{
		Method: http.MethodDelete, Path: base + "/" + id, Session: session,
	})
	h.requireProblem(again, apierr.CodeNotFound)
}

// TestCatalogue_AnOperatorAddedTarget_IsFoundByTheLadderOverHTTP is the escape hatch, end to end.
//
// The embedded list is what we are confident about; an operator adds what their server has and we
// did not. The add goes through the service because `createRaidTarget` is instance-realm and
// unreachable today; the FINDING of it is what this asserts, over HTTP, through the same ladder a
// plugin uses.
func TestCatalogue_AnOperatorAddedTarget_IsFoundByTheLadderOverHTTP(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedCatalogue()
	reader, _ := h.catalogueReader()

	_, err := h.catalogue.Create(t.Context(), catalogue.CreateRequest{
		Name: "Overking Bathezid", Zone: "Chardok",
		Expansion: "kunark", Category: "zone_boss",
		IsQuakeTarget: true, Aliases: []string{"Bathezid", "OKB"},
	})
	require.NoError(t, err)

	for _, typed := range []string{
		"Overking Bathezid", "overking bathezid", "Bathezid", "okb", "Bathez",
	} {
		got := h.do(request{
			Method: http.MethodPost, Path: api.BasePath + "/raid-targets/resolve",
			Token: reader, Body: `{"name": ` + mustJSON(t, typed) + `}`,
		})
		require.Equal(t, http.StatusOK, got.Status, "%q did not resolve: %s", typed, got.Body)
		require.Contains(t, got.Body, "Overking Bathezid")
	}

	// A second target claiming the same NORMALISED name is a conflict, not a duplicate row: both
	// unique indexes are on the normalised form, not on the typed one.
	_, err = h.catalogue.Create(t.Context(), catalogue.CreateRequest{
		Name: "overking  bathezid", Zone: "Chardok",
		Expansion: "kunark", Category: "zone_boss",
	})
	require.Error(t, err)
	coded, ok := apierr.From(err)
	require.True(t, ok)
	require.Equal(t, apierr.CodeConflict, coded.Code())
}

// ptrInt64Test is the local one-liner an optional window offset needs.
func ptrInt64Test(v int64) *int64 { return &v }

// resolveTargetID runs the resolve endpoint and returns the id, which every other test needs to
// build a path.
func (h *harness) resolveTargetID(token core.Secret, name string) string {
	h.t.Helper()
	got := h.do(request{
		Method: http.MethodPost, Path: api.BasePath + "/raid-targets/resolve",
		Token: token, Body: `{"name": ` + mustJSON(h.t, name) + `}`,
	})
	require.Equal(h.t, http.StatusOK, got.Status, got.Body)
	var body api.ResolutionResponse
	require.NoError(h.t, json.Unmarshal([]byte(got.Body), &body))
	return body.Target.ID.String()
}

// listEntry reads one target out of the list, with the given server's timer folded in.
func (h *harness) listEntry(token core.Secret, id, server string) catalogue.CatalogueEntry {
	h.t.Helper()
	got := h.do(request{
		Method: http.MethodGet,
		Path:   api.BasePath + "/raid-targets?limit=200&server=" + server,
		Token:  token,
	})
	require.Equal(h.t, http.StatusOK, got.Status, got.Body)
	var page api.Page[catalogue.CatalogueEntry]
	require.NoError(h.t, json.Unmarshal([]byte(got.Body), &page))
	for _, entry := range page.Items {
		if entry.ID.String() == id {
			return entry
		}
	}
	h.t.Fatalf("%s is not in the list for %s", id, server)
	return catalogue.CatalogueEntry{}
}

func mustJSON(t *testing.T, s string) string {
	t.Helper()
	raw, err := json.Marshal(s)
	require.NoError(t, err)
	return string(raw)
}

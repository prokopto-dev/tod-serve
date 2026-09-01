package api_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/canondoc"
)

// runbookPath is the operator's page for the bot.
const runbookPath = "docs/operations/discord-bot.md"

// The path an operator pastes into Discord's developer portal is the path this binary serves.
//
// **A wrong interactions URL cannot be saved at all** — Discord POSTs a signed `PING` and refuses
// the field unless it gets a well-signed `PONG` — so the failure this prevents is not a broken
// deployment. It is worse than that in one specific way: an operator following a stale runbook gets
// a refusal from Discord with no indication that the page they are reading is the reason, and every
// other cause in the list ("is the instance reachable", "is the key right") is more plausible.
//
// It is `ENV001`'s reasoning applied to a URL, which is what the runbook asked the implementation
// to do when it said "those are the authority and this page is a copy": the two sides are a route
// registry and a hand-written fenced block, and neither is generated from the other.
func TestDiscordBotRunbook_ThePathItPublishes_IsTheRouteRegistrys(t *testing.T) {
	t.Parallel()
	root, err := canondoc.RepoRoot()
	require.NoError(t, err)
	doc, err := canondoc.Load(root + "/" + runbookPath)
	require.NoError(t, err)

	blocks, err := doc.BlocksUnder("interactions endpoint URL")
	require.NoError(t, err)

	route, ok := api.Lookup(api.OpHandleDiscordInteraction)
	require.True(t, ok)

	// Both directions. The `text` block is the path on its own, and the `console` block shows the
	// command that prints it — so a page that dropped one of them, or updated one and not the
	// other, is red rather than half right.
	var sawPath, sawCommand bool
	for _, b := range blocks {
		body := strings.TrimSpace(b.Body)
		switch b.Language {
		case "text":
			require.Equal(t, route.FullPath(), body,
				"%s:%d publishes %q and the route registry serves %q",
				runbookPath, b.Line, body, route.FullPath())
			sawPath = true
		case "console":
			require.Contains(t, body, "tod-serve discord endpoint",
				"%s:%d shows a command that does not print the endpoint", runbookPath, b.Line)
			require.Contains(t, body, route.FullPath(),
				"%s:%d shows output that is not this route's path", runbookPath, b.Line)
			sawCommand = true
		}
	}
	require.True(t, sawPath, "%s publishes no interactions path; the parse is wrong", runbookPath)
	require.True(t, sawCommand,
		"%s names no command that prints it; the parse is wrong", runbookPath)
}

// The URL the binary prints is the origin plus that path, and it is DERIVED — so `tod-serve
// discord endpoint`, `tod-serve doctor` and the runbook cannot disagree.
func TestInteractionsURL_IsTheOriginPlusTheRegistrysPath(t *testing.T) {
	t.Parallel()
	route, ok := api.Lookup(api.OpHandleDiscordInteraction)
	require.True(t, ok)

	for _, tc := range []struct{ name, public, want string }{
		{"a bare origin", "https://tod.example.com", "https://tod.example.com" + route.FullPath()},
		{"a trailing slash", "https://tod.example.com/", "https://tod.example.com" + route.FullPath()},
		{"a sub-path", "https://example.com/tod", "https://example.com/tod" + route.FullPath()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := api.InteractionsURL(tc.public)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// A public URL that is not an origin is refused rather than stripped, exactly as the callback base
// is: a stripped one produces a working URL that is not the one the operator configured.
func TestInteractionsURL_APublicURLThatIsNotAnOrigin_IsRefused(t *testing.T) {
	t.Parallel()
	for _, public := range []string{
		"", "tod.example.com", "https://tod.example.com?tenant=one",
		"https://tod.example.com#fragment", "https://user@tod.example.com",
	} {
		t.Run(public, func(t *testing.T) {
			t.Parallel()
			_, err := api.InteractionsURL(public)
			require.Error(t, err)
		})
	}
}

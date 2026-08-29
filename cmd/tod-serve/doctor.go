package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/instancegrant"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// newDoctorCommand reports what is wrong with an instance, or says that nothing is.
//
// It is a separate verb rather than only the `/admin/doctor` route because the failures it is
// most useful for are the ones that stop the server starting: unmigrated database, no instance
// row, no identity provider. A diagnostic that needs the thing it diagnoses to be working is a
// diagnostic for the wrong problem.
func newDoctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check this instance and say what is wrong",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := databasePath(cmd)
			if err != nil {
				return err
			}
			db, closeDB, err := openStore(cmd.Context(), path, textLogger(io.Discard))
			if err != nil {
				return err
			}
			defer closeDB()

			out := cmd.OutOrStdout()
			problems := 0
			say := func(status, detail string) {
				// Deliberate waiver: the report is best-effort output, and the exit code below is
				// what a script reads. A closed stdout must not turn a healthy instance into a
				// failure.
				_, _ = fmt.Fprintf(out, "  %-8s %s\n", status, detail)
			}
			ok := func(detail string) { say("ok", detail) }
			warn := func(detail string) { say("warn", detail) }
			bad := func(detail string) { problems++; say("PROBLEM", detail) }

			if _, err := fmt.Fprintf(out, "tod-serve %s — %s\n\n", version, path); err != nil {
				return fmt.Errorf("write doctor report: %w", err)
			}
			checkDatabase(cmd.Context(), db, ok, bad)
			instance, haveInstance := checkInstance(cmd.Context(), db, ok, warn, bad)
			enabled := checkProviders(cmd.Context(), db, ok, warn, bad)
			checkRedirectURIs(cmd.Context(), db, enabled, ok, bad)
			checkAdministrable(cmd.Context(), db, ok, bad)
			checkHostAgreement(hostClaims(instance, haveInstance, enabled), ok, bad)

			if problems > 0 {
				return fmt.Errorf("%d problem(s) found", problems)
			}
			if _, err := fmt.Fprintln(out, "\nno problems found"); err != nil {
				return fmt.Errorf("write doctor report: %w", err)
			}
			return nil
		},
	}
}

func checkDatabase(ctx context.Context, db *store.DB, ok, bad func(string)) {
	version, err := db.SchemaVersion(ctx)
	if err != nil {
		bad("schema version is unreadable: " + err.Error())
	} else {
		ok(fmt.Sprintf("schema version %d", version))
	}
	if err := db.Ready(ctx); err != nil {
		bad("migrations are not up to date: run `tod-serve migrate` — " + err.Error())
	} else {
		ok("migrations are up to date")
	}
	if err := db.IntegrityCheck(ctx); err != nil {
		bad("integrity check: " + err.Error())
	} else {
		ok("integrity check passed")
	}
	// Foreign keys are only enforced when the pragma is on, and this schema is full of them. If it
	// were ever lost the damage is silent until a join returns nothing, which is why this runs
	// here rather than only in the test suite.
	if err := db.ForeignKeyCheck(ctx); err != nil {
		bad("foreign key check: " + err.Error())
	} else {
		ok("foreign key check passed")
	}
}

// checkInstance reports on the singleton, and hands the row to [checkHostAgreement].
func checkInstance(ctx context.Context, db *store.DB, ok, warn, bad func(string)) (sqlitegen.Instance, bool) {
	row, err := db.Queries().GetInstance(ctx)
	switch {
	case store.IsNotFound(err):
		bad("no instance row: run `tod-serve init`")
		return sqlitegen.Instance{}, false
	case err != nil:
		bad("instance row is unreadable: " + err.Error())
		return sqlitegen.Instance{}, false
	}
	ok(fmt.Sprintf("instance %q", row.Name))
	if strings.TrimSpace(row.PublicUrl) == "" {
		// A warning rather than a problem: everything works without it except the OAuth redirect,
		// and an instance running only `local` never needs one.
		warn("no public URL: the OAuth callback has nowhere to send a browser " +
			"(set it with `tod-serve init --public-url`, or $" + envPublicURL + ")")
	} else {
		ok("public URL " + row.PublicUrl)
	}
	if row.SelfServiceCircleCreation == 1 {
		warn("self-service circle creation is ON: any authenticated principal can create a circle")
	}
	return row, true
}

// checkProviders reports on the registry, and hands the ENABLED providers to
// [checkHostAgreement]. A disabled provider's redirect URI is not reachable and not a problem.
func checkProviders(ctx context.Context, db *store.DB, ok, warn, bad func(string)) []sqlitegen.IdentityProvider {
	rows, err := db.Queries().ListIdentityProviders(ctx)
	if err != nil {
		bad("identity providers are unreadable: " + err.Error())
		return nil
	}
	var live []sqlitegen.IdentityProvider
	enabled := 0
	for _, row := range rows {
		if row.Enabled != 1 {
			continue
		}
		enabled++
		live = append(live, row)
		if row.Kind == string(identity.KindLocal) {
			// `local` has no verifiable subject, so revoking a member who joined through it does
			// not stop them rejoining. The damage is the officers' false confidence, not the
			// re-entry, so this is said out loud on every run rather than once at setup.
			warn("provider \"" + row.Key + "\" is local: revocation there is ADVISORY, " +
				"and every circle accepting it reports revocation_strength=weak")
			continue
		}
		if row.ClientID == nil || *row.ClientID == "" {
			bad("provider \"" + row.Key + "\" has no client id: it cannot verify an audience")
			continue
		}
		ok("provider \"" + row.Key + "\" (" + row.Kind + ") is enabled")
	}
	if enabled == 0 {
		bad("no identity provider is enabled: nobody can join any circle")
	}
	return live
}

// checkRedirectURIs compares every provider's `redirect_uri` against the URL this binary actually
// serves the OAuth callback at.
//
// [checkHostAgreement] below already catches a redirect URI on the wrong ORIGIN. This catches the
// other half, which is the one that survives a careful operator: the right origin and the wrong
// path. `https://tod.example.com/callback` and
// `https://tod.example.com/api/v1/auth/callback/discord` agree about the host and are not the same
// URI, and Discord compares a redirect URI literally — so the first is an `invalid_request` on
// Discord's own error page, rendered by somebody else, about our configuration.
//
// It is a PROBLEM rather than a warning because there is exactly one string that works. This is
// also the check to run after moving an instance to a new domain, which is the operation that
// produces a stale redirect URI every time.
func checkRedirectURIs(
	ctx context.Context, db *store.DB, providers []sqlitegen.IdentityProvider, ok, bad func(string),
) {
	browserFlow := 0
	for _, p := range providers {
		if p.Kind == string(identity.KindLocal) {
			continue // `local` redirects nowhere because it goes nowhere.
		}
		browserFlow++
	}
	if browserFlow == 0 {
		return // checkProviders has already said whether that is a problem.
	}

	public, err := publicURL(ctx, db)
	if err != nil {
		bad("no public URL, so no redirect URI can be checked: " + err.Error())
		return
	}
	base, err := api.CallbackBaseURL(public)
	if err != nil {
		bad("the callback URL cannot be derived from " + public + ": " + err.Error())
		return
	}

	agreed := 0
	for _, p := range providers {
		if p.Kind == string(identity.KindLocal) {
			continue
		}
		want := identity.CanonicalRedirectURI(base + "/" + p.Key)
		got := ""
		if p.RedirectUri != nil {
			got = strings.TrimSpace(*p.RedirectUri)
		}
		// The same comparison the server makes, from the same function, so doctor cannot say a
		// redirect URI is fine that `createAuthorizationURL` then refuses.
		if identity.CanonicalRedirectURI(got) == want {
			agreed++
			continue
		}
		// Both strings, on their own lines, because an operator holding only one of them cannot
		// tell which end is wrong: the stored row, or what they registered with the provider.
		bad(fmt.Sprintf("provider %q redirect_uri does not match this instance's callback; "+
			"sign-in will not complete:\n           configured  %s"+
			"\n           must be     %s"+
			"\n           register that same string with the provider — it is compared literally",
			p.Key, quoteOrNone(got), want))
	}
	if agreed > 0 {
		ok(fmt.Sprintf("%d redirect %s the callback this instance serves (%s/…)",
			agreed, plural(agreed, "URI matches", "URIs match"), base))
	}
}

// quoteOrNone renders an empty redirect URI as a word rather than as `""`, which reads as a bug in
// the report rather than as the absence it is.
func quoteOrNone(raw string) string {
	if raw == "" {
		return "(none set)"
	}
	return raw
}

// checkAdministrable answers the question doctor did not ask: can ANYBODY administer this
// instance over the API?
//
// An instance nobody holds `instance.security.manage` on cannot add an identity provider, so its
// only administrator is whoever has a shell on the box — and nothing on screen says so. The
// console hides the Instance nav entry rather than explaining it, every CLI command succeeds, and
// the deployment runbook's bootstrap used to end one grant short of a working operator. This is
// where somebody looks, so this is where it is said.
//
// It asks [authz.ExpandInstance] rather than the ledger's rows, for the same reason the request
// path does: `instance.owner` expands to the whole instance realm, so an identity granted it IS an
// administrator, and a doctor reading raw rows would report a problem an operator had already
// fixed. The two would then disagree about the same database, which is worse than either answer.
//
// **A grant is not enough, and that is the second half.** An instance grant is on an IDENTITY, and
// an identity only reaches a request through a membership: `Authenticator.membership` reads one on
// EVERY call and refuses a revoked one, or one in a deleted circle. The ledger outlives both — a
// revocation is a membership row, not a grant row — so an identity can hold `instance.owner` while
// every credential it could present is refused. Counting it would be this report saying an
// instance is fine when nobody can log in to it, which is the exact failure the check exists for.
//
// `instancegrant.CanAuthenticate` is the predicate rather than a new query, because it wraps the one
// `listCircles` already serves: `revoked_at IS NULL AND deleted_at IS NULL`. Asking the question the
// API asks is what stops doctor growing a second definition of "can act" — and it lives beside the
// ledger because FIRST-RUN SETUP asks it too. An operator whose report says nobody can administer
// this instance must not then meet a wizard that says setup is over (ADR-0016).
//
// It counts only HUMAN memberships, and that is a schema fact rather than a filter this function
// applies: it joins on `identity_id`, and `ck_membership_human_has_an_identity` is the
// biconditional `(kind = 'human') = (identity_id IS NOT NULL)`, so a service membership carrying
// one is unrepresentable. Which matters here — a bot has an owner rather than an identity, and a
// token reaches no instance-realm permission at any scope, so counting one would be the same
// false-healthy in a narrower form.
//
// **It is a PROBLEM and not a warning**, and it stays one on a fresh database. That state is
// genuinely broken — it is simply broken in the way a brand-new instance is — and the deploy
// waives it BY NAME alongside the missing instance row rather than by doctor going quiet, because
// "nothing is bootstrapped yet" and "somebody's instance stopped having an administrator" are the
// same report and only the deploy knows which it is looking at. The recovery path stays open
// either way: `tod-serve instance grant` holds the database and needs no credential.
func checkAdministrable(ctx context.Context, db *store.DB, ok, bad func(string)) {
	grants, err := newGrantService(db)
	if err != nil {
		bad("the instance grant ledger is unusable: " + err.Error())
		return
	}
	rows, err := grants.Current(ctx)
	if err != nil {
		bad("instance grants are unreadable: " + err.Error())
		return
	}

	held := map[core.IdentityID][]authz.Permission{}
	for _, g := range rows {
		// Revoked decisions are in this listing too — `writeGrants` shows them on purpose — and a
		// revocation is exactly what this check must not count.
		if g.Decision == instancegrant.DecisionGranted {
			held[g.IdentityID] = append(held[g.IdentityID], g.Permission)
		}
	}
	granted, admins := 0, 0
	for identityID, perms := range held {
		if !authz.ExpandInstance(authz.NewSet(perms...)).Has(authz.PermissionInstanceSecurityManage) {
			continue
		}
		granted++
		live, err := instancegrant.CanAuthenticate(ctx, db.Queries(), identityID)
		if err != nil {
			bad(err.Error())
			return
		}
		if live {
			admins++
		}
	}

	if admins == 0 {
		// Two states, two REMEDIES, so two messages. "Nobody was granted it" is answered by a
		// grant. "They hold it and cannot log in" is not: the ledger already records the decision,
		// so `instance grant` refuses it with ErrNoChange, and an operator following that advice
		// would be told nothing to do about an instance nobody can administer.
		//
		// Both open with the same clause, deliberately: `.github/workflows/deploy.yml` subtracts
		// this problem by name on a first installation, and a wording that varied would waive one
		// state and fail the other for no reason either of them chose.
		//
		// The identities that were dropped are COUNTED rather than passed over. A filter that
		// silently removes the last administrator is how this check would come to report the exact
		// state it was written to find.
		const lead = "nobody can administer this instance: "
		if granted == 0 {
			bad(fmt.Sprintf(
				lead+"no identity holds %s, so no principal can add or change an identity "+
					"provider over the API.\n"+
					"           Fix it here: `tod-serve %s %s` for an id, then\n"+
					"           `tod-serve %s %s --%s <id> --%s %s`",
				authz.PermissionInstanceSecurityManage,
				verbInstance, verbIdentities,
				verbInstance, verbGrant, flagIdentity, flagPermission,
				authz.PermissionInstanceOwner))
			return
		}
		bad(fmt.Sprintf(
			lead+"%d %s %s and %s live membership — revoked, or in a circle "+
				"that was deleted — so no session can carry it.\n"+
				"           The grant is already recorded, so granting it again is refused. "+
				"Reinstate a membership,\n"+
				"           or grant %s to somebody who has one: `tod-serve %s %s`",
			granted, plural(granted, "identity holds", "identities hold"),
			authz.PermissionInstanceSecurityManage,
			plural(granted, "has no", "none of them has a"),
			authz.PermissionInstanceOwner, verbInstance, verbIdentities))
		return
	}
	// The count is said out loud rather than a bare tick: "somebody can administer this" and "one
	// person can, and they are on holiday" are different facts about the same instance.
	ok(fmt.Sprintf("%d %s administer this instance (%s)",
		admins, plural(admins, "identity can", "identities can"),
		authz.PermissionInstanceSecurityManage))
}

// plural picks the wording. A report that says "1 identities" is a report somebody stops reading.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// hostClaim is one written-down copy of this instance's origin, and where it was written down.
type hostClaim struct {
	source string
	raw    string
}

// hostClaims collects every copy of the origin this binary can see.
//
// It can see two of the five that exist. The droplet's `.env` and the identity provider's own
// dashboard are outside the process — `deploy.yml` compares the first against the repository
// variable before it swaps anything, and the second is what the provider rejects you with.
func hostClaims(
	instance sqlitegen.Instance, haveInstance bool, providers []sqlitegen.IdentityProvider,
) []hostClaim {
	var claims []hostClaim
	if haveInstance && strings.TrimSpace(instance.PublicUrl) != "" {
		claims = append(claims, hostClaim{source: "the instance row", raw: instance.PublicUrl})
	}
	if env := strings.TrimSpace(os.Getenv(envPublicURL)); env != "" {
		claims = append(claims, hostClaim{source: "$" + envPublicURL, raw: env})
	}
	for _, p := range providers {
		if p.RedirectUri == nil || strings.TrimSpace(*p.RedirectUri) == "" {
			// `local` has no callback to redirect to. An absent redirect URI is not a
			// disagreement about the host; it is a provider that does not use one.
			continue
		}
		claims = append(claims, hostClaim{
			source: "provider \"" + p.Key + "\" redirect_uri", raw: *p.RedirectUri,
		})
	}
	return claims
}

// checkHostAgreement is the gate on the copies of the hostname that fail QUIETLY.
//
// A tod-serve instance's origin is written down in five places: the droplet's `.env`, the
// repository variable the deploy reads, `instance.public_url`, every enabled provider's
// `redirect_uri`, and the identity provider's own dashboard. Nothing compared any of them.
//
// The two this binary can see are the two whose disagreement is silent. `spaJoinURL` prefers
// $TOD_PUBLIC_URL over the stored row, so an instance with a wrong `public_url` still hands out
// join links that LOOK right — and the first symptom is an opaque OAuth error days later, which is
// exactly the confident mistake this repository is built against. Reporting only an EMPTY
// `public_url`, which is all this did before, catches the case somebody notices anyway.
//
// A redirect_uri on a different origin is always broken rather than merely unusual: this instance
// serves the callback route, so a callback sent anywhere else never arrives. That is why this is a
// PROBLEM and not a warning.
func checkHostAgreement(claims []hostClaim, ok, bad func(string)) {
	if len(claims) < 2 {
		// Nothing to disagree with. Said out loud rather than passing silently, because "one copy"
		// and "two copies that match" are different states and a green tick over the first would
		// be a gate reporting success over an empty search space.
		ok(fmt.Sprintf("%d public URL to compare; nothing can disagree yet", len(claims)))
		return
	}

	origins := map[string][]string{}
	for _, c := range claims {
		origin, err := originOf(c.raw)
		if err != nil {
			bad(c.source + " is not a usable URL: " + err.Error())
			return
		}
		origins[origin] = append(origins[origin], c.source+" ("+c.raw+")")
	}
	if len(origins) == 1 {
		for origin := range origins {
			ok(fmt.Sprintf("%d copies of the public URL, all on %s", len(claims), origin))
		}
		return
	}

	// Sorted, so two runs against one database produce the same report rather than whichever
	// order the map yielded.
	keys := make([]string, 0, len(origins))
	for origin := range origins {
		keys = append(keys, origin)
	}
	sort.Strings(keys)

	// ONE call to bad, not one per line. This is a single finding about a single string, and a
	// report that counted its own detail lines would tell an operator there are three problems
	// when there is one — which is the sort of thing that makes a count worth ignoring.
	var b strings.Builder
	fmt.Fprintf(&b, "this instance's public URL is written down %d times and they do not agree; "+
		"every one of them must name the same origin:", len(claims))
	for _, origin := range keys {
		sort.Strings(origins[origin])
		for _, source := range origins[origin] {
			fmt.Fprintf(&b, "\n           %s — %s", origin, source)
		}
	}
	bad(b.String())
}

// originOf reduces a URL to scheme://host[:port], which is the whole of what has to match.
//
// A DEFAULT port is dropped, because `https://tod.example.com:443` and `https://tod.example.com`
// are the same origin and reporting them as a disagreement would be a false positive on a correct
// configuration — and a check with false positives is one somebody switches off. Scheme and host
// are lowercased for the same reason.
func originOf(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", raw, err)
	}
	scheme, host := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname())
	if scheme == "" || host == "" {
		return "", fmt.Errorf("%q names no scheme and host", raw)
	}
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		return scheme + "://" + net.JoinHostPort(host, port), nil
	}
	return scheme + "://" + host, nil
}

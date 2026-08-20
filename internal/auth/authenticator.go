package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// BearerScheme is the only accepted `Authorization` scheme.
const BearerScheme = "Bearer "

// touchInterval is how stale `api_token.last_used_at` is allowed to get before a request updates
// it. Writing it on every request would put a write behind every read on a database whose whole
// design assumes reads are free; never writing it would take away the one signal a person has for
// spotting a device they do not recognise.
const touchInterval = 5 * time.Minute

// tokenQueryParams are the query parameters a caller might try to smuggle a credential through.
// Presenting one is rejected outright — canonical §7 permits `Authorization: Bearer` and nothing
// else, with no exception and no compat shim, because a token in a URL lands in access logs,
// browser history, `Referer` headers and screenshots.
//
// The list is generous on purpose. It is not trying to catch a determined attacker; it is trying to
// make sure a client author who reaches for the convenient thing gets told, once, at the edge.
func tokenQueryParams() []string {
	return []string{
		"access_token", "api_key", "apikey", "auth", "authorization",
		"bearer", "pat", "session", "token",
	}
}

// ErrTokenInURL is returned when a credential is presented in the query string.
var ErrTokenInURL = errors.New("a token in a URL is never accepted")

// RejectTokenInURL reports whether a query string carries something shaped like a credential.
//
// It is a plain function over the parsed query rather than a method on the middleware so that
// TestAuth_TokenInAQueryString_IsRejected can drive it directly, and so the rule is stated in one
// place rather than repeated per route.
func RejectTokenInURL(q url.Values) error {
	for name := range q {
		lowered := strings.ToLower(name)
		if slices.Contains(tokenQueryParams(), lowered) {
			return fmt.Errorf("query parameter %q: %w", name, ErrTokenInURL)
		}
		// A value that is literally one of our tokens is refused under any parameter name at all.
		// Catching `?x=tods_pat_…` matters more than catching a parameter called `token` with
		// something else in it.
		for _, v := range q[name] {
			if strings.HasPrefix(v, TokenScheme) {
				return fmt.Errorf("query parameter %q carries a token: %w", name, ErrTokenInURL)
			}
		}
	}
	return nil
}

// Credentials is what a request presented. Both fields may be empty, which is an unauthenticated
// request rather than an error — whether that is acceptable is the route's business.
type Credentials struct {
	// Authorization is the raw header value, scheme included.
	Authorization string
	// SessionCookie is the raw `__Host-tod_session` value.
	SessionCookie string
	// Query is the request's query string, checked for a smuggled credential.
	Query url.Values
}

// Present reports whether any credential at all was offered.
func (c Credentials) Present() bool {
	return c.Authorization != "" || c.SessionCookie != ""
}

// Authenticator resolves a credential into a [Principal].
type Authenticator struct {
	db      *store.DB
	minter  *Minter
	codec   *SessionCodec
	clock   clock.Clock
	log     *slog.Logger
	stepUp  time.Duration
	touchAt time.Duration
}

// NewAuthenticator wires an authenticator. Every dependency is explicit: there is no default clock
// and no default logger, because a component that silently invents either is one that behaves
// differently in a test than in production.
func NewAuthenticator(
	db *store.DB, minter *Minter, codec *SessionCodec, clk clock.Clock, log *slog.Logger,
	stepUpWindow time.Duration,
) (*Authenticator, error) {
	switch {
	case db == nil:
		return nil, errors.New("new authenticator: store is nil")
	case minter == nil:
		return nil, errors.New("new authenticator: minter is nil")
	case codec == nil:
		return nil, errors.New("new authenticator: session codec is nil")
	case clk == nil:
		return nil, errors.New("new authenticator: clock is nil")
	case log == nil:
		return nil, errors.New("new authenticator: logger is nil")
	case stepUpWindow <= 0:
		return nil, errors.New("new authenticator: step-up window must be positive")
	}
	return &Authenticator{
		db: db, minter: minter, codec: codec, clock: clk, log: log,
		stepUp: stepUpWindow, touchAt: touchInterval,
	}, nil
}

// StepUpWindow returns how recently a session must have proved its identity.
func (a *Authenticator) StepUpWindow() time.Duration { return a.stepUp }

// Authenticate resolves a credential into a principal.
//
// The membership is read on every call, deliberately. Revoking a membership therefore takes effect
// on the very next request, with no token list to walk and nothing to forget — see
// docs/errors/membership_revoked.md. The cost is one indexed row read per request, which is the
// cheapest thing this handler does.
//
// Every failure is an *apierr.Error, so the edge renders a code rather than translating a sentinel
// through a switch statement nobody can enumerate.
func (a *Authenticator) Authenticate(ctx context.Context, creds Credentials) (Principal, error) {
	if err := RejectTokenInURL(creds.Query); err != nil {
		return Principal{}, apierr.Wrap(apierr.CodeUnauthenticated, err,
			"a token in a URL is never accepted; use Authorization: Bearer")
	}
	switch {
	case creds.Authorization != "":
		return a.authenticateToken(ctx, creds.Authorization)
	case creds.SessionCookie != "":
		return a.authenticateSession(ctx, creds.SessionCookie)
	default:
		return Principal{}, apierr.New(apierr.CodeUnauthenticated, "no credential was presented")
	}
}

func (a *Authenticator) authenticateToken(ctx context.Context, header string) (Principal, error) {
	raw, ok := strings.CutPrefix(header, BearerScheme)
	if !ok || strings.TrimSpace(raw) == "" {
		return Principal{}, apierr.New(apierr.CodeUnauthenticated,
			"Authorization must be `Bearer <token>`")
	}
	prefix, hash, err := a.minter.Verify(strings.TrimSpace(raw))
	if err != nil {
		return Principal{}, apierr.Wrap(apierr.CodeTokenInvalid, err, "the token is not valid")
	}

	row, err := a.db.Queries().GetAPITokenByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, store.ErrNoRows) {
			// Unknown and revoked answer identically on the wire. See docs/errors/token_invalid.md
			// for why: confirming that a particular string was once a token is worth nothing to
			// its holder and something to whoever found it.
			a.log.WarnContext(ctx, "token rejected",
				slog.String("token_prefix", prefix), slog.String("reason", "unknown"))
			return Principal{}, apierr.New(apierr.CodeTokenInvalid, "the token is not valid")
		}
		return Principal{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}

	now := a.clock.Now()
	if row.RevokedAt != nil {
		a.log.WarnContext(ctx, "token rejected",
			slog.String("token_prefix", row.TokenPrefix), slog.String("reason", "revoked"))
		return Principal{}, apierr.New(apierr.CodeTokenInvalid, "the token is not valid")
	}
	if row.ExpiresAt != nil && !now.Before(core.Micros(*row.ExpiresAt)) {
		return Principal{}, apierr.New(apierr.CodeTokenExpired, "the token has expired")
	}

	scopes, err := ParseScopesJSON(row.ScopesJson)
	if err != nil {
		// A token whose scopes are unreadable grants nothing rather than everything. Failing open
		// here would turn one bad row into a privilege escalation.
		return Principal{}, apierr.Wrap(apierr.CodeTokenInvalid, err, "the token is not valid")
	}

	member, err := a.membership(ctx, row.MembershipID)
	if err != nil {
		return Principal{}, err
	}

	tokenID, err := core.ParseID[core.APIToken](row.ID)
	if err != nil {
		return Principal{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	p := Principal{
		Kind:         KindPAT,
		MembershipID: member.id,
		CircleID:     member.circleID,
		Role:         member.role,
		DisplayName:  member.displayName,
		Scopes:       scopes,
		TokenID:      tokenID,
		TokenPrefix:  row.TokenPrefix,
	}
	if row.ExpiresAt != nil {
		p.TokenExpiresAt = core.Micros(*row.ExpiresAt)
	}
	a.touch(ctx, row, now)
	return p, nil
}

func (a *Authenticator) authenticateSession(ctx context.Context, value string) (Principal, error) {
	now := a.clock.Now()
	s, err := a.codec.Decode(value, now)
	if err != nil {
		return Principal{}, apierr.Wrap(apierr.CodeUnauthenticated, err, "the session is not valid")
	}
	member, err := a.membership(ctx, s.MembershipID)
	if err != nil {
		return Principal{}, err
	}
	return Principal{
		Kind:         KindSession,
		MembershipID: member.id,
		CircleID:     member.circleID,
		Role:         member.role,
		DisplayName:  member.displayName,
		SteppedUpAt:  s.SteppedUpAt,
	}, nil
}

// resolvedMembership is the live membership behind a credential.
type resolvedMembership struct {
	id          core.MembershipID
	circleID    core.CircleID
	role        authz.Role
	displayName string
}

// membership reads the membership row and applies the rules that are checked on EVERY request:
// it must exist, it must parse, and it must not be revoked.
func (a *Authenticator) membership(ctx context.Context, id string) (resolvedMembership, error) {
	row, err := a.db.Queries().GetMembershipByID(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNoRows) {
			// A credential bound to a membership that no longer exists is not a credential. There
			// is no delete-membership query, so this is a database somebody edited by hand.
			return resolvedMembership{}, apierr.Wrap(apierr.CodeTokenInvalid, err,
				"the credential is not valid")
		}
		return resolvedMembership{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	if row.RevokedAt != nil {
		return resolvedMembership{}, apierr.New(apierr.CodeMembershipRevoked,
			"this membership has been revoked")
	}
	if row.CircleDeletedAt != nil {
		// A credential bound to a membership in a circle that no longer exists is not a
		// credential, and it is refused HERE for the same reason a revocation is: this read
		// happens on every request, so a deleted circle stops its members acting on their very
		// next call rather than when their tokens happen to expire.
		//
		// `token_invalid` rather than `not_found`, matching the line above that answers for a
		// membership row which has gone: the fix is a new credential, not a retry. Saying the
		// circle was deleted would also tell a former member something about a circle they can no
		// longer see.
		return resolvedMembership{}, apierr.New(apierr.CodeTokenInvalid,
			"the credential is not valid")
	}

	membershipID, err := core.ParseID[core.Membership](row.ID)
	if err != nil {
		return resolvedMembership{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	circleID, err := core.ParseID[core.Circle](row.CircleID)
	if err != nil {
		return resolvedMembership{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	role, err := authz.ParseRole(row.Role)
	if err != nil {
		// A role outside the enum grants nothing. Failing open here would make a typo in a
		// migration into a privilege escalation.
		return resolvedMembership{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	return resolvedMembership{
		id: membershipID, circleID: circleID, role: role, displayName: row.DisplayName,
	}, nil
}

// touch records that a token was used, at most once every [touchInterval].
//
// Its error is logged and never returned: a request must not fail because a bookkeeping write did.
func (a *Authenticator) touch(ctx context.Context, row sqlitegen.ApiToken, now core.Micros) {
	if row.LastUsedAt != nil && now.Sub(core.Micros(*row.LastUsedAt)) < a.touchAt {
		return
	}
	err := a.db.Queries().TouchAPIToken(ctx, sqlitegen.TouchAPITokenParams{
		LastUsedAt: ptr(int64(now)), UpdatedAt: int64(now), ID: row.ID,
	})
	if err != nil {
		a.log.WarnContext(ctx, "record token use",
			slog.String("token_prefix", row.TokenPrefix), slog.Any("error", err))
	}
}

func ptr[T any](v T) *T { return &v }

// ParseScopesJSON reads `api_token.scopes_json` into the scope catalogue's own type.
//
// An unknown scope is an error rather than a value that is silently dropped: a token carrying a
// scope this binary does not know about was minted by a different binary, and quietly narrowing it
// would let a downgrade change what a token can do without saying so.
func ParseScopesJSON(raw string) ([]authz.Scope, error) {
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		return nil, fmt.Errorf("parse token scopes: %w", err)
	}
	scopes := make([]authz.Scope, 0, len(names))
	for _, n := range names {
		s, err := authz.ParseScope(n)
		if err != nil {
			return nil, err
		}
		scopes = append(scopes, s)
	}
	return scopes, nil
}

// ScopesJSON renders a scope set for storage, sorted so that two tokens with the same scopes have
// the same column value and a diff of the database is readable.
func ScopesJSON(scopes []authz.Scope) (string, error) {
	names := make([]string, 0, len(scopes))
	for _, s := range scopes {
		names = append(names, string(s))
	}
	slices.Sort(names)
	names = slices.Compact(names)
	b, err := json.Marshal(names)
	if err != nil {
		return "", fmt.Errorf("render token scopes: %w", err)
	}
	return string(b), nil
}

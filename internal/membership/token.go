package membership

import (
	"context"
	"log/slog"
	"time"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/auth"
	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// MaxTokenNameLen bounds the device name a client chooses.
const MaxTokenNameLen = 64

// Token is a freshly minted personal access token.
//
// `Secret` is the whole credential and this is the only response it appears in — the database
// holds `HMAC-SHA256(pepper, secret)` and the eight-character prefix, and the prefix is all
// anybody needs to recognise a device or trace a leak.
//
// It is a plain `string` rather than a [core.Secret], and that is deliberate at exactly this one
// place: a [core.Secret] renders as `***` on every path including `MarshalJSON`, which is the
// right default everywhere else and would make the response that HANDS a caller their credential
// useless. [Token.LogValue] is what keeps it out of a log line instead.
type Token struct {
	ID        core.APITokenID `json:"id"`
	Secret    string          `json:"token"`
	Prefix    string          `json:"token_prefix"`
	Name      string          `json:"name"`
	Scopes    []string        `json:"scopes"`
	ExpiresAt *core.Micros    `json:"expires_at"`
	CreatedAt core.Micros     `json:"created_at"`
}

// LogValue renders a token for slog with no secret in it.
//
// The eight-character prefix is loggable and is how a leaked token is traced back to the device it
// was minted for; the secret half never is. This method is what stops a struct logged whole from
// carrying one, and it is also what stops the NEXT field from being logged by whoever adds it.
func (t Token) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("id", t.ID.String()),
		slog.String("token_prefix", t.Prefix),
		slog.String("name", t.Name),
		slog.Any("scopes", t.Scopes),
	)
}

var _ slog.LogValuer = Token{}

// ParseScopes validates the scopes a client asked its token to carry.
//
// An empty list means every scope in the catalogue, and that is not a hole: effective capability
// is `role permissions ∩ token scopes`, so a token carrying every scope is still bounded by the
// role of the membership it is bound to, and no scope reaches a capability-floor permission at all.
// The narrowing a client can do here is a defence in depth it chooses, not the one that matters.
func ParseScopes(names []string) ([]authz.Scope, error) {
	if len(names) == 0 {
		out := make([]authz.Scope, 0, len(authz.Scopes()))
		for _, def := range authz.Scopes() {
			out = append(out, def.Key)
		}
		return out, nil
	}
	out := make([]authz.Scope, 0, len(names))
	for _, name := range names {
		scope, err := authz.ParseScope(name)
		if err != nil {
			return nil, apierr.Wrap(apierr.CodeValidationFailed, err,
				"scopes must come from this instance's catalogue").
				WithField("body.scopes", "names a scope this server does not know")
		}
		out = append(out, scope)
	}
	return out, nil
}

// mintToken writes a token bound to a membership.
//
// `expires_at` is left NULL by default, and that is [ADR-0005]'s consequence rather than an
// oversight: a PAT is bound to a membership and the membership is re-read on every request, so the
// token dies when the membership does. An expiry on top of that is a client's choice, not a
// substitute for revocation.
//
// [ADR-0005]: docs/adr/0005-pats-bound-to-memberships.md
func (s *Service) mintToken(
	ctx context.Context, q *sqlitegen.Queries, membershipID core.MembershipID,
	name string, scopes []authz.Scope, ttl time.Duration, now core.Micros,
) (Token, error) {
	minted, err := s.minter.Mint()
	if err != nil {
		return Token{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	scopesJSON, err := auth.ScopesJSON(scopes)
	if err != nil {
		return Token{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	id, err := core.NewID[core.APIToken](s.ids, now)
	if err != nil {
		return Token{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}

	params := sqlitegen.CreateAPITokenParams{
		ID: id.String(), MembershipID: membershipID.String(),
		TokenPrefix: minted.Prefix, TokenHash: minted.Hash,
		Name: name, ScopesJson: scopesJSON,
		CreatedAt: int64(now), UpdatedAt: int64(now),
	}
	var expiresAt *core.Micros
	if ttl > 0 {
		at := now.Add(ttl)
		expiresAt = &at
		stored := int64(at)
		params.ExpiresAt = &stored
	}
	if _, err := q.CreateAPIToken(ctx, params); err != nil {
		return Token{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}

	names := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		names = append(names, string(scope))
	}
	// The prefix is loggable and is how a leaked token is found. The secret never is.
	s.log.InfoContext(ctx, "token minted",
		"membership_id", membershipID.String(), "token_prefix", minted.Prefix)
	return Token{
		ID: id, Secret: minted.Token.Reveal(), Prefix: minted.Prefix, Name: name,
		Scopes: names, ExpiresAt: expiresAt, CreatedAt: now,
	}, nil
}

// tokenName normalises the device name a client sent, falling back to something an officer can
// still read in a token list.
func tokenName(raw string) (string, error) {
	name := trimSpace(raw)
	if name == "" {
		return "device", nil
	}
	if len([]rune(name)) > MaxTokenNameLen {
		return "", apierr.Newf(apierr.CodeValidationFailed,
			"name is %d characters; the maximum is %d", len([]rune(name)), MaxTokenNameLen).
			WithField("body.client.name", "above the maximum length")
	}
	return name, nil
}

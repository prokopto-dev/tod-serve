package api_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/authz"
)

// specOperation is one operation as it appears in the generated document.
type specOperation struct {
	Method     string
	Path       string
	ID         string                `json:"operationId"`
	Security   []map[string][]string `json:"security"`
	Permission struct {
		Kind           string `json:"kind"`
		RequiresStepUp bool   `json:"requires_step_up"`
		AnyOf          []struct {
			Key    string   `json:"key"`
			Scopes []string `json:"scopes"`
		} `json:"any_of"`
	} `json:"x-tod-permission"`
	Scopes       []string `json:"x-tod-scopes"`
	AnyScope     bool     `json:"x-tod-any-scope"`
	SessionOnly  bool     `json:"x-tod-session-only"`
	CircleScoped bool     `json:"x-tod-circle-scoped"`
	CreatesState bool     `json:"x-tod-creates-state"`
	Idempotency  string   `json:"x-tod-idempotency"`
	IfMatch      bool     `json:"x-tod-if-match-required"`
}

// specDocument is enough of the document for the lints below.
type specDocument struct {
	Paths      map[string]map[string]specPathOperation `json:"paths"`
	Components struct {
		Schemas         map[string]json.RawMessage `json:"schemas"`
		SecuritySchemes map[string]json.RawMessage `json:"securitySchemes"`
	} `json:"components"`
}

// specPathOperation keeps the raw operation alongside its responses, so one pass over the document
// serves both the extension lints and the response-shape one.
type specPathOperation struct {
	Raw       json.RawMessage
	Responses map[string]struct {
		Content map[string]struct {
			Schema struct {
				Ref string `json:"$ref"`
			} `json:"schema"`
		} `json:"content"`
	} `json:"responses"`
}

// UnmarshalJSON keeps the whole operation as well as the decoded half.
func (o *specPathOperation) UnmarshalJSON(b []byte) error {
	o.Raw = append([]byte(nil), b...)
	type plain specPathOperation
	var decoded plain
	if err := json.Unmarshal(b, &decoded); err != nil {
		return err
	}
	o.Responses = decoded.Responses
	return nil
}

func loadSpec(t *testing.T) (specDocument, []specOperation) {
	t.Helper()
	raw, err := api.SpecJSON()
	require.NoError(t, err)

	var doc specDocument
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.NotEmpty(t, doc.Paths, "the document describes no operations")

	var ops []specOperation
	for path, item := range doc.Paths {
		for method, body := range item {
			var op specOperation
			require.NoError(t, json.Unmarshal(body.Raw, &op))
			op.Method, op.Path = strings.ToUpper(method), path
			ops = append(ops, op)
		}
	}
	return doc, ops
}

// The spec lint docs/concepts/invariants.md names: every operation declares an explicit
// lowerCamelCase `operationId`, a `Security` requirement and `x-tod-permission`.
//
// `operationId` is never auto-derived and never renamed — a generated SDK's method names come from
// it, so a rename breaks clients even when the HTTP surface is unchanged.
func TestSpec_EveryOperation_DeclaresAnOperationIDSecurityAndPermission(t *testing.T) {
	t.Parallel()
	_, ops := loadSpec(t)
	require.NotEmpty(t, ops)

	lowerCamel := regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
	for _, op := range ops {
		require.NotEmpty(t, op.ID, "%s %s has no operationId", op.Method, op.Path)
		require.Regexp(t, lowerCamel, op.ID, "%s is not lowerCamelCase", op.ID)
		require.NotEmpty(t, op.Security, "%s declares no security requirement", op.ID)
		require.NotEmpty(t, op.Permission.Kind, "%s declares no x-tod-permission", op.ID)
	}
}

// The document, the registry and the middleware are one fact. The middleware reads the registry, so
// this is what proves the document a client generates against says the same thing.
func TestSpec_Extensions_MatchTheRouteRegistry(t *testing.T) {
	t.Parallel()
	_, ops := loadSpec(t)

	for _, op := range ops {
		route, ok := api.Lookup(api.OperationID(op.ID))
		require.True(t, ok, "the document describes %s, which is not in the registry", op.ID)

		require.Equal(t, route.Method, op.Method, "%s", op.ID)
		require.Equal(t, route.FullPath(), op.Path, "%s", op.ID)
		require.Equal(t, string(route.Auth), op.Permission.Kind, "%s", op.ID)
		require.Equal(t, route.RequiresStepUp(), op.Permission.RequiresStepUp, "%s", op.ID)
		require.Equal(t, route.SessionOnly(), op.SessionOnly, "%s", op.ID)
		require.Equal(t, route.CircleScoped, op.CircleScoped, "%s", op.ID)
		require.Equal(t, route.CreatesState, op.CreatesState, "%s", op.ID)
		require.Equal(t, string(route.Idempotency), op.Idempotency, "%s", op.ID)
		require.Equal(t, route.IfMatch, op.IfMatch, "%s", op.ID)
		require.Equal(t, route.AnyScope, op.AnyScope, "%s", op.ID)

		want := make([]string, 0, len(route.Scopes))
		for _, s := range route.Scopes {
			want = append(want, string(s))
		}
		require.Equal(t, want, op.Scopes, "%s: declared scopes", op.ID)

		names := make([]string, 0, len(op.Permission.AnyOf))
		for _, p := range op.Permission.AnyOf {
			names = append(names, p.Key)
		}
		got := make([]string, 0, len(route.Permissions))
		for _, p := range route.Permissions {
			got = append(got, string(p))
		}
		require.Equal(t, got, names, "%s: declared permissions", op.ID)
	}
}

// A capability-floor operation must not OFFER the bearer scheme. The security requirement is the
// published half of "no token reaches the floor at any scope": a client generator reads it, and a
// document that offered a token there would be advertising a door the middleware then refuses.
func TestSpec_AFloorOperation_OffersOnlyTheSessionScheme(t *testing.T) {
	t.Parallel()
	_, ops := loadSpec(t)
	floor := 0
	for _, op := range ops {
		route, ok := api.Lookup(api.OperationID(op.ID))
		require.True(t, ok)
		if !route.SessionOnly() {
			continue
		}
		floor++
		for _, requirement := range op.Security {
			_, offersBearer := requirement[api.SchemeBearer]
			require.False(t, offersBearer,
				"%s is session-only and its security offers %s", op.ID, api.SchemeBearer)
		}
	}
	require.Positive(t, floor, "no session-only operation is documented; the filter is wrong")
}

// The security schemes are exactly four, and the absence of a fifth is the point: `Authorization:
// Bearer` and the session cookie are the only API credentials, and the other two are environment
// variables reaching one operational surface each — the metrics listener and first-run setup.
func TestSpec_SecuritySchemes_AreTheFiveThatExist(t *testing.T) {
	t.Parallel()
	doc, _ := loadSpec(t)
	require.Len(t, doc.Components.SecuritySchemes, 5)
	for _, name := range []string{
		api.SchemeBearer, api.SchemeSession, api.SchemeMetricsToken, api.SchemeSetupToken,
		// The fifth authenticates a SENDER rather than a principal: an interaction signature says
		// the payload is Discord's, and who typed the command is a fact inside it. ADR-0017.
		api.SchemeDiscordSignature,
	} {
		require.Contains(t, doc.Components.SecuritySchemes, name)
	}
}

// A schema that reflection could not describe comes out as an empty object, which documents
// nothing and generates a useless SDK type. It is also exactly the shape a [core.Secret] would take
// if one ever reached a response body, which is why this is a gate rather than a nicety.
func TestSpec_NoSchema_IsAnEmptyObject(t *testing.T) {
	t.Parallel()
	doc, _ := loadSpec(t)
	require.NotEmpty(t, doc.Components.Schemas)

	for name, raw := range doc.Components.Schemas {
		var schema struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
			Enum       []json.RawMessage          `json:"enum"`
			Items      json.RawMessage            `json:"items"`
		}
		require.NoError(t, json.Unmarshal(raw, &schema))
		if schema.Type != "object" {
			continue
		}
		require.NotEmpty(t, schema.Properties,
			"schema %s is an empty object: reflection could not describe the Go type behind it, "+
				"so it needs an entry in registerSchemaAliases", name)
	}
}

// The error schema publishes the closed enum, so a client can generate the branch it takes on
// `code` rather than comparing strings it copied out of a document.
func TestSpec_TheProblemSchema_PublishesTheClosedCodeEnum(t *testing.T) {
	t.Parallel()
	doc, _ := loadSpec(t)
	raw, ok := doc.Components.Schemas["Problem"]
	require.True(t, ok, "the document has no Problem schema")

	var schema struct {
		Properties struct {
			Code struct {
				Enum []string `json:"enum"`
			} `json:"code"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &schema))
	require.NotEmpty(t, schema.Properties.Code.Enum)
	require.Contains(t, schema.Properties.Code.Enum, "membership_revoked")
	require.Contains(t, schema.Properties.Code.Enum, "unauthenticated")
}

// Hidden operations are absent from `paths`, which is what makes them hidden — and is also why
// [api.Server.Registered] rather than the document is the honest answer to "what is served".
func TestSpec_HiddenOperations_AreAbsentFromThePaths(t *testing.T) {
	t.Parallel()
	_, ops := loadSpec(t)
	for _, op := range ops {
		route, ok := api.Lookup(api.OperationID(op.ID))
		require.True(t, ok)
		require.False(t, route.Hidden, "%s is hidden and appears in the document", op.ID)
	}
}

// Every scope named in a security requirement is in the catalogue. A generated SDK would otherwise
// ask a user for a scope no token can carry.
func TestSpec_EveryDeclaredScope_IsInTheCatalogue(t *testing.T) {
	t.Parallel()
	_, ops := loadSpec(t)
	for _, op := range ops {
		for _, requirement := range op.Security {
			for _, scopes := range requirement {
				for _, s := range scopes {
					_, ok := authz.LookupScope(authz.Scope(s))
					require.True(t, ok, "%s declares scope %s, which is not in the catalogue",
						op.ID, s)
				}
			}
		}
	}
}

// Canonical §1: every response carries a top-level `as_of`, and every timestamp in it is read
// against that rather than against the reader's own clock.
//
// This is a gate rather than a habit because the rule is invisible at the call site — a handler
// that returns a perfectly good representation without one looks finished. It was in fact missed on
// `listMyTokens` and `revokeToken`, both of which carry `expires_at`, which is exactly the field an
// overlay on a machine with a fast clock renders wrong on screen and right in the database.
func TestSpec_EverySuccessResponse_CarriesAsOf(t *testing.T) {
	t.Parallel()
	doc, _ := loadSpec(t)

	checked := 0
	for path, item := range doc.Paths {
		for method, op := range item {
			for status, response := range op.Responses {
				if status < "200" || status >= "300" {
					continue
				}
				content, ok := response.Content[api.MediaTypeJSON]
				if !ok || content.Schema.Ref == "" {
					continue
				}
				name := strings.TrimPrefix(content.Schema.Ref, "#/components/schemas/")
				raw, found := doc.Components.Schemas[name]
				require.True(t, found, "%s %s: schema %s is not in the document", method, path, name)

				var schema struct {
					Properties map[string]json.RawMessage `json:"properties"`
				}
				require.NoError(t, json.Unmarshal(raw, &schema))
				require.Contains(t, schema.Properties, "as_of",
					"%s %s answers %s with %s, which carries no top-level as_of; "+
						"canonical §1 says every response does, and every timestamp in it is read "+
						"against that rather than against the caller's own clock",
					strings.ToUpper(method), path, status, name)
				checked++
			}
		}
	}
	require.Positive(t, checked, "no success responses were checked; the walk is wrong")
}

// TestSpec_EveryConditionalRead_Documents304 keeps the contract and the behaviour together.
//
// A conditional read returns its `304` through a dynamic `Status` field, and the framework derives
// documented responses from the output STRUCT rather than from what a handler assigns — so the
// response is real and, without [api.responsesFor], absent from the document. A generated client
// then treats a genuine 304 as an undocumented error, which is precisely the failure the ETag was
// added to avoid.
//
// It is compared in BOTH directions. A route that revalidates and documents nothing is the bug
// above; a route that documents a 304 it never emits is a client waiting for a response that
// cannot arrive, which is the same lie facing the other way.
func TestSpec_EveryConditionalRead_Documents304(t *testing.T) {
	t.Parallel()
	doc, _ := loadSpec(t)

	conditional := map[string]bool{}
	for _, route := range api.Routes() {
		if route.ConditionalRead {
			conditional[string(route.ID)] = true
		}
	}
	require.NotEmpty(t, conditional, "no route revalidates; the filter is wrong")

	documented := map[string]bool{}
	for path, item := range doc.Paths {
		for method, body := range item {
			var op specOperation
			require.NoError(t, json.Unmarshal(body.Raw, &op))
			response, has304 := body.Responses["304"]
			if !has304 {
				continue
			}
			documented[op.ID] = true
			require.Empty(t, response.Content,
				"%s %s documents a 304 with a body; a 304 carries none", method, path)
		}
	}

	for id := range conditional {
		require.True(t, documented[id],
			"%s revalidates and answers a real 304, and the document does not describe one. A "+
				"generated client treats it as an undocumented error", id)
	}
	for id := range documented {
		require.True(t, conditional[id],
			"%s documents a 304 and carries no ConditionalRead, so nothing answers one; a client "+
				"would wait for a response that cannot arrive", id)
	}
}

// TestSpec_ADocumented304_CarriesTheETagHeader. A 304 without the tag is a dead end: the client has
// nothing to send on the next revalidation and falls back to unconditional reads, which is the cost
// the whole mechanism exists to avoid.
func TestSpec_ADocumented304_CarriesTheETagHeader(t *testing.T) {
	t.Parallel()
	raw, err := api.SpecJSON()
	require.NoError(t, err)

	var doc struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Headers map[string]json.RawMessage `json:"headers"`
			} `json:"responses"`
		} `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))

	checked := 0
	for path, item := range doc.Paths {
		for method, op := range item {
			response, has304 := op.Responses["304"]
			if !has304 {
				continue
			}
			require.Contains(t, response.Headers, api.ETagHeader,
				"%s %s answers 304 without an ETag; the client has nothing to revalidate with next "+
					"time and gives up on conditional requests", method, path)
			checked++
		}
	}
	require.Positive(t, checked, "no 304 was checked; the walk is wrong")
}

// TestSpec_ARouteThatRevalidates_AlsoReturnsAnETag. `ConditionalRead` without `ETag` is a route
// that compares against a tag it never gave anybody.
func TestSpec_ARouteThatRevalidates_AlsoReturnsAnETag(t *testing.T) {
	t.Parallel()
	for _, route := range api.Routes() {
		if !route.ConditionalRead {
			continue
		}
		require.True(t, route.ETag,
			"%s revalidates and returns no ETag, so there is no tag for a caller to send back",
			route.ID)
	}
}

package core_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/core"
)

// theSecret is a value that would be unmistakable in output. If it appears anywhere below, a token
// appears in a log.
const theSecret = "tods_pat_do_not_log_me_0123456789"

// config stands in for the real configuration struct: a whole value, marshalled whole, with a
// secret nested inside it. The invariant is about what happens to that value, not about what
// happens when somebody remembers to redact a field.
type config struct {
	PublicURL   string      `json:"public_url"`
	MetricsUser string      `json:"metrics_user"`
	Token       core.Secret `json:"token"`
	Nested      struct {
		ClientSecret core.Secret `json:"client_secret"`
	} `json:"nested"`
}

func TestSecret_NeverRendered(t *testing.T) {
	t.Parallel()
	s := core.Secret(theSecret)

	// The three paths the invariant names, plus the ones fmt provides whether or not anybody
	// remembers they exist.
	for _, tc := range []struct {
		name string
		got  string
	}{
		{"String", s.String()},
		{"GoString", s.GoString()},
		{"%s", fmt.Sprintf("%s", s)},
		{"%v", fmt.Sprintf("%v", s)},
		{"%q", fmt.Sprintf("%q", s)},
		{"%#v", fmt.Sprintf("%#v", s)},
		{"%+v", fmt.Sprintf("%+v", s)},
		// A string type under a numeric verb prints its contents in the error text. This is the
		// verb somebody gets wrong at three in the morning.
		{"%d", fmt.Sprintf("%d", s)},
		{"%x", fmt.Sprintf("%x", s)},
		{"print", fmt.Sprint(s)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.NotContains(t, tc.got, theSecret)
			require.Contains(t, tc.got, "***")
		})
	}
}

func TestSecret_NestedInAStruct_NeverRendered(t *testing.T) {
	t.Parallel()
	var cfg config
	cfg.PublicURL = "https://tod.example"
	cfg.MetricsUser = "prometheus"
	cfg.Token = core.Secret(theSecret)
	cfg.Nested.ClientSecret = core.Secret(theSecret)

	encoded, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), theSecret)
	require.JSONEq(t, `{
		"public_url": "https://tod.example",
		"metrics_user": "prometheus",
		"token": "***",
		"nested": {"client_secret": "***"}
	}`, string(encoded))

	// fmt reaches an exported field's methods, so the whole value redacts too.
	require.NotContains(t, fmt.Sprintf("%+v", cfg), theSecret)
	require.NotContains(t, fmt.Sprintf("%#v", cfg), theSecret)

	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("config loaded", "config", cfg, "token", cfg.Token)
	require.NotContains(t, buf.String(), theSecret)
	require.Contains(t, buf.String(), "***")

	buf.Reset()
	slog.New(slog.NewTextHandler(&buf, nil)).Info("config loaded", "config", cfg, "token", cfg.Token)
	require.NotContains(t, buf.String(), theSecret)
}

func TestSecret_Reveal_IsTheOnlyWayOut(t *testing.T) {
	t.Parallel()
	s := core.Secret(theSecret)
	require.Equal(t, theSecret, s.Reveal())
	require.True(t, core.Secret("").IsZero())
	require.False(t, s.IsZero())
}

func TestSecret_Equal_ComparesWithoutRevealing(t *testing.T) {
	t.Parallel()
	s := core.Secret(theSecret)
	require.True(t, s.Equal(core.Secret(theSecret)))
	require.False(t, s.Equal(core.Secret(theSecret+"x")))
	require.False(t, s.Equal(""))
	require.True(t, core.Secret("").Equal(""))
}

func TestSecret_UnmarshalJSON_TheRedaction_IsRefused(t *testing.T) {
	t.Parallel()
	var cfg config

	require.NoError(t, json.Unmarshal([]byte(`{"token":"`+theSecret+`"}`), &cfg))
	require.Equal(t, theSecret, cfg.Token.Reveal())

	// A Secret does not round-trip, and a config write path built on marshal-then-unmarshal must
	// fail loudly rather than persist `***` as somebody's token.
	err := json.Unmarshal([]byte(`{"token":"***"}`), &cfg)
	require.ErrorIs(t, err, core.ErrSecretRedacted)

	require.Error(t, json.Unmarshal([]byte(`{"token":42}`), &cfg))
}

// The hole this type cannot close, pinned so that it is a known limit rather than a surprise:
// fmt prints an unexported field by reflection without consulting its methods. Configuration
// structs carry exported fields because they are decoded from JSON and the environment, so the
// shape that leaks is not one this codebase writes — but nothing stops someone writing it.
func TestSecret_InAnUnexportedField_IsNotProtectedByFmt(t *testing.T) {
	t.Parallel()
	type leaky struct {
		Name  string
		token core.Secret
	}

	rendered := fmt.Sprintf("%+v", leaky{Name: "discord", token: core.Secret(theSecret)})
	require.Contains(t, rendered, theSecret,
		"if fmt has learned to consult methods on unexported fields, delete this test and the "+
			"caveat on core.Secret")

	// json.Marshal ignores unexported fields entirely, so the wire is unaffected either way.
	encoded, err := json.Marshal(leaky{Name: "discord", token: core.Secret(theSecret)})
	require.NoError(t, err)
	require.NotContains(t, string(encoded), theSecret)
	require.False(t, strings.Contains(string(encoded), "token"))
}

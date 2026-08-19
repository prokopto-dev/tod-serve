package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The regression this file exists for: `Accept: */*` is what curl sends by default and what most
// HTTP clients send, and it admits everything. Reading it as "admits nothing" 406s every route.
//
// The framework's own negotiator is an exact string match against its format keys, so `*/*` is
// never one of them. It survives that by falling back to JSON; a check that reuses it and treats
// the empty answer as unacceptable does not.
func TestAcceptable_MediaRanges_AreMatchedByRFC9110Precedence(t *testing.T) {
	t.Parallel()
	json := []string{MediaTypeJSON}
	text := []string{MediaTypePlainText}

	cases := []struct {
		name   string
		accept string
		served []string
		want   bool
	}{
		{"the wildcard curl sends", "*/*", json, true},
		{"the wildcard, on the metrics listener", "*/*", text, true},
		{"a type wildcard", "application/*", json, true},
		{"a type wildcard for another type", "text/*", json, false},
		{"an exact match", "application/json", json, true},
		{"an exact match with parameters", "application/json; charset=utf-8", json, true},
		{"a type we do not produce", "application/xml", json, false},
		{"plain text against a JSON endpoint", "text/plain", json, false},
		{"plain text against the metrics endpoint", "text/plain", text, true},
		{"a versioned exposition, as Prometheus writes it", "text/plain;version=0.0.4;q=0.4", text, true},
		{
			"the stock Prometheus header, on the metrics listener",
			"application/openmetrics-text;version=1.0.0;q=0.5,text/plain;version=0.0.4;q=0.4,*/*;q=0.1",
			text, true,
		},
		{
			"the stock Prometheus header, on the API listener",
			"application/openmetrics-text;version=1.0.0;q=0.5,text/plain;version=0.0.4;q=0.4,*/*;q=0.1",
			json, true,
		},
		{"a browser's header", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", json, true},
		{"several types, one of which we serve", "text/html, application/json", json, true},
		{"whitespace and casing", "  APPLICATION/JSON ", json, true},

		// q=0 is the one way a client says "not this", and it has to be honoured or the header
		// means less than it says.
		{"the type explicitly refused", "application/json;q=0", json, false},
		{"the wildcard explicitly refused", "*/*;q=0", json, false},
		{"a low but non-zero weight is still acceptance", "*/*;q=0.001", json, true},
		{
			// The wildcard does NOT rescue it: the exact range is more specific, so it decides for
			// this type, and it is the only type served.
			"the only type served, explicitly refused beside a wildcard",
			"application/json;q=0, */*", json, false,
		},
		{
			"the metrics type, explicitly refused beside a wildcard",
			"text/plain;q=0, */*", text, false,
		},

		// A malformed entry must not take the whole header down with it: one stray comma from one
		// client must not be answered with "this server produces nothing you want".
		{"a stray comma", "application/json,,", json, true},
		{"an entry with no subtype", "application, */*", json, true},
		{"an unreadable weight falls back to 1", "application/json;q=banana", json, true},
		{"nothing parseable at all", "garbage", json, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, acceptable(tc.accept, tc.served),
				"Accept: %q against %v", tc.accept, tc.served)
		})
	}
}

// The exact type must beat the wildcard for that type alone, or `text/plain;q=0, */*` cannot mean
// what RFC 9110 says it means.
func TestAcceptable_TheMoreSpecificRange_Wins(t *testing.T) {
	t.Parallel()
	require.False(t, acceptable("application/json;q=0, */*", []string{MediaTypeJSON}))
	require.True(t, acceptable("application/json;q=0, */*", []string{MediaTypePlainText}))
}

// The two listeners produce different things, and each must say so for itself.
func TestMediaTypes_TheTwoListeners_ProduceDifferentThings(t *testing.T) {
	t.Parallel()
	require.Equal(t, []string{MediaTypeJSON}, apiMediaTypes())
	require.Equal(t, []string{MediaTypePlainText}, metricsMediaTypes())
	require.NotEqual(t, apiMediaTypes(), metricsMediaTypes(),
		"one shared list is what refused every scraper")
}

func TestParseQuality_ReadsWhatRFC9110Permits(t *testing.T) {
	t.Parallel()
	cases := []struct {
		params string
		want   int
	}{
		{"", qScale},
		{"charset=utf-8", qScale},
		{"q=1", qScale},
		{"q=1.0", qScale},
		{"q=0", 0},
		{"q=0.0", 0},
		{"q=0.5", 500},
		{"q=0.001", 1},
		{"version=0.0.4; q=0.4", 400},
		{"Q=0.25", 250},
		{"q=7", qScale},
		{"q=banana", qScale},
	}
	for _, tc := range cases {
		t.Run(tc.params, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, parseQuality(tc.params))
		})
	}
}

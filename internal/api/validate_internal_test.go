package api

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
)

// The response validator runs across every request the integration suite makes. A checker nobody
// has seen fail is a checker nobody knows works, so each rule it holds is driven with a response
// that breaks it.
func TestCheckResponse_AContractViolation_IsReported(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		status      int
		contentType string
		body        string
	}{
		{
			name: "a 200 carrying a problem", status: http.StatusOK,
			contentType: "application/json",
			body:        `{"code":"not_found","type":"https://docs.tod-serve.org/errors/not_found"}`,
		},
		{
			name: "an error that is not problem+json", status: http.StatusNotFound,
			contentType: "application/json",
			body: `{"code":"not_found","type":"https://docs.tod-serve.org/errors/not_found",` +
				`"status":404,"title":"Not found"}`,
		},
		{
			name: "a code outside the closed enum", status: http.StatusNotFound,
			contentType: apierr.ContentType,
			body: `{"code":"nope","type":"https://docs.tod-serve.org/errors/nope",` +
				`"status":404,"title":"Not found"}`,
		},
		{
			name: "a type whose last segment is not the code", status: http.StatusNotFound,
			contentType: apierr.ContentType,
			body: `{"code":"not_found","type":"https://docs.tod-serve.org/errors/other",` +
				`"status":404,"title":"Not found"}`,
		},
		{
			name: "a status that disagrees with the body", status: http.StatusNotFound,
			contentType: apierr.ContentType,
			body: `{"code":"not_found","type":"https://docs.tod-serve.org/errors/not_found",` +
				`"status":500,"title":"Not found"}`,
		},
		{
			name: "a problem with no title", status: http.StatusNotFound,
			contentType: apierr.ContentType,
			body:        `{"code":"not_found","type":"https://docs.tod-serve.org/errors/not_found","status":404}`,
		},
		{
			name: "a 204 with a body", status: http.StatusNoContent,
			contentType: "application/json", body: `{"anything":1}`,
		},
		{
			name: "an error body that does not parse", status: http.StatusNotFound,
			contentType: apierr.ContentType, body: `{`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := checkResponse(http.MethodGet, "/x", tc.status, tc.contentType, []byte(tc.body))
			require.NotEmpty(t, got, "the validator did not fire on %s", tc.name)
		})
	}
}

// The validator must not fire on a response that is correct, or the suite it guards becomes noise
// somebody eventually turns off.
func TestCheckResponse_AConformingResponse_IsNotReported(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		status      int
		contentType string
		body        string
	}{
		{
			name: "a well-formed problem", status: http.StatusNotFound,
			contentType: apierr.ContentType,
			body: `{"code":"not_found","type":"https://docs.tod-serve.org/errors/not_found",` +
				`"status":404,"title":"Not found"}`,
		},
		{
			name: "an ordinary representation", status: http.StatusOK,
			contentType: "application/json", body: `{"items":[],"has_more":false}`,
		},
		{
			name: "a 304 with no body", status: http.StatusNotModified,
			contentType: "", body: "",
		},
		{
			name: "a stream, which has no single body to check", status: http.StatusOK,
			contentType: "text/event-stream", body: "data: anything\n\n",
		},
		{
			name: "a plain-text body such as the metrics exposition", status: http.StatusOK,
			contentType: "text/plain; version=0.0.4", body: "tod_build_info{version=\"x\"} 1\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := checkResponse(http.MethodGet, "/x", tc.status, tc.contentType, []byte(tc.body))
			require.Empty(t, got, "%v", got)
		})
	}
}

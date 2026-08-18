package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
)

// Violation is one response that broke the response contract.
type Violation struct {
	// Method and Path identify the request.
	Method string
	Path   string
	// Status is what was answered.
	Status int
	// Reason says what was wrong, in a sentence a failing test can print.
	Reason string
	// Body is what was sent, so a failure shows the response rather than describing it.
	Body string
}

// String renders a violation for a test failure.
func (v Violation) String() string {
	return fmt.Sprintf("%s %s -> %d: %s\n%s", v.Method, v.Path, v.Status, v.Reason, v.Body)
}

// validateResponses checks every response against the rules canonical §7 states, and reports each
// violation to report.
//
// It runs across the whole integration suite rather than in production. The rules it checks are
// held in production by the code that writes the responses — there is exactly one problem type and
// one place that renders it — and this is what proves that claim on every request the tests make,
// including the ones the framework answers before any handler runs.
//
// What it checks, and why each one:
//
//   - A failure carries `application/problem+json`. A client that has to guess whether a body is a
//     representation or an error is a client that eventually guesses wrong.
//   - Its `code` is in the closed enum, its `type` ends in that code, and its `status` matches the
//     HTTP status. The `type` URL's last segment IS the code, so a mismatch ships a broken link.
//   - **Never HTTP 200 with an error body.** A success that carries a problem is the failure mode
//     that turns a broken write into a client that reports success.
func validateResponses(next http.Handler, report func(Violation)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &responseRecorder{header: http.Header{}, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		for _, v := range checkResponse(r.Method, r.URL.Path, rec.status,
			rec.header.Get("Content-Type"), rec.body.Bytes()) {
			report(v)
		}
		rec.flushTo(w)
	})
}

// checkResponse is the rule set, as a pure function, so a test can drive it with a response it
// builds by hand rather than only with one a handler happened to produce.
func checkResponse(method, path string, status int, contentType string, body []byte) []Violation {
	var out []Violation
	add := func(reason string) {
		out = append(out, Violation{
			Method: method, Path: path, Status: status, Reason: reason, Body: string(body),
		})
	}

	if strings.HasPrefix(contentType, "text/event-stream") {
		// A stream is not a document and has no single body to check.
		return nil
	}
	if status == http.StatusNoContent || status == http.StatusNotModified {
		if len(bytes.TrimSpace(body)) > 0 {
			add("a " + http.StatusText(status) + " carries a body")
		}
		return out
	}

	if status >= http.StatusBadRequest {
		if !strings.HasPrefix(contentType, apierr.ContentType) {
			add("an error response must be " + apierr.ContentType + ", not " + contentType)
			return out
		}
		var problem apierr.Problem
		if err := json.Unmarshal(body, &problem); err != nil {
			add("the problem body does not parse: " + err.Error())
			return out
		}
		if _, ok := apierr.Lookup(problem.Code); !ok {
			add("code " + string(problem.Code) + " is not in the closed enum")
		}
		if problem.Status != status {
			add(fmt.Sprintf("the problem says status %d and the response is %d",
				problem.Status, status))
		}
		if want := problem.Code.TypeURL(); problem.Type != want {
			add("type is " + problem.Type + ", and the last segment must be the code: " + want)
		}
		if problem.Title == "" {
			add("the problem carries no title")
		}
		return out
	}

	if !strings.HasPrefix(contentType, MediaTypeJSON) || len(bytes.TrimSpace(body)) == 0 {
		return out
	}
	var maybe struct {
		Code string `json:"code"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &maybe); err != nil {
		// A 2xx body that is not an object is legal — a list operation returns an envelope, but a
		// future one might not — and it certainly is not an error body.
		return out
	}
	if _, isCode := apierr.Lookup(apierr.Code(maybe.Code)); isCode && maybe.Type != "" {
		add("a success response carries an error body: never HTTP 200 with a problem in it")
	}
	return out
}

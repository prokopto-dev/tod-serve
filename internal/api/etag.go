package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
)

// IfMatchHeader and the two beside it are named rather than spelled at each call site, because a
// header compared by a literal in four places is a header misspelled in one of them.
const (
	IfMatchHeader     = "If-Match"
	IfNoneMatchHeader = "If-None-Match"
	ETagHeader        = "ETag"
)

// anyETag is the wildcard: `If-Match: *` means "the resource must exist", which it does by the
// time a handler is computing its tag.
const anyETag = "*"

// ETagOf returns a strong entity tag for a representation.
//
// It is the hash of the rendered JSON rather than a version column, deliberately: a version column
// is a second thing to remember to bump, and a tag derived from what was actually sent cannot
// disagree with it. The cost is that the representation is marshalled twice on a write path, which
// at a few hundred rows a week is not a cost.
func ETagOf[T any](v T) (string, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("compute etag: %w", err)
	}
	sum := sha256.Sum256(body)
	return `"` + base64.RawURLEncoding.EncodeToString(sum[:]) + `"`, nil
}

// MatchesIfNoneMatch reports whether a client's cached copy is still current, so a read can answer
// 304 instead of a body.
func MatchesIfNoneMatch(header, etag string) bool {
	if header == "" || etag == "" {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == anyETag || candidate == etag {
			return true
		}
	}
	return false
}

// RequireIfMatch enforces the concurrency rule on a state transition: the caller must say which
// version they read, and it must still be the current one.
//
// The 412 carries the CURRENT representation in `meta.current`, so the read-merge-retry round trip
// costs no extra request and a client can show the user what actually changed rather than "please
// try again". An optional check is one nobody sends, so a missing header is 428 rather than a
// silent write.
func RequireIfMatch[T any](header string, current T) error {
	header = strings.TrimSpace(header)
	if header == "" {
		return apierr.New(apierr.CodePreconditionRequired,
			"this operation overwrites state; read the resource and send its ETag in If-Match").
			WithField("header.If-Match", "required")
	}
	etag, err := ETagOf(current)
	if err != nil {
		return apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	if MatchesIfNoneMatch(header, etag) {
		return nil
	}
	body, err := json.Marshal(current)
	if err != nil {
		return apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	return apierr.New(apierr.CodePreconditionFailed,
		"the resource has changed since the ETag you sent").
		WithCurrent(body)
}

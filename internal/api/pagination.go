package api

import (
	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

// The pagination bounds. Cursor only, in the body envelope: never a `Link` header, never an offset.
const (
	// DefaultLimit is how many items a collection returns when the caller does not say.
	DefaultLimit = 50
	// MaxLimit is the most a caller may ask for.
	MaxLimit = 200
)

// Page is the collection envelope every list operation returns.
//
// It is a body envelope rather than `Link` headers because a client that has to parse a header to
// paginate is a client that eventually does not paginate. `has_more` is explicit rather than
// inferred from an empty cursor: "no cursor" and "no more items" are the same thing today and a
// client that assumed so would break the first time they were not.
type Page[T any] struct {
	// Items are this page's rows, in cursor order.
	Items []T `json:"items"`
	// NextCursor is the cursor to pass to read the next page, empty when there is none.
	NextCursor string `json:"next_cursor"`
	// HasMore says whether another page exists.
	HasMore bool `json:"has_more"`
	// AsOf is the instant this page was computed, from the injected clock.
	//
	// Canonical §1: every response carries one, and every timestamp in it is read against this
	// rather than against the reader's own clock. A page of tokens carries `expires_at`, and a
	// machine whose clock is four minutes fast would otherwise render "expired" for a token that
	// is not — wrong on screen and right in the database, which is the worst available combination.
	AsOf core.Micros `json:"as_of"`
}

// NewPage builds an envelope from a slice already trimmed to the page size, the cursor of its last
// row, and the instant it was computed.
func NewPage[T any](items []T, nextCursor string, hasMore bool, asOf core.Micros) Page[T] {
	if items == nil {
		// An empty collection is `[]`, never `null`. A client that special-cases null is a client
		// somebody had to debug.
		items = []T{}
	}
	if !hasMore {
		nextCursor = ""
	}
	return Page[T]{Items: items, NextCursor: nextCursor, HasMore: hasMore, AsOf: asOf}
}

// NormaliseLimit validates a caller's `limit`.
//
// A limit above the maximum is REFUSED rather than clamped. Clamping would return fewer rows than
// the caller asked for without saying so, and a client that read the short page as "the end of the
// collection" would silently drop rows — which is the failure this codebase is built against.
func NormaliseLimit(limit int) (int, error) {
	switch {
	case limit == 0:
		return DefaultLimit, nil
	case limit < 0:
		return 0, apierr.Newf(apierr.CodeValidationFailed, "limit must be positive").
			WithField("query.limit", "must be positive")
	case limit > MaxLimit:
		return 0, apierr.Newf(apierr.CodeValidationFailed,
			"limit is %d; the maximum is %d", limit, MaxLimit).
			WithField("query.limit", "above the maximum")
	default:
		return limit, nil
	}
}

// ParseCursor validates a cursor. It is a ULID: identifiers here are time-ordered, so a row's own
// id is its cursor and there is no separate encoding to keep in step with the sort order.
//
// It is still documented as opaque. A client that starts deriving meaning from it — a timestamp, a
// count — is a client that breaks when the sort key changes.
func ParseCursor(cursor string) (core.ULID, error) {
	if cursor == "" {
		return core.ULID{}, nil
	}
	u, err := core.ParseULID(cursor)
	if err != nil {
		return core.ULID{}, apierr.Wrap(apierr.CodeValidationFailed, err, "cursor is not valid").
			WithField("query.cursor", "not a valid cursor")
	}
	return u, nil
}

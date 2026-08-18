package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Micros is a Unix timestamp in microseconds, UTC. It is the only time type that appears in a
// domain signature, and the only thing stored in an `_at` column.
//
// A plain int64 would do the same arithmetic. The named type is here so that a value read out of
// a column, a value parsed off the wire and a value computed by the consensus derivation are the
// same type, and so that "seconds or milliseconds?" is never a question anybody has to ask about a
// number in this repository.
type Micros int64

// MicrosPerSecond is the conversion the whole project uses. Spelled once, because a stray factor
// of a thousand in a window boundary is a bug that looks like a plausible answer.
const MicrosPerSecond = int64(time.Second / time.Microsecond)

// timestampLayout is RFC 3339 with exactly six fractional digits. The digits are fixed rather than
// trimmed so every timestamp on the wire has the same width: a client that string-compares two
// timestamps — and one will — must not get a different answer than a client that parses them.
const timestampLayout = "2006-01-02T15:04:05.000000Z07:00"

// ErrInvalidTimestamp is returned for anything that is not RFC 3339 with an offset.
var ErrInvalidTimestamp = errors.New("invalid timestamp")

// MicrosFromTime converts a time.Time, discarding anything finer than a microsecond. Storage is
// microseconds, so a caller that hands over nanoseconds is not asking for them to be kept; the
// alternative — refusing the value — would reject every Go client, since time.Time marshals as
// RFC 3339 with nanoseconds by default.
func MicrosFromTime(t time.Time) Micros { return Micros(t.UnixMicro()) }

// Time converts back, always in UTC.
func (m Micros) Time() time.Time { return time.UnixMicro(int64(m)).UTC() }

// String renders RFC 3339 with microsecond precision, always `Z`.
func (m Micros) String() string { return m.Time().Format(timestampLayout) }

// IsZero reports whether this is the zero value — the Unix epoch. A `_at` column is NOT NULL
// wherever it means anything, so the epoch stands in for "unset" only in a Go zero value that has
// not been filled in yet, and this is how a test says so.
func (m Micros) IsZero() bool { return m == 0 }

// Add offsets by a duration, discarding anything finer than a microsecond.
func (m Micros) Add(d time.Duration) Micros { return m + Micros(d/time.Microsecond) }

// Sub returns the duration from other to m. The result overflows beyond roughly ±292 years, which
// no window in this domain approaches.
func (m Micros) Sub(other Micros) time.Duration {
	return time.Duration(m-other) * time.Microsecond
}

// Before reports whether m is earlier than other.
func (m Micros) Before(other Micros) bool { return m < other }

// After reports whether m is later than other.
func (m Micros) After(other Micros) bool { return m > other }

// ParseMicros reads an RFC 3339 timestamp.
//
// It accepts any offset and any fractional precision, and always emits `Z` with six digits. The
// asymmetry is deliberate: an offset is unambiguous, so refusing `+02:00` on input would only
// break clients for the sake of tidiness, whereas emitting anything but `Z` invites a consumer to
// compare two timestamps as strings and get it wrong.
func ParseMicros(s string) (Micros, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0, fmt.Errorf("parse timestamp %q: %w: %w", s, ErrInvalidTimestamp, err)
	}
	return MicrosFromTime(t), nil
}

// MarshalJSON renders the timestamp as an RFC 3339 string.
func (m Micros) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(m.String())
	if err != nil {
		return nil, fmt.Errorf("marshal timestamp %d: %w", int64(m), err)
	}
	return b, nil
}

// UnmarshalJSON reads an RFC 3339 string.
//
// A bare number is rejected even though it would be trivial to accept as raw microseconds. Two
// accepted representations means two client behaviours, and only one of them ever gets tested.
func (m *Micros) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("unmarshal timestamp %s: %w: %w", b, ErrInvalidTimestamp, err)
	}
	parsed, err := ParseMicros(s)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

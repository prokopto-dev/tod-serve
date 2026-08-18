package core

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
)

// Secret is a string that does not render itself.
//
// "No secret is ever logged" is an invariant, and an invariant needs a mechanism. The mechanism is
// that the value is only reachable through [Secret.Reveal] — every other route out, including the
// ones nobody remembers exist, produces `***`. That means [fmt.Formatter] rather than only
// [fmt.Stringer]: `%s` on a Stringer is safe, but `%d` on a plain string type prints
// `%!d(core.Secret=hunter2)`, and the day someone writes the wrong verb is not the day to find out.
//
// One hole remains and cannot be closed from here: fmt prints an UNEXPORTED struct field by
// reflection without consulting its methods, so `%+v` on a struct with a lowercase Secret field
// leaks it. Config structs carry exported fields — they are decoded from JSON and environment
// variables — so the shape that leaks is not the shape this codebase writes.
// TestSecret_NestedInAStruct_NeverRendered pins the exported case.
type Secret string

// redacted is what a Secret renders as everywhere. Eight characters of a token prefix are
// loggable and are how a leaked token is traced; the secret itself never is.
const redacted = "***"

// ErrSecretRedacted is returned when a redacted value is unmarshalled back into a Secret.
var ErrSecretRedacted = errors.New("value is the redaction marker, not a secret")

// Reveal returns the secret. It is the one way out, named so that `grep -rn Reveal()` lists every
// place a secret is handled and review can look at all of them at once.
func (s Secret) Reveal() string { return string(s) }

// IsZero reports whether the secret is empty, which is how required-but-missing configuration is
// detected without revealing anything.
func (s Secret) IsZero() bool { return s == "" }

// Equal compares in constant time. A secret compared with == is a timing oracle, and the metrics
// token is compared on every scrape.
func (s Secret) Equal(other Secret) bool {
	return subtle.ConstantTimeCompare([]byte(s), []byte(other)) == 1
}

// String renders the redaction.
func (s Secret) String() string { return redacted }

// GoString renders the redaction for `%#v`, which does not go through String.
func (s Secret) GoString() string { return redacted }

// Format renders the redaction for every verb, including the wrong ones.
func (s Secret) Format(f fmt.State, _ rune) {
	// Deliberate waiver: this writes to a formatter's own buffer, and fmt has already decided what
	// to do about write errors. Returning one is not possible and there is nothing to log.
	_, _ = f.Write([]byte(redacted))
}

// MarshalJSON renders the redaction.
//
// A Secret therefore does not round-trip. That is the point, and [Secret.UnmarshalJSON] refuses
// the redaction marker so that a config write path built on marshal-then-unmarshal fails loudly
// instead of persisting `***` as somebody's token.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

// UnmarshalJSON reads a secret, refusing the redaction marker.
func (s *Secret) UnmarshalJSON(b []byte) error {
	var raw string
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("unmarshal secret: %w", err)
	}
	if raw == redacted {
		return fmt.Errorf("unmarshal secret: %w", ErrSecretRedacted)
	}
	*s = Secret(raw)
	return nil
}

// LogValue renders the redaction for slog, which is how everything in this project logs.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// The compiler checks the three rendering paths the invariant names, plus the two that exist only
// because fmt has more ways out than anybody remembers.
var (
	_ fmt.Stringer   = Secret("")
	_ fmt.GoStringer = Secret("")
	_ fmt.Formatter  = Secret("")
	_ json.Marshaler = Secret("")
	_ slog.LogValuer = Secret("")
)

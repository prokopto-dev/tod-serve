// Package local is the provider with nothing to verify.
//
// Its subject is a server-minted ULID and its display name is self-asserted, so a `local`
// identity proves nothing about the person holding it. That is not a gap to be closed later — it
// is what the provider IS, and the whole design around it exists to keep that honest:
// `verifiable_subject = 0` is a CHECK against `kind` rather than a toggle, `local` ships
// disabled, enabling it needs an explicit acknowledgement, a circle never auto-accepts it, and it
// can never participate in an `identity_link`.
//
// It exists because a LAN binary with no outbound network, four friends, a demo and a CI fixture
// are all real, and a product that cannot run without a third party is a product those four
// people cannot run at all. It ships honest rather than not shipping.
//
// The failure mode it carries, stated where somebody implementing against it will read it: an
// officer revokes a leaker, the leaker redeems another invite under a new name, and the officers
// believe the problem is handled. **The false confidence is the damage**, not the re-entry.
package local

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/prokopto-dev/tod-serve/internal/core"
)

// MaxDisplayNameLen bounds a self-asserted name. It is in runes rather than bytes because the
// limit somebody hits should be the one they can count.
const MaxDisplayNameLen = 64

// ErrDisplayNameRequired is returned when no display name was given. `local` is the one provider
// where it is mandatory: there is no provider to ask, so an empty name would leave a member list
// of blanks.
var ErrDisplayNameRequired = errors.New("local identities need a self-asserted display name")

// Identity is a newly minted local identity.
type Identity struct {
	Subject     string
	DisplayName string
}

// Mint creates one. The subject is a server-minted ULID: unguessable, unique, and — the point —
// carrying no claim about who holds it.
//
// `at` is the clock's now rather than a caller-chosen instant, because the ULID is also a cursor.
func Mint(gen *core.Generator, at core.Micros, displayName string) (Identity, error) {
	name := strings.TrimSpace(displayName)
	if name == "" {
		return Identity{}, ErrDisplayNameRequired
	}
	if utf8.RuneCountInString(name) > MaxDisplayNameLen {
		return Identity{}, fmt.Errorf("display name is %d runes, over the %d limit: %w",
			utf8.RuneCountInString(name), MaxDisplayNameLen, ErrDisplayNameRequired)
	}

	subject, err := core.NewID[core.Identity](gen, at)
	if err != nil {
		return Identity{}, fmt.Errorf("mint local subject: %w", err)
	}
	return Identity{Subject: subject.String(), DisplayName: name}, nil
}

// MaxInviteUses is the ceiling `local` forces on an invite minted for it.
//
// A `local` identity has no credential to re-present, so `POST /sessions` cannot work for one and
// every lost token becomes a new invite. Invite hygiene degrades from there until somebody leaves
// a 30-day, 50-use invite lying around — which is the same hole the weak revocation opens, from
// the other side. One use is the mitigation.
const MaxInviteUses = 1

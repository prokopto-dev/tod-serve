package core

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// ULID is a 128-bit identifier: 48 bits of Unix milliseconds followed by 80 bits of entropy,
// rendered as 26 characters of Crockford base32.
//
// It is the only id shape in the schema. The point is that it sorts: `tod_report.id` is also its
// own pagination cursor, which matters because the report log is the one collection that grows
// without bound. A UUIDv4 would need a separate cursor column and a composite index; `uuidv7()`
// would put the generator in the database and split across dialects.
type ULID [16]byte

const (
	// ULIDLen is the encoded length. Every id in the schema is TEXT of exactly this width.
	ULIDLen = 26

	// crockford is Crockford base32: no I, L, O or U, so no character pair reads alike and no
	// accidental word forms. This is the ULID spec's alphabet, not a local choice.
	crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

	// ulidTimeBits is the width of the millisecond timestamp prefix.
	ulidTimeBits = 48

	// ulidEntropyOffset is the first byte of the 80-bit entropy tail.
	ulidEntropyOffset = ulidTimeBits / 8
)

// microsPerMillisecond converts between the storage resolution and the resolution a ULID encodes.
const microsPerMillisecond = int64(time.Millisecond / time.Microsecond)

var (
	// ErrInvalidULID is returned by [ParseULID] for anything that is not the canonical encoding.
	ErrInvalidULID = errors.New("invalid ulid")

	// ErrEntropyExhausted is returned when more than 2^80 ids are minted inside one millisecond.
	// It is unreachable in practice and is an error rather than a wraparound because wrapping
	// would silently break the sort order that the cursor depends on.
	ErrEntropyExhausted = errors.New("ulid entropy exhausted within one millisecond")
)

// String renders the canonical 26-character encoding.
func (u ULID) String() string {
	hi := binary.BigEndian.Uint64(u[0:8])
	lo := binary.BigEndian.Uint64(u[8:16])
	out := make([]byte, ULIDLen)
	for i := range out {
		out[i] = crockford[quintet(hi, lo, uint(125-5*i))]
	}
	return string(out)
}

// IsZero reports whether this is the zero id, which no generator produces.
func (u ULID) IsZero() bool { return u == ULID{} }

// Time returns the minting instant encoded in the id, to millisecond resolution.
//
// This is the instant the row was written, never game truth. `died_at` is backdated routinely and
// is a column; reading it off an id would silently conflate the two.
func (u ULID) Time() Micros { return Micros(u.milliseconds() * uint64(microsPerMillisecond)) }

// milliseconds returns the encoded timestamp at the resolution the encoding actually holds.
func (u ULID) milliseconds() uint64 {
	return binary.BigEndian.Uint64(u[0:8]) >> (64 - ulidTimeBits)
}

// Compare orders two ids as their encodings sort, which is by minting time and then by entropy.
// Ids are cursors, so ordering them is a domain operation and not a detail of some sort call.
func (u ULID) Compare(other ULID) int {
	for i := range u {
		switch {
		case u[i] < other[i]:
			return -1
		case u[i] > other[i]:
			return 1
		}
	}
	return 0
}

// ParseULID reads the canonical encoding.
//
// It accepts uppercase only, and none of Crockford's decoding aliases (`i` and `l` for 1, `o` for
// 0). Being liberal here would give one id two spellings, and both would reach an idempotency key,
// a cursor and a cache key. Ids in this system are machine-produced, so nothing legitimate types
// them in the wrong case.
func ParseULID(s string) (ULID, error) {
	if len(s) != ULIDLen {
		return ULID{}, fmt.Errorf("parse ulid %q: %w: want %d characters, got %d",
			s, ErrInvalidULID, ULIDLen, len(s))
	}
	var hi, lo uint64
	for i := range ULIDLen {
		v, ok := unquintet(s[i])
		if !ok {
			return ULID{}, fmt.Errorf("parse ulid %q: %w: character %q at %d is not Crockford base32",
				s, ErrInvalidULID, s[i], i)
		}
		// 26 characters carry 130 bits and a ULID is 128, so the first character contributes only
		// three. A larger leading character encodes a value no ULID can hold.
		if i == 0 && v > 7 {
			return ULID{}, fmt.Errorf("parse ulid %q: %w: overflows 128 bits", s, ErrInvalidULID)
		}
		setQuintet(&hi, &lo, uint(125-5*i), v)
	}
	var u ULID
	binary.BigEndian.PutUint64(u[0:8], hi)
	binary.BigEndian.PutUint64(u[8:16], lo)
	return u, nil
}

// quintet reads the five bits at shift out of the 128-bit value (hi, lo). Bits at or above 128 —
// the two bits of padding the encoding adds — read as zero.
func quintet(hi, lo uint64, shift uint) byte {
	switch {
	case shift >= 64:
		return byte(hi>>(shift-64)) & 0x1f
	case shift+5 <= 64:
		return byte(lo>>shift) & 0x1f
	default:
		return byte((lo>>shift)|(hi<<(64-shift))) & 0x1f
	}
}

// setQuintet writes the five bits at shift into the 128-bit value (hi, lo).
func setQuintet(hi, lo *uint64, shift uint, v byte) {
	x := uint64(v)
	switch {
	case shift >= 64:
		*hi |= x << (shift - 64)
	case shift+5 <= 64:
		*lo |= x << shift
	default:
		*lo |= x << shift
		*hi |= x >> (64 - shift)
	}
}

// unquintet decodes one character of the alphabet.
func unquintet(c byte) (byte, bool) {
	i := strings.IndexByte(crockford, c)
	if i < 0 {
		return 0, false
	}
	return byte(i), true
}

// Generator mints ULIDs. It is safe for concurrent use.
//
// The entropy source is injected rather than reached for, so a test can mint a known id without
// stubbing a package-level variable, and so the one place that reads crypto/rand is main's wiring.
type Generator struct {
	mu      sync.Mutex
	entropy io.Reader
	last    ULID
	minted  bool
}

// NewGenerator returns a generator reading entropy from r, which in the running binary is
// crypto/rand.Reader. There is no default: a generator that quietly falls back to a weak source is
// a generator nobody notices is weak.
func NewGenerator(r io.Reader) *Generator { return &Generator{entropy: r} }

// New mints an id stamped with at, which must be the clock's now — never game truth such as
// `died_at`, because the id is a cursor and a cursor that jumps backwards drops rows out of a
// paginated read.
//
// Two ids minted in the same millisecond are still ordered: the second reuses the first's entropy
// plus one. If at goes backwards — an NTP step, a clock correction — the previous millisecond is
// reused for the same reason. The id is then briefly ahead of the wall clock, which costs nothing:
// the real instant is in `created_at`, and the id's job is to sort.
func (g *Generator) New(at Micros) (ULID, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var ms uint64
	if at > 0 {
		ms = uint64(at) / uint64(microsPerMillisecond)
	}
	if g.minted && ms <= g.last.milliseconds() {
		next, err := increment(g.last)
		if err != nil {
			return ULID{}, err
		}
		g.last = next
		return next, nil
	}

	var u ULID
	binary.BigEndian.PutUint64(u[0:8], ms<<(64-ulidTimeBits))
	if _, err := io.ReadFull(g.entropy, u[ulidEntropyOffset:]); err != nil {
		return ULID{}, fmt.Errorf("read ulid entropy: %w", err)
	}
	g.last, g.minted = u, true
	return u, nil
}

// increment returns the id with its 80 entropy bits raised by one, keeping the timestamp.
func increment(u ULID) (ULID, error) {
	for i := len(u) - 1; i >= ulidEntropyOffset; i-- {
		u[i]++
		if u[i] != 0 {
			return u, nil
		}
	}
	return ULID{}, fmt.Errorf("mint ulid: %w", ErrEntropyExhausted)
}

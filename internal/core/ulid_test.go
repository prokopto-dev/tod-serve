package core_test

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/core"
)

func TestParseULID_KnownVectors_RoundTripAndCarryTheirTimestamp(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		encoded string
		at      core.Micros
	}{
		// The ULID specification's own example, so this is a check against the world and not
		// against ourselves.
		{"spec example", "01ARZ3NDEKTSV4RRFFQ69G5FAV", 1_469_922_850_259_000},
		{"minimum", "00000000000000000000000000", 0},
		{"maximum", "7ZZZZZZZZZZZZZZZZZZZZZZZZZ", 281_474_976_710_655_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			u, err := core.ParseULID(tc.encoded)
			require.NoError(t, err)
			require.Equal(t, tc.encoded, u.String())
			require.Equal(t, tc.at, u.Time())
		})
	}
}

func TestParseULID_NotTheCanonicalEncoding_ReturnsErrInvalidULID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		given string
	}{
		{"empty", ""},
		{"too short", "01ARZ3NDEKTSV4RRFFQ69G5FA"},
		{"too long", "01ARZ3NDEKTSV4RRFFQ69G5FAVV"},
		// Crockford's decoding aliases are refused so that one id has exactly one spelling: both
		// spellings would otherwise reach an idempotency key, a cursor and a cache key.
		{"lowercase", "01arz3ndektsv4rrffq69g5fav"},
		{"letter I", "01ARZ3NDEKTSV4RRFFQ69G5FAI"},
		{"letter U", "01ARZ3NDEKTSV4RRFFQ69G5FAU"},
		{"hyphenated", "01ARZ3NDEK-TSV4RRFFQ69G5F"},
		// 26 characters carry 130 bits; a leading character above 7 encodes more than 128.
		{"overflows 128 bits", "8ZZZZZZZZZZZZZZZZZZZZZZZZZ"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := core.ParseULID(tc.given)
			require.ErrorIs(t, err, core.ErrInvalidULID)
		})
	}
}

func TestULID_Compare_OrdersByTimeThenEntropy(t *testing.T) {
	t.Parallel()
	early, err := core.ParseULID("01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.NoError(t, err)
	later, err := core.ParseULID("01ARZ3NDEMTSV4RRFFQ69G5FAV")
	require.NoError(t, err)
	sameMSMoreEntropy, err := core.ParseULID("01ARZ3NDEKTSV4RRFFQ69G5FAW")
	require.NoError(t, err)

	require.Equal(t, -1, early.Compare(later))
	require.Equal(t, 1, later.Compare(early))
	require.Equal(t, 0, early.Compare(early))
	require.Equal(t, -1, early.Compare(sameMSMoreEntropy))
	require.True(t, early.String() < sameMSMoreEntropy.String(),
		"the encoding must sort the same way the value does; the id is the cursor")
}

// countingEntropy yields a predictable stream, so a minted id is a known value.
func countingEntropy(start byte) io.Reader {
	b := make([]byte, 1024)
	for i := range b {
		b[i] = start
	}
	return bytes.NewReader(b)
}

func TestGenerator_SameMillisecond_MintsStrictlyIncreasingIds(t *testing.T) {
	t.Parallel()
	g := core.NewGenerator(countingEntropy(0x00))
	at := core.Micros(1_755_483_247_000_000)

	first, err := g.New(at)
	require.NoError(t, err)
	second, err := g.New(at + 999) // still the same millisecond
	require.NoError(t, err)

	require.Equal(t, first.Time(), second.Time())
	require.Equal(t, -1, first.Compare(second),
		"two ids minted in one millisecond must still order, because the id is the cursor")
}

func TestGenerator_ClockGoesBackwards_IdsStillIncrease(t *testing.T) {
	t.Parallel()
	g := core.NewGenerator(countingEntropy(0x11))
	at := core.Micros(1_755_483_247_000_000)

	first, err := g.New(at)
	require.NoError(t, err)
	// An NTP step. A cursor that jumps backwards silently drops rows out of a paginated read, so
	// the id keeps the previous millisecond and the real instant stays in `created_at`.
	second, err := g.New(at.Add(-5_000_000_000))
	require.NoError(t, err)

	require.Equal(t, -1, first.Compare(second))
	require.Equal(t, first.Time(), second.Time())
}

func TestGenerator_NewMillisecond_ReadsFreshEntropy(t *testing.T) {
	t.Parallel()
	g := core.NewGenerator(countingEntropy(0x7f))
	at := core.Micros(1_755_483_247_000_000)

	first, err := g.New(at)
	require.NoError(t, err)
	second, err := g.New(at.Add(2000 * 1000)) // two milliseconds later, in microseconds
	require.NoError(t, err)

	require.Equal(t, -1, first.Compare(second))
	require.NotEqual(t, first.Time(), second.Time())
}

// failingEntropy stands in for a broken or exhausted random source.
type failingEntropy struct{}

var errNoEntropy = errors.New("no entropy")

func (failingEntropy) Read([]byte) (int, error) { return 0, errNoEntropy }

func TestGenerator_EntropyFails_ReturnsTheErrorAndNoID(t *testing.T) {
	t.Parallel()
	g := core.NewGenerator(failingEntropy{})

	got, err := g.New(1_755_483_247_000_000)
	require.ErrorIs(t, err, errNoEntropy)
	require.True(t, got.IsZero(), "a failed mint must not return a usable id")
}

func TestGenerator_Concurrent_MintsUniqueIds(t *testing.T) {
	t.Parallel()
	const mints = 200
	g := core.NewGenerator(countingEntropy(0x42))
	at := core.Micros(1_755_483_247_000_000)

	// Results are collected and asserted on the test's own goroutine: require calls t.FailNow,
	// which is only valid there, and a failed assertion in a worker would hang instead of failing.
	var (
		mu     sync.Mutex
		minted = make([]core.ULID, 0, mints)
		errs   []error
		wg     sync.WaitGroup
	)
	for range mints {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, err := g.New(at)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			minted = append(minted, u)
		}()
	}
	wg.Wait()

	require.Empty(t, errs)
	seen := make(map[core.ULID]bool, mints)
	for _, u := range minted {
		require.False(t, seen[u], "%s was minted twice", u)
		seen[u] = true
	}
	require.Len(t, seen, mints)
}

func TestULID_Zero_IsNotSomethingAGeneratorProduces(t *testing.T) {
	t.Parallel()
	require.True(t, core.ULID{}.IsZero())

	g := core.NewGenerator(countingEntropy(0x00))
	u, err := g.New(0)
	require.NoError(t, err)
	require.True(t, u.IsZero(), "the epoch with zero entropy is the only way to mint the zero id")

	u, err = g.New(1_755_483_247_000_000)
	require.NoError(t, err)
	require.False(t, u.IsZero())
}

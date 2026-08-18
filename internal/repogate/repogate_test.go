package repogate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/tod-serve/internal/repogate"
)

// The analyser exists because a grep for `time.Now(` is defeated by two characters. These are
// those two characters.
func TestCheckSource_TimeNow_IsFoundHoweverItIsSpelled(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		src   string
		want  int
		wantR string
	}{
		{
			name: "plain import",
			src: `package p
import "time"
func f() { _ = time.Now() }`,
			want: 1, wantR: "time.Now",
		},
		{
			name: "aliased import — what the grep misses",
			src: `package p
import t "time"
func f() { _ = t.Now() }`,
			want: 1, wantR: "t.Now",
		},
		{
			name: "dot import — what the grep also misses",
			src: `package p
import . "time"
func f() { _ = Now() }`,
			want: 1, wantR: "Now",
		},
		{
			name: "taken as a value rather than called",
			src: `package p
import "time"
func f() { g := time.Now; _ = g }`,
			want: 1, wantR: "time.Now",
		},
		{
			name: "several in one file",
			src: `package p
import (
	"time"
	clock "time"
)
func f() { _, _ = time.Now(), clock.Now() }`,
			want: 2, wantR: "time.Now",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			found, err := repogate.CheckSource(repogate.ClockRule(), "example.go", tc.src)
			require.NoError(t, err)
			require.Len(t, found, tc.want)
			require.Equal(t, tc.wantR, found[0].Ref)
			require.Equal(t, "CLOCK001", found[0].Rule)
			require.Contains(t, found[0].String(), "example.go:")
		})
	}
}

// A gate that fires on things it should not gets turned off, so the false positives matter as much
// as the true ones.
func TestCheckSource_SomethingElseCalledNow_IsNotAFinding(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		src  string
	}{
		{"a method on another type", `package p
type c struct{}
func (c) Now() int { return 0 }
func f(x c) { _ = x.Now() }`},
		{"a field named Now", `package p
type s struct{ Now int }
func f() { _ = s{Now: 1} }`},
		{"time imported but not for Now", `package p
import "time"
func f() { _ = time.Since(time.Time{}) }`},
		{"time imported for its side effects only", `package p
import _ "time"
func f() {}`},
		{"no import at all", `package p
func f() { _ = 1 }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			found, err := repogate.CheckSource(repogate.ClockRule(), "example.go", tc.src)
			require.NoError(t, err)
			require.Empty(t, found)
		})
	}
}

func TestCheckSource_Unparseable_ReturnsAnError(t *testing.T) {
	t.Parallel()
	_, err := repogate.CheckSource(repogate.ClockRule(), "broken.go", "package p\nfunc f( {")
	require.Error(t, err)
	require.Contains(t, err.Error(), "broken.go")
}

func TestClockRule_AllowsInternalClock_AndNothingElse(t *testing.T) {
	t.Parallel()
	rule := repogate.ClockRule()
	require.Equal(t, []string{"internal/clock"}, rule.AllowDirs)
	require.Equal(t, "time", rule.Import)
	require.Equal(t, []string{"Now"}, rule.Names)
	require.NotEmpty(t, rule.Reason, "a gate whose message is only its own name teaches nobody")
}

func TestCheck_TestFilesOnlyRule_SkipsProductionCode(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "pkg"), 0o750))
	sleeper := "package p\nimport \"time\"\nfunc f() { time.Sleep(time.Second) }\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "pkg/a.go"), []byte(sleeper), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "pkg/a_test.go"), []byte(sleeper), 0o600))

	got, err := repogate.Check(root, []string{"pkg"}, []repogate.Rule{repogate.SleepRule()})
	require.NoError(t, err)
	require.Equal(t, 2, got.Files, "both files are read; only one is judged")
	require.Len(t, got.Findings, 1)
	require.Equal(t, "pkg/a_test.go", got.Findings[0].File)
	require.Equal(t, "SLEEP001", got.Findings[0].Rule)
}

func TestCheck_AllowedDirectory_IsNotJudged(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "internal/clock"), 0o750))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "internal/tod"), 0o750))
	// An aliased import, so this also proves the tree walk uses the analyser and not a grep.
	src := "package p\nimport t \"time\"\nfunc f() { _ = t.Now() }\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "internal/clock/c.go"), []byte(src), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "internal/tod/t.go"), []byte(src), 0o600))

	got, err := repogate.Check(root, []string{"internal"}, []repogate.Rule{repogate.ClockRule()})
	require.NoError(t, err)
	require.Len(t, got.Findings, 1)
	require.Equal(t, "internal/tod/t.go", got.Findings[0].File)
}

func TestCheck_MissingRoot_ReturnsAnError(t *testing.T) {
	t.Parallel()
	_, err := repogate.Check(t.TempDir(), []string{"absent"}, []repogate.Rule{repogate.ClockRule()})
	require.Error(t, err)
}

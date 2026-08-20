package catalogue

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// MaxCandidates caps the candidate list an ambiguity reports. A substring of one letter matches
// most of the catalogue, and a problem body carrying every row of it is a problem body nobody
// reads. The number that were dropped is in the detail, because a filter that drops rows counts
// them somewhere visible.
const MaxCandidates = 20

// MatchKind says which rung of the ladder matched. It is on the wire, so it is a closed set of
// snake_case values like every other enum here.
type MatchKind string

// The rungs, strongest first. The order IS the rule: an exact hit is never ranked below a
// substring hit, which is what makes `createTodReport` able to accept a `target_name` at all. Get
// this wrong and the nParse+ plugin silently reports the wrong mob — it sends a parsed name and
// holds no catalogue, so it cannot notice.
const (
	// MatchID is the id branch: the caller named the target outright and no matching happened.
	MatchID MatchKind = "id"
	// MatchName is byte-for-byte equality with the canonical name, backtick included.
	MatchName MatchKind = "name"
	// MatchNameNormalised is equality on `name_norm` — the same name typed with an apostrophe,
	// without the backtick, in the wrong case, or with the spaces somewhere else.
	MatchNameNormalised MatchKind = "name_normalised"
	// MatchAlias is byte-for-byte equality with a registered alias.
	MatchAlias MatchKind = "alias"
	// MatchAliasNormalised is equality on `alias_norm`.
	MatchAliasNormalised MatchKind = "alias_normalised"
	// MatchPrefix is a normalised prefix of exactly one target's name or alias.
	MatchPrefix MatchKind = "prefix"
	// MatchSubstring is a normalised substring of exactly one target's name or alias. It is the
	// last rung on purpose: it is the one most likely to be a coincidence.
	MatchSubstring MatchKind = "substring"
)

// Ref names a target, by id or by name. Exactly one is set.
//
// It is one type rather than two parameters because `createTodReport` accepts exactly one of
// `target_id` and `target_name`, and a function taking both as strings has a fourth state — both
// sent — that the caller has to remember to refuse.
type Ref struct {
	ID   core.RaidTargetID
	Name string
}

// Resolution is what the ladder found.
type Resolution struct {
	Target    Target    `json:"target"`
	MatchKind MatchKind `json:"match_kind"`
	// Candidates is what matched at the winning rung — one target on a successful resolve. It is
	// present on success as well as in the ambiguity problem so a client can render "matched X"
	// and "matched X, Y or Z" with one shape.
	Candidates []Target `json:"candidates"`
}

// Resolve turns a caller's reference into exactly one target, or into an ambiguity that names the
// alternatives.
//
// **It takes no server, and that is deliberate.** A mob's existence is a fact about the game and
// the catalogue is instance-wide — 02-api-design says so, and `raid_target` is on the
// instance-scoped allowlist in canonical §9 for that reason. A server parameter here would narrow
// nothing; what it would do is suggest that one mob on blue and the same mob on green are two
// different rows, which is exactly the confusion the `raid_target` / `raid_target_timer` split
// exists to prevent.
// The server-dependent half is the timer, and [Service.ResolveTimer] takes one.
//
// The `server` check `createTodReport` owes its caller — the body's server against the circle's,
// answered `422 server_mismatch` — is a comparison of two values the caller already holds and
// needs no catalogue read.
func (s *Service) Resolve(ctx context.Context, ref Ref) (Resolution, error) {
	if !ref.ID.IsZero() {
		if strings.TrimSpace(ref.Name) != "" {
			return Resolution{}, apierr.New(apierr.CodeValidationFailed,
				"send exactly one of target_id and target_name").
				WithField("body.target_name", "not both")
		}
		target, err := s.Get(ctx, ref.ID)
		if err != nil {
			// A target id that names nothing is `unknown_target` and not `404`: the caller sent it
			// in a body, alongside fields that were fine, and the resource they addressed exists.
			if coded, ok := apierr.From(err); ok && coded.Code() == apierr.CodeNotFound {
				return Resolution{}, apierr.New(apierr.CodeUnknownTarget,
					"no raid target with that id").
					WithField("body.target_id", "no such target")
			}
			return Resolution{}, err
		}
		return Resolution{
			Target: target, MatchKind: MatchID, Candidates: []Target{target},
		}, nil
	}

	name := strings.TrimSpace(ref.Name)
	if name == "" {
		return Resolution{}, apierr.New(apierr.CodeValidationFailed,
			"send exactly one of target_id and target_name").
			WithField("body.target_name", "required when target_id is absent")
	}

	targets, err := s.loadTargets(ctx)
	if err != nil {
		return Resolution{}, err
	}
	return resolveIn(targets, name)
}

// resolveIn is the ladder itself, over an already-loaded catalogue.
//
// It is separated from the read so that it is a pure function of (catalogue, input): the rung
// ordering is the rule most worth testing exhaustively, and a test of it should not need a
// database to say what `Naggy` resolves to.
func resolveIn(targets []Target, name string) (Resolution, error) {
	norm := core.Normalise(name)
	if norm == "" {
		// Nothing but punctuation. It would be a prefix of every target in the catalogue, so
		// answering `ambiguous_target` with fifty candidates would be technically true and useless.
		return Resolution{}, apierr.New(apierr.CodeUnknownTarget,
			"that name has no letters or digits in it").
			WithField("body.target_name", "must contain more than punctuation")
	}

	// The rungs, in order. Each returns the targets that matched it; the first non-empty rung
	// wins, and a rung matching more than one target is the ambiguity — it is never fallen
	// through, because falling through would let a substring outrank the exact hit above it.
	rungs := []struct {
		kind  MatchKind
		match func(Target) bool
	}{
		{MatchName, func(t Target) bool { return t.Name == name }},
		{MatchNameNormalised, func(t Target) bool { return t.NameNorm == norm }},
		{MatchAlias, func(t Target) bool { return slices.Contains(t.Aliases, name) }},
		{MatchAliasNormalised, func(t Target) bool { return anyAlias(t, equals(norm)) }},
		{MatchPrefix, func(t Target) bool {
			return live(t) &&
				(strings.HasPrefix(t.NameNorm, norm) || anyAlias(t, hasPrefix(norm)))
		}},
		{MatchSubstring, func(t Target) bool {
			return live(t) &&
				(strings.Contains(t.NameNorm, norm) || anyAlias(t, contains(norm)))
		}},
	}

	for _, rung := range rungs {
		var hits []Target
		for _, t := range targets {
			if rung.match(t) {
				hits = append(hits, t)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return Resolution{Target: hits[0], MatchKind: rung.kind, Candidates: hits}, nil
		default:
			return Resolution{}, ambiguous(name, hits)
		}
	}

	return Resolution{}, apierr.Newf(apierr.CodeUnknownTarget,
		"nothing in the catalogue matches %q", name).
		WithField("body.target_name", "no such target")
}

// live reports whether a target may be reached by a FUZZY rung.
//
// A retired target stays reachable by its exact name, its normalised name and its aliases, so a
// backdated report about a mob the server has since removed still names the right row. It is kept
// out of the prefix and substring rungs because a dead mob should never be what a half-typed name
// resolves to, and should never be the second candidate that turns a live target's match into an
// ambiguity.
func live(t Target) bool { return t.State == schemaenum.RaidTargetStateActive }

func anyAlias(t Target, pred func(string) bool) bool {
	for _, alias := range t.Aliases {
		if pred(core.Normalise(alias)) {
			return true
		}
	}
	return false
}

func equals(norm string) func(string) bool {
	return func(alias string) bool { return alias == norm }
}

func hasPrefix(norm string) func(string) bool {
	return func(alias string) bool { return strings.HasPrefix(alias, norm) }
}

func contains(norm string) func(string) bool {
	return func(alias string) bool { return strings.Contains(alias, norm) }
}

// ambiguous renders the tie as `422 ambiguous_target` carrying `meta.candidates[]`.
//
// The plugin's whole contract depends on this body: it sends a parsed name, holds no catalogue,
// and this is the only thing that tells it which mobs it might have meant.
func ambiguous(name string, hits []Target) error {
	slices.SortFunc(hits, func(a, b Target) int { return strings.Compare(a.NameNorm, b.NameNorm) })
	shown := hits
	detail := "\"" + name + "\" matches " + plural(len(hits)) + "; send target_id instead"
	if len(shown) > MaxCandidates {
		shown = shown[:MaxCandidates]
		detail = "\"" + name + "\" matches " + plural(len(hits)) + ", of which " +
			plural(MaxCandidates) + " are listed; send target_id instead"
	}
	raw, err := json.Marshal(shown)
	if err != nil {
		return apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	return apierr.New(apierr.CodeAmbiguousTarget, detail).
		WithCandidates(raw).
		WithField("body.target_name", "matches more than one target")
}

func plural(n int) string {
	if n == 1 {
		return "1 target"
	}
	return strconv.Itoa(n) + " targets"
}

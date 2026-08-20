package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/core"
)

// TargetResponse is one raid target, and the instant it was read.
//
// `as_of` sits on the RESPONSE and not on the view, which is what lets the ETag be computed over
// the view alone: a tag that included the read time would change on every request and turn every
// `If-Match` into a `412`.
type TargetResponse struct {
	catalogue.Target
	// Timers are every per-server window this instance holds for the target. It is empty on an
	// instance nobody has seeded, which is the honest answer and not an error — canonical §15.
	Timers []catalogue.TargetTimer `json:"timers"`
	AsOf   core.Micros             `json:"as_of"`
}

// ResolutionResponse is what the ladder found, and when.
type ResolutionResponse struct {
	catalogue.Resolution
	AsOf core.Micros `json:"as_of"`
}

// TimerResponse is one catalogue timer, and when it was read.
type TimerResponse struct {
	catalogue.TargetTimer
	TargetID core.RaidTargetID `json:"target_id"`
	AsOf     core.Micros       `json:"as_of"`
}

// OverrideResponse is one circle's timer override, and when it was read.
type OverrideResponse struct {
	catalogue.TimerOverride
	AsOf core.Micros `json:"as_of"`
}

type listRaidTargetsInput struct {
	Server         string `query:"server" doc:"Fold this server's timer into each row" enum:"blue,green,red"`
	Expansion      string `query:"expansion" enum:"classic,kunark,velious"`
	Zone           string `query:"zone" doc:"Matched case- and punctuation-insensitively"`
	Query          string `query:"q" doc:"Substring of a target's name or one of its aliases"`
	IncludeRetired bool   `query:"include_retired" doc:"Include targets the server no longer spawns"`
	Cursor         string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor"`
	Limit          int    `query:"limit" doc:"Page size, 1-200" minimum:"0" maximum:"200"`
}

type listRaidTargetsOutput struct {
	Body Page[catalogue.CatalogueEntry]
}

type getRaidTargetInput struct {
	TargetID    string `path:"target_id" doc:"The raid target"`
	IfNoneMatch string `header:"If-None-Match" doc:"Revalidate a cached copy"`
}

type getRaidTargetOutput struct {
	ETag string `header:"ETag"`
	Body TargetResponse
}

type resolveRaidTargetInput struct {
	Body struct {
		Name string `json:"name" doc:"A name as somebody typed it: wrong case, missing backtick and stray whitespace are all fine" maxLength:"200"`
	}
}

type resolveRaidTargetOutput struct{ Body ResolutionResponse }

type createRaidTargetInput struct {
	Body struct {
		Name          string   `json:"name" doc:"The canonical spelling, punctuation included" maxLength:"120"`
		Zone          string   `json:"zone" maxLength:"120"`
		Expansion     string   `json:"expansion" enum:"classic,kunark,velious"`
		Category      string   `json:"category" enum:"open_world,zone_boss,planar,ntov,sleeper,key_holder"`
		IsQuakeTarget bool     `json:"is_quake_target,omitempty" doc:"Whether a server-wide repop resets this target"`
		Aliases       []string `json:"aliases,omitempty" doc:"Short forms raiders type. Every one must be unique across the whole catalogue"`
	}
}

type createRaidTargetOutput struct{ Body TargetResponse }

type updateRaidTargetInput struct {
	TargetID string `path:"target_id" doc:"The raid target"`
	IfMatch  string `header:"If-Match" doc:"The ETag a previous read returned"`
	Body     struct {
		Name          *string   `json:"name,omitempty" maxLength:"120"`
		Zone          *string   `json:"zone,omitempty" maxLength:"120"`
		Expansion     *string   `json:"expansion,omitempty" enum:"classic,kunark,velious"`
		Category      *string   `json:"category,omitempty" enum:"open_world,zone_boss,planar,ntov,sleeper,key_holder"`
		IsQuakeTarget *bool     `json:"is_quake_target,omitempty"`
		State         *string   `json:"state,omitempty" enum:"active,retired"`
		Aliases       *[]string `json:"aliases,omitempty" doc:"REPLACES the alias set. Sending [] removes every alias"`
	}
}

type updateRaidTargetOutput struct {
	ETag string `header:"ETag"`
	Body TargetResponse
}

// TimerWindow is the window columns, shared by the two operations that write them because they
// write the same columns under the same four CHECK constraints. Two copies would be two shapes a
// client has to learn and two validators to keep in step.
//
// It is EXPORTED because the schema generator reflects over it: an unexported embedded struct is
// one reflection cannot describe, and the operation's request body comes out as an empty object
// that documents nothing. TestSpec_NoSchema_IsAnEmptyObject is the gate that says so.
type TimerWindow struct {
	WindowKind               string `json:"window_kind" enum:"fixed,variance,unknown"`
	WindowOpenOffsetSeconds  *int64 `json:"window_open_offset_seconds,omitempty" doc:"Seconds from the ToD to the earliest possible spawn. Null iff window_kind is unknown"`
	WindowCloseOffsetSeconds *int64 `json:"window_close_offset_seconds,omitempty" doc:"Seconds from the ToD to the latest possible spawn. Equal to the open offset iff window_kind is fixed"`
	FixedGraceSeconds        *int64 `json:"fixed_grace_seconds,omitempty" doc:"How long a fixed spawn stays in_window. Defaults to 900" minimum:"0"`
	ClusterEpsilonSeconds    *int64 `json:"cluster_epsilon_seconds,omitempty" doc:"Per-target clustering window. Null derives it" minimum:"0"`
	Note                     string `json:"note,omitempty" doc:"Why these numbers, and who disputes them" maxLength:"500"`
}

func (w TimerWindow) request(source string) catalogue.WindowRequest {
	return catalogue.WindowRequest{
		WindowKind:               w.WindowKind,
		WindowOpenOffsetSeconds:  w.WindowOpenOffsetSeconds,
		WindowCloseOffsetSeconds: w.WindowCloseOffsetSeconds,
		FixedGraceSeconds:        w.FixedGraceSeconds,
		ClusterEpsilonSeconds:    w.ClusterEpsilonSeconds,
		Source:                   source,
		Note:                     w.Note,
	}
}

type putRaidTargetTimerInput struct {
	TargetID string `path:"target_id" doc:"The raid target"`
	Server   string `path:"server" doc:"The server this window is for" enum:"blue,green,red"`
	IfMatch  string `header:"If-Match" doc:"The ETag getRaidTarget returned. The tag is the TARGET's, because a timer has no read of its own"`
	Body     struct {
		TimerWindow
		Source string `json:"source,omitempty" doc:"Where these numbers came from. They are not ours and they are disputed" maxLength:"200"`
	}
}

type putRaidTargetTimerOutput struct {
	ETag string `header:"ETag"`
	Body TimerResponse
}

type listCircleTimerOverridesInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
}

type listCircleTimerOverridesOutput struct {
	Body Page[catalogue.TimerOverride]
}

type putCircleTimerOverrideInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
	TargetID string `path:"target_id" doc:"The raid target this circle disagrees about"`
	IfMatch  string `header:"If-Match" doc:"The ETag a previous read returned, or * to write an override that does not exist yet"`
	Body     struct {
		TimerWindow
	}
}

type putCircleTimerOverrideOutput struct {
	ETag string `header:"ETag"`
	Body OverrideResponse
}

type deleteCircleTimerOverrideInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
	TargetID string `path:"target_id" doc:"The raid target"`
}

type deleteCircleTimerOverrideOutput struct{ Body OverrideResponse }

// registerCatalogue attaches the raid-target catalogue and the per-circle timer overrides.
//
// The split runs through every operation here: the six `/raid-targets` routes are instance-wide,
// because a mob's existence is a fact about the game, and the three `/circles/{circle_id}` ones are
// tenanted, because a circle's disagreement with the catalogue is that circle's. The registry rows
// already say which is which; registering the three tenanted ones is what puts them under
// TestTenancy_CrossCircle_EveryOperationDenies.
func (s *Server) registerCatalogue() error {
	return errors.Join(
		s.registerRaidTargets(),
		s.registerTimerOverrides(),
	)
}

func (s *Server) registerRaidTargets() error {
	return errors.Join(
		registerFailure(OpListRaidTargets, Register(s.api, OpListRaidTargets,
			func(ctx context.Context, in *listRaidTargetsInput) (*listRaidTargetsOutput, error) {
				limit, err := NormaliseLimit(in.Limit)
				if err != nil {
					return nil, err
				}
				cursor, err := ParseCursor(in.Cursor)
				if err != nil {
					return nil, err
				}
				filter := catalogue.ListFilter{
					Server: core.Server(in.Server), Expansion: in.Expansion, Zone: in.Zone,
					Query: in.Query, IncludeRetired: in.IncludeRetired, Limit: limit,
				}
				if !cursor.IsZero() {
					filter.Cursor = core.IDFromULID[core.RaidTarget](cursor)
				}
				page, err := s.cfg.Catalogue.List(ctx, filter)
				if err != nil {
					return nil, err
				}
				return &listRaidTargetsOutput{
					Body: NewPage(page.Entries, page.NextCursor.String(), page.HasMore,
						s.cfg.Clock.Now()),
				}, nil
			})),

		registerFailure(OpGetRaidTarget, Register(s.api, OpGetRaidTarget,
			func(ctx context.Context, in *getRaidTargetInput) (*getRaidTargetOutput, error) {
				id, err := parseTargetID(in.TargetID)
				if err != nil {
					return nil, err
				}
				view, err := s.targetResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				etag, err := ETagOf(view)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}
				view.AsOf = s.cfg.Clock.Now()
				return &getRaidTargetOutput{ETag: etag, Body: view}, nil
			})),

		registerFailure(OpResolveRaidTarget, Register(s.api, OpResolveRaidTarget,
			func(ctx context.Context, in *resolveRaidTargetInput) (*resolveRaidTargetOutput, error) {
				// The whole ladder, exposed. It is the same call `createTodReport` makes, which is
				// the point: a plugin can ask what a parsed name would resolve to and get the same
				// answer the report would have got, rather than holding a catalogue of its own.
				resolution, err := s.cfg.Catalogue.Resolve(ctx,
					catalogue.Ref{Name: in.Body.Name})
				if err != nil {
					return nil, err
				}
				return &resolveRaidTargetOutput{Body: ResolutionResponse{
					Resolution: resolution, AsOf: s.cfg.Clock.Now(),
				}}, nil
			})),

		registerFailure(OpCreateRaidTarget, Register(s.api, OpCreateRaidTarget,
			func(ctx context.Context, in *createRaidTargetInput) (*createRaidTargetOutput, error) {
				p, ok := PrincipalFrom(ctx)
				if !ok {
					return nil, apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
				}
				key, _ := IdempotencyKeyFrom(ctx)
				// EVERY field of the body, not the memorable ones. The hash is what turns a
				// replayed key carrying a different request into `idempotency_key_reused` rather
				// than a silent replay of the first one, and a field left out of it is a field a
				// client can change without being told their second request did nothing.
				// Marshalling the whole struct is the encoding that cannot fall behind the struct.
				body, marshalErr := json.Marshal(in.Body)
				if marshalErr != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, marshalErr, "")
				}
				hash := hashBody("createRaidTarget", string(body))

				out, _, err := runIdempotentHandler(ctx, s.api, p, key, hash,
					func(ctx context.Context) (TargetResponse, error) {
						view, createErr := s.cfg.Catalogue.Create(ctx, catalogue.CreateRequest{
							Name: in.Body.Name, Zone: in.Body.Zone,
							Expansion: in.Body.Expansion, Category: in.Body.Category,
							IsQuakeTarget: in.Body.IsQuakeTarget, Aliases: in.Body.Aliases,
						})
						if createErr != nil {
							return TargetResponse{}, createErr
						}
						// A brand-new target has no timers, and never can at this instant:
						// timers are per-server rows written afterwards, by a seed or by hand.
						return TargetResponse{
							Target: view, Timers: []catalogue.TargetTimer{},
							AsOf: s.cfg.Clock.Now(),
						}, nil
					})
				if err != nil {
					return nil, err
				}
				return &createRaidTargetOutput{Body: out}, nil
			})),

		registerFailure(OpUpdateRaidTarget, Register(s.api, OpUpdateRaidTarget,
			func(ctx context.Context, in *updateRaidTargetInput) (*updateRaidTargetOutput, error) {
				id, err := parseTargetID(in.TargetID)
				if err != nil {
					return nil, err
				}
				current, err := s.targetResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				if err = RequireIfMatch(in.IfMatch, current); err != nil {
					return nil, err
				}
				if _, err = s.cfg.Catalogue.Update(ctx, id, catalogue.UpdateRequest{
					Name: in.Body.Name, Zone: in.Body.Zone,
					Expansion: in.Body.Expansion, Category: in.Body.Category,
					IsQuakeTarget: in.Body.IsQuakeTarget, State: in.Body.State,
					Aliases: in.Body.Aliases,
				}); err != nil {
					return nil, err
				}
				view, err := s.targetResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				etag, err := ETagOf(view)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}
				view.AsOf = s.cfg.Clock.Now()
				return &updateRaidTargetOutput{ETag: etag, Body: view}, nil
			})),

		registerFailure(OpPutRaidTargetTimer, Register(s.api, OpPutRaidTargetTimer,
			func(ctx context.Context, in *putRaidTargetTimerInput) (*putRaidTargetTimerOutput, error) {
				id, err := parseTargetID(in.TargetID)
				if err != nil {
					return nil, err
				}
				server, err := core.ParseServer(in.Server)
				if err != nil {
					return nil, apierr.Newf(apierr.CodeNotFound, "no such server").
						WithField("path.server", "not a server")
				}
				// The ETag is the TARGET's, because a timer has no GET of its own: the
				// representation a client read this from is `getRaidTarget`, whose `timers[]`
				// carries it. A tag for a resource nobody can read is a tag nobody can send.
				current, err := s.targetResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				if err = RequireIfMatch(in.IfMatch, current); err != nil {
					return nil, err
				}

				timer, err := s.cfg.Catalogue.PutTimer(ctx, id, server,
					in.Body.request(in.Body.Source))
				if err != nil {
					return nil, err
				}
				// A catalogue timer is instance-wide and per-server, so this moved the window for
				// every circle on that server that has not overridden it. Nothing was appended
				// anywhere, so the projection is told rather than left to notice.
				if err = s.cfg.Invalidator.OnCatalogueTimerChange(ctx, server, id); err != nil {
					return nil, err
				}
				after, err := s.targetResponse(ctx, id)
				if err != nil {
					return nil, err
				}
				etag, err := ETagOf(after)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}
				return &putRaidTargetTimerOutput{ETag: etag, Body: TimerResponse{
					TargetTimer: timer, TargetID: id, AsOf: s.cfg.Clock.Now(),
				}}, nil
			})),
	)
}

func (s *Server) registerTimerOverrides() error {
	return errors.Join(
		registerFailure(OpListCircleTimerOverrides, Register(s.api, OpListCircleTimerOverrides,
			func(ctx context.Context, in *listCircleTimerOverridesInput) (*listCircleTimerOverridesOutput, error) {
				id, err := parseCircleID(in.CircleID)
				if err != nil {
					return nil, err
				}
				overrides, err := s.cfg.Catalogue.ListOverrides(ctx, id)
				if err != nil {
					return nil, err
				}
				// One page, no cursor. An override is a deliberate disagreement an officer typed;
				// a circle with more than a handful has a different problem than pagination.
				return &listCircleTimerOverridesOutput{
					Body: NewPage(overrides, "", false, s.cfg.Clock.Now()),
				}, nil
			})),

		registerFailure(OpPutCircleTimerOverride, Register(s.api, OpPutCircleTimerOverride,
			func(ctx context.Context, in *putCircleTimerOverrideInput) (*putCircleTimerOverrideOutput, error) {
				circleID, err := parseCircleID(in.CircleID)
				if err != nil {
					return nil, err
				}
				targetID, err := parseTargetID(in.TargetID)
				if err != nil {
					return nil, err
				}
				p, ok := PrincipalFrom(ctx)
				if !ok {
					return nil, apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
				}
				if err = s.requireOverrideIfMatch(ctx, in.IfMatch, circleID, targetID); err != nil {
					return nil, err
				}

				view, err := s.cfg.Catalogue.PutOverride(ctx, circleID, targetID, p.MembershipID,
					in.Body.request(""))
				if err != nil {
					return nil, err
				}
				// The override now outranks the catalogue for this circle, and no row was appended
				// to say so. Pushed rather than inferred — see [TimerInvalidator].
				if err = s.cfg.Invalidator.OnTimerChange(ctx, circleID, targetID); err != nil {
					return nil, err
				}
				etag, err := ETagOf(view)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}
				return &putCircleTimerOverrideOutput{ETag: etag, Body: OverrideResponse{
					TimerOverride: view, AsOf: s.cfg.Clock.Now(),
				}}, nil
			})),

		registerFailure(OpDeleteCircleTimerOverride, Register(s.api, OpDeleteCircleTimerOverride,
			func(ctx context.Context, in *deleteCircleTimerOverrideInput) (*deleteCircleTimerOverrideOutput, error) {
				circleID, err := parseCircleID(in.CircleID)
				if err != nil {
					return nil, err
				}
				targetID, err := parseTargetID(in.TargetID)
				if err != nil {
					return nil, err
				}
				p, ok := PrincipalFrom(ctx)
				if !ok {
					return nil, apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
				}
				// The override as it stood, returned once. After this the circle falls back to the
				// catalogue timer, or to `no_timer` if there is none — and a DELETE that answered
				// with nothing would leave the caller unable to tell which.
				removed, err := s.cfg.Catalogue.DeleteOverride(ctx, circleID, targetID,
					p.MembershipID)
				if err != nil {
					return nil, err
				}
				// Removing an override moves the window as surely as setting one did: the circle
				// falls back to the catalogue timer, or to `unknown` if there is none. A board
				// still serving the deleted override is the same bug as one that never saw it set.
				if err = s.cfg.Invalidator.OnTimerChange(ctx, circleID, targetID); err != nil {
					return nil, err
				}
				return &deleteCircleTimerOverrideOutput{Body: OverrideResponse{
					TimerOverride: removed, AsOf: s.cfg.Clock.Now(),
				}}, nil
			})),
	)
}

// requireOverrideIfMatch enforces the concurrency rule on an override that may not exist yet.
//
// This PUT both creates and replaces, and the two need different preconditions. A create has no
// prior tag for a caller to send, so `If-Match: *` is borrowed as "and it must NOT exist"; a
// replace has one, so nothing but that tag will do.
//
// **The wildcard is therefore refused on an existing override, and that is the whole point of this
// helper rather than a bare [RequireIfMatch] call.** `RequireIfMatch` treats `*` as matching every
// current representation — correct RFC 9110 semantics, and correct everywhere else in this API —
// which would let an officer overwrite another officer's update having read nothing. Borrowing the
// wildcard for creation while still honouring it for replacement is the concurrency rule inverted:
// the one caller who can prove they have seen nothing gets to overwrite anything.
func (s *Server) requireOverrideIfMatch(
	ctx context.Context, header string, circleID core.CircleID, targetID core.RaidTargetID,
) error {
	current, err := s.cfg.Catalogue.GetOverride(ctx, circleID, targetID)
	if err != nil {
		coded, ok := apierr.From(err)
		if !ok || coded.Code() != apierr.CodeNotFound {
			return err
		}
		if strings.TrimSpace(header) != anyETag {
			return apierr.New(apierr.CodePreconditionRequired,
				"this circle has no override for that target yet; send If-Match: * to create one").
				WithField("header.If-Match", "must be * when the override does not exist")
		}
		return nil
	}
	if strings.TrimSpace(header) == anyETag {
		// Not a 428: the caller sent a precondition, it was syntactically fine, and it is refused
		// because the resource is already there. 412 is the answer that tells them to re-read,
		// and it carries the current representation so the retry costs no extra request.
		body, marshalErr := json.Marshal(current)
		if marshalErr != nil {
			return apierr.Wrap(apierr.CodeInternalError, marshalErr, "")
		}
		return apierr.New(apierr.CodePreconditionFailed,
			"this circle already has an override for that target; If-Match: * creates one, so "+
				"send the ETag you read instead of overwriting an update you have not seen").
			WithCurrent(body)
	}
	return RequireIfMatch(header, current)
}

// targetResponse builds the representation `getRaidTarget` returns and every write ETags against.
//
// `as_of` is left at zero here on purpose: the caller sets it AFTER computing the tag, so the tag
// covers the target and its timers and not the instant they were read. A tag that moved every
// second would make `If-Match` unusable.
func (s *Server) targetResponse(
	ctx context.Context, id core.RaidTargetID,
) (TargetResponse, error) {
	target, err := s.cfg.Catalogue.Get(ctx, id)
	if err != nil {
		return TargetResponse{}, err
	}
	timers, err := s.cfg.Catalogue.Timers(ctx, id)
	if err != nil {
		return TargetResponse{}, err
	}
	return TargetResponse{Target: target, Timers: timers}, nil
}

// parseTargetID reads a target id out of a path.
//
// It answers 404 for a malformed id, matching what a well-formed id that names nothing answers:
// the catalogue is instance-wide and hides nothing, but "not a ULID" and "no such target" are the
// same fact to a client and splitting them would only invite a probe.
func parseTargetID(raw string) (core.RaidTargetID, error) {
	id, err := core.ParseID[core.RaidTarget](raw)
	if err != nil {
		return core.RaidTargetID{}, apierr.Wrap(apierr.CodeNotFound, err, "no such raid target")
	}
	return id, nil
}

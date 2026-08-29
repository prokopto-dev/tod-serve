package setup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/catalogue"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/instancegrant"
	"github.com/prokopto-dev/tod-serve/internal/invite"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// Config is what a [Service] needs. Every field is required: a service that invents a clock
// behaves differently in a test than in production.
type Config struct {
	Store      *store.DB
	Circles    *circle.Service
	Invites    *invite.Service
	Identities *identity.Service
	Catalogue  *catalogue.Service
	Clock      clock.Clock
	Log        *slog.Logger
}

// Service runs first-run setup and answers whether it is still open.
type Service struct {
	db         *store.DB
	circles    *circle.Service
	invites    *invite.Service
	identities *identity.Service
	catalogue  *catalogue.Service
	clock      clock.Clock
	log        *slog.Logger
	// runs admits one [Service.Run] at a time. See the comment on Run for what it buys and what
	// it does not: it is a channel rather than a [sync.Mutex] so that a caller whose request was
	// cancelled while waiting stops waiting, instead of queueing to do work nobody will read.
	runs chan struct{}
}

// New returns a service.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("setup service: no store")
	case cfg.Circles == nil:
		return nil, errors.New("setup service: no circle service")
	case cfg.Invites == nil:
		return nil, errors.New("setup service: no invite service")
	case cfg.Identities == nil:
		return nil, errors.New("setup service: no identity service")
	case cfg.Catalogue == nil:
		return nil, errors.New("setup service: no catalogue service")
	case cfg.Clock == nil:
		return nil, errors.New("setup service: no clock")
	case cfg.Log == nil:
		return nil, errors.New("setup service: no logger")
	}
	return &Service{
		db: cfg.Store, circles: cfg.Circles, invites: cfg.Invites,
		identities: cfg.Identities, catalogue: cfg.Catalogue,
		clock: cfg.Clock, log: cfg.Log,
		runs: make(chan struct{}, 1),
	}, nil
}

// Available reports whether first-run setup may still write anything.
//
// **This is the whole security boundary of the wizard, and it is one read.** It is true exactly
// while nobody administers this instance, and it says nothing about whether the `instance` row
// exists: an instance with a row, a provider, a circle and no administrator is a half-finished
// setup somebody has to be able to finish.
func (s *Service) Available(ctx context.Context) (bool, error) {
	exists, err := instancegrant.AdministratorExists(ctx, s.db.Queries())
	return !exists, err
}

// Provider is one of the instance's identity providers, as the wizard reports it. The client
// secret is not here: nothing in this package ever renders one.
type Provider struct {
	Key               string
	Kind              string
	DisplayName       string
	Enabled           bool
	VerifiableSubject bool
}

// Circle is one of the instance's circles, as the wizard reports it. It is enough to name one in
// a re-run and nothing more.
type Circle struct {
	ID     core.CircleID
	Name   string
	Server string
}

// State is what already exists, so a re-run can say what it is about to do rather than surprise
// somebody with it.
//
// Every field here is READ, none is stored: `Available` in particular is derived on every call.
type State struct {
	// Available is [Service.Available]: setup may still write.
	Available bool
	// AdministratorExists is the fact Available is the negation of, reported in its own right so
	// an operator reading a refusal can see which half it was.
	AdministratorExists bool
	// Configured mirrors `/meta`: an `instance` row exists. It is deliberately NOT what closes
	// the window — see the package comment.
	Configured                bool
	InstanceName              string
	PublicURL                 string
	Timezone                  string
	SelfServiceCircleCreation bool
	Providers                 []Provider
	Circles                   []Circle
	// RaidTargets is how many the catalogue holds. Zero means `seed targets` has not run, which
	// is a working instance that reports `no_timer` everywhere rather than a broken one.
	RaidTargets int
	AsOf        core.Micros
}

// Describe reads what setup has to work with.
func (s *Service) Describe(ctx context.Context) (State, error) {
	q := s.db.Queries()
	out := State{AsOf: s.clock.Now()}

	exists, err := instancegrant.AdministratorExists(ctx, q)
	if err != nil {
		return State{}, err
	}
	out.AdministratorExists = exists
	out.Available = !exists

	row, err := q.GetInstance(ctx)
	switch {
	case errors.Is(err, store.ErrNoRows):
		// Not an error, and the ordinary case: a fresh database is exactly the state this package
		// exists for.
	case err != nil:
		return State{}, fmt.Errorf("read the instance row: %w", err)
	default:
		out.Configured = true
		out.InstanceName = row.Name
		out.PublicURL = row.PublicUrl
		out.Timezone = row.Timezone
		out.SelfServiceCircleCreation = row.SelfServiceCircleCreation == 1
	}

	providers, err := s.identities.Providers(ctx)
	if err != nil {
		return State{}, err
	}
	out.Providers = make([]Provider, 0, len(providers))
	for _, p := range providers {
		out.Providers = append(out.Providers, Provider{
			Key: p.Key, Kind: string(p.Kind), DisplayName: p.DisplayName,
			Enabled: p.Enabled, VerifiableSubject: p.VerifiableSubject,
		})
	}

	circles, err := q.ListLiveCircles(ctx)
	if err != nil {
		return State{}, fmt.Errorf("read the circles: %w", err)
	}
	out.Circles = make([]Circle, 0, len(circles))
	for _, c := range circles {
		id, parseErr := core.ParseID[core.Circle](c.ID)
		if parseErr != nil {
			return State{}, parseErr
		}
		out.Circles = append(out.Circles, Circle{ID: id, Name: c.Name, Server: c.Server})
	}

	targets, err := q.ListAllRaidTargets(ctx)
	if err != nil {
		return State{}, fmt.Errorf("read the raid targets: %w", err)
	}
	out.RaidTargets = len(targets)
	return out, nil
}

// ProviderRequest is the identity provider the instance will accept. `local` is a provider like
// any other here, and takes the same acknowledgement it takes everywhere else.
type ProviderRequest struct {
	Key         string
	Kind        string
	DisplayName string

	Issuer                string
	AuthorizationEndpoint string
	JWKSURI               string
	SubjectClaim          string

	ClientID      string
	ClientSecret  core.Secret
	RedirectURI   string
	TokenEndpoint string

	// AcknowledgeWeakRevocation is required to enable a provider with no verifiable subject, and
	// required again for the circle to accept one. It is not a checkbox this package can supply
	// on the caller's behalf: it is the only thing between an officer and a false belief that
	// revoking somebody ended their access.
	AcknowledgeWeakRevocation bool
}

// CircleRequest is the first circle: either one to create, or one that already exists and needs a
// fresh owner code.
type CircleRequest struct {
	// ID names an EXISTING circle. Required once the instance has any, so that a re-run cannot
	// quietly leave a second circle behind — see [Service.Run].
	ID string
	// Name and Server create a new one. A circle is pinned to its server permanently (ADR-0009).
	Name        string
	Server      string
	Description string
	Timezone    string
}

// RunRequest is the whole wizard, as one submission.
type RunRequest struct {
	Name                      string
	PublicURL                 string
	Timezone                  string
	SelfServiceCircleCreation bool
	Provider                  ProviderRequest
	Circle                    CircleRequest
}

// The outcomes a step reports. A re-run says what it did to each thing rather than reporting a
// blanket success, because "nothing hidden silently" applies hardest to the run that writes the
// rows nobody can yet sign in to inspect.
const (
	OutcomeCreated  = "created"
	OutcomeUpdated  = "updated"
	OutcomeExisting = "already_present"
)

// Step is one thing setup did, and what it did to it.
type Step struct {
	Name    string
	Outcome string
	Detail  string
}

// Result is what one run produced.
type Result struct {
	Steps []Step
	// Circle is the circle the owner code admits somebody to.
	Circle circle.Circle
	// OwnerCode is the one-time code, returned EXACTLY ONCE and stored nowhere: `tod_meta` holds
	// only its hash. Redeeming it at `/join` is what creates the instance's first administrator.
	OwnerCode invite.Code
	// OwnerCodeExpiresAt is when it stops working — 24 hours, like every other owner grant.
	OwnerCodeExpiresAt core.Micros
	// JoinPath is where to send the browser, code in the FRAGMENT: a fragment is not sent to any
	// server, not to a proxy and not in a `Referer`.
	JoinPath string
	// Seeded is what the catalogue seed did. It is additive and safe to re-run.
	Seeded catalogue.TargetSeedReport
	AsOf   core.Micros
}

// localProviderKey is the one provider kind with no third party behind it. The constant is here so
// the acknowledgement and the circle's accept list cannot spell it differently.
const localKind = "local"

// Run performs first-run setup and returns the one-time owner code.
//
// **Every step is create-if-absent, and the order is the invariant.** The provider is written
// before the circle, because [circle.Service.Create] auto-accepts the enabled providers that exist
// at the moment it runs; the owner code is minted last, because it is the only step that produces
// something the caller cannot recover by asking again. A run that dies half-way leaves a prefix
// [Service.Describe] reports and this function resumes from, which is what
// [Service.Available] being derived from the ADMINISTRATOR buys.
//
// It does NOT open one transaction across the whole thing. The composing services each own one,
// and holding SQLite's single write lock across the catalogue seed would block the instance for
// the duration. ADR-0016 states the trade.
//
// The caller has already checked [Service.Available]; this checks it again, because the gap
// between an authorization decision and the write it authorises is where a second caller fits.
//
// **Two runs at once are stopped twice, because one mechanism cannot cover both halves.**
//
// The state below is a SNAPSHOT, and every write that follows it is a decision made from that
// snapshot. Without serialisation, run B can describe the instance in the window after A wrote
// the instance row and before A created its circle: both see no circle, both create one, and the
// operator ends up with a circle nobody asked for beside the one the owner code admits them to.
//
//   - `runs` admits one run at a time in this process, so B waits and then describes what A
//     actually left. B is refused by [validate] before it writes ANYTHING — which is the half
//     that matters to an operator, because a run refused after `instanceStep` has already
//     overwritten the instance name with the losing run's.
//   - [circle.Service.CreateFirst] counts and inserts in ONE transaction, so the circle cannot be
//     duplicated even by a second process sharing the database file, where this channel reaches
//     nothing at all.
//
// Neither is redundant: the first is about what a losing run leaves behind, the second about what
// it creates.
func (s *Service) Run(ctx context.Context, req RunRequest) (Result, error) {
	select {
	case s.runs <- struct{}{}:
		defer func() { <-s.runs }()
	case <-ctx.Done():
		return Result{}, fmt.Errorf("wait for the setup run already in progress: %w", ctx.Err())
	}

	state, err := s.Describe(ctx)
	if err != nil {
		return Result{}, err
	}
	if !state.Available {
		return Result{}, ErrAdministratorExists
	}
	if err := validate(req, state); err != nil {
		return Result{}, err
	}

	out := Result{AsOf: s.clock.Now()}
	if err := s.instanceStep(ctx, req, state, &out); err != nil {
		return Result{}, err
	}
	providerKey, err := s.providerStep(ctx, req, state, &out)
	if err != nil {
		return Result{}, err
	}
	view, err := s.circleStep(ctx, req, state, providerKey, &out)
	if err != nil {
		return Result{}, err
	}
	out.Circle = view

	// The seed is last of the writes and deliberately not fatal to what came before: an instance
	// with no raid targets is a working instance that reports `no_timer` everywhere, and failing
	// the whole run over it would throw away a circle and a provider that are already correct.
	report, err := s.catalogue.SeedTargets(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("seed the raid-target catalogue: %w", err)
	}
	out.Seeded = report
	out.Steps = append(out.Steps, Step{
		Name: "catalogue", Outcome: outcomeFor(report.TargetsAdded > 0),
		Detail: fmt.Sprintf("%d targets added, %d already present. Timers are NOT bundled: "+
			"until `tod-serve seed timers --file` runs, every target reports no_timer",
			report.TargetsAdded, report.TargetsPresent),
	})

	code, expiresAt, err := s.invites.MintOwnerGrant(ctx, view.ID)
	if err != nil {
		return Result{}, err
	}
	out.OwnerCode = code
	out.OwnerCodeExpiresAt = expiresAt
	out.JoinPath = "/join#" + code.String()
	out.Steps = append(out.Steps, Step{
		Name: "owner_code", Outcome: OutcomeCreated,
		Detail: "shown once and stored nowhere; redeeming it makes its holder this instance's " +
			"first administrator",
	})

	// The PREFIX is loggable and is how a code in a screenshot is later recognised. The code is
	// not, and neither is the setup token that authorised this.
	s.log.InfoContext(ctx, "first-run setup completed",
		slog.String("circle_id", view.ID.String()),
		slog.String("provider", providerKey),
		slog.String("code_prefix", code.Prefix()))
	return out, nil
}

// errCircleExists is the refusal to create a second circle, worded in one place because it is
// reached from two: [validate], which read the circles before the run started, and [circleStep],
// which is told by the database that another run created one while this one was working.
//
// `count` is how many circles the refusing run could see. Zero means it saw none and lost the
// race anyway, and the sentence says so rather than claiming a number it does not have — an
// operator reading "already has 0 circle(s)" would reasonably conclude the wizard was broken.
func errCircleExists(count int) error {
	problem := apierr.New(apierr.CodeConflict,
		"another setup run created this instance's first circle while this one was working; "+
			"setup will not create a second. GET /setup, then name the circle the owner code "+
			"should admit somebody to in circle.id")
	if count > 0 {
		problem = apierr.Newf(apierr.CodeConflict,
			"this instance already has %d circle(s); setup will not create another. Name the one "+
				"the owner code should admit somebody to in circle.id", count)
	}
	return problem.WithField("body.circle.id", "required once the instance has a circle")
}

// ErrAdministratorExists is returned when setup is asked to write on an instance somebody already
// administers. It is the third of the three refusals, and the only one that is not a 404: a caller
// who got this far presented the setup token, so telling them setup is over reveals nothing.
var ErrAdministratorExists = errors.New("this instance already has an administrator")

func validate(req RunRequest, state State) error {
	if strings.TrimSpace(req.Name) == "" {
		return apierr.New(apierr.CodeValidationFailed, "the instance needs a name").
			WithField("body.name", "required")
	}
	if strings.TrimSpace(req.Provider.Key) == "" {
		return apierr.New(apierr.CodeValidationFailed,
			"setup needs an identity provider, or nobody can sign in").
			WithField("body.provider.key", "required")
	}
	if req.Provider.Kind == localKind && !req.Provider.AcknowledgeWeakRevocation {
		return apierr.Newf(apierr.CodeAcknowledgementRequired,
			"%q has no verifiable subject, so revoking a member who joined through it does not "+
				"stop them rejoining under a new name", req.Provider.Key).
			WithField("body.provider.acknowledge_weak_revocation", "required for this provider")
	}
	switch {
	case req.Circle.ID != "":
		if _, err := core.ParseID[core.Circle](req.Circle.ID); err != nil {
			return apierr.Wrap(apierr.CodeValidationFailed, err, "that is not a circle id").
				WithField("body.circle.id", "not a circle id")
		}
	case len(state.Circles) > 0:
		// Refused rather than reused or duplicated. Reusing whichever circle happened to be
		// first would issue an owner code for a circle the operator did not name; creating a
		// second would leave the first orphaned with nobody able to delete it.
		return errCircleExists(len(state.Circles))
	case strings.TrimSpace(req.Circle.Name) == "":
		return apierr.New(apierr.CodeValidationFailed, "the first circle needs a name").
			WithField("body.circle.name", "required")
	case !core.Server(req.Circle.Server).Valid():
		return apierr.Newf(apierr.CodeValidationFailed,
			"a circle is pinned to one server permanently; it must be one of %s", core.Servers()).
			WithField("body.circle.server", "not a server")
	}
	return nil
}

// instanceStep writes the singleton, or corrects the one already there.
//
// A re-run UPDATES rather than skips, because the reason to re-run is usually that something in
// here was wrong: a public URL that does not match the redirect URI registered with the provider
// is a sign-in that completes and lands nowhere.
func (s *Service) instanceStep(
	ctx context.Context, req RunRequest, state State, out *Result,
) error {
	now := s.clock.Now()
	flag := int64(0)
	if req.SelfServiceCircleCreation {
		flag = 1
	}
	timezone := req.Timezone
	if timezone == "" {
		timezone = "UTC"
	}
	if state.Configured {
		if _, err := s.db.Queries().UpdateInstance(ctx, sqlitegen.UpdateInstanceParams{
			Name: req.Name, PublicUrl: req.PublicURL, Timezone: timezone,
			SelfServiceCircleCreation: flag, UpdatedAt: int64(now),
		}); err != nil {
			return fmt.Errorf("update the instance row: %w", err)
		}
		out.Steps = append(out.Steps, Step{
			Name: "instance", Outcome: OutcomeUpdated,
			Detail: "an instance row was already here and now says what this run submitted",
		})
		return nil
	}
	if _, err := s.db.Queries().CreateInstance(ctx, sqlitegen.CreateInstanceParams{
		Name: req.Name, PublicUrl: req.PublicURL, Timezone: timezone,
		SelfServiceCircleCreation: flag, CreatedAt: int64(now), UpdatedAt: int64(now),
	}); err != nil {
		return fmt.Errorf("create the instance row: %w", err)
	}
	out.Steps = append(out.Steps, Step{Name: "instance", Outcome: OutcomeCreated})
	return nil
}

// providerStep adds the identity provider, or leaves the one already registered exactly as it is.
//
// It never overwrites. A re-run whose form carried an empty client secret — which is what a
// browser sends for a write-only field it cannot read back — would otherwise clear the OAuth
// application's credential and break every sign-in the instance had.
func (s *Service) providerStep(
	ctx context.Context, req RunRequest, state State, out *Result,
) (string, error) {
	key := strings.TrimSpace(req.Provider.Key)
	for _, existing := range state.Providers {
		if existing.Key != key {
			continue
		}
		out.Steps = append(out.Steps, Step{
			Name: "provider", Outcome: OutcomeExisting,
			Detail: fmt.Sprintf(
				"%q was already registered and is left exactly as it is, secret included", key),
		})
		return key, nil
	}

	created, err := s.identities.AddProvider(ctx, identity.AddProviderRequest{
		Key:                       key,
		Kind:                      identity.Kind(req.Provider.Kind),
		DisplayName:               req.Provider.DisplayName,
		Enabled:                   true,
		Issuer:                    req.Provider.Issuer,
		AuthorizationEndpoint:     req.Provider.AuthorizationEndpoint,
		JWKSURI:                   req.Provider.JWKSURI,
		SubjectClaim:              req.Provider.SubjectClaim,
		ClientID:                  req.Provider.ClientID,
		ClientSecret:              req.Provider.ClientSecret,
		RedirectURI:               req.Provider.RedirectURI,
		TokenEndpoint:             req.Provider.TokenEndpoint,
		AcknowledgeWeakRevocation: req.Provider.AcknowledgeWeakRevocation,
	})
	if err != nil {
		return "", err
	}
	detail := "revocation through it is durable: the provider can tell us the account is gone"
	if !created.VerifiableSubject {
		detail = "revocation through it is ADVISORY: nobody can tell us the account is gone, so " +
			"a revoked member holding any live invite returns under a new name"
	}
	out.Steps = append(out.Steps, Step{
		Name: "provider", Outcome: OutcomeCreated, Detail: detail,
	})
	return created.Key, nil
}

// circleStep resolves the circle the owner code will admit somebody to, creating one only when the
// instance has none.
//
// Making the circle accept the provider is part of this step rather than the last one, because a
// circle that accepts nothing cannot be joined at all — and an owner code for a circle nobody can
// join is a setup that reports success and leaves the operator locked out.
func (s *Service) circleStep(
	ctx context.Context, req RunRequest, state State, providerKey string, out *Result,
) (circle.Circle, error) {
	if req.Circle.ID != "" {
		id, err := core.ParseID[core.Circle](req.Circle.ID)
		if err != nil {
			return circle.Circle{}, err
		}
		view, err := s.circles.Get(ctx, id)
		if err != nil {
			return circle.Circle{}, err
		}
		out.Steps = append(out.Steps, Step{
			Name: "circle", Outcome: OutcomeExisting,
			Detail: fmt.Sprintf("%q on %s, which this run issues a fresh owner code for",
				view.Name, view.Server),
		})
		return s.acceptProvider(ctx, view, providerKey, req.Provider.AcknowledgeWeakRevocation, out)
	}

	// CreateFirst, not Create: the condition this step was authorised by — that the instance has
	// no circle — was read in [Service.Describe], and it is re-checked inside the transaction that
	// does the insert. A run that lost the race writes nothing and says so.
	view, err := s.circles.CreateFirst(ctx, circle.CreateRequest{
		Name:        req.Circle.Name,
		Description: req.Circle.Description,
		Server:      core.Server(req.Circle.Server),
		Timezone:    firstNonEmpty(req.Circle.Timezone, req.Timezone),
	})
	if errors.Is(err, circle.ErrNotFirst) {
		return circle.Circle{}, errCircleExists(0)
	}
	if err != nil {
		return circle.Circle{}, err
	}
	out.Steps = append(out.Steps, Step{Name: "circle", Outcome: OutcomeCreated})
	return s.acceptProvider(ctx, view, providerKey, req.Provider.AcknowledgeWeakRevocation, out)
}

// acceptProvider makes the circle accept the provider setup just configured, if it does not
// already.
//
// A new circle auto-accepts every enabled provider with a verifiable subject and NEVER `local` —
// [identity.AutoAccepted] is the one place that rule lives. So for `local` this is the operator
// reaching for it explicitly, which is the same decision `circle create --accept-local` is, made
// at the only moment there is nobody in the UI to make it.
func (s *Service) acceptProvider(
	ctx context.Context, view circle.Circle, providerKey string, acknowledged bool, out *Result,
) (circle.Circle, error) {
	accepted := make([]circle.AcceptedProvider, 0, len(view.AcceptedProviders)+1)
	for _, p := range view.AcceptedProviders {
		if p.Key == providerKey {
			return view, nil
		}
		accepted = append(accepted, circle.AcceptedProvider{
			Key:                    p.Key,
			DiscordGuildID:         p.DiscordGuildID,
			DiscordRequiredRoleIDs: p.DiscordRequiredRoleIDs,
		})
	}
	accepted = append(accepted, circle.AcceptedProvider{Key: providerKey})
	view, err := s.circles.SetProviders(ctx, view.ID, circle.SetProvidersRequest{
		Providers:                 accepted,
		AcknowledgeWeakRevocation: acknowledged,
	})
	if err != nil {
		return circle.Circle{}, err
	}
	out.Steps = append(out.Steps, Step{
		Name: "circle_providers", Outcome: OutcomeUpdated,
		Detail: fmt.Sprintf("the circle accepts %q; revocation in it is %s",
			providerKey, view.RevocationStrength),
	})
	return view, nil
}

func outcomeFor(changed bool) string {
	if changed {
		return OutcomeCreated
	}
	return OutcomeExisting
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

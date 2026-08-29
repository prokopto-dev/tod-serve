package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/setup"
)

// checkSetupOpen refuses a setup route on an instance somebody already administers.
//
// It runs AFTER [Builder.checkSetupToken] and never before it, which is what keeps the three
// refusals from leaking into each other: a caller with no token learns nothing about the instance's
// state, and a caller with the right token gets a different code from the one they get for a wrong
// one, because at that point there is nothing left to hide from them.
//
// The window is derived on every request rather than cached. It is one read on two routes nobody
// calls after setup, and a cached answer to "may this request take the instance over" is the last
// thing in this codebase that should be allowed to go stale.
func (b *Builder) checkSetupOpen(ctx huma.Context) error {
	available, err := b.cfg.Setup.Available(ctx.Context())
	if err != nil {
		return apierr.Wrap(apierr.CodeInternalError, err, "")
	}
	if !available {
		return apierr.New(apierr.CodeConflict,
			"this instance already has an administrator, so first-run setup is over. "+
				"Sign in, or use `tod-serve instance grant` at the console")
	}
	return nil
}

// SetupProvider is one identity provider, as first-run setup reports it. No secret is here: the
// wizard never renders one, and there is no field for one to arrive in.
type SetupProvider struct {
	Key         string `json:"key"`
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
	// VerifiableSubject is a CHECK against `kind`, never a toggle. A circle accepting a provider
	// with this false is weakly revocable, and the wizard says so before anybody chooses it.
	VerifiableSubject bool `json:"verifiable_subject"`
}

// SetupCircle is one circle that already exists — enough to name it in a re-run and nothing more.
type SetupCircle struct {
	ID     core.CircleID `json:"id"`
	Name   string        `json:"name"`
	Server string        `json:"server"`
}

// SetupState is what first-run setup has to work with.
//
// It exists so a RE-RUN can say what it is about to do. The dangerous state this whole flow is
// shaped around — an instance row with no administrator behind it — looks like success from
// `/meta` and is reported honestly here.
type SetupState struct {
	// Available is the window, DERIVED on every read: no identity administers this instance. It
	// is never a stored flag, and it is deliberately not "the instance row is missing".
	Available bool `json:"available"`
	// AdministratorExists is what Available is the negation of, reported in its own right.
	AdministratorExists bool `json:"administrator_exists"`
	// Configured mirrors `/meta`: an `instance` row exists. It does NOT close the window.
	Configured                bool            `json:"configured"`
	InstanceName              string          `json:"instance_name"`
	PublicURL                 string          `json:"public_url"`
	Timezone                  string          `json:"timezone"`
	SelfServiceCircleCreation bool            `json:"self_service_circle_creation"`
	Providers                 []SetupProvider `json:"providers"`
	Circles                   []SetupCircle   `json:"circles"`
	// RaidTargets is how many the catalogue holds. Zero is a working instance that reports
	// `no_timer` everywhere, not a broken one.
	RaidTargets int `json:"raid_targets"`
	// AsOf is the instant this answer was computed.
	AsOf core.Micros `json:"as_of"`
}

// SetupStep is one thing a run did, and what it did to it. A re-run reports every step rather than
// a blanket success: nothing hidden silently applies hardest to the rows nobody can yet sign in to
// look at.
type SetupStep struct {
	Name    string `json:"name"`
	Outcome string `json:"outcome" doc:"created, updated or already_present"`
	Detail  string `json:"detail,omitempty"`
}

// SetupResult is what one run produced, including the code that has to be used before it expires.
type SetupResult struct {
	Steps []SetupStep `json:"steps"`
	// CircleID and CircleName name the circle the owner code admits somebody to.
	CircleID   core.CircleID `json:"circle_id"`
	CircleName string        `json:"circle_name"`
	// RevocationStrength is `weak` or `strong`, derived from the providers the circle accepts. It
	// is in the response because the operator has just chosen it and this is the moment to say so.
	RevocationStrength string `json:"revocation_strength"`
	// OwnerCode is returned EXACTLY ONCE and stored nowhere — `tod_meta` holds only its hash, so
	// a database read yields no working credential. Redeeming it makes its holder this instance's
	// first administrator.
	OwnerCode string `json:"owner_code"`
	// OwnerCodeExpiresAt is when it stops working.
	OwnerCodeExpiresAt core.Micros `json:"owner_code_expires_at"`
	// JoinPath is where to send the browser, with the code in the FRAGMENT — never a query string
	// and never a path segment, because a fragment is not sent to any server, not to a proxy and
	// not in a `Referer`.
	JoinPath string `json:"join_path"`
	// RaidTargetsAdded and RaidTargetsPresent are the catalogue seed. Timers are NOT bundled and
	// this does not load any: until `tod-serve seed timers --file` runs, every target reports
	// `no_timer` and times of death are still recorded correctly.
	RaidTargetsAdded   int         `json:"raid_targets_added"`
	RaidTargetsPresent int         `json:"raid_targets_present"`
	AsOf               core.Micros `json:"as_of"`
}

type getSetupStateInput struct{}

type getSetupStateOutput struct{ Body SetupState }

// SetupProviderRequest is the first identity provider, as the wizard submits it.
//
// It is a named type rather than an anonymous struct because the OpenAPI schema namer strips the
// package: an inline `Provider` field here would be documented as `ProviderStruct`, and a second
// one anywhere in this repository would collide with it and panic at startup.
type SetupProviderRequest struct {
	Key         string `json:"key" doc:"The wire key /join dispatches on" maxLength:"40"`
	Kind        string `json:"kind" doc:"discord, oidc or local. Immutable after this: it decides verifiable_subject" enum:"discord,oidc,local"`
	DisplayName string `json:"display_name,omitempty" doc:"What the join page calls it" maxLength:"80"`

	Issuer                string `json:"issuer,omitempty" doc:"OIDC only"`
	AuthorizationEndpoint string `json:"authorization_endpoint,omitempty" doc:"OIDC only"`
	JWKSURI               string `json:"jwks_uri,omitempty" doc:"OIDC only"`
	SubjectClaim          string `json:"subject_claim,omitempty" doc:"OIDC only. Defaults to sub"`

	ClientID string `json:"client_id,omitempty" doc:"The operator's own OAuth application. Required for discord and oidc, forbidden for local"`
	// ClientSecret is write-only. It is a plain string here because that is what arrives on the
	// wire; it becomes a [core.Secret] before it leaves this package, and no response type in this
	// file has a field it could come back out of.
	ClientSecret  string `json:"client_secret,omitempty" doc:"Write-only: it is never returned by any operation"`
	RedirectURI   string `json:"redirect_uri,omitempty"`
	TokenEndpoint string `json:"token_endpoint,omitempty"`

	AcknowledgeWeakRevocation bool `json:"acknowledge_weak_revocation,omitempty" doc:"Required for the local provider: revocation through a provider with no verifiable subject does not stop a revoked member rejoining under a new name"`
}

// SetupCircleRequest is the first circle: one to create, or an existing one to issue a fresh owner
// code for.
type SetupCircleRequest struct {
	ID          string `json:"id,omitempty" doc:"An EXISTING circle to issue the owner code for. Required once this instance has any circle"`
	Name        string `json:"name,omitempty" doc:"The first circle's name" maxLength:"80"`
	Server      string `json:"server,omitempty" doc:"blue, green or red. Immutable after creation" enum:"blue,green,red"`
	Description string `json:"description,omitempty" maxLength:"280"`
	Timezone    string `json:"timezone,omitempty"`
}

type runSetupInput struct {
	Body struct {
		Name                      string `json:"name" doc:"The instance's name" maxLength:"80"`
		PublicURL                 string `json:"public_url,omitempty" doc:"Where this instance is reachable. It must match the redirect URI registered with the identity provider EXACTLY"`
		Timezone                  string `json:"timezone,omitempty" doc:"IANA timezone, display only. Defaults to UTC"`
		SelfServiceCircleCreation bool   `json:"self_service_circle_creation,omitempty" doc:"Let any authenticated principal create a circle"`

		Provider SetupProviderRequest `json:"provider"`
		Circle   SetupCircleRequest   `json:"circle"`
	}
}

type runSetupOutput struct{ Body SetupResult }

// registerSetup attaches the two first-run operations.
//
// Neither resolves a principal and neither can: on the database they exist for, no credential has
// ever been issued. What authorises them is `TOD_SETUP_TOKEN` and the absence of an administrator,
// both checked in the route middleware before either handler runs — see [Builder.checkSetupToken]
// and [Builder.checkSetupOpen]. ADR-0016.
func (s *Server) registerSetup() error {
	return errors.Join(
		registerFailure(OpGetSetupState, Register(s.api, OpGetSetupState,
			func(ctx context.Context, _ *getSetupStateInput) (*getSetupStateOutput, error) {
				state, err := s.cfg.Setup.Describe(ctx)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}
				return &getSetupStateOutput{Body: setupState(state)}, nil
			})),

		registerFailure(OpRunSetup, Register(s.api, OpRunSetup,
			func(ctx context.Context, in *runSetupInput) (*runSetupOutput, error) {
				// `Idempotency-Key` is required by the registry row and the middleware enforces it,
				// and this handler does not read it — which is stated rather than hidden. What
				// makes a retry safe here is the WORLD, not the header: every step is
				// create-if-absent and a second circle is refused outright, so two runs converge
				// whether or not they carried the same key. `idempotency_record` is not available
				// either — its `principal_membership_id` is NOT NULL and setup has no principal.
				result, err := s.cfg.Setup.Run(ctx, setup.RunRequest{
					Name:                      in.Body.Name,
					PublicURL:                 in.Body.PublicURL,
					Timezone:                  in.Body.Timezone,
					SelfServiceCircleCreation: in.Body.SelfServiceCircleCreation,
					Provider: setup.ProviderRequest{
						Key:                       in.Body.Provider.Key,
						Kind:                      in.Body.Provider.Kind,
						DisplayName:               in.Body.Provider.DisplayName,
						Issuer:                    in.Body.Provider.Issuer,
						AuthorizationEndpoint:     in.Body.Provider.AuthorizationEndpoint,
						JWKSURI:                   in.Body.Provider.JWKSURI,
						SubjectClaim:              in.Body.Provider.SubjectClaim,
						ClientID:                  in.Body.Provider.ClientID,
						ClientSecret:              core.Secret(in.Body.Provider.ClientSecret),
						RedirectURI:               in.Body.Provider.RedirectURI,
						TokenEndpoint:             in.Body.Provider.TokenEndpoint,
						AcknowledgeWeakRevocation: in.Body.Provider.AcknowledgeWeakRevocation,
					},
					Circle: setup.CircleRequest{
						ID:          in.Body.Circle.ID,
						Name:        in.Body.Circle.Name,
						Server:      in.Body.Circle.Server,
						Description: in.Body.Circle.Description,
						Timezone:    in.Body.Circle.Timezone,
					},
				})
				if errors.Is(err, setup.ErrAdministratorExists) {
					// Reachable despite the middleware having just checked: somebody redeemed an
					// owner code between that check and this write. The same answer either way.
					return nil, apierr.Wrap(apierr.CodeConflict, err,
						"this instance already has an administrator, so first-run setup is over")
				}
				if err != nil {
					return nil, err
				}
				return &runSetupOutput{Body: setupResult(result)}, nil
			})),
	)
}

func setupState(state setup.State) SetupState {
	providers := make([]SetupProvider, 0, len(state.Providers))
	for _, p := range state.Providers {
		providers = append(providers, SetupProvider{
			Key: p.Key, Kind: p.Kind, DisplayName: p.DisplayName,
			Enabled: p.Enabled, VerifiableSubject: p.VerifiableSubject,
		})
	}
	circles := make([]SetupCircle, 0, len(state.Circles))
	for _, c := range state.Circles {
		circles = append(circles, SetupCircle{ID: c.ID, Name: c.Name, Server: c.Server})
	}
	return SetupState{
		Available:                 state.Available,
		AdministratorExists:       state.AdministratorExists,
		Configured:                state.Configured,
		InstanceName:              state.InstanceName,
		PublicURL:                 state.PublicURL,
		Timezone:                  state.Timezone,
		SelfServiceCircleCreation: state.SelfServiceCircleCreation,
		Providers:                 providers,
		Circles:                   circles,
		RaidTargets:               state.RaidTargets,
		AsOf:                      state.AsOf,
	}
}

func setupResult(result setup.Result) SetupResult {
	steps := make([]SetupStep, 0, len(result.Steps))
	for _, step := range result.Steps {
		steps = append(steps, SetupStep{
			Name: step.Name, Outcome: step.Outcome, Detail: step.Detail,
		})
	}
	return SetupResult{
		Steps:              steps,
		CircleID:           result.Circle.ID,
		CircleName:         result.Circle.Name,
		RevocationStrength: result.Circle.RevocationStrength,
		OwnerCode:          result.OwnerCode.String(),
		OwnerCodeExpiresAt: result.OwnerCodeExpiresAt,
		JoinPath:           result.JoinPath,
		RaidTargetsAdded:   result.Seeded.TargetsAdded,
		RaidTargetsPresent: result.Seeded.TargetsPresent,
		AsOf:               result.AsOf,
	}
}

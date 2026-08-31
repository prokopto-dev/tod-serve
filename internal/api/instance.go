package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/instancesettings"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// InstanceSettingKey is which setting one ledger row is about, as the document publishes it.
//
// It is a type rather than a `string` with an `enum:"…"` tag so that the published list comes from
// the ONE catalogue that also generates the SQL CHECK - canonical section 5 - instead of being a
// third hand-written copy that can fall behind it. `public_url` is absent from both for the same
// reason: no endpoint moves it, so no row can be about it.
type InstanceSettingKey string

// Schema returns the setting-key schema, with every setting in the catalogue.
//
// The lookup's "found" result is deliberately discarded: the name is a constant in the same
// catalogue, so a miss means somebody deleted the enum, and this signature cannot report it. What
// catches it is that a miss renders an EMPTY `enum` — a list nothing can satisfy — which
// `TestOpenAPISpec` sees as a diff against the checked-in document rather than as a document that
// still looks plausible.
func (InstanceSettingKey) Schema(huma.Registry) *huma.Schema {
	enum, _ := schemaenum.Lookup(schemaenum.NameInstanceSettingChange)
	values := make([]any, 0, len(enum.Values))
	for _, v := range enum.Values {
		values = append(values, v)
	}
	return &huma.Schema{
		Type:        huma.TypeString,
		Description: "Which instance setting a ledger row is about.",
		Enum:        values,
	}
}

// InstanceSettings is the instance-wide policy, as an operator holding `instance.security.manage`
// reads it.
//
// **`public_url` is here and is READ-ONLY, and the asymmetry is the point.** An operator debugging
// a sign-in that "does nothing" needs to see the origin this instance believes it is at; what they
// must not be able to do is change it here. It has to match the redirect URI registered with every
// identity provider character for character, a mismatch sends the browser somewhere that leaves no
// evidence on this instance at all (#26), and it is resolved at boot from `$TOD_PUBLIC_URL` BEFORE
// this row — so a change would take effect at some later restart, long after anybody connected the
// two. `updateInstanceSettings` refuses it with `422 field_immutable`, and
// `instance_setting_change.setting` cannot hold it, so there is no second way in.
//
// `updated_at` and `revision` are part of the representation rather than beside it, because the
// entity tag is computed over this type. `revision` is the load-bearing one: turning a switch on
// and off again returns every other field here to its old value, and a tag that then repeated
// would answer `304` to a client whose copy is two ledger rows behind.
type InstanceSettings struct {
	// Name is the operator's name for the instance, as `/meta` publishes it.
	Name string `json:"name"`
	// PublicURL is the origin this instance is reachable at. Read-only — see the type comment.
	PublicURL string `json:"public_url" doc:"Read-only: it must keep matching every registered redirect URI. Sending it is 422 field_immutable"`
	// Timezone is the instance default. Display only: every instant on the wire is `Micros` and
	// every countdown is a signed offset from a response's `as_of`.
	Timezone string `json:"timezone"`
	// SelfServiceCircleCreation is the instance's stated policy on who may create a circle. It is
	// what `/meta` publishes and what a client reads to decide whether to offer the option.
	//
	// It is PUBLISHED, NOT ENFORCED: `createCircle` declares `instance.circle.create` in the route
	// registry unconditionally, so nothing on that path reads this row. See
	// [ServerMeta.SelfServiceCircleCreation] for the whole of it and the test that pins it.
	SelfServiceCircleCreation bool `json:"self_service_circle_creation" doc:"The instance's stated policy on who may create a circle. Published, not yet enforced: createCircle still requires instance.circle.create"`
	// UpdatedAt is when the row last moved.
	UpdatedAt core.Micros `json:"updated_at"`
	// Revision is the settings ledger's chain head, and it is what makes the entity tag over this
	// type a version rather than a guess.
	//
	// `updated_at` is a CLOCK READING, not a revision: two commits can land in the same
	// microsecond, and if the second restores what the first replaced then every other field here
	// returns to its old value — so a tag without this repeats, a revalidating client is told
	// `304`, and the two ledger rows it should have seen never arrive. A chain hash covers the
	// row's own ULID and `ux_instance_setting_change_hash` forbids a duplicate, so it cannot
	// repeat. It is also what lets a reader check the chain for themselves.
	Revision string `json:"revision" doc:"The settings ledger's chain head. Empty on an instance nothing has changed"`
}

// InstanceSettingChange is one row of the hash-chained ledger: one setting that moved, and who
// moved it.
//
// The values are rendered exactly as the database holds them, so the boolean reads `0` and `1`
// here as it does in the `instance` row. Translating them would put a second spelling of the
// same fact in front of the person checking what happened.
type InstanceSettingChange struct {
	// ID is the ledger row's id.
	ID string `json:"id"`
	// Setting is which switch moved.
	Setting InstanceSettingKey `json:"setting"`
	// OldValue and NewValue are the database's own renderings.
	OldValue string `json:"old_value"`
	NewValue string `json:"new_value"`
	// ChangedByIdentityID is the identity that decided, and is EMPTY for a change written at the
	// console — which is a different fact from a person having decided it, and is why `by_console`
	// sits beside it rather than a client having to infer it from an empty string.
	ChangedByIdentityID string `json:"changed_by_identity_id"`
	ByConsole           bool   `json:"by_console"`
	// Reason is free text somebody typed. It is shown in every listing and carries no secret.
	Reason string `json:"reason"`
	// ChangedAt is when.
	ChangedAt core.Micros `json:"changed_at"`
}

// InstanceSettingsResponse is the settings, the whole ledger behind them, and the instant it was
// read.
//
// **The ledger is not truncated.** One row is one setting an administrator changed on a singleton,
// through a step-up route; an instance with enough of them to need a page has a different problem,
// and dropping the oldest rows would hide exactly what an audit record exists to show. `changes`
// and `as_of` sit on the response rather than in [InstanceSettings] so the entity tag is computed
// over the settings alone — the thing `If-Match` is a precondition on.
type InstanceSettingsResponse struct {
	InstanceSettings
	Changes []InstanceSettingChange `json:"changes" doc:"Every recorded change, newest first"`
	AsOf    core.Micros             `json:"as_of"`
}

// instanceSettings renders the domain settings for the wire.
func instanceSettings(s instancesettings.Settings) InstanceSettings {
	return InstanceSettings{
		Name:                      s.Name,
		PublicURL:                 s.PublicURL,
		Timezone:                  s.Timezone,
		SelfServiceCircleCreation: s.SelfServiceCircleCreation,
		UpdatedAt:                 s.UpdatedAt,
		Revision:                  s.Revision,
	}
}

// instanceSettingChanges renders the ledger for the wire.
func instanceSettingChanges(changes []instancesettings.Change) []InstanceSettingChange {
	out := make([]InstanceSettingChange, 0, len(changes))
	for _, c := range changes {
		row := InstanceSettingChange{
			ID:        c.ID.String(),
			Setting:   InstanceSettingKey(c.Setting),
			OldValue:  c.OldValue,
			NewValue:  c.NewValue,
			ByConsole: c.ByConsole(),
			Reason:    c.Reason,
			ChangedAt: c.ChangedAt,
		}
		if !c.ChangedBy.IsZero() {
			row.ChangedByIdentityID = c.ChangedBy.String()
		}
		out = append(out, row)
	}
	return out
}

type getInstanceSettingsInput struct {
	IfNoneMatch string `header:"If-None-Match" doc:"Revalidate a cached copy"`
}

type getInstanceSettingsOutput struct {
	ETag string `header:"ETag"`
	Body InstanceSettingsResponse
}

type updateInstanceSettingsInput struct {
	IfMatch string `header:"If-Match" doc:"The ETag a previous read returned"`
	Body    struct {
		Name                      *string `json:"name,omitempty" maxLength:"80"`
		Timezone                  *string `json:"timezone,omitempty" doc:"IANA timezone, display only"`
		SelfServiceCircleCreation *bool   `json:"self_service_circle_creation,omitempty" doc:"Let any authenticated principal create a circle"`
		// Reason is recorded in the ledger and shown in every listing, so it must carry no secret.
		Reason string `json:"reason,omitempty" doc:"Why, recorded in the hash-chained ledger and shown in every listing" maxLength:"280"`

		// PublicURL is accepted only so that sending it is REFUSED with the code that says why.
		// Ignoring it would let an operator believe they had moved this instance's origin, and the
		// failure that produces leaves no evidence here: the provider redirects the browser to the
		// old origin, so no request arrives and nothing logs anything. See #26.
		PublicURL *string `json:"public_url,omitempty" doc:"Rejected with 422 field_immutable: it must keep matching every registered redirect URI"`
	}
}

type updateInstanceSettingsOutput struct {
	ETag string `header:"ETag"`
	Body InstanceSettingsResponse
}

// registerInstance attaches the instance-settings operations.
//
// **Both carry `instance.security.manage` rather than `instance.owner`, and the choice is not the
// obvious one.** `instance.owner` expands to the WHOLE instance realm (ADR-0015), so requiring it
// here would mean the only way to let somebody flip this switch is to hand them the identity
// providers, the raid-target catalogue and the ops dashboard as well — a strictly narrower route
// reachable only through a strictly wider grant. `instance.security.manage` is already the key
// `instancegrant.Administers` reads as "administers this instance", it is in the capability floor
// so no token reaches it at any scope and a re-authenticated session is required, and every
// instance owner holds it through the same expansion. Whoever may add a `local` identity provider
// and let the world in can already do far more than turn self-service circle creation on.
//
// Neither declares `CreatesState`, and `Idempotency-Key` is therefore not required. What makes a
// retry safe here is `If-Match`: the second attempt carries the tag of the state the first one
// replaced, so it is refused with `412` rather than applied twice. A settings change is not an
// append to the domain — the ledger row it writes is the audit record OF the change, not the
// change itself.
func (s *Server) registerInstance() error {
	return errors.Join(
		registerFailure(OpGetInstanceSettings, Register(s.api, OpGetInstanceSettings,
			func(ctx context.Context, _ *getInstanceSettingsInput) (
				*getInstanceSettingsOutput, error,
			) {
				// ONE read snapshot for the settings, the revision and the ledger. As three
				// pooled statements a writer committing between them returns the old settings
				// beside the new revision — an entity tag describing a state that never existed,
				// which refuses the caller's very next write with `412` although nobody changed
				// anything after their read — or a revision that does not cover the rows returned
				// beside it. ADR-0014, and the shape of issue #17.
				current, changes, err := s.cfg.InstanceSettings.Describe(ctx)
				if err != nil {
					return nil, err
				}
				view := instanceSettings(current)
				etag, err := ETagOf(view)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}
				return &getInstanceSettingsOutput{
					ETag: etag,
					Body: InstanceSettingsResponse{
						InstanceSettings: view,
						Changes:          instanceSettingChanges(changes),
						AsOf:             s.cfg.Clock.Now(),
					},
				}, nil
			})),

		registerFailure(OpUpdateInstanceSettings, Register(s.api, OpUpdateInstanceSettings,
			func(ctx context.Context, in *updateInstanceSettingsInput) (
				*updateInstanceSettingsOutput, error,
			) {
				if in.Body.PublicURL != nil {
					// Refused before the read, so the answer does not depend on what the current
					// row says. See the field's own comment for why this is not merely ignored.
					return nil, apierr.New(apierr.CodeFieldImmutable,
						"this instance's public URL is not changeable over the API: it must match "+
							"the redirect URI registered with every identity provider exactly, and "+
							"is resolved at startup from $TOD_PUBLIC_URL. Change that, re-register "+
							"the redirect URI with the provider, and restart").
						WithField("body.public_url", "immutable")
				}

				// The principal's IDENTITY, not their membership: an instance decision outlives
				// any one circle, which is the same reason `instance_grant` is keyed on an
				// identity (ADR-0012). A service membership has no identity and cannot reach here
				// anyway — this route is session-only.
				p, ok := PrincipalFrom(ctx)
				if !ok {
					return nil, apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
				}

				// **`If-Match` is enforced INSIDE the transaction that writes**, by handing the
				// comparison to `Apply` rather than making it here against a separate read. Two
				// administrators holding the same tag would otherwise both pass a check out here,
				// serialise in the writing transaction, and the loser would commit anyway —
				// appending a ledger row on a precondition that had stopped holding, while this
				// operation documents a `412`. A believed audit row is worse than none.
				//
				// It costs one read: `Apply` has to read the row it is replacing regardless, so
				// the tag is computed from that read rather than from an earlier one.
				updated, recorded, err := s.cfg.InstanceSettings.Apply(ctx,
					instancesettings.ChangeRequest{
						Name:                      in.Body.Name,
						Timezone:                  in.Body.Timezone,
						SelfServiceCircleCreation: in.Body.SelfServiceCircleCreation,
						ChangedBy:                 p.IdentityID,
						Reason:                    in.Body.Reason,
					},
					func(current instancesettings.Settings) error {
						return RequireIfMatch(in.IfMatch, instanceSettings(current))
					})
				if err != nil {
					return nil, err
				}
				view := instanceSettings(updated)
				etag, err := ETagOf(view)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}
				// The changes THIS request recorded, not the whole ledger: the response to a write
				// is where an operator finds out exactly what was written, and a reader who wants
				// the history asks for it.
				return &updateInstanceSettingsOutput{
					ETag: etag,
					Body: InstanceSettingsResponse{
						InstanceSettings: view,
						Changes:          instanceSettingChanges(recorded),
						AsOf:             s.cfg.Clock.Now(),
					},
				}, nil
			})),
	)
}

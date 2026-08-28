package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"

	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/circle"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// The flags the bootstrap verbs take.
const (
	flagName        = "name"
	flagServer      = "server"
	flagCircle      = "circle"
	flagPublicURL   = "public-url"
	flagTimezone    = "timezone"
	flagSelfService = "self-service"
	flagDescription = "description"
	flagLocal       = "local"
	flagAcceptLocal = "accept-local"
	flagAcknowledge = "acknowledge-weak-revocation"
)

// The verbs this file registers. Named because each appears in the command itself, in the message
// another verb prints to point at it, and in the error a missing flag produces — three copies of
// one string is exactly the drift this repository gates against elsewhere.
const (
	verbInit   = "init"
	verbCircle = "circle"
	verbCreate = "create"
)

// newInitCommand creates the `instance` singleton, and optionally the first circle.
//
// **This is how the instance gets its first user.** On a fresh database nobody holds a credential,
// no circle exists, and there is therefore no principal any HTTP route could authorise — so the
// bootstrap has to be a command that already holds the database. It must work, and the code it
// prints must be single-use.
func newInitCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   verbInit,
		Short: "Create the instance singleton, and optionally the first circle",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			name, _ := cmd.Flags().GetString(flagName)
			publicURL, _ := cmd.Flags().GetString(flagPublicURL)
			timezone, _ := cmd.Flags().GetString(flagTimezone)
			selfService, _ := cmd.Flags().GetBool(flagSelfService)
			circleName, _ := cmd.Flags().GetString(flagCircle)
			server, _ := cmd.Flags().GetString(flagServer)
			enableLocal, _ := cmd.Flags().GetBool(flagLocal)
			acceptLocal, _ := cmd.Flags().GetBool(flagAcceptLocal)
			acknowledged, _ := cmd.Flags().GetBool(flagAcknowledge)

			if err := checkWeakAcknowledgement(enableLocal || acceptLocal, acknowledged); err != nil {
				return err
			}
			if circleName != "" && server == "" {
				return fmt.Errorf(
					"--%s needs --%s: a circle is pinned to one server permanently (ADR-0009)",
					flagCircle, flagServer)
			}

			path, err := databasePath(cmd)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			log := textLogger(io.Discard)
			db, closeDB, err := openStore(cmd.Context(), path, log)
			if err != nil {
				return err
			}
			defer closeDB()
			if err := db.Ready(cmd.Context()); err != nil {
				return fmt.Errorf("%w: run `tod-serve migrate` first", err)
			}

			clk := clock.System{}
			now := clk.Now()
			if _, err := db.Queries().GetInstance(cmd.Context()); err == nil {
				return errors.New("this instance is already initialised; " +
					"use `tod-serve circle create` to add a circle")
			} else if !store.IsNotFound(err) {
				return fmt.Errorf("read the instance row: %w", err)
			}

			flag := int64(0)
			if selfService {
				flag = 1
			}
			row, err := db.Queries().CreateInstance(cmd.Context(), sqlitegen.CreateInstanceParams{
				Name: name, PublicUrl: publicURL, Timezone: timezone,
				SelfServiceCircleCreation: flag,
				CreatedAt:                 int64(now), UpdatedAt: int64(now),
			})
			if err != nil {
				return fmt.Errorf("create the instance row: %w", err)
			}
			if _, err := fmt.Fprintf(out, "instance %q initialised at %s\n", row.Name, path); err != nil {
				return fmt.Errorf("write init result: %w", err)
			}

			if enableLocal {
				if err := enableLocalProvider(cmd.Context(), db, now); err != nil {
					return err
				}
				if _, err := fmt.Fprintf(out,
					"\nthe %q identity provider is enabled. Revocation through it is ADVISORY:\n"+
						"  a revoked member holding any live invite returns under a new name, and the\n"+
						"  damage is the officers' belief that revocation worked, not the re-entry.\n",
					localProviderKey); err != nil {
					return fmt.Errorf("write init result: %w", err)
				}
			}

			if circleName == "" {
				_, err := fmt.Fprintf(out,
					"\nNo circle yet. Create one, and the owner code to redeem, with:\n"+
						"  tod-serve %s %s --%s \"Your Guild\" --%s blue\n",
					verbCircle, verbCreate, flagName, flagServer)
				if err != nil {
					return fmt.Errorf("write init result: %w", err)
				}
				return nil
			}
			return createCircle(cmd.Context(), out, db, clk, createCircleRequest{
				Name: circleName, Server: server, Timezone: timezone,
				AcceptLocal: acceptLocal, Acknowledged: acknowledged,
			})
		},
	}
	cmd.Flags().String(flagName, "tod-serve", "the instance's name")
	cmd.Flags().String(flagPublicURL, "",
		"where this instance is reachable, e.g. https://tod.example.com")
	cmd.Flags().String(flagTimezone, defaultTimezone, "IANA timezone, display only")
	cmd.Flags().Bool(flagSelfService, false,
		"let any authenticated principal create a circle")
	cmd.Flags().String(flagCircle, "", "also create a first circle with this name")
	cmd.Flags().String(flagServer, "", "the first circle's server: blue, green or red")
	cmd.Flags().Bool(flagLocal, false,
		"create and enable the `local` identity provider — needs --"+flagAcknowledge)
	cmd.Flags().Bool(flagAcceptLocal, false,
		"have the first circle accept `local` — needs --"+flagAcknowledge)
	cmd.Flags().Bool(flagAcknowledge, false,
		"acknowledge that revocation through an unverifiable provider is advisory")
	return cmd
}

// localProviderKey is the wire key the CLI gives the `local` provider. `identity_provider.key` is
// unique and there is at most one `local` row, so it is a constant rather than a flag.
const localProviderKey = "local"

// checkWeakAcknowledgement is docs/design/04-identity §6 at the command line.
//
// `local` ships disabled and enabling it takes an explicit acknowledgement, because the failure it
// creates is not a technical one: an officer revokes a leaker, the leaker redeems another invite
// as "Tanky", and is reading the same ToDs a minute later while the officers believe the problem
// is handled. A flag somebody has to type is the only thing that reliably reaches them.
func checkWeakAcknowledgement(wantsWeak, acknowledged bool) error {
	if wantsWeak && !acknowledged {
		return fmt.Errorf(
			"%q has no verifiable subject, so revoking a member who joined through it does not "+
				"stop them rejoining: pass --%s to accept that",
			localProviderKey, flagAcknowledge)
	}
	return nil
}

// enableLocalProvider writes the instance's `local` row.
//
// `verifiable_subject` is 0 and `client_id` is NULL, and neither is a choice this function makes:
// both are CHECKs against `kind`, so a `local` row claiming otherwise is unrepresentable rather
// than merely wrong. That is the whole reason revocation strength can be trusted to be derived.
func enableLocalProvider(ctx context.Context, db *store.DB, now core.Micros) error {
	if _, err := db.Queries().GetIdentityProviderByKey(ctx, localProviderKey); err == nil {
		return fmt.Errorf("the %q provider already exists", localProviderKey)
	} else if !store.IsNotFound(err) {
		return fmt.Errorf("read the %q provider: %w", localProviderKey, err)
	}
	id, err := core.NewID[core.IdentityProvider](core.NewGenerator(rand.Reader), now)
	if err != nil {
		return fmt.Errorf("mint an identity provider id: %w", err)
	}
	_, err = db.Queries().CreateIdentityProvider(ctx, sqlitegen.CreateIdentityProviderParams{
		ID: id.String(), Key: localProviderKey, Kind: localProviderKey,
		DisplayName: "This server", Enabled: 1, VerifiableSubject: 0,
		CreatedAt: int64(now), UpdatedAt: int64(now),
	})
	if err != nil {
		return fmt.Errorf("create the %q provider: %w", localProviderKey, err)
	}
	return nil
}

// newCircleCommand groups the circle verbs. `create` is the only one: everything else a circle
// needs is an API operation an owner reaches with the credential this command produces.
func newCircleCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   verbCircle,
		Short: "Circle administration that needs the database rather than a credential",
	}

	create := &cobra.Command{
		Use:   verbCreate,
		Short: "Create a circle and print its one-time owner code",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			name, _ := cmd.Flags().GetString(flagName)
			server, _ := cmd.Flags().GetString(flagServer)
			description, _ := cmd.Flags().GetString(flagDescription)
			timezone, _ := cmd.Flags().GetString(flagTimezone)
			acceptLocal, _ := cmd.Flags().GetBool(flagAcceptLocal)
			acknowledged, _ := cmd.Flags().GetBool(flagAcknowledge)
			if err := checkWeakAcknowledgement(acceptLocal, acknowledged); err != nil {
				return err
			}
			switch {
			case name == "":
				return fmt.Errorf("--%s is required", flagName)
			case server == "":
				return fmt.Errorf("--%s is required: blue, green or red", flagServer)
			}

			path, err := databasePath(cmd)
			if err != nil {
				return err
			}
			db, closeDB, err := openStore(cmd.Context(), path, textLogger(io.Discard))
			if err != nil {
				return err
			}
			defer closeDB()
			if err := db.Ready(cmd.Context()); err != nil {
				return fmt.Errorf("%w: run `tod-serve migrate` first", err)
			}
			return createCircle(cmd.Context(), cmd.OutOrStdout(), db, clock.System{},
				createCircleRequest{
					Name: name, Server: server, Description: description, Timezone: timezone,
					AcceptLocal: acceptLocal, Acknowledged: acknowledged,
				})
		},
	}
	create.Flags().String(flagName, "", "the circle's name")
	create.Flags().String(flagServer, "", "blue, green or red. Immutable after creation")
	create.Flags().String(flagDescription, "", "free text")
	create.Flags().String(flagTimezone, defaultTimezone, "IANA timezone, display only")
	create.Flags().Bool(flagAcceptLocal, false,
		"also accept the `local` provider — needs --"+flagAcknowledge)
	create.Flags().Bool(flagAcknowledge, false,
		"acknowledge that revocation through an unverifiable provider is advisory")
	group.AddCommand(create)
	return group
}

type createCircleRequest struct {
	Name        string
	Server      string
	Description string
	Timezone    string
	// AcceptLocal adds the unverifiable provider to what this circle accepts.
	//
	// A new circle auto-accepts every enabled provider with a verifiable subject and NEVER
	// `local` — an owner must reach for it. Typing this flag is an operator reaching for it, which
	// is the same decision made at the only moment there is nobody to make it in the UI.
	AcceptLocal  bool
	Acknowledged bool
}

// createCircle writes the circle and prints the one-time owner code that admits its first owner.
//
// **The code is not an invite, and it cannot be.** `invite` carries `CHECK (role <> 'owner')`, so
// an invite that granted ownership is unrepresentable — which is the point: an invite is
// time-boxed, role-capped and mintable by a bot token, and a leaked one must never seize a circle.
// The owner code is a different thing with different properties: printed once, on the operator's
// own terminal, by a command that already holds the database, single-use by compare-and-swap, and
// expiring. It resolves through the same lookup as an invite, so `previewInvite` and `/join` have
// one code path for both.
func createCircle(
	ctx context.Context, out io.Writer, db *store.DB, clk clock.Clock, req createCircleRequest,
) error {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ids := core.NewGenerator(rand.Reader)
	circles, invites, _, err := dataServices(db, clk, ids, log)
	if err != nil {
		return err
	}

	view, err := circles.Create(ctx, circle.CreateRequest{
		Name:        req.Name,
		Description: req.Description,
		Server:      core.Server(req.Server),
		Timezone:    req.Timezone,
	})
	if err != nil {
		return err
	}
	if req.AcceptLocal {
		accepted := make([]circle.AcceptedProvider, 0, len(view.AcceptedProviders)+1)
		for _, p := range view.AcceptedProviders {
			accepted = append(accepted, circle.AcceptedProvider{
				Key:                    p.Key,
				DiscordGuildID:         p.DiscordGuildID,
				DiscordRequiredRoleIDs: p.DiscordRequiredRoleIDs,
			})
		}
		accepted = append(accepted, circle.AcceptedProvider{Key: localProviderKey})
		view, err = circles.SetProviders(ctx, view.ID, circle.SetProvidersRequest{
			Providers:                 accepted,
			AcknowledgeWeakRevocation: req.Acknowledged,
		})
		if err != nil {
			return err
		}
	}

	code, expiresAt, err := invites.MintOwnerGrant(ctx, view.ID)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(out, `
circle %q created on %s
  id           %s
  providers    %s
  revocation   %s

Redeem this ONE-TIME owner code to become its owner. It is shown once and never stored:

  %s

  expires      %s

Redeem it at POST /api/v1/join, or paste it into the join page:
  <public url>/join#%s

Redeeming it creates an IDENTITY, and instance administration hangs off that identity
rather than off this circle's owner role. If NOBODY administers this instance yet,
redeeming this code grants that identity %s in the same transaction, and there is no
further step: the API is administrable, adding the Discord provider included.

Once somebody does, that branch is closed for good. Handing administration to a second
operator, or recovering an instance whose grants were all revoked, is done here:

  tod-serve %s %s              # find the identity a join created
  tod-serve %s %s --%s <id> --%s %s

See docs/adr/0012-instance-grants-are-a-capability-ledger.md and
docs/adr/0016-first-run-setup-is-an-env-token-and-a-derived-window.md.
`,
		view.Name, view.Server, view.ID, providerKeys(view), view.RevocationStrength,
		code, expiresAt, code,
		authz.PermissionInstanceOwner,
		verbInstance, verbIdentities,
		verbInstance, verbGrant, flagIdentity, flagPermission,
		authz.PermissionInstanceOwner)
	if err != nil {
		return fmt.Errorf("write circle result: %w", err)
	}
	return nil
}

func providerKeys(view circle.Circle) string {
	if len(view.AcceptedProviders) == 0 {
		// Named rather than left blank: a circle that accepts nothing cannot be joined at all, and
		// an operator who has not configured a provider yet should read that here rather than
		// discover it when the owner code fails.
		return "none yet — POST /admin/identity-providers with `instance.security.manage`, " +
			"then setCircleProviders"
	}
	keys := make([]string, 0, len(view.AcceptedProviders))
	for _, p := range view.AcceptedProviders {
		keys = append(keys, p.Key)
	}
	return strings.Join(keys, ", ")
}

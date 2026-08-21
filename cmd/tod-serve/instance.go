package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/prokopto-dev/tod-serve/internal/authz"
	"github.com/prokopto-dev/tod-serve/internal/clock"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/instancegrant"
	"github.com/prokopto-dev/tod-serve/internal/store"
)

// The verbs and flags this file registers. Named because each appears in the command, in the
// message another verb prints to point at it, and in the error a missing flag produces.
const (
	verbInstance   = "instance"
	verbGrant      = "grant"
	verbRevoke     = "revoke"
	verbGrants     = "grants"
	verbIdentities = "identities"

	flagIdentity   = "identity"
	flagPermission = "permission"
	flagReason     = "reason"
	flagHistory    = "history"
)

// newInstanceCommand groups the instance-level authorization verbs.
//
// **This is the only writer of `instance_grant`, and that is ADR-0012's bootstrap answer rather
// than an omission.** A grant names an identity, an identity is created by joining a circle, and a
// fresh database has neither — so the first grant has to be written by something that already
// holds the database, exactly as `tod-serve init` and `tod-serve circle create` already write a
// circle without holding `instance.circle.create`.
//
// It is also the RECOVERY path, which is why an instance needs no `last_owner` rule: an instance
// whose last `instance.owner` grant is gone is still administrable from here. A rule forbidding
// that revocation would forbid the console the one operation it exists to make possible.
func newInstanceCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   verbInstance,
		Short: "Instance-level permissions, which need the database rather than a credential",
		Long: "Instance permissions are granted to an IDENTITY, not to a membership: a membership\n" +
			"is in one circle and an instance permission is about the whole instance. See\n" +
			"docs/adr/0012-instance-grants-are-a-capability-ledger.md.\n\n" +
			"Rows written here record no decider, which reads as \"the operator at the console\"\n" +
			"and is a different fact from a person having decided it.\n",
	}
	group.AddCommand(
		newInstanceGrantCommand(),
		newInstanceRevokeCommand(),
		newInstanceGrantsCommand(),
		newInstanceIdentitiesCommand(),
	)
	return group
}

func newInstanceGrantCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   verbGrant,
		Short: "Grant an instance-level permission to an identity",
		Long: "Grantable permissions: " + strings.Join(instancePermissionKeys(), ", ") + "\n\n" +
			"Find an identity with `tod-serve " + verbInstance + " " + verbIdentities + "`.\n",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return decide(cmd, instancegrant.DecisionGranted)
		},
	}
	addDecisionFlags(cmd)
	return cmd
}

func newInstanceRevokeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   verbRevoke,
		Short: "Revoke an instance-level permission from an identity",
		Long: "The revocation is a NEW ROW naming the grant it supersedes: `instance_grant` is\n" +
			"append-only, so the decision to take a permission away is as durable as the\n" +
			"decision to give it. It takes effect on the holder's very next request.\n",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return decide(cmd, instancegrant.DecisionRevoked)
		},
	}
	addDecisionFlags(cmd)
	return cmd
}

func addDecisionFlags(cmd *cobra.Command) {
	cmd.Flags().String(flagIdentity, "", "the identity's id, from `"+verbInstance+" "+verbIdentities+"`")
	cmd.Flags().String(flagPermission, "",
		"one of: "+strings.Join(instancePermissionKeys(), ", "))
	cmd.Flags().String(flagReason, "", "why, recorded in the ledger and shown in every listing")
}

// decide is the shared body of `grant` and `revoke`: the two differ only in what they record.
func decide(cmd *cobra.Command, decision instancegrant.Decision) error {
	rawIdentity, _ := cmd.Flags().GetString(flagIdentity)
	rawPermission, _ := cmd.Flags().GetString(flagPermission)
	reason, _ := cmd.Flags().GetString(flagReason)

	switch {
	case rawIdentity == "":
		return fmt.Errorf("--%s is required: find one with `tod-serve %s %s`",
			flagIdentity, verbInstance, verbIdentities)
	case rawPermission == "":
		return fmt.Errorf("--%s is required, one of: %s",
			flagPermission, strings.Join(instancePermissionKeys(), ", "))
	}
	identityID, err := core.ParseID[core.Identity](rawIdentity)
	if err != nil {
		return fmt.Errorf("--%s: %w", flagIdentity, err)
	}
	permission, err := authz.ParsePermission(rawPermission)
	if err != nil {
		return fmt.Errorf("--%s: %w; grantable permissions are: %s",
			flagPermission, err, strings.Join(instancePermissionKeys(), ", "))
	}
	if !authz.IsInstanceRealm(permission) {
		return fmt.Errorf(
			"--%s: %q is granted by a circle membership's role, not at the instance level; "+
				"grantable permissions are: %s",
			flagPermission, permission, strings.Join(instancePermissionKeys(), ", "))
	}

	grants, closeDB, err := openGrants(cmd)
	if err != nil {
		return err
	}
	defer closeDB()

	// DecidedBy is left ZERO deliberately: this row was written by whoever holds the database, not
	// by a principal the server authenticated, and recording a person here would be a stronger
	// claim than the truth.
	out, err := grants.Decide(cmd.Context(), instancegrant.DecideRequest{
		IdentityID: identityID,
		Permission: permission,
		Decision:   decision,
		Reason:     reason,
	})
	switch {
	case errors.Is(err, instancegrant.ErrNoChange):
		return fmt.Errorf("nothing to do: the ledger already records %q as %s for identity %s",
			permission, decision, identityID)
	case errors.Is(err, instancegrant.ErrUnknownIdentity):
		return fmt.Errorf("no identity %s on this instance: list them with `tod-serve %s %s`",
			identityID, verbInstance, verbIdentities)
	case err != nil:
		return err
	}

	_, err = fmt.Fprintf(cmd.OutOrStdout(),
		"%s %s for identity %s\n  decision id  %s\n  reason       %s\n",
		permission, out.Decision, out.IdentityID, out.ID, displayReason(out.Reason))
	if err != nil {
		return fmt.Errorf("write grant result: %w", err)
	}
	return nil
}

func newInstanceGrantsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   verbGrants,
		Short: "Show who holds which instance-level permissions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			history, _ := cmd.Flags().GetBool(flagHistory)
			grants, closeDB, err := openGrants(cmd)
			if err != nil {
				return err
			}
			defer closeDB()

			var rows []instancegrant.Grant
			if history {
				rows, err = grants.History(cmd.Context())
			} else {
				rows, err = grants.Current(cmd.Context())
			}
			if err != nil {
				return err
			}
			return writeGrants(cmd.OutOrStdout(), rows, history)
		},
	}
	cmd.Flags().Bool(flagHistory, false,
		"every decision ever recorded, oldest first, rather than the current ones")
	return cmd
}

// writeGrants prints the ledger. A revoked row is shown rather than filtered, even in the
// current-state listing: "nothing hidden silently" applies most to the table that says who may
// administer this instance, and an operator asking who holds `ops.read` is also asking who used to.
func writeGrants(out io.Writer, rows []instancegrant.Grant, history bool) error {
	if len(rows) == 0 {
		what := "no instance permission has been granted on this instance"
		if history {
			what = "no instance permission has ever been decided on this instance"
		}
		if _, err := fmt.Fprintf(out, "%s\n", what); err != nil {
			return fmt.Errorf("write grants: %w", err)
		}
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "IDENTITY\tPERMISSION\tDECISION\tDECIDED BY\tREASON"); err != nil {
		return fmt.Errorf("write grants: %w", err)
	}
	for _, g := range rows {
		by := "console"
		if !g.ByConsole() {
			by = g.DecidedBy.String()
		}
		_, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			g.IdentityID, g.Permission, g.Decision, by, displayReason(g.Reason))
		if err != nil {
			return fmt.Errorf("write grants: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("write grants: %w", err)
	}
	return nil
}

func newInstanceIdentitiesCommand() *cobra.Command {
	return &cobra.Command{
		Use:   verbIdentities,
		Short: "List the identities this instance knows, so a grant can name one",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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

			rows, err := db.Queries().ListIdentities(cmd.Context())
			if err != nil {
				return fmt.Errorf("list identities: %w", err)
			}
			out := cmd.OutOrStdout()
			if len(rows) == 0 {
				_, err := fmt.Fprintf(out,
					"no identities yet: nobody has joined a circle.\n"+
						"  Create one with `tod-serve %s %s`, redeem the owner code it prints,\n"+
						"  then come back and run `tod-serve %s %s`.\n",
					verbCircle, verbCreate, verbInstance, verbGrant)
				if err != nil {
					return fmt.Errorf("write identities: %w", err)
				}
				return nil
			}

			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "ID\tPROVIDER\tSUBJECT\tDISPLAY NAME\tBLOCKED"); err != nil {
				return fmt.Errorf("write identities: %w", err)
			}
			for _, row := range rows {
				blocked := "no"
				if row.BlockedAt != nil {
					blocked = "yes"
				}
				_, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					row.ID, row.ProviderKey, row.Subject, row.DisplayName, blocked)
				if err != nil {
					return fmt.Errorf("write identities: %w", err)
				}
			}
			if err := w.Flush(); err != nil {
				return fmt.Errorf("write identities: %w", err)
			}
			return nil
		},
	}
}

// openGrants opens the database and wires the ledger over it, returning the closer the caller
// defers. The verbs here hold no credential and authorise nothing: holding the database IS the
// authorisation, which is what makes this the bootstrap and the recovery path both.
func openGrants(cmd *cobra.Command) (*instancegrant.Service, func(), error) {
	path, err := databasePath(cmd)
	if err != nil {
		return nil, nil, err
	}
	db, closeDB, err := openStore(cmd.Context(), path, textLogger(io.Discard))
	if err != nil {
		return nil, nil, err
	}
	if err := db.Ready(cmd.Context()); err != nil {
		closeDB()
		return nil, nil, fmt.Errorf("%w: run `tod-serve migrate` first", err)
	}
	grants, err := newGrantService(db)
	if err != nil {
		closeDB()
		return nil, nil, err
	}
	return grants, closeDB, nil
}

// newGrantService is a named function rather than an inline literal so RAND001 has something to
// point at: the ULID generator's entropy has to be `crypto/rand.Reader` at the wiring site, not
// merely non-nil.
func newGrantService(db *store.DB) (*instancegrant.Service, error) {
	return instancegrant.New(instancegrant.Config{
		Store: db,
		Clock: clock.System{},
		IDs:   core.NewGenerator(rand.Reader),
		Log:   textLogger(io.Discard),
	})
}

// instancePermissionKeys is what the flag help and every "grantable permissions are" message list.
// It is derived from the catalogue rather than typed, so a permission added to the instance realm
// appears here without anybody remembering to.
func instancePermissionKeys() []string {
	perms := authz.InstancePermissions()
	keys := make([]string, 0, len(perms))
	for _, p := range perms {
		keys = append(keys, string(p))
	}
	return keys
}

func displayReason(reason string) string {
	if reason == "" {
		return "-"
	}
	return reason
}

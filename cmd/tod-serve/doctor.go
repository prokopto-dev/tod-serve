package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/prokopto-dev/tod-serve/internal/identity"
	"github.com/prokopto-dev/tod-serve/internal/store"
)

// newDoctorCommand reports what is wrong with an instance, or says that nothing is.
//
// It is a separate verb rather than only the `/admin/doctor` route because the failures it is
// most useful for are the ones that stop the server starting: unmigrated database, no instance
// row, no identity provider. A diagnostic that needs the thing it diagnoses to be working is a
// diagnostic for the wrong problem.
func newDoctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check this instance and say what is wrong",
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

			out := cmd.OutOrStdout()
			problems := 0
			say := func(status, detail string) {
				// Deliberate waiver: the report is best-effort output, and the exit code below is
				// what a script reads. A closed stdout must not turn a healthy instance into a
				// failure.
				_, _ = fmt.Fprintf(out, "  %-8s %s\n", status, detail)
			}
			ok := func(detail string) { say("ok", detail) }
			warn := func(detail string) { say("warn", detail) }
			bad := func(detail string) { problems++; say("PROBLEM", detail) }

			if _, err := fmt.Fprintf(out, "tod-serve %s — %s\n\n", version, path); err != nil {
				return fmt.Errorf("write doctor report: %w", err)
			}
			checkDatabase(cmd.Context(), db, ok, bad)
			checkInstance(cmd.Context(), db, ok, warn, bad)
			checkProviders(cmd.Context(), db, ok, warn, bad)

			if problems > 0 {
				return fmt.Errorf("%d problem(s) found", problems)
			}
			if _, err := fmt.Fprintln(out, "\nno problems found"); err != nil {
				return fmt.Errorf("write doctor report: %w", err)
			}
			return nil
		},
	}
}

func checkDatabase(ctx context.Context, db *store.DB, ok, bad func(string)) {
	version, err := db.SchemaVersion(ctx)
	if err != nil {
		bad("schema version is unreadable: " + err.Error())
	} else {
		ok(fmt.Sprintf("schema version %d", version))
	}
	if err := db.Ready(ctx); err != nil {
		bad("migrations are not up to date: run `tod-serve migrate` — " + err.Error())
	} else {
		ok("migrations are up to date")
	}
	if err := db.IntegrityCheck(ctx); err != nil {
		bad("integrity check: " + err.Error())
	} else {
		ok("integrity check passed")
	}
	// Foreign keys are only enforced when the pragma is on, and this schema is full of them. If it
	// were ever lost the damage is silent until a join returns nothing, which is why this runs
	// here rather than only in the test suite.
	if err := db.ForeignKeyCheck(ctx); err != nil {
		bad("foreign key check: " + err.Error())
	} else {
		ok("foreign key check passed")
	}
}

func checkInstance(ctx context.Context, db *store.DB, ok, warn, bad func(string)) {
	row, err := db.Queries().GetInstance(ctx)
	switch {
	case store.IsNotFound(err):
		bad("no instance row: run `tod-serve init`")
		return
	case err != nil:
		bad("instance row is unreadable: " + err.Error())
		return
	}
	ok(fmt.Sprintf("instance %q", row.Name))
	if strings.TrimSpace(row.PublicUrl) == "" {
		// A warning rather than a problem: everything works without it except the OAuth redirect,
		// and an instance running only `local` never needs one.
		warn("no public URL: the OAuth callback has nowhere to send a browser " +
			"(set it with `tod-serve init --public-url`, or $" + envPublicURL + ")")
	} else {
		ok("public URL " + row.PublicUrl)
	}
	if row.SelfServiceCircleCreation == 1 {
		warn("self-service circle creation is ON: any authenticated principal can create a circle")
	}
}

func checkProviders(ctx context.Context, db *store.DB, ok, warn, bad func(string)) {
	rows, err := db.Queries().ListIdentityProviders(ctx)
	if err != nil {
		bad("identity providers are unreadable: " + err.Error())
		return
	}
	enabled := 0
	for _, row := range rows {
		if row.Enabled != 1 {
			continue
		}
		enabled++
		if row.Kind == string(identity.KindLocal) {
			// `local` has no verifiable subject, so revoking a member who joined through it does
			// not stop them rejoining. The damage is the officers' false confidence, not the
			// re-entry, so this is said out loud on every run rather than once at setup.
			warn("provider \"" + row.Key + "\" is local: revocation there is ADVISORY, " +
				"and every circle accepting it reports revocation_strength=weak")
			continue
		}
		if row.ClientID == nil || *row.ClientID == "" {
			bad("provider \"" + row.Key + "\" has no client id: it cannot verify an audience")
			continue
		}
		ok("provider \"" + row.Key + "\" (" + row.Kind + ") is enabled")
	}
	if enabled == 0 {
		bad("no identity provider is enabled: nobody can join any circle")
	}
}

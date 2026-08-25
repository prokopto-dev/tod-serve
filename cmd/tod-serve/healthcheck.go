package main

import (
	"github.com/spf13/cobra"

	"github.com/prokopto-dev/tod-serve/internal/probe"
)

// flagAddr is the listen address the health check reads the port from.
const flagAddr = "addr"

// newHealthcheckCommand probes this instance's own listener and exits 0 or 1.
//
// It exists because the shipped image is `FROM scratch`: there is no shell, no `curl` and no
// `wget` for a `HEALTHCHECK` to call, so the binary probes itself. That is not a workaround for an
// austere image — it is what makes the austere image possible, and an image with a package manager
// in it to satisfy a health check is an image with a package manager in it.
//
// **Liveness only.** `/healthz` never touches the database (`internal/api/health.go`, and
// `TestLiveness_MakesNoDatabaseCall` drives it against a closed store), which is exactly what makes
// it safe for a container runtime to act on. Pointing this at `/readyz` would let a brief disk
// problem — or a migration in flight — mark the container unhealthy, and the runtime's response to
// that is not "wait".
//
// It prints nothing when the server answers. The exit code is the whole report, because the caller
// is a container runtime; when it does not answer, the reason goes to stderr, where
// `docker inspect` keeps it.
func newHealthcheckCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "healthcheck",
		Short: "Probe this instance's own listener; exit 0 if it answers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			addr, err := cmd.Flags().GetString(flagAddr)
			if err != nil {
				return err
			}
			if addr == "" {
				addr = envOr(envAddr, defaultAddr)
			}
			// Only the PORT of that address is used. The host is a loopback literal
			// `internal/probe` writes itself — see the package comment there, and PROBE001.
			return probe.Liveness(cmd.Context(), addr, probe.DefaultTimeout)
		},
	}
	cmd.Flags().String(flagAddr, "",
		"the listen address to read the port from ($"+envAddr+", default "+defaultAddr+")")
	return cmd
}

package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The deployment gates, watched failing.
//
// ENV001 and IMG001 are greps over files nothing compiles, so they fail in the direction that
// reports success: a pattern that stops matching reports "no findings" over a file it never read
// and looks exactly like a clean tree. These point them at deliberately broken fixtures and require
// them to fire, which is why scripts/deploy-gates.sh takes a directory rather than hard-coding
// `deploy`.

// deployGate runs one gate over a directory of fixture files and returns its exit code and output.
func deployGate(t *testing.T, mode string, files map[string]string, extra ...string) (int, string) {
	t.Helper()

	dir := t.TempDir()
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}

	args := append([]string{filepath.Join("..", "..", "scripts", "deploy-gates.sh"), mode, dir}, extra...)
	cmd := exec.CommandContext(t.Context(), "bash", args...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, "the gate must exit with a status, not fail to run: %s", out)
	return exit.ExitCode(), string(out)
}

// rootGo writes a stand-in for cmd/tod-serve/root.go holding the given constants, and returns its
// path. The gate reads the const block, so the fixture has to be a const block.
func rootGo(t *testing.T, names ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("package main\n\nconst (\n")
	for i, n := range names {
		b.WriteString("\tenv")
		b.WriteString(strings.ToUpper(n[:1]))
		b.WriteString(string(rune('a' + i)))
		b.WriteString(" = \"")
		b.WriteString(n)
		b.WriteString("\"\n")
	}
	b.WriteString(")\n")
	path := filepath.Join(t.TempDir(), "root.go")
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o600))
	return path
}

// TestENV001_FiresWhenTheTwoListsDisagree — every way the binary and the deployment can drift.
//
// The two sides are independent HAND-WRITTEN lists: a Go const block and a set of deployment files.
// That is what makes checking them in both directions worth anything — a both-directions comparison
// where both sides read from one derivation proves nothing and looks more rigorous than a one-way
// check rather than less.
func TestENV001_FiresWhenTheTwoListsDisagree(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		consts  []string
		files   map[string]string
		wantOut string
		why     string
	}{
		{
			name:   "a variable the binary reads and nobody documented",
			consts: []string{"TOD_ADDR", "TOD_NEW_THING"},
			files: map[string]string{
				"env.example":  "TOD_ADDR=:8080\n",
				"compose.yaml": "services:\n  s:\n    environment:\n      TOD_ADDR: \":8080\"\n",
			},
			wantOut: "TOD_NEW_THING",
			why:     "an operator cannot set what nobody wrote down",
		},
		{
			name:   "a variable the deployment names and the binary does not read",
			consts: []string{"TOD_TOKEN_PEPPER"},
			files: map[string]string{
				"env.example":  "TOD_TOKEN_PEPPER=x\n",
				"compose.yaml": "services:\n  s:\n    environment:\n      TOD_TOKEN_PEPER: x\n",
			},
			wantOut: "TOD_TOKEN_PEPER",
			why: "the typo interpolates to empty and the server refuses to start naming a " +
				"variable that IS set — the worst shape this failure can take",
		},
		{
			name:   "documented in a paragraph rather than as an entry",
			consts: []string{"TOD_ADDR"},
			files: map[string]string{
				"env.example": "The server listens on whatever TOD_ADDR says.\n",
			},
			wantOut: "TOD_ADDR",
			why:     "a mention is not documentation; the operator is looking for a line to copy",
		},
		{
			name:    "no env.example at all",
			consts:  []string{"TOD_ADDR"},
			files:   map[string]string{"compose.yaml": "services: {}\n"},
			wantOut: "env.example",
			why:     "the file the whole gate compares against is the one that can go missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			code, out := deployGate(t, "env", tt.files, rootGo(t, tt.consts...))
			require.Equal(t, 1, code, "ENV001 must fail this: %s\n%s", tt.why, out)
			require.Contains(t, out, tt.wantOut)
		})
	}
}

// The other half, and the half that keeps the gate usable. A gate with false positives gets
// switched off, which is worse than the problem it was added for.
func TestENV001_PassesWhatItShould(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		consts []string
		files  map[string]string
	}{
		{
			name:   "an entry, and the same name used in a compose file",
			consts: []string{"TOD_ADDR", "TOD_DB_PATH"},
			files: map[string]string{
				"env.example":  "TOD_ADDR=:8080\nTOD_DB_PATH=/data/tod.db\n",
				"compose.yaml": "services:\n  s:\n    environment:\n      TOD_DB_PATH: /data/tod.db\n",
			},
		},
		{
			name:   "documented as a commented-out entry",
			consts: []string{"TOD_METRICS_TOKEN"},
			files:  map[string]string{"env.example": "# TOD_METRICS_TOKEN=\n"},
		},
		{
			name:   "documented as an entry with prose after it",
			consts: []string{"TOD_PUBLIC_URL"},
			files:  map[string]string{"env.example": "# TOD_PUBLIC_URL   compose.yaml derives it\n"},
		},
		{
			// The convention that removes the need for an allowlist. TOD_DEPLOY_* is read by
			// docker compose and by the binary never, so it is not expected in the const block.
			name:   "a deployment-only variable",
			consts: []string{"TOD_ADDR"},
			files: map[string]string{
				"env.example":  "TOD_ADDR=:8080\nTOD_DEPLOY_HOST=tod.example.com\n",
				"compose.yaml": "services:\n  s:\n    image: ${TOD_DEPLOY_IMAGE:?set it}\n",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			code, out := deployGate(t, "env", tt.files, rootGo(t, tt.consts...))
			require.Equal(t, 0, code,
				"ENV001 must NOT fail this; a gate with false positives gets switched off\n%s", out)
		})
	}
}

// TestENV001_RefusesAPartialScan — the gate must never report success over a file it could not
// read. Neither `find` nor `grep` is all-or-nothing: grep prints every match from the files it
// opened and exits 2, find lists what it reached and exits non-zero, and partial output passes an
// is-it-empty test. With `2>/dev/null` swallowing the diagnostic and no `set -e` in the script,
// that status used to be dropped — a fixture whose compose.yaml was mode 000 and held
// TOD_TOKEN_PEPER exited 0 printing the usual count, silently skipping the one file the typo was
// in. This is the direction that catches a typo, so a partial scan is a false clean bill.
func TestENV001_RefusesAPartialScan(t *testing.T) {
	t.Parallel()

	// Mode 000 denies nobody when you are root, so there would be no partial read to refuse.
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 000 still reads, so this cannot be provoked")
	}

	tests := []struct {
		name    string
		hide    func(t *testing.T, dir string)
		wantOut string
		why     string
	}{
		{
			name: "a deployment file that cannot be opened",
			hide: func(t *testing.T, dir string) {
				unreadable(t, filepath.Join(dir, "compose.yaml"))
			},
			wantOut: "could not read every file",
			why:     "grep exits 2 having printed the matches from every OTHER file",
		},
		{
			name: "a directory that cannot be descended",
			hide: func(t *testing.T, dir string) {
				sub := filepath.Join(dir, "sub")
				require.NoError(t, os.Mkdir(sub, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(sub, "extra.yaml"),
					[]byte("TOD_NOT_A_CONSTANT: x\n"), 0o600))
				unreadable(t, sub)
			},
			wantOut: "could not list every file",
			why:     "find exits non-zero having listed everything it did reach",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "env.example"),
				[]byte("TOD_ADDR=:8080\n"), 0o600))
			// The typo this direction exists to catch, in the file that goes unread.
			require.NoError(t, os.WriteFile(filepath.Join(dir, "compose.yaml"),
				[]byte("services:\n  s:\n    environment:\n      TOD_TOKEN_PEPER: x\n"), 0o600))
			tt.hide(t, dir)

			args := []string{
				filepath.Join("..", "..", "scripts", "deploy-gates.sh"),
				"env", dir, rootGo(t, "TOD_ADDR"),
			}
			cmd := exec.CommandContext(t.Context(), "bash", args...)
			out, err := cmd.CombinedOutput()

			require.Error(t, err, "ENV001 must refuse rather than pass: %s\n%s", tt.why, out)
			require.Contains(t, string(out), tt.wantOut)
		})
	}
}

// TestDeployGates_RefuseAPartialRead — the same defect, checked across every mode of the script.
//
// It is a class, not an incident: a shell pipeline whose status nobody reads. `|| true` and a bare
// `$(…)` both discard it, `2>/dev/null` hides the diagnostic, and the script runs without `set -e`,
// so every one of these scans used to accept whatever it managed to read as the whole input. The
// guards are only worth having if they fire, so each mode is given a file it may not open and
// required to refuse.
func TestDeployGates_RefuseAPartialRead(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 000 still reads, so this cannot be provoked")
	}

	const pinned = "FROM node:24-bookworm@sha256:" + sixtyFourHex + "\n"
	const labelled = "services:\n  s:\n    image: caddy:2@sha256:" + sixtyFourHex + "\n" +
		"    labels:\n      traefik.http.routers.r.service: svc\n" +
		"      traefik.http.services.svc.loadbalancer.server.port: \"80\"\n"

	tests := []struct {
		name    string
		mode    string
		files   map[string]string
		hidden  string
		wantOut string
		why     string
	}{
		{
			name:    "IMG001, a Dockerfile it may not open",
			mode:    "images",
			files:   map[string]string{"Dockerfile": pinned, "Dockerfile.web": pinned, "compose.yaml": labelled},
			hidden:  "Dockerfile.web",
			wantOut: "could not read every Dockerfile",
			why:     "grep exits 2 having printed the FROM lines of every other Dockerfile",
		},
		{
			name:    "IMG001, a compose file it may not open",
			mode:    "images",
			files:   map[string]string{"Dockerfile": pinned, "compose.yaml": labelled, "extra.yaml": labelled},
			hidden:  "extra.yaml",
			wantOut: "could not read every compose file",
			why:     "an unread compose file is an unpinned image nobody sees",
		},
		{
			name:    "LBL001, a compose file it may not open",
			mode:    "traefik",
			files:   map[string]string{"compose.yaml": labelled, "extra.yaml": labelled},
			hidden:  "extra.yaml",
			wantOut: "could not read every compose file",
			why:     "a partial read drops a label that DEFINES a name, so a valid router looks dangling",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			for name, body := range tt.files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
			}
			// Readable, it must pass: otherwise the refusal below proves nothing about the guard.
			args := []string{filepath.Join("..", "..", "scripts", "deploy-gates.sh"), tt.mode, dir}
			out, err := exec.CommandContext(t.Context(), "bash", args...).CombinedOutput()
			require.NoError(t, err, "the fixture must be clean before it is broken\n%s", out)

			unreadable(t, filepath.Join(dir, tt.hidden))

			out, err = exec.CommandContext(t.Context(), "bash", args...).CombinedOutput()
			require.Error(t, err, "%s must refuse rather than pass: %s\n%s", tt.mode, tt.why, out)
			require.Contains(t, string(out), tt.wantOut)
		})
	}
}

// unreadable strips every permission bit from path and restores them afterwards, because
// t.TempDir's own cleanup cannot remove a directory it may not descend. Registered after the
// TempDir call, so it runs before that removal — t.Cleanup is last-in, first-out.
func unreadable(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() {
		require.NoError(t, os.Chmod(path, info.Mode().Perm()))
	})
}

// TestIMG001_FiresOnAnUnpinnedImage — PIN001's reasoning, applied to images.
func TestIMG001_FiresOnAnUnpinnedImage(t *testing.T) {
	t.Parallel()

	const goodFrom = "FROM node:24-bookworm@sha256:" + sixtyFourHex + " AS web\n"

	tests := []struct {
		name    string
		files   map[string]string
		wantOut string
		why     string
	}{
		{
			name:    "a bare tag in a Dockerfile",
			files:   map[string]string{"Dockerfile": "FROM node:24\n"},
			wantOut: "not pinned",
			why:     "`node:24` in six months is not the `node:24` this was tested against",
		},
		{
			name:    "a bare tag behind a --platform flag",
			files:   map[string]string{"Dockerfile": "FROM --platform=$BUILDPLATFORM golang:1.26\n"},
			wantOut: "not pinned",
			why:     "the flag is what a naive `awk '{print $2}'` would report as the image",
		},
		{
			name:    "a digest with no readable tag",
			files:   map[string]string{"Dockerfile": "FROM node@sha256:" + sixtyFourHex + "\n"},
			wantOut: "not pinned",
			why:     "a pin nobody can update, because nobody can tell what it was meant to be",
		},
		{
			name: "a third-party image in a compose file",
			files: map[string]string{
				"Dockerfile":   goodFrom,
				"compose.yaml": "services:\n  proxy:\n    image: caddy:2-alpine\n",
			},
			wantOut: "not pinned",
			why:     "the compose half is where a proxy or a sidecar arrives",
		},
		{
			name: "a compose digest with no readable tag anywhere",
			files: map[string]string{
				"Dockerfile":   goodFrom,
				"compose.yaml": "services:\n  proxy:\n    image: caddy@sha256:" + sixtyFourHex + "\n",
			},
			wantOut: "no readable tag",
			why:     "same as the Dockerfile case: a digest alone cannot be reviewed or bumped",
		},
		{
			name: "an interpolation that is not the one variable the deploy pins",
			files: map[string]string{
				"Dockerfile":   goodFrom,
				"compose.yaml": "services:\n  proxy:\n    image: ${PROXY_IMAGE:-caddy:2}\n",
			},
			wantOut: "which is not the one variable",
			why:     "this is exactly how an unpinned third-party image walks past a gate that waives interpolations",
		},
		{
			name: "the deploy variable defaulting to somebody else's image",
			files: map[string]string{
				"Dockerfile":   goodFrom,
				"compose.yaml": "services:\n  s:\n    image: ${TOD_DEPLOY_IMAGE:-ghcr.io/someone/else:edge}\n",
			},
			wantOut: "does not build",
			why:     "a default is what RUNS when the variable is unset, so it is a real reference",
		},
		{
			name:    "no image references at all",
			files:   map[string]string{"compose.yaml": "services: {}\n"},
			wantOut: "not matching",
			why:     "a checker that checked nothing must never look like a checker that found nothing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			code, out := deployGate(t, "images", tt.files)
			require.Equal(t, 1, code, "IMG001 must fail this: %s\n%s", tt.why, out)
			require.Contains(t, out, tt.wantOut)
		})
	}
}

// And the shapes that are correct, including `scratch`, which is not an image and has nothing to
// pin: refusing it would make the one thing that cannot be attacked the thing the gate complains
// about.
func TestIMG001_PassesWhatItShould(t *testing.T) {
	t.Parallel()

	code, out := deployGate(t, "images", map[string]string{
		"Dockerfile": "FROM --platform=$BUILDPLATFORM node:24-bookworm@sha256:" + sixtyFourHex + " AS web\n" +
			"FROM golang:1.26-bookworm@sha256:" + sixtyFourHex + " AS build\n" +
			"FROM scratch\n",
		"compose.yaml": "services:\n" +
			"  s:\n    image: ${TOD_DEPLOY_IMAGE:?set TOD_DEPLOY_IMAGE in .env}\n" +
			"  proxy:\n    image: caddy@sha256:" + sixtyFourHex + " # 2-alpine\n",
		"compose.local.yaml": "services:\n  s:\n    image: ${TOD_DEPLOY_IMAGE:-ghcr.io/prokopto-dev/tod-serve:edge}\n",
	})
	require.Equal(t, 0, code, "IMG001 must not fail a correctly pinned tree\n%s", out)

	// The counts are what prove it READ them: three pinned references and two waived
	// interpolations. A gate that had stopped matching would report a clean tree with zeroes.
	require.Equal(t, "3 2 3", strings.TrimSpace(out))
}

// sixtyFourHex is a syntactically valid digest. It names nothing; the gate checks shape.
const sixtyFourHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestLBL001_FiresOnANameNothingDefines — the Traefik labels are resolved BY STRING.
//
// A router whose `service=` names something no label defines is not an error anybody sees. The
// router exists, its target does not, and the host answers **404** — which is the same 404 Traefik
// gives a host it has never heard of, and the same one this project's runbook teaches an operator
// to read as "the container has not come up yet". Nothing in `docker compose config` looks at what
// a label SAYS.
func TestLBL001_FiresOnANameNothingDefines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		compose string
		wantOut string
		why     string
	}{
		{
			name: "a router pointing at a service nothing defines",
			compose: "services:\n  x:\n    labels:\n" +
				"      traefik.http.routers.x-secure.service: todserve\n" +
				"      traefik.http.services.tod-serve.loadbalancer.server.port: \"8080\"\n",
			wantOut: "names the service todserve",
			why:     "one missing hyphen, and the host 404s exactly as if it were unconfigured",
		},
		{
			name: "one bad name in a middleware chain",
			compose: "services:\n  x:\n    labels:\n" +
				"      traefik.http.routers.x.middlewares: x-redirect,x-hstsz\n" +
				"      traefik.http.middlewares.x-redirect.redirectscheme.scheme: https\n" +
				"      traefik.http.middlewares.x-hsts.headers.stsSeconds: \"31536000\"\n" +
				"      traefik.http.routers.x.service: x\n" +
				"      traefik.http.services.x.loadbalancer.server.port: \"80\"\n",
			wantOut: "names the middleware x-hstsz",
			why:     "a chain is comma-separated, so checking only the whole value would miss this",
		},
		{
			name: "the list spelling, which the other stacks on the droplet use",
			compose: "services:\n  x:\n    labels:\n" +
				"      - \"traefik.http.routers.x-secure.service=nope\"\n" +
				"      - \"traefik.http.services.x.loadbalancer.server.port=9000\"\n",
			wantOut: "names the service nope",
			why:     "a file copied from a working stack must be read, not waved through",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			code, out := deployGate(t, "traefik", map[string]string{"compose.yaml": tt.compose})
			require.Equal(t, 1, code, "LBL001 must fail this: %s\n%s", tt.why, out)
			require.Contains(t, out, tt.wantOut)
		})
	}
}

// And the shapes that are correct — including, verbatim, the Portainer labels running on the target
// droplet today. They are the reference this project's own labels were diffed against, so a gate
// that rejected them would be a gate measuring the wrong thing.
func TestLBL001_PassesWhatItShould(t *testing.T) {
	t.Parallel()

	const portainer = `services:
  portainer:
    labels:
      - "traefik.enable=true"
      - "traefik.http.routers.portainer.entrypoints=http"
      - "traefik.http.routers.portainer.rule=Host(` + "`portainer.prokopto.dev`" + `)"
      - "traefik.http.middlewares.portainer-https-redirect.redirectscheme.scheme=https"
      - "traefik.http.routers.portainer.middlewares=portainer-https-redirect"
      - "traefik.http.routers.portainer-secure.entrypoints=https"
      - "traefik.http.routers.portainer-secure.rule=Host(` + "`portainer.prokopto.dev`" + `)"
      - "traefik.http.routers.portainer-secure.tls=true"
      - "traefik.http.routers.portainer-secure.tls.certresolver=http"
      - "traefik.http.routers.portainer-secure.service=portainer"
      - "traefik.http.services.portainer.loadbalancer.server.port=9000"
      - "traefik.docker.network=proxy"
`

	code, out := deployGate(t, "traefik", map[string]string{"compose.yaml": portainer})
	require.Equal(t, 0, code, "LBL001 rejected a configuration that is serving in production\n%s", out)
	// Two references — the secure router's service, and the plain router's middleware. The count is
	// what proves it READ them: a scanner that had stopped matching would report a clean tree.
	require.Equal(t, "2 1", strings.TrimSpace(out))
}

// The vacancy check. Labels with no router reference at all means the pattern stopped matching, and
// a checker that checked nothing must never look like a checker that found nothing.
func TestLBL001_FiresWhenNothingWasChecked(t *testing.T) {
	t.Parallel()

	code, out := deployGate(t, "traefik", map[string]string{
		"compose.yaml": "services:\n  x:\n    labels:\n      traefik.enable: \"true\"\n",
	})
	require.Equal(t, 1, code, "a compose file with no Traefik references is a finding\n%s", out)
	require.Contains(t, out, "not matching")
}

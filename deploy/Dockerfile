# syntax=docker/dockerfile:1
#
# Three stages, final image FROM scratch.
#
# CGO is off — modernc.org/sqlite is pure Go — so there is nothing to link against and the image
# can be empty apart from the binary, the CA bundle and the tzdata it needs. An empty image is not
# austerity: it is the difference between "a CVE in the base image" being a rebuild and being an
# incident, and this one holds the only copy of a circle's report log.
#
# Base images are pinned by digest with the tag in a trailing comment — gate IMG001, which is
# PIN001's reasoning applied to images. A tag is mutable, and `node:24` in six months is not the
# `node:24` this was tested against.

# --- the console -------------------------------------------------------------------------------
#
# A stage of its own because the Go toolchain image has no npm and the node image has no Go, and
# installing either into the other is how a build starts taking four minutes. Node 24 because
# web/.nvmrc says 24, which is also what CI reads — one answer to "which node", in one file.
FROM --platform=$BUILDPLATFORM node:24-bookworm@sha256:ffeee58a257b390b80b9b656cba440bbc3116c1bc03139c31318f9d9c29a8975 AS web

WORKDIR /src/web

# The lockfile first, so a change to a component does not re-resolve the dependency tree.
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund

COPY web/ ./
RUN npm run build

# --- the binary --------------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514 AS build

WORKDIR /src

# Dependencies first, for the same reason the lockfile is copied first above.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# The console, built in the stage above. `make build-web` stages this into internal/ui/dist, where
# `go:embed` reads it — see the Makefile, which is where that mechanism lives and the only place
# it is described.
COPY --from=web /src/web/dist ./web/dist

# The data directory, created here because the final image has no shell to create it in.
#
# A new Docker named volume is root-owned, and this image runs as uid 65532: without this the very
# first boot cannot create /data/tod.db or the WAL and shared-memory files beside it, and the
# container dies on a message about a file nobody has heard of. Docker initialises an empty volume
# from the image's directory at the mount point — contents, ownership AND mode — so shipping /data
# owned by 65532 is what makes the volume writable.
#
# A BIND MOUNT gets none of this: Docker never changes the ownership of a host directory. If you
# switch to one, chown it 65532:65532 on the host first.
RUN mkdir -p /out/data && chown 65532:65532 /out/data

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=0.0.0-dev

# `make build`, not a re-spelling of `go build`. One description of how this binary is produced, so
# the image cannot drift from what CI runs and what a developer gets. VERSION is the only thing
# overridden; the flags, the trimpath and the output name all come from the Makefile.
#
# CROSS-COMPILE per TARGETOS/TARGETARCH rather than emulate. buildx with QEMU is minutes per
# architecture and Go does this natively in seconds; CGO_ENABLED=0 is also what makes FROM scratch
# possible at all.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    make build VERSION=${VERSION} BUILD_DIR=/out

# --- the image ---------------------------------------------------------------------------------
FROM scratch

# The Discord and OIDC providers speak TLS to the public internet, so the CA bundle is not
# optional — without it every sign-in fails on certificate verification. zoneinfo is here because
# an instance carries a timezone and renders local times in it; a scratch image has no tzdata, and
# without this every local-time rendering silently becomes UTC.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /out/tod-serve /usr/local/bin/tod-serve

# The mount point, owned by the uid below. See the RUN in the build stage for why it exists at all.
# --chown takes numeric ids because scratch has no /etc/passwd for a name to resolve against.
COPY --from=build --chown=65532:65532 /out/data /data

# Non-root by number, for the same reason. 65532 is the conventional "nonroot" uid.
USER 65532:65532

# TOD_DB_PATH is read by `serve`, `migrate`, `backup` and `doctor`. The volume must be writable by
# uid 65532: SQLite creates its WAL and shared-memory files beside the database, so the DIRECTORY
# matters as much as the file.
#
# TOD_ADDR is the ENVIRONMENT and not a flag, because `serve` takes no flags — the listen address
# is configuration, and a value that can be set two ways is a value that gets set the other way.
# It is spelled here as well as in deploy/compose.yaml so that a bare `docker run` of this image
# lands on the port EXPOSE and HEALTHCHECK below both name.
ENV TOD_DB_PATH=/data/tod.db
ENV TOD_ADDR=:8080
VOLUME ["/data"]
EXPOSE 8080

# No shell and no curl in this image, so the binary probes itself.
#
# LIVENESS ONLY. `/healthz` never touches the database (internal/api/health.go, and
# TestLiveness_MakesNoDatabaseCall drives it against a closed store), which is exactly what makes it
# safe for a container runtime to act on: a brief disk problem, or a migration in flight, must not
# be the reason a container is marked unhealthy.
# No --addr: the verb reads TOD_ADDR, so overriding the listen address moves the probe with it.
# A hard-coded port here would keep probing 8080 and report a healthy container unhealthy.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/tod-serve", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/tod-serve"]

# `serve` and NOT `migrate`: a container restart must not migrate. A server that upgrades its
# schema on boot applies a half-tested migration to the only copy of the report log whenever
# Docker feels like restarting it, and migrations are forward-only. The deploy workflow runs
# `docker compose run --rm tod-serve migrate` as its own step, after a human approved it — which is
# the deliberate act the rule asks for.
CMD ["serve"]

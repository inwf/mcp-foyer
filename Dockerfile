# mcp-foyer, built the way the Makefile builds it.
#
# Three stages, because the two toolchains are only needed to build: the
# frontend is built with Node, the binary with Go, and the image that
# actually runs carries neither. Same ordering the Makefile explains —
# frontend first, its output copied where `embed` can reach it, then the
# binary with the tag that turns embedding on.
#
# The versions are pinned to what the repository asks for: go.mod says
# 1.25, README says Node 22+.

# ===== the web interface =====

FROM node:22-alpine AS web

# pnpm comes from the lockfile's own packageManager field where there is
# one; this project has none, so the version is pinned here rather than
# left to whatever `corepack` resolves on the day the image is built.
RUN corepack enable && corepack prepare pnpm@11.9.0 --activate

WORKDIR /src/frontend

# The manifest, the lockfile and the workspace file first, so that a
# change to application source does not re-run the install.
# --frozen-lockfile is what makes the build reproducible: it fails rather
# than quietly resolving something new.
#
# pnpm-workspace.yaml is not optional here even though this is a single
# project: it carries the `allowBuilds` decision about msw's install
# script. Without it, pnpm stops and asks — which in a build means it
# fails.
COPY frontend/package.json frontend/pnpm-lock.yaml frontend/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile

COPY frontend/ ./
RUN pnpm build

# ===== the binary =====

FROM golang:1.25-alpine AS build

WORKDIR /src/backend

# Modules before source, for the same caching reason as above.
COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./

# An embed directive cannot reach outside its own module directory, which
# is why this is a copy rather than a path. It is the one step of the
# build that is not a plain `go` or `pnpm` command.
COPY --from=web /src/frontend/dist/ ./internal/webui/dist/

# CGO off so the result runs on a base image with no libc of its own.
# The version is stamped the way the Makefile's release builds do it.
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -tags webui \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/mcp-foyer ./cmd/mcp-foyer

# ===== what runs =====

FROM alpine:3.21

# ca-certificates for dialling a streamable-http upstream over TLS;
# tzdata so log timestamps in a configured timezone are right. Neither is
# in a bare alpine.
#
# nodejs and npm are here because the most common stdio upstreams are npx
# packages, and an mcp-foyer that cannot spawn the servers people actually
# configure would need a derived image before it was useful. This is the
# one place this image is deliberately larger than it has to be.
RUN apk add --no-cache ca-certificates tzdata nodejs npm

# An unprivileged user, and the data directory owned by it. mcp-foyer
# writes its configuration and logs there, so ownership has to be set
# before the volume is mounted over it — a named volume inherits the
# ownership of the directory it covers.
RUN addgroup -g 10001 mcp-foyer \
 && adduser -u 10001 -G mcp-foyer -s /bin/sh -D mcp-foyer \
 && mkdir -p /data \
 && chown -R mcp-foyer:mcp-foyer /data

COPY --from=build /out/mcp-foyer /usr/local/bin/mcp-foyer
# Mode set explicitly rather than inherited: a checkout on a filesystem
# that does not carry the executable bit would otherwise produce an image
# whose entrypoint cannot run.
COPY --chmod=755 docker/entrypoint.sh /usr/local/bin/mcp-foyer-entrypoint

USER mcp-foyer

# Everything mcp-foyer writes lives here: config.yaml, logs/. Kept out of
# the image so that a rebuild does not discard a configuration.
ENV MCP_FOYER_DATA_DIR=/data
VOLUME ["/data"]

EXPOSE 7788

# The entrypoint seeds a first configuration and then execs this. See
# docker/entrypoint.sh for why a container needs one at all: mcp-foyer's
# loopback-only default rejects everything that arrives through a published
# port.
#
# --host 0.0.0.0 rather than the default loopback, because loopback inside a
# container is reachable from nothing. As a flag rather than a setting in
# the file, so it stays out of what the user edits and applies to this run
# only.
#
# Reaching the management API is still a deliberate act: the compose file
# publishes to 127.0.0.1 on the host, and security.allowedNetworks still
# has to admit the caller.
ENTRYPOINT ["mcp-foyer-entrypoint", "mcp-foyer"]
CMD ["serve", "--host", "0.0.0.0", "--port", "7788"]

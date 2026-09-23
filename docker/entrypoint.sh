#!/bin/sh
# Seed the data directory on a first start, then get out of the way.
#
# Why this exists at all: mcp-foyer's own default allows loopback only,
# which is right for a program on someone's machine and wrong inside a
# container. Traffic published to the host does not arrive from loopback —
# it arrives from the container network's gateway address — so a container
# left on that default answers 403 to everything, the web interface
# included. That is not a rejection a user should have to decode from a log.
#
# So the seeded configuration opens the allowlist to every client; the
# reasoning is written into the file itself. It writes only on a first
# start — after that the file belongs to whoever edits it, through the web
# interface, the CLI or by hand, and this leaves it alone.

set -eu

CONFIG="${MCP_FOYER_DATA_DIR:-/data}/config.yaml"

if [ ! -e "$CONFIG" ]; then
    mkdir -p "$(dirname "$CONFIG")"

    # Only the keys that have to differ from the defaults. A configuration
    # document is decoded over them, so everything absent here — the log
    # level, the timeouts, the session settings — stays whatever this build
    # defaults to, and a later upgrade that changes a default is free to.
    cat > "$CONFIG" <<'YAML'
version: 1

# Written on the first start by the container's entrypoint, and not touched
# again afterwards. Safe to edit.
security:
  # Empty list means "accept every client" — as opposed to leaving the key
  # out, which asks for the loopback-only default, and that would reject
  # everything reaching this container through its published port.
  #
  # For a container the access boundary is the published port and whatever
  # sits in front of it, not this list. To restrict by source address
  # instead, replace [] with the CIDR blocks allowed to reach the
  # management API, e.g. [10.0.0.0/8].
  allowedNetworks: []
YAML

    echo "mcp-foyer: wrote an initial $CONFIG" >&2
fi

exec "$@"

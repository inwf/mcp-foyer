<div align="center">
  <img src="frontend/public/favicon.svg" alt="MCP Foyer logo" width="80" height="80" />
  <h1>MCP Foyer</h1>
  <p><strong>One MCP endpoint. Tools, on demand.</strong></p>
  <p>Connect your MCP servers once. Let models discover the tools they need.<br />Manage everything from a web console and CLI, packaged in a single binary.</p>
  <p>
    <a href="#quick-start"><strong>Quick start</strong></a> ·
    <a href="#how-it-works">How it works</a> ·
    <a href="#the-web-console">Screenshots</a> ·
    <a href="#documentation">Documentation</a> ·
    <a href="README.zh-CN.md">简体中文</a>
  </p>
  <p>
    <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-3b7c57?style=flat-square" alt="MIT License" /></a>
    <a href="backend/go.mod"><img src="https://img.shields.io/badge/Go-1.25%2B-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go 1.25 or newer" /></a>
    <a href="frontend/package.json"><img src="https://img.shields.io/badge/React-19-149ECA?style=flat-square&logo=react&logoColor=white" alt="React 19" /></a>
    <a href="docker-compose.example.yml"><img src="https://img.shields.io/badge/Docker-Compose-2496ED?style=flat-square&logo=docker&logoColor=white" alt="Docker Compose" /></a>
  </p>
</div>

<br />

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/tools-dark.png" />
  <source media="(prefers-color-scheme: light)" srcset="docs/assets/tools-light.png" />
  <img src="docs/assets/tools-light.png" alt="MCP Foyer tool directory: four system tools, grouped upstream tools, usage counts, exposure switches and disable controls" width="1440" />
</picture>

<p align="center"><sub>Browse tools, choose what clients see, and control what can run. Light and dark themes included.</sub></p>

## Built for a growing toolbox

Every new MCP server brings more configuration, more tool schemas, and another place to check when something goes wrong. MCP Foyer brings them together behind **one Streamable HTTP endpoint**.

It is built for personal, local use — especially with clients that do not provide their own progressive tool discovery. By default, clients receive **four system tools**. The model searches for upstream tools and retrieves their schemas when needed, keeping the initial tool list small as your collection grows.

<table>
  <tr>
    <td width="33%" valign="top">
      <h3>🔌 Connect once</h3>
      Point each client at one address. Add or change upstream servers in MCP Foyer without editing every client's configuration.
    </td>
    <td width="33%" valign="top">
      <h3>🔎 Discover on demand</h3>
      Search by tool, description, server, or argument. Fetch matching schemas in the same request when preparing a call.
    </td>
    <td width="33%" valign="top">
      <h3>🎛️ Control each tool</h3>
      Expose frequently used tools directly. Disable individual tools to remove them from discovery and block calls.
    </td>
  </tr>
  <tr>
    <td valign="top">
      <h3>🖥️ See what's happening</h3>
      Inspect server status, tool usage, resources, and live logs. Browse tools as cards or a compact list.
    </td>
    <td valign="top">
      <h3>🔗 Mix local and remote</h3>
      Run stdio servers as child processes and connect to remote Streamable HTTP servers through the same endpoint.
    </td>
    <td valign="top">
      <h3>📦 Deploy one binary</h3>
      The web interface is embedded at build time. Configuration, logs, and usage records stay in the data directory.
    </td>
  </tr>
</table>

## Quick start

### 1. Start MCP Foyer

With **Docker Compose**, Go and Node.js are not required on the host. The image is built locally from the repository and includes Node.js and npm for `npx`-based upstream servers.

```bash
git clone https://github.com/inwf/mcp-foyer.git
cd mcp-foyer
cp docker-compose.example.yml docker-compose.yml
docker compose up -d --build
```

| Open | Address |
| --- | --- |
| Web console | <http://127.0.0.1:7788/> |
| MCP endpoint | <http://127.0.0.1:7788/mcp> |

<details>
<summary><strong>Prefer a native binary? Build from source</strong></summary>

Requires **Go 1.25+**, **Node.js 22.12+**, **pnpm**, and **make**. Run from the repository root:

```bash
make build
./backend/bin/mcp-foyer serve
```

`make build` installs frontend dependencies, builds the web interface, and embeds it in `backend/bin/mcp-foyer`. Both addresses above are the same for a native deployment.

For subsequent commands, use `./backend/bin/mcp-foyer`, or put the binary on your `PATH` to invoke it as `mcp-foyer`.

Upstream servers need their own runtimes. For example, an `npx`-based stdio server needs Node.js and npm on the machine running MCP Foyer.

</details>

### 2. Add your upstream servers

Open the web console, go to **Servers** (服务器), and add a server or import existing configuration. Choose **stdio** for a local command or **streamable-http** for a remote MCP URL.

For a filesystem server inside Docker, you can also use the CLI:

```bash
docker compose exec mcp-foyer mkdir -p /data/shared
docker compose exec mcp-foyer mcp-foyer servers add files -- \
  npx -y @modelcontextprotocol/server-filesystem /data/shared
```

This server can access `/data/shared` inside the container. To use a host directory, add a bind mount to your `docker-compose.yml` and configure the server with its container-side path.

<details>
<summary><strong>Add the filesystem server to a native deployment</strong></summary>

With MCP Foyer already running, execute these commands from the repository root in another terminal:

```bash
mkdir -p data/shared
./backend/bin/mcp-foyer servers add files -- \
  npx -y @modelcontextprotocol/server-filesystem "$(pwd)/data/shared"
./backend/bin/mcp-foyer servers list
```

</details>

### 3. Connect your MCP client

Use **Streamable HTTP** and the endpoint `http://127.0.0.1:7788/mcp`.

For Claude Code:

```bash
claude mcp add --transport http mcp-foyer http://127.0.0.1:7788/mcp
```

Then ask your model to find a tool for a task, such as listing files in the directory you configured. You can leave every upstream tool unexposed: discovery and calls still work through the four system tools.

> [!IMPORTANT]
> MCP Foyer has no built-in authentication. Keep the management interface and MCP endpoint on loopback or behind access controls you trust. The management API can configure commands to run on the host or inside the container.

## How it works

A client's initial `tools/list` contains these four system tools:

| Tool | Purpose |
| --- | --- |
| `list_servers` | Get server names, descriptions, connection states, and tool/resource counts. |
| `search_tools` | Find tools across servers, or browse one server. Supports pagination and optional schemas. |
| `get_tool_details` | Retrieve a tool's full input schema, description, and annotations. |
| `call_tool` | Call an upstream tool by its server and original name. |

A typical request needs just a search and a call:

1. **Find a tool.** Use `search_tools` with `includeSchema: true` to retrieve relevant tools and their argument schemas together.
2. **Call it.** Pass the selected `server`, `tool`, and `args` to `call_tool`.

Already know the tool and its arguments? Call it directly through `call_tool`. Exposed tools are also available under names such as `files_read_text_file`, with naming collisions handled automatically.

### Exposure and disabling

| Tool state | In the initial tool list | Discoverable | Callable |
| --- | :---: | :---: | :---: |
| **Enabled, unexposed** — default | — | Yes | Yes, through `call_tool` |
| **Enabled, exposed** | Yes | Yes | Yes, directly or through `call_tool` |
| **Disabled** | — | — | No, including Web and CLI calls |

Disabling an exposed tool removes its exposure in the same save. Re-enabling makes it available for discovery again and leaves it unexposed. Neither operation reconnects the server. The four system tools remain available.

## The web console

Manage connections, inspect what each server offers, and reach the tools and logs for that server from one place.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/servers-dark.png" />
  <source media="(prefers-color-scheme: light)" srcset="docs/assets/servers-light.png" />
  <img src="docs/assets/servers-light.png" alt="MCP Foyer server management showing connection states, stdio and Streamable HTTP transports, tool counts and connection controls" width="1440" />
</picture>

<p align="center"><sub>Screenshots from a running test installation. The web interface currently uses Simplified Chinese.</sub></p>

- **Servers:** add, edit, import, connect, and disconnect upstreams.
- **Tools:** search, inspect schemas, try calls, manage exposure, and disable tools individually.
- **Usage:** see model search and call counts, failures, and recent activity; sort tools by usage.
- **Resources and logs:** browse upstream resources and inspect live logs with filters.
- **Configuration:** edit settings through the UI or the YAML configuration file.

## Deployment notes

<details>
<summary><strong>Docker storage, networking, and everyday commands</strong></summary>

```bash
docker compose logs -f       # Follow logs
docker compose restart       # Restart the instance
docker compose down          # Stop; keep the data volume
docker compose up -d --build # Rebuild and start after updating the source
```

- The named volume `mcp-foyer-data` stores configuration, logs, and usage data under `/data`. Removing the volume also removes those files.
- `docker-compose.yml` is your local copy and is ignored by Git. Change its host port or add bind mounts there; `docker-compose.override.yml` is also ignored.
- The example publishes only `127.0.0.1:7788`. Publishing on all interfaces makes both the web console and MCP endpoint reachable beyond the host.
- On the first container start, the entrypoint initializes the configuration with an empty `security.allowedNetworks` list. Incoming container traffic is accepted; the host port binding controls exposure. Existing configuration is preserved on later starts.
- Restart after changing listener settings or session-mode rules.

</details>

<details>
<summary><strong>Native data directory and headless builds</strong></summary>

The default data directory is `./data`, relative to the process's working directory. Choose another location with `--data-dir` or `MCP_FOYER_DATA_DIR`.

```bash
./backend/bin/mcp-foyer serve --data-dir ./data
```

`make build` includes the web UI. Building without the `webui` tag produces a headless binary that still serves the management API and MCP endpoint:

```bash
cd backend
go build -o bin/mcp-foyer ./cmd/mcp-foyer
```

</details>

## Documentation

| Looking for | Start here |
| --- | --- |
| 中文介绍与快速开始 | [简体中文 README](README.zh-CN.md) |
| Full CLI usage | `mcp-foyer guide` · [Usage guide](backend/internal/guide/guide.md) |
| Configuration fields and defaults | [Configuration reference](docs/configuration.md) |
| Discovery, search, and MCP behavior | [MCP surface](docs/mcp-surface.md) |
| Search benchmarks and SDK interoperability | [Performance and compatibility](docs/performance.md) |

The detailed guides linked above are currently in Chinese.

## Development

```bash
make help   # Available targets
make build  # Build the web UI and single binary
make check  # Go formatting, vet, race tests; TypeScript, oxlint, Vitest
make dev    # Print the separate backend/frontend development commands
```

For development, run these in two terminals:

```bash
# Terminal 1
cd backend && go run ./cmd/mcp-foyer serve
```

```bash
# Terminal 2; install dependencies first if needed
cd frontend && pnpm install --frozen-lockfile && pnpm dev
```

Vite proxies `/api`, `/ws`, and `/mcp` to the backend while preserving the browser's origin for WebSocket checks. Run `make check` before submitting changes.

| Area | Technologies |
| --- | --- |
| Backend | Go · official MCP Go SDK · Gin · Cobra · `log/slog` |
| Frontend | React 19 · TypeScript · Vite · Ant Design · TanStack Query · Zustand |
| Distribution | Embedded web assets · single binary · Docker Compose |

## License

[MIT](LICENSE) © 2026 inwf.

<p align="center"><sub>If MCP Foyer makes your MCP setup easier to manage, a <a href="https://github.com/inwf/mcp-foyer">GitHub star</a> helps others find it.</sub></p>

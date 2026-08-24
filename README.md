# MUDP

MUDP is a compact, self-hosted **multi-user Docker management platform** — a single binary built with Go, the Docker Engine API, SQLite, and an embedded HTML/CSS/JS console. It brings Portainer-grade depth to a single Docker host while keeping a strict per-user workspace model: containers, volumes, and networks are namespaced under each user, with optional SSH, web-based VS Code, and NVIDIA GPU access.

## Highlights

- **Single binary** — web assets are embedded via `//go:embed`; ship one file, no runtime deps.
- **SQLite** database, auto-created and migrated on first launch (additive, non-breaking migrations).
- **Workspace isolation** — every resource lives under `mudp-<user>-…`; users see only their own.
- **One-click containers** with optional per-container SSH and web VS Code, each served by mudp itself on a LAN-reachable port — no sshd or code-server ever installed inside the container.
- **GPU passthrough** via Docker NVIDIA device requests.
- **Per-user quotas** (container cap), roles, and audit logging.
- **Feishu (Lark) SSO** with admin-approval pending queue.

## Feature matrix

| Area | Capabilities |
|------|-------------|
| **Dashboard** | Environment info (Docker version, OS, CPU/RAM, storage driver), container/image/volume/network counts, containers-by-state donut, per-user usage rollup, recent activity feed. |
| **Containers** | List (state filter: all/running/stopped/paused + multi-select batch start/stop/restart/remove), create (wizard with env/ports/mounts/networks/GPU + advanced overrides: command, entrypoint, workdir, hostname, user, CPU/memory/PID limits, cap-add/drop, labels), start/stop/restart/remove, pause/unpause, logs (live tail + grep + wrap + download), web terminal (xterm.js over WebSocket), live stats (CPU/mem/net/block sparklines), inspect (curated + raw JSON), duplicate, commit-to-image. |
| **Images** | Pull (SSE progress), build from Dockerfile (SSE), import/export tar, retag, push to registry, prune dangling, group-based visibility. |
| **Volumes** | List, create (local/NFS drivers), delete, prune unused — all per-user namespaced; **in-volume file browser** (browse/upload with resume/download-zip/rename/delete/mkdir). |
| **Networks** | List, create (bridge/overlay/macvlan with subnet + advanced IPAM: gateway/IP range/IPv6), delete, **detail view with attach/detach containers** — attachable in the container wizard. |
| **Stacks** | Deploy `docker-compose.yml` via the host's `docker compose` CLI plugin with live SSE progress; web editor, env-var substitution (`${VAR}`), up/down, delete; quota-guarded. |
| **Users & Groups** | 5-role RBAC (admin / operator / help-desk / read-only / user), group membership (Feishu approval flow), password reset, disable/enable, container-cap edit, delete. |
| **Registries** | Store authenticated registry credentials (Docker Hub, GHCR, private); used automatically for pulls/builds/pushes; test-login button. |
| **Activity log** | Filterable audit trail (actor / action / target / time) with CSV export. |
| **Processes** | Every process across your running containers (per-container view too); one-click **exit watches** — when a watched process ends you get an in-app notification and, with a personal Feishu bot webhook configured in Settings, a Feishu message. |
| **Error monitor** | Sentry-style aggregation of server panics and 5xx responses, grouped per issue with counts, first/last seen, stack viewer, resolve/clear, and CSV export (admin). |
| **Updates** | Admin-only: the dashboard shows the running version and checks the GitHub latest release (cached 1 h); a newer tag badges "update available" with per-OS download links — and a **one-click upgrade** that downloads the asset for this server's OS/arch (windows/linux × amd64/arm64), swaps it in, restarts, and **rolls back to the previous binary automatically if the new one fails to start** (startup health check; under systemd the restart is left to the unit). |
| **Settings** | Feishu SSO config and registry management. |

## Roles & permissions

| Role | Rank | Can do |
|------|------|--------|
| **admin** | 50 | Everything + user/registry/group management. |
| **operator** | 40 | Full CRUD on containers/images/volumes/networks/stacks. |
| **user** | 30 | Workspace containers (SSH/VS Code/GPU/quota). |
| **helpdesk** | 20 | View all + logs/exec/stats/inspect, no mutations. |
| **readonly** | 10 | View only. |

Ownership is enforced server-side on every Docker operation — UI hiding is cosmetic only.

## Quick start

```powershell
go run ./cmd/mudp
```

Open <http://127.0.0.1:9000>. On the first start without `MUDP_ADMIN_PASSWORD`, the web UI prompts for initial setup (admin username/password, default users group netdisk path, etc.). You can also pre-seed the admin account by setting `MUDP_ADMIN_USER` and `MUDP_ADMIN_PASSWORD`.

### Environment variables

| Var | Default | Purpose |
|-----|---------|---------|
| `MUDP_ADDR` | `127.0.0.1:9000` | Listen address. |
| `MUDP_DB` | `mudp.db` | SQLite database path. |
| `MUDP_SESSION_SECRET` | random per launch | Cookie signing secret. Auto-generated when not set. |
| `MUDP_ADMIN_USER` | `admin` | Bootstrap admin username. |
| `MUDP_ADMIN_PASSWORD` | _empty_ | Bootstrap admin password. When empty, the first-run setup wizard is required.
| `MUDP_DOCKER_HOST` | _empty_ (uses `DOCKER_HOST`) | Override the Docker Engine endpoint. |
| `MUDP_WEB_DIR` | _empty_ | Serve UI from disk (dev mode) instead of the embed. |

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│  cmd/mudp/main.go   →  config.Load  →  store.Open  →      │
│                       server.New  →  http.Server           │
└──────────────────────────────────────────────────────────┘
        │
        ▼
┌─────────────── internal ───────────────┐   ┌──── web ────┐
│ config   configuration + env           │   │ index.html  │
│ store    SQLite: users, groups, images,│   │ styles.css  │
│          stacks, settings, audit_logs  │   │ app.js      │
│ auth     session cookies + Feishu OIDC │   │ modules/*.js│
│ dockerx  Docker SDK wrapper:           │   │ vendor/     │
│          containers, images, volumes,  │   │  (xterm.js) │
│          networks, compose, stats      │   └─────────────┘
│ server   chi router + HTTP handlers    │
│ httpx    uniform errors/responses      │
│ middleware request ID, logging, rate   │
│          limiting, CSRF protection     │

└────────────────────────────────────────┘
```

- **Layering**: `config → store → dockerx → server/handlers → web`. New Docker features live in `dockerx`; new persistence in `store`; HTTP glue in `server`.
- **Streaming**: long operations (image pull/build, container create, stack up/down) stream progress over Server-Sent Events and cancel on client disconnect.
- **Terminal**: interactive exec sessions bridged over a minimal WebSocket (handshake implemented inline; no extra dependency).

## Build

Requires **Go 1.21+** (see `go.mod`).

```bash
# Linux / macOS (native binary)
./build.sh

# Cross-compile
GOOS=linux GOARCH=amd64 ./build.sh
GOOS=windows GOARCH=amd64 ./build.sh
GOOS=darwin GOARCH=arm64 ./build.sh
```

```powershell
# Windows
go build -o dist/mudp.exe ./cmd/mudp
# or, to produce all four release assets at once
#   dist\mudp-windows-amd64.exe  + .zip   dist\mudp-linux-amd64  + .tar.gz
#   dist\mudp-windows-arm64.exe  + .zip   dist\mudp-linux-arm64  + .tar.gz
.\build.bat   # scripts\build.ps1 is equivalent
```

## Run as a service

```bash
mudp install        # Windows (admin) or Linux (root): registers the OS service
                    # with automatic restart, then `mudp start`
# Linux alternative with dedicated user + stable session secret:
sudo ./scripts/install-service.sh [/path/to/mudp]
```

Both `mudp install` paths make the web UI's one-click upgrade restart-safe: the upgrade swaps the binary and exits, and the service manager brings the new version up — see [docs/operations.md](docs/operations.md#running-as-a-service).

## Test

Backend (Go):

```powershell
go test ./...
go vet ./...

# SQLite concurrency stress test (skipped in -short mode):
go test ./internal/store/ -run TestSQLiteStressMixedLoad -v
```

Frontend (Node.js):

```powershell
cd web
npm install
npm run lint      # ESLint: catches JS syntax/undefined/import errors
npm run test:unit
npm run test:integration
npm run test:e2e  # requires the Go binary at dist/mudp-windows-amd64.exe (or dist/mudp-linux-amd64)
```

End-to-end tests start the real MUDP binary, log in via Chromium, and navigate the major tabs. They fail automatically on any page JS error or HTTP 5xx response.

Docker-touching Go tests auto-skip when no daemon is reachable, so CI without Docker still passes.

For a single command that runs both suites, use the all-in-one runners: `test.bat [go|web]` on Windows, `./test.sh [go|web]` on Linux/macOS.

## Notes & requirements

- Docker Desktop or Docker Engine must be running and reachable.
- **Stacks** require the `docker compose` CLI plugin v2 on the host (`docker compose version` should print a version). Without it, the Stacks tab surfaces a clear "not installed" error.
- GPU scheduling uses Docker NVIDIA device requests and expects the host NVIDIA container runtime.
- Registry tokens are stored at-rest in the SQLite `settings` table. For a single-host deployment this is acceptable; encrypt-at-rest is a flagged follow-up.
- **Production safety**: Set `MUDP_ADMIN_PASSWORD` and `MUDP_SESSION_SECRET` explicitly to keep credentials and login cookies stable across restarts. When `MUDP_ADMIN_PASSWORD` is omitted, the first-run setup wizard creates the admin account. CSRF tokens are required for all mutating API calls.
- Set `MUDP_SESSION_SECRET` in production to keep login cookies valid across restarts — otherwise the random default rotates on each launch.

## License (dual licensing)

MUDP is available under a **dual-license** model:

- **Open source — GNU LGPL v3** ([LICENSE.txt](LICENSE.txt)): free for individuals, research, and internal evaluation. Derivative works distributed to third parties must comply with LGPLv3 (source disclosure of modifications to MUDP itself).
- **Commercial license** ([LICENSE-COMMERCIAL.md](LICENSE-COMMERCIAL.md)): required to ship MUDP inside a commercial product/appliance/SaaS, to sublicense it under different terms, or to keep modifications proprietary without LGPL source-disclosure obligations. Includes updates and priority support. Contact the project maintainers for a quote.

## Roadmap (out of scope for this release)

Multi-environment endpoints (Docker/Swarm/K8s), LDAP/OAuth beyond Feishu, app-template catalog, image vulnerability scanning, two-factor auth, and theming. These can be layered on without rearchitecting the current single-host model.

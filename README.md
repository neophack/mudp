# MUDP

MUDP is a compact, self-hosted **multi-user Docker management platform** — a single binary built with Go, the Docker Engine API, SQLite, and an embedded HTML/CSS/JS console. It brings Portainer-grade depth to a single Docker host while keeping a strict per-user workspace model: containers, volumes, and networks are namespaced under each user, with optional SSH, web-based VS Code, and NVIDIA GPU access.

## Highlights

- **Single binary** — web assets are embedded via `//go:embed`; ship one file, no runtime deps.
- **SQLite** database, auto-created and migrated on first launch (additive, non-breaking migrations).
- **Workspace isolation** — every resource lives under `mudp-<user>-…`; users see only their own.
- **One-click containers** with optional host-side SSH terminal and VS Code attach, without installing services inside the container.
- **GPU passthrough** via Docker NVIDIA device requests.
- **Per-user quotas** (container cap), roles, and audit logging.
- **Feishu (Lark) SSO** with admin-approval pending queue.

## Feature matrix

| Area | Capabilities |
|------|-------------|
| **Dashboard** | Environment info (Docker version, OS, CPU/RAM, storage driver), container/image/volume/network counts, containers-by-state donut, per-user usage rollup, recent activity feed. |
| **Containers** | List, create (wizard with env/ports/mounts/networks/GPU), start/stop/restart/remove, logs (live tail + grep + wrap + download), web terminal (xterm.js over WebSocket), live stats (CPU/mem/net/block sparklines), inspect. |
| **Images** | Pull (SSE progress), build from Dockerfile (SSE), import/export tar, retag, push to registry, prune dangling, group-based visibility. |
| **Volumes** | List, create (local/NFS drivers), delete, prune unused — all per-user namespaced. |
| **Networks** | List, create (bridge/overlay/macvlan with subnet), delete — attachable in the container wizard. |
| **Stacks** | Deploy `docker-compose.yml` via the host's `docker compose` CLI plugin with live SSE progress; web editor, env-var substitution (`${VAR}`), up/down, delete; quota-guarded. |
| **Users & Groups** | 5-role RBAC (admin / operator / help-desk / read-only / user), group membership (Feishu approval flow), password reset, disable/enable, container-cap edit, delete. |
| **Registries** | Store authenticated registry credentials (Docker Hub, GHCR, private); used automatically for pulls/builds/pushes; test-login button. |
| **Activity log** | Filterable audit trail (actor / action / target / time) with CSV export. |
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

Open <http://127.0.0.1:9000>. Default first admin: `admin / admin123`.

### Environment variables

| Var | Default | Purpose |
|-----|---------|---------|
| `MUDP_ADDR` | `127.0.0.1:9000` | Listen address. |
| `MUDP_DB` | `mudp.db` | SQLite database path. |
| `MUDP_SESSION_SECRET` | random per launch | Cookie signing secret. **Set in production.** |
| `MUDP_ADMIN_USER` | `admin` | Bootstrap admin username. |
| `MUDP_ADMIN_PASSWORD` | `admin123` | Bootstrap admin password. |
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
│ bootstrap SSH/VS Code tar injection    │
└────────────────────────────────────────┘
```

- **Layering**: `config → store → dockerx → server/handlers → web`. New Docker features live in `dockerx`; new persistence in `store`; HTTP glue in `server`.
- **Streaming**: long operations (image pull/build, container create, stack up/down) stream progress over Server-Sent Events and cancel on client disconnect.
- **Terminal**: interactive exec sessions bridged over a minimal WebSocket (handshake implemented inline; no extra dependency).

## Build

```powershell
# Windows
go build -o dist/mudp.exe ./cmd/mudp
# or:
.\build.bat

# Linux
$env:GOOS="linux"; $env:GOARCH="amd64"; go build -o dist/mudp-linux-amd64 ./cmd/mudp

# macOS
$env:GOOS="darwin"; $env:GOARCH="arm64"; go build -o dist/mudp-darwin-arm64 ./cmd/mudp
```

## Test

```powershell
go test ./...
```

Docker-touching tests auto-skip when no daemon is reachable, so CI without Docker still passes.

## Notes & requirements

- Docker Desktop or Docker Engine must be running and reachable.
- **Stacks** require the `docker compose` CLI plugin v2 on the host (`docker compose version` should print a version). Without it, the Stacks tab surfaces a clear "not installed" error.
- GPU scheduling uses Docker NVIDIA device requests and expects the host NVIDIA container runtime.
- SSH and VS Code options are host-side helpers; they do not expose container ports or install sshd/code-server inside the container.
- Registry tokens are stored at-rest in the SQLite `settings` table. For a single-host deployment this is acceptable; encrypt-at-rest is a flagged follow-up.
- Set `MUDP_SESSION_SECRET` in production — otherwise the random default rotates on each launch and invalidates login cookies.

## Roadmap (out of scope for this release)

Multi-environment endpoints (Docker/Swarm/K8s), LDAP/OAuth beyond Feishu, volume file browser, app-template catalog, image vulnerability scanning, two-factor auth, and theming. These can be layered on without rearchitecting the current single-host model.

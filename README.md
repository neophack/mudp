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
| `MUDP_TRUSTED_PROXIES` | _empty_ (uses private-LAN default) | Comma-separated CIDRs whose forwarded headers (`X-Forwarded-For`, `X-Real-IP`, `CF-Connecting-IP`) are trusted. **Must be set correctly when running behind nginx/Caddy/Cloudflare**, otherwise rate limiting, the geo/CIDR gate, and brute-force protection key on the proxy's IP and can be bypassed by forging `X-Forwarded-For`. |

## Security

MUDP ships with two layers of access control an admin can toggle from **Settings → Security**:

- **IP / region gate.** Restrict the whole site to one or more countries, or to specific Chinese provinces (e.g. "only Guangdong"). Disallowed origins get a flat `404` — nothing to fingerprint. CIDR allow/block lists override the geo check: the allowlist is the safety valve that prevents you from locking yourself out.
- **Login brute-force protection.** After 5 failed attempts from one IP within 15 minutes, that IP is locked for 30 minutes; after 10 failures against one account, that account is soft-locked for 15 minutes. Lockouts are in-memory and clear automatically; an admin can view active lockouts on the same settings page.

GeoIP data is the MIT-licensed [ip2region](https://github.com/lionsoul2014/ip2region) v4 database, embedded into the binary (~11 MiB) — no download, no external service, China province/city accurate.

### Critical: configure `MUDP_TRUSTED_PROXIES` behind a reverse proxy

If you run MUDP behind nginx, Caddy, or Cloudflare (the common case), the direct connection always comes from the proxy. The forwarding headers (`X-Forwarded-For`, …) carry the *real* client IP — but a client can forge them too. MUDP therefore only honors those headers when the direct peer is in the trusted-proxy list. Default trusts private LAN ranges, which covers a co-located proxy on the same host.

For a public proxy add its egress explicitly, e.g.:

```bash
# Trust only the loopback (nginx on the same box)
MUDP_TRUSTED_PROXIES=127.0.0.0/8

# Trust a specific upstream
MUDP_TRUSTED_PROXIES=10.0.0.5/32

# Cloudflare — trust their published ranges
MUDP_TRUSTED_PROXIES=173.245.48.0/20,103.21.244.0/22,...
```

Without this, an attacker can spoof `X-Forwarded-For` to appear from Guangdong and bypass the region gate.

### Lockout recovery

The region gate always passes `/api/setup/*`, `/healthz`, `/readyz`, and `/metrics`, so a misconfigured policy can be recovered via the first-run setup flow on the box. The settings page also refuses to save a policy that would block the saving admin's own current IP unless that IP is in the CIDR allowlist.


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

Requires **Go 1.20+**.

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
# or:
.\build.bat
```

## Test

Backend (Go):

```powershell
go test ./...
go vet ./...
```

Frontend (Node.js):

```powershell
cd web
npm install
npm run lint      # ESLint: catches JS syntax/undefined/import errors
npm run test:unit
npm run test:integration
npm run test:e2e  # requires the Go binary at dist/mudp.exe (or dist/mudp)
```

End-to-end tests start the real MUDP binary, log in via Chromium, and navigate the major tabs. They fail automatically on any page JS error or HTTP 5xx response.

Docker-touching Go tests auto-skip when no daemon is reachable, so CI without Docker still passes.

## Notes & requirements

- Docker Desktop or Docker Engine must be running and reachable.
- **Stacks** require the `docker compose` CLI plugin v2 on the host (`docker compose version` should print a version). Without it, the Stacks tab surfaces a clear "not installed" error.
- GPU scheduling uses Docker NVIDIA device requests and expects the host NVIDIA container runtime.
- Registry tokens are stored at-rest in the SQLite `settings` table. For a single-host deployment this is acceptable; encrypt-at-rest is a flagged follow-up.
- **Production safety**: Set `MUDP_ADMIN_PASSWORD` and `MUDP_SESSION_SECRET` explicitly to keep credentials and login cookies stable across restarts. When `MUDP_ADMIN_PASSWORD` is omitted, the first-run setup wizard creates the admin account. CSRF tokens are required for all mutating API calls.
- Set `MUDP_SESSION_SECRET` in production to keep login cookies valid across restarts — otherwise the random default rotates on each launch.

## Roadmap (out of scope for this release)

Multi-environment endpoints (Docker/Swarm/K8s), LDAP/OAuth beyond Feishu, app-template catalog, image vulnerability scanning, two-factor auth, and theming. These can be layered on without rearchitecting the current single-host model.

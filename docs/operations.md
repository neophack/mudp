# MUDP Operations Guide

## Port Allocation

Each user can be assigned a numeric port prefix. Prefix `100` means the user can publish host ports `10000-10099`.

- Host ports below `10000` are reserved and rejected for user mappings.
- Explicit mappings in the create form must be `host:container`.
- SSH and VS Code each reserve one host port from the same assigned range when enabled (see Host-side Access below).
- Users without a prefix cannot publish custom host ports until an admin assigns one.

Admins set the prefix from **Users & Groups -> Edit -> Port prefix**.

A mapping may name a protocol: `10001:8080` and `10001:8080/tcp` are the same, `10002:53/udp` publishes a UDP port. A stopped container keeps its host ports reserved, so restarting it never finds them taken by a newer container.

## Port Forwarding (containers that Docker cannot publish)

Docker publishes a port by installing NAT rules and binding the host port. On a host where something else owns the firewall — an OpenWrt router appliance routing a LAN, where Docker's iptables integration is off or its rules are overwritten — that does not work: `-p 10001:8080` either fails to bind or is bypassed, and the published port answers nothing. The container itself is fine; it holds a LAN address and `curl 10.210.1.3:8080` works.

For those hosts, mudp can carry the host port itself. Mark the network with the **Forward** button on its row in **Networks**, or from the **Port forwarding** page (admin-only), which is also where every running forward is listed. From then on:

- A container created on one of those networks still takes host ports from its owner's assigned range, exactly as before.
- Instead of asking Docker to publish them, mudp records the mapping on the container and relays `0.0.0.0:<host port>` to the container's own address (`10.210.1.3:8080`) inside its own process.
- Containers that were already on the network are adopted: the host ports Docker assigned them are relayed too, so marking the network fixes what is already running without recreating anything. If Docker's own proxy still holds one of those ports, that forward is reported as failed and nothing is taken over silently.
- TCP and UDP are both carried byte for byte, with no interpretation of the payload — SSH, HTTP, WebSockets, gRPC, DNS, QUIC and VPN protocols all work.
- Every other network keeps Docker's normal publishing. An install where nothing is selected behaves exactly as it did before.

### The Port forwarding page

**Port forwarding** (admin-only) is the whole picture in one place:

| Column | Meaning |
| --- | --- |
| Host port | The port mudp is listening on, and its protocol. |
| User | The owner of the container the port belongs to. |
| Container | The container being relayed to (or the note, for a manual rule). |
| Target | The address and port each connection is relayed to. |
| Source | `container` for a rule derived from a container, `manual` for one added here. |
| Connections | Live connection count, and the total since the listener started. |

**+ Add Forward** creates a rule that no container implies — a service on a network mudp does not manage, a port that was never published, or a second host port for an existing container. The target is either:

- **A container**, resolved to its current address on every reconcile, so the forward follows it across restarts and address changes; or
- **A fixed address** (e.g. `10.210.1.3:8080`), for anything else the host can reach.

Manual rules are stored, survive restarts, and are the only rules that can be deleted from this page — container-derived rules come and go with their containers. A host port already relayed for a container cannot be claimed manually, and the console's own port is refused.

Notes:

- The network may be named either the way it appears on the Networks view (`openwrt-lan`) or by its full Docker name (`mudp-alice-net-openwrt-lan`). Both resolve to the same network.
- If Docker becomes unreachable, the forwards already running are kept as they are rather than torn down, and manual forwards to a fixed address keep working — they never needed Docker.
- Forwards are reconciled from container state every 15 seconds and after every create/start/stop. A container that restarts onto a new address keeps its host port and is repointed automatically; a container that is removed releases its port.
- A stopped container has no address, so its forwards are not listening — but its host ports stay reserved for it.
- The Networks view marks a forwarding network with a **forward** badge, the create wizard says so next to the network, the container list marks a container whose ports mudp relays, and the container detail view marks each forwarded mapping.
- Turning forwarding **off** hands publishing back to Docker for containers created afterwards; containers created while it was on keep their recorded mapping and stop being reachable on the host until they are recreated (**Duplicate** reproduces a container under the current setting).
- Forwarding runs inside the mudp process, so the forwarded ports are up only while mudp is. They are re-established at startup.
- Compose stacks publish through `docker compose` itself and are not forwarded; on such a host, reach a stack's services at the container address directly.

## Netdisk

Admins assign a netdisk root path per group from **Users & Groups -> Group Netdisk Paths**. A user's files are stored under:

```text
<group-netdisk-root>/<username>-<user-id>/
```

When a container is created, **Mount netdisk at /workspace** is checked by default. The host user directory is bind-mounted into the container at `/workspace`.

The Netdisk page supports:

- Folder browsing and creation.
- Batch upload with multipart files.
- Interrupted upload resume by appending to a partial destination file when the existing file is smaller than the upload.
- Downloading a single file or a zipped folder.
- Deleting one or more files.
- Viewing the user's used space.

## Resource Usage

Users can view live container stats from a container's **Stats** action. Admins can open **Usage** to see:

- Per-user container, memory, disk, and GPU rollups.
- Last 24 hours of resource samples.
- Top container processes sorted by CPU.

The history endpoint stores a fresh sample on access and keeps recent samples for 48 hours.

## Disks And Backup

Admins can open **Disks** to view host disk information, run mount/unmount helper commands, and create a zipped database backup on a mounted disk path.

The backup currently includes the MUDP SQLite database. Netdisk data can be copied or archived from the assigned netdisk root paths using the Netdisk file manager or host-level tools.

The database is running in WAL mode, so the backup does not copy the live file directly (recent commits can still be sitting in the `-wal` file and would be missing from a raw copy). Instead it runs `VACUUM INTO` to produce a fully checkpointed, consistent snapshot, zips that snapshot, and deletes the temporary snapshot file afterward. The resulting zip is written with `0600` permissions.

### Restoring from a backup

1. Stop the MUDP server process.
2. Unzip the backup: it contains a single file named the same as the configured `MUDP_DB` (default `mudp.db`).
3. Move the current `mudp.db` (and any `mudp.db-wal` / `mudp.db-shm`) aside, then copy the extracted file into its place.
4. Start the MUDP server. On first connection it will recreate a fresh `-wal`/`-shm` pair; no further migration step is needed since the snapshot already reflects the schema at backup time.

## MCP External Access

MCP tokens normally only work from the network the console is on. To let an agent connect from outside, an admin can publish a second listener that serves **only** the MCP endpoints, and point a Cloudflare tunnel hostname at it.

Configure it in **Settings -> MCP external access**:

| Field | Meaning |
| --- | --- |
| Public domain | The hostname the tunnel serves, e.g. `mcp.example.com`. Users copy this. |
| Port | The loopback port the tunnel connects to. Default `19090`. |
| Safe network | The Docker network a container must be attached to before its token works from outside. Default `openwrt-lan`. |
| Enable external access | Starts the listener. |

Then run the tunnel on the same host:

```sh
cloudflared tunnel --url http://127.0.0.1:19090
```

Notes:

- The listener binds to `127.0.0.1` only. Nothing but `/mcp/...` and `/healthz` is served on it — the console, the API, and the netdisk are not reachable through the public hostname.
- A token is refused on the external listener unless its container is attached to the safe network, so enabling external access does not by itself expose any container. Put a container on the safe network to make it reachable; disconnect it to revoke that.
- On top of the safe-network check, every remote request must also carry the token's **external key** as an `Authorization: Bearer <key>` header. This is a separate secret from the token embedded in the `/mcp/{token}` URL — generated per token via the **GEN** button on the MCP page (only shown once external access is enabled) — so a URL that ends up in a tunnel's or proxy's access log cannot by itself authenticate a remote request. LAN access uses the URL token alone and never needs this header. A token with no external key generated yet is refused on the external listener.
- The safe network name matches either the Docker network name (`openwrt-lan`) or the display name of a mudp-managed network (`openwrt-lan` for `mudp-<user>-net-openwrt-lan`).
- Changing the port moves the listener immediately; disabling stops it. Existing tokens are unchanged either way — only where they can be used changes.
- Users see the domain, and a per-token external link, on the **MCP** page.

## Feishu Login

When Feishu SSO is enabled and has both App ID and App Secret configured, the login page shows a Feishu login entry. New Feishu users are placed in the `pending` group until an admin assigns them to a normal group.

## Running as a Service

mudp ships its own service management on both platforms (administrator/root required). `mudp install` registers it with systemd on Linux and the Windows service controller on Windows, with automatic restart on failure; `mudp uninstall`, `mudp start`, `mudp stop`, `mudp restart` and `mudp status` control it afterwards:

```bash
sudo ./mudp install     # Linux: /etc/systemd/system/mudp.service, Restart=always
mudp.exe install        # Windows (admin): auto-start service, restart 5s after any failure
```

The database defaults to `mudp.db` **next to the executable** (not the working directory), so a Windows service — whose working directory is `C:\Windows\System32` — reads the same database as a console run from the same folder. Set `MUDP_DB` for a custom location.

When running under a service manager, the web UI's one-click upgrade swaps the binary files and exits; the supervisor (systemd `Restart=always` / Windows recovery actions) restarts the new version within seconds. The dying process never spawns its replacement, so the restart survives closed consoles and sessions. Logs go to the systemd journal / Windows event log (source `mudp`) in addition to stderr.

Linux also supports the richer [scripts/install-service.sh](../scripts/install-service.sh), which generates a stable `MUDP_SESSION_SECRET`, runs under a dedicated user in the `docker` group, and preserves data across in-place upgrades — prefer it when you need those.

## Releasing A Version

Releases are manual, the openp2p model: run the **Release** workflow from the GitHub Actions tab and give it the version to ship (e.g. `v1.2.0`). CI builds all four release assets, runs the test suite first, tags the commit, and publishes a GitHub release with the packaged archives plus `SHA256SUMS`.

```bash
# 1. bump the version constant in internal/version/version.go
#    (var Version = "v1.2.0") and commit — the workflow refuses to run if it
#    does not match the version you type into the dispatch form
# 2. Actions → Release → Run workflow → version: v1.2.0
```

There is no build-time version injection: the `version.go` constant is the single source of truth, so CI binaries, local builds and plain `go build` installs all report the same string. Version format is `vMAJOR.MINOR.PATCH`.

Asset naming and packaging also follow openp2p: binaries are `mudp-<os>-<arch>` (`mudp-windows-amd64.exe`, `mudp-linux-arm64`, …), shipped as versioned archives — `mudp-windows-<arch>-<version>.zip`, `mudp-linux-<arch>-<version>.tar.gz`. The in-app self-upgrader downloads these archives and extracts the binary, so the names are its contract — never rename them. Once the release is published, every running instance sees it via the update check and can upgrade in place (see "Running as a Service" for the restart model).

## Platform Notes (Windows vs Linux)

- **Docker endpoint**: with neither `MUDP_DOCKER_HOST` nor `DOCKER_HOST` set, mudp uses the platform default — `unix:///var/run/docker.sock` on Linux, `npipe:////./pipe/docker_engine` on Windows (Docker Desktop). Both variables accept `unix://`, `npipe://`, and `tcp://` URLs.
- **Disks page**: Windows enumerates all logical disks via PowerShell; Linux lists mounts backed by real block devices from `/proc/mounts` (loop devices hidden), falling back to the root filesystem.
- **Netdisk on Windows**: bind-mounting host paths (`C:\...`) into containers requires the drive to be shared in Docker Desktop settings (automatic with the WSL2 backend); GPU passthrough requires the WSL2 backend with NVIDIA support.
- **Timezone data**: the binary embeds the IANA timezone database (`time/tzdata`), so the GeoIP/browser timezone comparison in the security log works on Windows hosts without a Go installation.
- **Host metrics**: CPU/memory/load come from `/proc` on Linux and from the Windows API on Windows; other platforms report zeroes.
- **Shell-outs**: disk mount/unmount uses PowerShell on Windows and `mount`/`umount` on Linux; netdisk ACLs use `setfacl` on Linux only; Stacks needs the `docker compose` CLI plugin on either platform.

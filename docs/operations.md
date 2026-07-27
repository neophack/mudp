# MUDP Operations Guide

## Port Allocation

Each user can be assigned a numeric port prefix. Prefix `100` means the user can publish host ports `10000-10099`.

- Host ports below `10000` are reserved and rejected for user mappings.
- Explicit mappings in the create form must be `host:container`.
- SSH and VS Code each reserve one host port from the same assigned range when enabled (see Host-side Access below).
- Users without a prefix cannot publish custom host ports until an admin assigns one.

Admins set the prefix from **Users & Groups -> Edit -> Port prefix**.

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
- The safe network name matches either the Docker network name (`openwrt-lan`) or the display name of a mudp-managed network (`openwrt-lan` for `mudp-<user>-net-openwrt-lan`).
- Changing the port moves the listener immediately; disabling stops it. Existing tokens are unchanged either way — only where they can be used changes.
- Users see the domain, and a per-token external link, on the **MCP** page.

## Feishu Login

When Feishu SSO is enabled and has both App ID and App Secret configured, the login page shows a Feishu login entry. New Feishu users are placed in the `pending` group until an admin assigns them to a normal group.

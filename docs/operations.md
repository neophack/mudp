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

When a container is created, **Mount netdisk at /netdisk** is checked by default. The host user directory is bind-mounted into the container at `/netdisk`.

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

## Feishu Login

When Feishu SSO is enabled and has both App ID and App Secret configured, the login page shows a Feishu login entry. New Feishu users are placed in the `pending` group until an admin assigns them to a normal group.

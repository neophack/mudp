import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import path from "node:path";
import fs from "node:fs";
import os from "node:os";
import net from "node:net";
import { request } from "@playwright/test";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, "../../../../");
const binaryName = process.platform === "win32" ? "mudp.exe" : "mudp";
const binaryPath = path.join(repoRoot, "dist", binaryName);

async function waitForPort(port, timeout = 30000) {
  const start = Date.now();
  while (Date.now() - start < timeout) {
    const ready = await new Promise((resolve) => {
      const socket = new net.Socket();
      socket.setTimeout(1000);
      socket.once("connect", () => {
        socket.destroy();
        resolve(true);
      });
      socket.once("error", () => resolve(false));
      socket.once("timeout", () => {
        socket.destroy();
        resolve(false);
      });
      socket.connect(port, "127.0.0.1");
    });
    if (ready) return;
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`Server did not start on port ${port} within ${timeout}ms`);
}

// startServer boots a throwaway mudp instance against a fresh SQLite database.
// Options let a spec pick its own port so two spec files never fight over one.
export async function startServer(opts = {}) {
  const port = opts.port || 19000;
  const adminUser = opts.adminUser || "admin";
  const adminPassword = opts.adminPassword || "e2e-secret";

  // Ensure the binary exists.
  if (!fs.existsSync(binaryPath)) {
    throw new Error(
      `MUDP binary not found at ${binaryPath}. Run "go build -o dist/${binaryName} ./cmd/mudp" first.`
    );
  }

  const dbPath = path.join(repoRoot, "dist", `mudp-e2e-${port}-${Date.now()}.db`);
  // The netdisk root must be a real directory the server may write into; stop()
  // removes it again.
  const netdiskRoot = path.join(os.tmpdir(), `mudp-e2e-netdisk-${port}-${Date.now()}`);
  const env = {
    ...process.env,
    MUDP_ADDR: `127.0.0.1:${port}`,
    MUDP_DB: dbPath,
    MUDP_ADMIN_USER: adminUser,
    MUDP_ADMIN_PASSWORD: adminPassword,
    MUDP_SESSION_SECRET: "e2e-session-secret-must-be-32-bytes-long",
    MUDP_WEB_DIR: path.join(repoRoot, "web"),
  };

  const proc = spawn(binaryPath, [], { env, cwd: repoRoot, stdio: "pipe" });
  let stdout = "";
  let stderr = "";
  proc.stdout.on("data", (d) => { stdout += d.toString(); });
  proc.stderr.on("data", (d) => { stderr += d.toString(); });

  await waitForPort(port);

  return {
    url: `http://127.0.0.1:${port}`,
    port,
    dbPath,
    netdiskRoot,
    adminUser,
    adminPassword,
    stop: async () => {
      proc.kill("SIGTERM");
      await new Promise((resolve) => proc.on("close", resolve));
      try { fs.unlinkSync(dbPath); } catch { /* already gone */ }
      // SQLite WAL files may exist; clean them up too.
      for (const ext of ["-shm", "-wal"]) {
        try { fs.unlinkSync(dbPath + ext); } catch { /* already gone */ }
      }
      try { fs.rmSync(netdiskRoot, { recursive: true, force: true }); } catch { /* best effort */ }
    },
    logs: () => ({ stdout, stderr }),
  };
}

// apiClient wraps a logged-in APIRequestContext together with its CSRF token so
// callers can issue mutating requests without re-reading cookies each time.
export async function apiClient(baseURL, username, password) {
  const ctx = await request.newContext({ baseURL });
  const res = await ctx.post("/api/login", { data: { username, password } });
  if (!res.ok()) throw new Error(`login as ${username} failed: ${res.status()} ${await res.text()}`);
  const { cookies } = await ctx.storageState();
  const csrf = cookies.find((c) => c.name === "mudp_csrf")?.value || (await res.json()).csrfToken;
  return {
    ctx,
    get: async (url) => {
      const r = await ctx.get(url);
      return r.ok() ? r.json() : null;
    },
    post: async (url, data) => {
      const r = await ctx.post(url, { headers: { "X-CSRF-Token": csrf }, data: data ?? {} });
      return { ok: r.ok(), status: r.status(), body: await r.text() };
    },
    dispose: () => ctx.dispose(),
  };
}

// Images the seeder tries to publish, cheapest first. Registration re-tags an
// image that already exists on the host, so nothing is fetched from a registry
// and the suite still runs on an air-gapped machine.
const CANDIDATE_IMAGES = ["alpine:latest", "alpine:3.20", "busybox:latest", "hello-world:latest", "ubuntu:22.04"];

// seed prepares a realistic workspace so the list pages have rows to click: a
// netdisk root, a second (non-admin) account, a published image and — when
// Docker cooperates — one running container, a volume and a network per user.
//
// Every Docker-dependent step is best-effort; the returned flags tell the specs
// which parts of the UI can be exercised for real.
export async function seed(server, opts = {}) {
  const runId = opts.runId || Math.random().toString(36).slice(2, 7);
  const userName = opts.userName || "e2euser";
  const userPassword = opts.userPassword || "e2e-user-secret";

  const admin = await apiClient(server.url, server.adminUser, server.adminPassword);

  // 1. Point the default group's netdisk at a scratch directory. Without it the
  //    Netdisk tab renders an error box instead of a file browser.
  const groups = (await admin.get("/api/groups")) || [];
  const usersGroup = groups.find((g) => g.name === "users");
  if (!usersGroup) throw new Error("default 'users' group missing from a fresh database");
  fs.mkdirSync(server.netdiskRoot, { recursive: true });
  const ndRes = await admin.post("/api/groups/netdisk", { groupId: usersGroup.id, path: server.netdiskRoot });
  if (!ndRes.ok) throw new Error(`setting the group netdisk path failed: ${ndRes.body}`);

  // 2. A plain "user" account in the same group, for the non-admin pass.
  const createUser = await admin.post("/api/users", {
    username: userName,
    password: userPassword,
    role: "user",
    groupIds: [usersGroup.id],
    containerCap: 5,
    netdiskQuotaBytes: 2 * 1024 * 1024 * 1024,
  });
  if (!createUser.ok) throw new Error(`creating the test user failed: ${createUser.body}`);

  // 3. Publish a locally present image under a mudp name, visible to the group.
  const imageName = `e2e-img-${runId}`;
  let imagePublished = false;
  for (const ref of CANDIDATE_IMAGES) {
    const r = await admin.post("/api/images/register", { dockerRef: ref, name: imageName, groupIds: [usersGroup.id] });
    if (r.ok) { imagePublished = true; break; }
  }

  const user = await apiClient(server.url, userName, userPassword);

  // 4. One running container, one volume and one network per account, so both
  //    the admin and the user view have rows with per-row action buttons.
  const containers = {};
  for (const [role, client] of [["admin", admin], ["user", user]]) {
    if (!imagePublished) break;
    const create = await client.post("/api/containers", {
      name: `e2e-${role}-${runId}`,
      image: imageName,
      command: "sleep 3600",
      mountNetdisk: false,
    });
    if (!create.ok) continue;
    const id = JSON.parse(create.body).id;
    await client.post("/api/containers/action", { id, action: "start" });
    containers[role] = id;
  }
  for (const client of [admin, user]) {
    await client.post("/api/volumes", { name: `e2e-vol-${runId}`, driver: "local" });
    await client.post("/api/networks", { name: `e2e-net-${runId}`, driver: "bridge" });
  }

  await user.dispose();

  return {
    runId,
    imageName,
    imagePublished,
    hasAdminContainer: !!containers.admin,
    hasUserContainer: !!containers.user,
    user: { username: userName, password: userPassword },
    // cleanup unwinds everything the seeder created in Docker. The database is
    // thrown away with the server, so only Docker needs undoing.
    cleanup: async () => {
      try {
        for (const c of (await admin.get("/api/containers")) || []) {
          if ((c.name || "").includes(runId)) {
            await admin.post("/api/containers/action", { id: c.id, action: "remove" });
          }
        }
        for (const v of (await admin.get("/api/volumes")) || []) {
          if ((v.name || "").includes(runId)) {
            await admin.post("/api/volumes/delete", { name: v.fullName || v.name, force: true });
          }
        }
        for (const n of (await admin.get("/api/networks")) || []) {
          if ((n.name || "").includes(runId)) {
            await admin.post("/api/networks/delete", { name: n.fullName || n.name });
          }
        }
        for (const img of (await admin.get("/api/images")) || []) {
          if ((img.name || "").includes(runId)) {
            // The endpoint requires both fields; imageId-only is silently a
            // 400 and leaves the re-tagged image behind in Docker.
            await admin.post("/api/images/delete", { imageId: img.id, dockerRef: img.dockerRef });
          }
        }
      } finally {
        await admin.dispose();
      }
    },
  };
}

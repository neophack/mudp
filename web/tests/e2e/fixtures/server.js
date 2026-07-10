import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import path from "node:path";
import fs from "node:fs";
import net from "node:net";

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

export async function startServer() {
  // Ensure the binary exists.
  if (!fs.existsSync(binaryPath)) {
    throw new Error(
      `MUDP binary not found at ${binaryPath}. Run "go build -o dist/${binaryName} ./cmd/mudp" first.`
    );
  }

  const dbPath = path.join(repoRoot, "dist", `mudp-e2e-${Date.now()}.db`);
  const env = {
    ...process.env,
    MUDP_ADDR: "127.0.0.1:19000",
    MUDP_DB: dbPath,
    MUDP_ADMIN_USER: "admin",
    MUDP_ADMIN_PASSWORD: "e2e-secret",
    MUDP_SESSION_SECRET: "e2e-session-secret-must-be-32-bytes-long",
    MUDP_WEB_DIR: path.join(repoRoot, "web"),
  };

  const proc = spawn(binaryPath, [], { env, cwd: repoRoot, stdio: "pipe" });
  let stdout = "";
  let stderr = "";
  proc.stdout.on("data", (d) => { stdout += d.toString(); });
  proc.stderr.on("data", (d) => { stderr += d.toString(); });

  await waitForPort(19000);

  return {
    url: "http://127.0.0.1:19000",
    dbPath,
    stop: async () => {
      proc.kill("SIGTERM");
      await new Promise((resolve) => proc.on("close", resolve));
      try { fs.unlinkSync(dbPath); } catch {}
      // SQLite WAL files may exist; clean them up too.
      for (const ext of ["-shm", "-wal"]) {
        try { fs.unlinkSync(dbPath + ext); } catch {}
      }
    },
    logs: () => ({ stdout, stderr }),
  };
}

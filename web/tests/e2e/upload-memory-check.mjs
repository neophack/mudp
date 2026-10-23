// Memory probe for the netdisk upload pipeline: boots a throwaway server,
// serves the SPA through Vite (so source edits apply without a Go rebuild),
// uploads many files through the file picker, and samples the page's JS heap
// (after a forced GC) and the browser's total private memory as it goes. A
// flat curve means the pipeline holds O(batch) state; a rising one is a leak.
//
// Run from web/:  node tests/e2e/upload-memory-check.mjs [files] [KiB]
// BIG=n adds n files of 139 MiB (the shape of a camera-capture folder);
// FOLDER=1 uploads the source as a folder (webkitdirectory) instead of a pick;
// DROP=1 drags the folder onto the netdisk card, the FileSystemEntry walk.
import { startServer, apiClient } from "./fixtures/server.js";
import { login } from "./fixtures/ui.js";
import { chromium } from "playwright";
import { spawn, execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const N = Number(process.argv[2] || 3000);
const KIB = Number(process.argv[3] || 128);
const PORT = Number(process.env.PORT || 19411);
const VITE_PORT = 5199;

const BIG = Number(process.env.BIG || 0);
const BIG_BYTES = 145981440;
const FOLDER = process.env.FOLDER === "1";
const DROP = process.env.DROP === "1";
const srcDir = path.join(os.tmpdir(), `mudp-upload-mem-${N}x${KIB}-big${BIG}`);
if (!fs.existsSync(srcDir)) {
  fs.mkdirSync(srcDir, { recursive: true });
  for (let i = 0; i < N; i++) {
    fs.writeFileSync(path.join(srcDir, `f${String(i).padStart(6, "0")}.bin`), Buffer.alloc(KIB * 1024, i & 0xff));
  }
  const big = Buffer.alloc(BIG_BYTES);
  for (let i = 0; i < BIG; i++) {
    big.fill(i & 0xff);
    fs.writeFileSync(path.join(srcDir, `big${String(i).padStart(4, "0")}.raw`), big);
  }
}
const files = fs.readdirSync(srcDir).map((f) => path.join(srcDir, f));
const TOTAL = files.length;

// Sum of private bytes across every process of the Playwright-managed browser.
function browserPrivateMB() {
  const out = execFileSync("powershell", ["-NoProfile", "-Command",
    "(Get-Process | Where-Object { $_.Path -like '*ms-playwright*' } | Measure-Object PrivateMemorySize64 -Sum).Sum"]).toString().trim();
  return Math.round(Number(out) / 1048576);
}

const server = await startServer({ port: PORT, docker: false });
// PROD=1 tests the embedded production bundle (rebuild web + binary first);
// otherwise Vite serves the source so edits apply without a Go rebuild.
const PROD = process.env.PROD === "1";
const vite = PROD ? null : spawn(process.execPath, ["node_modules/vite/bin/vite.js", "--port", String(VITE_PORT), "--strictPort"], {
  env: { ...process.env, MUDP_UPSTREAM: server.url }, stdio: "pipe",
});
let browser;
try {
  if (vite) await new Promise((resolve) => vite.stdout.on("data", (d) => { if (String(d).includes(String(VITE_PORT))) resolve(); }));
  const admin = await apiClient(server.url, server.adminUser, server.adminPassword);
  const usersGroup = ((await admin.get("/api/groups")) || []).find((g) => g.name === "users");
  fs.mkdirSync(server.netdiskRoot, { recursive: true });
  await admin.post("/api/groups/netdisk", { groupId: usersGroup.id, path: server.netdiskRoot });
  const userName = "uploadmem";
  const userPassword = "upload-mem-secret";
  const created = await admin.post("/api/users", {
    username: userName, password: userPassword, role: "user",
    groupId: usersGroup.id, containerCap: 5, netdiskQuotaBytes: 64 * 1024 ** 3,
  });
  if (!created.ok) throw new Error(`creating user failed: ${created.body}`);

  browser = await chromium.launch();
  const ctx = await browser.newContext({ baseURL: PROD ? server.url : `http://127.0.0.1:${VITE_PORT}` });
  const page = await ctx.newPage();
  await login(page, userName, userPassword);
  await page.click(`nav button[data-tab="netdisk"]`);
  await page.waitForSelector("input[type=file]:not([webkitdirectory])", { state: "attached", timeout: 60000 });
  console.log("netdisk open");
  const cdp = await ctx.newCDPSession(page);
  await cdp.send("Performance.enable");
  const sample = async () => {
    await cdp.send("HeapProfiler.collectGarbage");
    const { metrics } = await cdp.send("Performance.getMetrics");
    const heap = metrics.find((m) => m.name === "JSHeapUsedSize").value;
    const counts = await page.evaluate(() => document.querySelector(".upload-counts")?.textContent || "");
    return { heapMB: +(heap / 1048576).toFixed(1), browserMB: browserPrivateMB(), counts: counts.trim() };
  };
  console.log("before", await sample());
  const t0 = Date.now();
  if (DROP) {
    const box = await page.locator(".netdisk-card").boundingBox();
    const at = { x: box.x + box.width / 2, y: box.y + 80 };
    const data = { items: [], files: [srcDir], dragOperationsMask: 1 };
    for (const type of ["dragEnter", "dragOver", "drop"]) {
      await cdp.send("Input.dispatchDragEvent", { type, ...at, data });
    }
  } else if (FOLDER) await page.setInputFiles("input[webkitdirectory]", srcDir);
  else await page.setInputFiles("input[type=file]:not([webkitdirectory])", files);
  for (;;) {
    await page.waitForTimeout(3000);
    const s = await sample();
    console.log(`${((Date.now() - t0) / 1000).toFixed(0)}s`, s);
    const done = /^(\d+) \/ (\d+)/.exec(s.counts);
    const failed = /(\d+) failed/.exec(s.counts);
    if (done && +done[1] + +(failed?.[1] || 0) >= TOTAL) break;
  }
  console.log("after", await sample());
  console.log("errors", await page.evaluate(() => [...new Set([...document.querySelectorAll(".upload-file-row.is-error .upload-file-status")].map((e) => e.textContent))]));
  // Leave and re-enter the netdisk: what is freed by leaving belongs to the
  // listing view, not the upload pipeline.
  await page.click(`nav button[data-tab="dashboard"]`, { timeout: 180000 });
  await page.waitForTimeout(3000);
  console.log("away", await sample());
  if (process.env.SNAPSHOT) {
    const out = fs.createWriteStream(process.env.SNAPSHOT);
    cdp.on("HeapProfiler.addHeapSnapshotChunk", ({ chunk }) => out.write(chunk));
    await cdp.send("HeapProfiler.takeHeapSnapshot", { reportProgress: false });
    await new Promise((resolve) => out.end(resolve));
    console.log("snapshot", process.env.SNAPSHOT);
  }
  await page.click(`nav button[data-tab="netdisk"]`);
  await page.waitForTimeout(5000);
  console.log("back", await sample());
} finally {
  await browser?.close();
  vite?.kill();
  await server.stop();
}

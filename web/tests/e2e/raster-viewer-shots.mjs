// Visual review for the YUV/RAW raster viewer (lib/yuv.js + lib/raster.css):
// boots a throwaway server, drops synthetic (but real-format) YUV/RAW media
// into the netdisk root, then screenshots the redesigned viewer in its main
// states — YUV frame, ISP panel open, 1:1 zoom, playback, and the RAW file
// with its board-calibrated ISP defaults.
//
// Run from web/:  node tests/e2e/raster-viewer-shots.mjs
// Shots land in test-results/raster-*.png.
import { startServer, apiClient } from "./fixtures/server.js";
import { login, openTab } from "./fixtures/ui.js";
import { chromium } from "playwright";
import fs from "node:fs";
import path from "node:path";

const PORT = 19399;

// ---- Synthetic media -------------------------------------------------------

// RGB test pattern: smooth color field + a white marker whose x position
// advances with the frame index (visible when scrubbing/playing).
function patternRGB(w, h, f, frames) {
  return (x, y) => {
    const mx = ((f + 1) / (frames + 1)) * w;
    if (y > h * 0.72 && Math.abs(x - mx) < 26) return [235, 235, 235];
    const r = Math.round(255 * (x / w));
    const g = Math.round(255 * (y / h));
    const b = Math.round(128 + 120 * Math.sin(2 * Math.PI * (x / w + f / frames)));
    return [r, g, b];
  };
}

// NV12 (Y + interleaved UV), BT.601, 8 frames of 640x360.
function makeYuv(w, h, frames) {
  const ySize = w * h;
  const cW = w / 2;
  const cH = h / 2;
  const buf = Buffer.alloc((ySize + cW * cH * 2) * frames);
  const rgb = (f) => patternRGB(w, h, f, frames);
  for (let f = 0; f < frames; f++) {
    const base = f * (ySize + cW * cH * 2);
    const px = rgb(f);
    for (let y = 0; y < h; y++) {
      for (let x = 0; x < w; x++) {
        const [r, g, b] = px(x, y);
        const Y = Math.min(255, Math.max(0, Math.round(0.299 * r + 0.587 * g + 0.114 * b)));
        buf[base + y * w + x] = Y;
        if ((y & 1) === 0 && (x & 1) === 0) {
          const cb = Math.min(255, Math.max(0, Math.round(128 - 0.168736 * r - 0.331264 * g + 0.5 * b)));
          const cr = Math.min(255, Math.max(0, Math.round(128 + 0.5 * r - 0.418688 * g - 0.081312 * b)));
          const cOff = base + ySize + (y / 2) * cW * 2 + (x / 2) * 2;
          buf[cOff] = cb;
          buf[cOff + 1] = cr;
        }
      }
    }
  }
  return buf;
}

// RGGB 16-bit little-endian Bayer dump, 2 frames of 640x360: the RGB pattern
// sampled through the CFA mosaic.
function makeRaw(w, h, frames) {
  const buf = Buffer.alloc(w * h * 2 * frames);
  const rgb = (f) => patternRGB(w, h, f, frames);
  for (let f = 0; f < frames; f++) {
    const base = f * w * h * 2;
    const px = rgb(f);
    for (let y = 0; y < h; y++) {
      for (let x = 0; x < w; x++) {
        // RGGB: (0,0)=R (0,1)=G (1,0)=G (1,1)=B
        const ch = (y & 1) === 0 ? ((x & 1) === 0 ? 0 : 1) : (x & 1) === 0 ? 1 : 2;
        const v = Math.min(1023, px(x, y)[ch] * 4);
        const off = base + (y * w + x) * 2;
        buf[off] = v & 0xff;
        buf[off + 1] = v >> 8;
      }
    }
  }
  return buf;
}

// ---- Drive -----------------------------------------------------------------

const server = await startServer({ port: PORT, docker: false });
try {
  const admin = await apiClient(server.url, server.adminUser, server.adminPassword);
  const groups = (await admin.get("/api/groups")) || [];
  const usersGroup = groups.find((g) => g.name === "users");
  fs.mkdirSync(server.netdiskRoot, { recursive: true });
  await admin.post("/api/groups/netdisk", { groupId: usersGroup.id, path: server.netdiskRoot });
  // The admin account is not in the users group, so its netdisk has no root;
  // browse as a fresh member of the group instead (same as the seeder).
  const userName = "rastershots";
  const userPassword = "raster-shots-secret";
  const createUser = await admin.post("/api/users", {
    username: userName, password: userPassword, role: "user",
    groupId: usersGroup.id, containerCap: 5, netdiskQuotaBytes: 2 * 1024 ** 3,
  });
  if (!createUser.ok) throw new Error(`creating user failed: ${createUser.body}`);
  // Each user's netdisk is a "<name>-<userID>" folder under the group root
  // (EnsureUserNetdiskDir); drop the media straight into it.
  const users = (await admin.get("/api/users")) || [];
  const created = (users.items || users).find?.((u) => u.username === userName) || {};
  const userDir = path.join(server.netdiskRoot, `${userName}-${created.id}`);
  fs.mkdirSync(userDir, { recursive: true });
  fs.writeFileSync(path.join(userDir, "gradient_640x360_nv12.yuv"), makeYuv(640, 360, 8));
  fs.writeFileSync(path.join(userDir, "RAW_640x360_16bits_RGGB_Linear.raw"), makeRaw(640, 360, 2));
  console.log(`[raster-shots] server ${server.url}, media written to ${userDir}`);

  const browser = await chromium.launch();
  const ctx = await browser.newContext({ baseURL: server.url, viewport: { width: 1440, height: 900 } });
  const page = await ctx.newPage();
  await login(page, userName, userPassword);
  await openTab(page, "netdisk");

  const shot = (name) => page.screenshot({ path: `test-results/${name}.png` });
  const waitFrame = () =>
    page.waitForFunction(() => {
      const s = document.querySelector("#rasterStatus");
      return s && !/Loading|Failed|Enter|Unsupported/.test(s.textContent);
    }, null, { timeout: 30000 });

  async function openFile(name) {
    await page.locator(".name-link", { hasText: name }).first().click();
    await page.locator(".raster-root").waitFor({ timeout: 15000 });
    await waitFrame();
  }

  // YUV: default (fit) view with the pixel probe lit.
  await openFile("gradient_640x360_nv12.yuv");
  await page.locator("#rasterCanvas").hover({ position: { x: 200, y: 120 } });
  await page.waitForTimeout(200);
  await shot("raster-yuv");

  // Frame scrub + playback state.
  await page.locator("#rasterFrameSlider").fill("6");
  await page.locator("#rasterPlayBtn").click();
  await page.waitForTimeout(700);
  await shot("raster-yuv-playing");
  await page.locator("#rasterPlayBtn").click();

  // ISP parameter panel open (sliders + readouts).
  await page.locator("#rasterIspPanel > summary").click();
  await page.waitForTimeout(300);
  await shot("raster-yuv-isp");

  // 1:1 zoom (nearest-neighbor pixel peeping).
  await page.locator("#rasterZoomReset").click();
  await page.locator("#rasterCanvasWrap").hover({ position: { x: 300, y: 200 } });
  await waitFrame();
  await shot("raster-yuv-100");

  // Panel collapsed.
  await page.keyboard.press("Escape");
  await openFile("gradient_640x360_nv12.yuv");
  await page.locator("#rasterPanelToggle").click();
  await page.locator("#rasterCanvas").hover({ position: { x: 300, y: 150 } });
  await shot("raster-yuv-nopanel");

  // RAW: ISP on by default with the Bayer controls.
  await page.keyboard.press("Escape");
  await page.waitForTimeout(300);
  await openFile("RAW_640x360_16bits_RGGB_Linear.raw");
  await page.locator("#rasterCanvas").hover({ position: { x: 200, y: 120 } });
  await page.waitForTimeout(200);
  await shot("raster-raw");
  await page.locator("#rasterIspPanel > summary").click();
  await page.waitForTimeout(300);
  await shot("raster-raw-isp");

  await browser.close();
  console.log("[raster-shots] done: test-results/raster-*.png");
} finally {
  await server.stop();
}

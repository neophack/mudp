// One-off visual check for the file-icon + viewer-header work:
//   1. the netdisk list shows per-extension icons (unknown = gray),
//   2. the viewer dialog puts fullscreen/download on the title row,
//   3. the video player carries controlslist="nofullscreen".
// Boots a throwaway mudp server via the shared e2e fixtures. Run:
//   node tests/e2e/viewer-icons-check.mjs
import fs from "node:fs";
import path from "node:path";
import { chromium } from "playwright";
import { startServer, seed } from "./fixtures/server.js";
import { login } from "./fixtures/ui.js";

const OUT = "test-results/viewer-icons";
const server = await startServer({ port: 19500 });
try {
  const seeded = await seed(server);

  const browser = await chromium.launch();
  const context = await browser.newContext({ baseURL: server.url, viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();
  page.on("pageerror", (e) => { throw new Error(`Page JS error: ${e.message}`); });
  await page.goto(server.url);
  // The seeded netdisk root belongs to the "users" group, and a regular user
  // browses their own "<username>-<id>" subfolder inside it.
  await login(page, seeded.user.username, seeded.user.password);
  await page.click('nav button[data-tab="netdisk"]');
  await page.waitForSelector(".netdisk-card");

  const userRoot = fs
    .readdirSync(server.netdiskRoot)
    .find((d) => d.startsWith(`${seeded.user.username}-`));
  if (!userRoot) throw new Error(`user netdisk subdir not found under ${server.netdiskRoot}`);
  const root = path.join(server.netdiskRoot, userRoot);
  const png = Buffer.from(
    "iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAYAAADED76LAAAAHElEQVQoz2P4z8DAwMDAwMDwn4GBgYGBgYGBAQAmmgGGRkm2fAAAAABJRU5ErkJggg==",
    "base64",
  );
  fs.writeFileSync(path.join(root, "pic.png"), png);
  fs.writeFileSync(path.join(root, "clip.mp4"), Buffer.from([0, 0, 0, 24, 102, 116, 121, 112]));
  fs.writeFileSync(path.join(root, "song.mp3"), Buffer.alloc(64));
  fs.writeFileSync(path.join(root, "arch.zip"), Buffer.alloc(64));
  fs.writeFileSync(path.join(root, "main.go"), "package main\n");
  fs.writeFileSync(path.join(root, "notes.txt"), "plain text\n");
  fs.writeFileSync(path.join(root, "blob.bin"), Buffer.alloc(64));

  await page.reload();
  await page.waitForSelector(".netdisk-file");
  await page.screenshot({ path: `${OUT}/list.png`, fullPage: true });

  // Unknown-extension rows must render the gray generic page, never black.
  const colors = await page.$$eval(".netdisk-icon path", (paths) =>
    paths.map((p) => p.getAttribute("fill")).filter(Boolean),
  );
  console.log("icon fills:", JSON.stringify([...new Set(colors)]));

  // Image preview: header holds title + actions on one row.
  await page.click('.netdisk-file:has-text("pic.png") .name-link');
  await page.waitForSelector(".viewer-head");
  await page.screenshot({ path: `${OUT}/viewer-image.png` });
  const headRight = await page.$eval(".viewer-head", (el) => {
    const box = el.getBoundingClientRect();
    const acts = el.querySelector(".viewer-head-actions").getBoundingClientRect();
    return { actsRightGap: Math.round(box.right - acts.right), sameRow: acts.top < box.bottom && acts.bottom > box.top };
  });
  const imageButtons = await page.locator(".viewer-head-actions button").count();
  console.log("viewer head actions:", JSON.stringify({ ...headRight, imageButtons }));

  // Video preview: no fullscreen anywhere — the header offers download only
  // and the player itself hides its fullscreen control.
  await page.keyboard.press("Escape");
  await page.click('.netdisk-file:has-text("clip.mp4") .name-link');
  await page.waitForSelector("video.viewer-media");
  const videoButtons = await page.locator(".viewer-head-actions button").count();
  const videoButtonTexts = await page.locator(".viewer-head-actions button").allTextContents();
  const ctl = await page.$eval("video.viewer-media", (v) => ({
    controlslist: v.getAttribute("controlslist"),
  }));
  await page.screenshot({ path: `${OUT}/viewer-video.png` });
  console.log("video header buttons:", videoButtons, JSON.stringify(videoButtonTexts));
  console.log("video controls:", JSON.stringify(ctl));

  await browser.close();
} finally {
  await server.stop();
}
console.log("done");

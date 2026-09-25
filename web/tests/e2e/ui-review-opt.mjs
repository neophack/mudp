// Optimization-pass visual review: capture the pages touched by the
// optimization changes (dashboard donut, containers, hardware charts, netdisk,
// images) in light and dark mode, using the fresh dist build.
import { chromium } from "@playwright/test";
import fs from "node:fs";
import path from "node:path";
import { startServer } from "./fixtures/server.js";

const OUT = "C:/Users/penghongxia/AppData/Local/Temp/mudp-opt-review";
fs.mkdirSync(OUT, { recursive: true });

const server = await startServer({ port: 19344 });
const browser = await chromium.launch();
const manifest = [];
let n = 0;

async function shot(page, slug, desc, opts = {}) {
  n += 1;
  const name = `${String(n).padStart(2, "0")}-${slug}.png`;
  await page.waitForTimeout(opts.settle ?? 900);
  await page.screenshot({ path: path.join(OUT, name), fullPage: !!opts.fullPage });
  manifest.push(`${name}\t${desc}`);
  console.log(`[shot] ${name} ${desc}`);
}

async function login(page, theme) {
  const ctx = page.context();
  if (theme) await ctx.addInitScript((t) => localStorage.setItem("mudp:theme", t), theme);
  const [capRes] = await Promise.all([
    page.waitForResponse((r) => r.url().includes("/api/captcha")),
    page.goto(`${server.url}/login`),
  ]);
  await page.waitForLoadState("domcontentloaded");
  await page.fill("input[name=username]", server.adminUser);
  await page.fill("input[name=password]", server.adminPassword);
  await page.fill(".captcha-row input", capRes.headers()["x-mudp-captcha-answer"]);
  await page.locator("button.auth-submit").click();
  await page.waitForURL("**/dashboard");
}

try {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: "zh-CN" });
  const page = await ctx.newPage();
  page.setDefaultTimeout(8000);
  await login(page);

  // Dashboard: donut (echarts on-demand), env card, recent activity.
  await page.waitForSelector(".echart-box canvas", { timeout: 10000 });
  await shot(page, "dashboard-light", "仪表盘(浅色):环形图/环境卡/最近动态");

  // Containers.
  await page.goto(`${server.url}/containers`);
  await page.waitForSelector(".el-table__row", { timeout: 8000 }).catch(() => {});
  await shot(page, "containers-light", "容器列表(浅色):工具栏/筛选/表格");

  // Hardware: line charts with theme-aware axis colors (light).
  await page.goto(`${server.url}/hardware`);
  await page.waitForSelector(".echart-box canvas", { timeout: 10000 }).catch(() => {});
  await shot(page, "hardware-light", "硬件页(浅色):CPU/内存折线图");

  // Netdisk with a seeded file + a share to render.
  await page.goto(`${server.url}/netdisk`);
  await page.waitForSelector(".el-table__row", { timeout: 8000 }).catch(() => {});
  await shot(page, "netdisk-light", "网盘(浅色):表格/配额条");

  // Images.
  await page.goto(`${server.url}/images`);
  await page.waitForSelector(".el-table__row", { timeout: 8000 }).catch(() => {});
  await shot(page, "images-light", "镜像页(浅色)");

  // Dark mode: re-login context with theme=dark, revisit dashboard + hardware.
  const darkCtx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: "zh-CN" });
  await darkCtx.addInitScript(() => localStorage.setItem("mudp:theme", "dark"));
  const dpage = await darkCtx.newPage();
  dpage.setDefaultTimeout(8000);
  await login(dpage);
  await dpage.waitForSelector(".echart-box canvas", { timeout: 10000 });
  await shot(dpage, "dashboard-dark", "仪表盘(暗色)");
  await dpage.goto(`${server.url}/hardware`);
  await dpage.waitForSelector(".echart-box canvas", { timeout: 10000 }).catch(() => {});
  await shot(dpage, "hardware-dark", "硬件页(暗色):轴线/网格暗色适配");
  await darkCtx.close();
  await ctx.close();
} finally {
  fs.writeFileSync(path.join(OUT, "manifest.txt"), manifest.join("\n"));
  await browser.close();
  await server.stop();
}
console.log("done:", path.join(OUT, "manifest.txt"));

// Crawler suites: click every enabled button on every page as admin (desktop)
// and as admin (phone), closing whatever opens. Native confirms stay cancelled
// via installPage, so destructive paths are exercised up to their confirmation
// but never past it. The point is total interaction coverage: any button that
// throws, opens an unclosable overlay, or 500s fails the suite.
import { test, expect } from "@playwright/test";
import { startServer, seed } from "./fixtures/server.js";
import { installPage, login, openTab, fillCaptcha, closeModals, clickEveryButton, CRAWLER_SKIP_BUTTONS } from "./fixtures/ui.js";
import { ADMIN_TABS } from "./fixtures/ui.js";
import fs from "node:fs";
import path from "node:path";

test.use({ baseURL: "http://127.0.0.1:19061" });

let server;

test.beforeAll(async () => {
  server = await startServer({ port: 19061 });
  await seed(server, { runId: "crawl" });
  const root = path.join(server.netdiskRoot, "admin-1");
  fs.mkdirSync(path.join(root, "crawl-dir"), { recursive: true });
  fs.writeFileSync(path.join(root, "crawl.txt"), "crawl\n");
});

test.afterAll(async () => {
  if (server) await server.stop();
});

test("desktop crawler: every button on every admin tab opens and closes cleanly", async ({ page }) => {
  const h = installPage(page);
  await login(page, server.adminUser, server.adminPassword);

  for (const tab of ADMIN_TABS) {
    await openTab(page, tab);
    const clicked = await clickEveryButton(page, {
      skip: [
        ...CRAWLER_SKIP_BUTTONS,
        // zh labels the crawler must not press: host-level writes without a
        // native confirm, or actions covered by dedicated specs
        "立即挂载", "备份数据库", "立即备份所有用户", "保存计划", "保存配置", "卸载",
        "保存网络", "保存", "发送", "创建令牌", "检查最新版本", "一键升级",
        "登出", "退出",
      ],
    });
    // pages that are pure tables/monitors legitimately expose zero buttons
    // when Docker is absent
    const zeroButtonOk = ["processes", "usage", "hardware", "help", "containers", "images", "volumes", "networks", "stacks", "mcp", "forwards", "audit", "security"];
    if (!zeroButtonOk.includes(tab)) {
      expect(clicked.length, `${tab}: crawler pressed nothing — selector drift?`).toBeGreaterThan(0);
    }
    h.assertClean(`crawling ${tab}`, { ignore503: true });
  }
});

test("phone crawler: every admin tab opens, drawer navigates, action sheets close", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, locale: "zh-CN", hasTouch: true });
  const page = await ctx.newPage();
  const h = installPage(page);
  await page.addInitScript(() => { if (!localStorage.getItem("mudp_language")) localStorage.setItem("mudp_language", "zh_CN"); });

  await page.goto("/");
  await page.locator("form.auth-card").waitFor();
  await page.fill("input[name='username']", server.adminUser);
  await page.fill("input[name='password']", server.adminPassword);
  await fillCaptcha(page);
  await page.click("form.auth-card .auth-submit");
  await expect(page.locator(".mobile-nav-toggle")).toBeVisible({ timeout: 30000 });

  for (const tab of ADMIN_TABS) {
    await page.locator(".mobile-nav-toggle").click();
    await page.locator(`.drawer-nav button[data-tab='${tab}']`).click();
    await page.waitForTimeout(450);

    // every visible button on the phone page: tap, then close what opened
    const buttons = await page.$$eval(
      ".app-main button",
      (els) => els
        .filter((e) => !e.disabled && e.offsetParent !== null)
        .map((e) => ({ title: e.getAttribute("title") || "", text: (e.textContent || "").trim().slice(0, 40) })),
    );
    for (const b of buttons) {
      const label = b.title || b.text;
      if (!label) continue;
      if ([...CRAWLER_SKIP_BUTTONS, "立即挂载", "备份数据库", "立即备份所有用户", "保存计划", "保存配置", "卸载", "保存网络", "保存", "发送", "创建令牌", "检查最新版本", "一键升级", "登出"].some((s) => label.includes(s))) continue;
      const loc = page.locator(".app-main button", { hasText: b.text }).first();
      if ((await loc.count()) === 0 || !(await loc.isVisible())) continue;
      try {
        await loc.click({ timeout: 2000 });
      } catch {
        continue; // re-rendered away mid-loop
      }
      await page.waitForTimeout(250);
      await closeModals(page);
    }
    h.assertClean(`phone crawling ${tab}`, { ignore503: true });
  }

  // action sheet: right-half tap on a netdisk row opens the sheet, close via Esc
  await page.locator(".mobile-nav-toggle").click();
  await page.locator(".drawer-nav button[data-tab='netdisk']").click();
  await page.locator(".el-table__row").first().waitFor({ timeout: 15000 });
  const box = await page.locator(".el-table__row").first().boundingBox();
  await page.touchscreen.tap(box.x + box.width - 50, box.y + box.height / 2);
  await expect(page.locator(".el-drawer:visible")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.locator(".el-drawer:visible")).toHaveCount(0);

  await ctx.close();
});

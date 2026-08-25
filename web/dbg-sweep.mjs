// Reproduce the sweep script's exact selector logic with logging.
import { chromium } from "playwright";

const BASE = process.argv[2] || "http://127.0.0.1:19200";
const browser = await chromium.launch();

for (const { name, width, height } of [{ name: "w375", width: 375, height: 720 }, { name: "w768", width: 768, height: 1024 }]) {
  const page = await browser.newPage({ viewport: { width, height } });
  await page.goto(BASE + "/");
  await page.fill("input[name='username']", "admin");
  await page.fill("input[name='password']", "smoke-secret-123");
  await page.click("form.auth-card .auth-submit");
  await page.waitForSelector(".work-header", { timeout: 60000 });
  console.log(`\n=== ${name} ===`);

  for (const tab of ["dashboard", "netdisk", "containers", "hardware", "users", "disks", "database", "settings", "help"]) {
    const navBtn = page.locator(`nav button[data-tab="${tab}"]:visible`).first();
    if (!(await navBtn.count())) {
      console.log(`${tab}: nav btn count 0, opening drawer...`);
      await page.locator(".mobile-nav-toggle").click().catch((e) => console.log(`${tab}: toggle FAIL ${e.message.split("\n")[0]}`));
      await page.waitForTimeout(400);
      console.log(`${tab}: after toggle, nav btn count:`, await navBtn.count());
      console.log(`${tab}: drawer-nav btn count:`, await page.locator(`.drawer-nav button[data-tab="${tab}"]:visible`).count());
    } else {
      console.log(`${tab}: nav btn already visible (${await navBtn.count()})`);
    }
    await navBtn.click({ timeout: 3000 }).then(() => console.log(`${tab}: click ok`), (e) => console.log(`${tab}: click FAIL ${e.message.split("\n")[0]}`));
    await page.waitForURL(`**/${tab}`, { timeout: 5000 }).catch(() => {});
    // close drawer if still open
    await page.keyboard.press("Escape");
    await page.waitForTimeout(200);
  }
  await page.close();
}
await browser.close();

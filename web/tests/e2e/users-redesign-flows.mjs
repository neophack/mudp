// Flow-level checks for the redesigned Users page: create a user through the
// dialog and switch a group's language through the group-settings dialog.
// Run: node tests/e2e/users-redesign-flows.mjs [url]
import { chromium } from "playwright";

const BASE = process.argv[2] || "http://127.0.0.1:19200";
const results = [];
const check = (name, ok, detail = "") => results.push(`${ok ? "PASS" : "FAIL"} ${name}${detail ? ` — ${detail}` : ""}`);

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
const errors = [];
page.on("pageerror", (e) => errors.push(e.message));

await page.goto(BASE + "/");
await page.fill("input[name='username']", "admin");
await page.fill("input[name='password']", "smoke-secret-123");
const [capResp] = await Promise.all([
  page.waitForResponse((r) => r.url().includes("/api/captcha")),
  page.click(".captcha-img"),
]);
await page.fill("input[name='captcha']", capResp.headers()["x-mudp-captcha-answer"]);
await page.click("form.auth-card .auth-submit");
await page.waitForSelector(".work-header", { timeout: 60000 });

await page.goto(BASE + "/users");
await page.waitForSelector(".users-page", { timeout: 20000 });
await page.waitForTimeout(1200);

// --- Create user through the dialog ---
await page.getByRole("button", { name: "New User" }).click();
await page.waitForSelector(".el-dialog:visible", { timeout: 5000 });
const dlg = page.locator(".el-dialog:visible");
await dlg.locator(".el-form-item").filter({ hasText: "Username" }).locator("input").fill("alice");
await dlg.locator(".el-form-item").filter({ hasText: "Password" }).locator("input").fill("alice-secret-123");
await dlg.locator(".el-select").first().click();
await page.locator(".el-select-dropdown__item:visible", { hasText: "Help Desk" }).first().click();
await dlg.locator(".el-form-item").filter({ hasText: "Netdisk quota" }).locator("input").fill("2");
await page.screenshot({ path: "test-results/users-redesign/dialog-create-filled.png" });
await dlg.getByRole("button", { name: "Create User" }).click();
await page.waitForTimeout(1200);
const aliceRow = await page.locator(".el-table__row:has-text('alice')").count();
check("user created via dialog", aliceRow >= 1, `rows: ${aliceRow}`);
check("created user shows helpdesk role", await page.locator(".el-table__row:has-text('alice') .el-tag:has-text('helpdesk')").count() >= 1);

// --- Group language via group-settings dialog ---
const groupRow = page.locator(".el-table__row:has-text('research')").first();
await groupRow.getByRole("button", { name: "Edit" }).click();
await page.waitForSelector(".el-dialog:visible", { timeout: 5000 });
const gdlg = page.locator(".el-dialog:visible");
await gdlg.locator(".el-select").click();
await page.locator(".el-select-dropdown__item:visible", { hasText: "中文" }).first().click();
await gdlg.getByRole("button", { name: "Save" }).click();
await page.waitForTimeout(1000);
check("group language saved", await groupRow.locator(":scope").textContent().then((t) => t.includes("中文")));

await browser.close();
for (const r of results) console.log(r);
console.log(errors.length ? `JS ERRORS:\n${errors.join(" | ")}` : "NO JS ERRORS");
process.exit(results.some((r) => r.startsWith("FAIL")) || errors.length ? 1 : 0);

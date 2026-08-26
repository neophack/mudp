// Users-page redesign verification: layout overflow checks, dialog flows
// (create user / group settings), dark mode, and phone-width sweeps.
// Run: node tests/e2e/users-redesign-check.mjs [url]
import { chromium } from "playwright";
import fs from "node:fs";

const BASE = process.argv[2] || "http://127.0.0.1:19200";
const OUT = "test-results/users-redesign";
fs.mkdirSync(OUT, { recursive: true });

const results = [];
const check = (name, ok, detail = "") => {
  results.push(`${ok ? "PASS" : "FAIL"} ${name}${detail ? ` — ${detail}` : ""}`);
};

const browser = await chromium.launch();

async function login(page) {
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
}

// True when any element spills past the right edge of its scroll container,
// ignoring Element's own intentional table scrollers.
async function overflowInfo(page) {
  return page.evaluate(() => {
    const doc = document.documentElement;
    const bodySpill = doc.scrollWidth - doc.clientWidth;
    let clipped = 0;
    const offenders = [];
    for (const el of document.querySelectorAll(".app-main *")) {
      // el-table owns its horizontal scroller (min-width columns + fixed-right
      // panels), so anything inside it is intentionally scrollable, not spill.
      if (el.closest(".el-table")) continue;
      const r = el.getBoundingClientRect();
      if (r.width > 0 && r.right > doc.clientWidth + 1) {
        clipped++;
        if (offenders.length < 5) offenders.push(el.className && el.className.baseVal !== undefined ? el.tagName : (el.className || el.tagName));
      }
    }
    return { bodySpill, clipped, offenders };
  });
}

// ---------- Desktop, light ----------
{
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await login(page);
  await page.goto(BASE + "/users");
  await page.waitForSelector(".users-page", { timeout: 20000 });
  await page.waitForTimeout(1500);

  const cards = await page.locator(".users-page > .card").count();
  const duoCards = await page.locator(".users-duo > .card").count();
  check("card structure", cards === 2 && duoCards === 2, `top ${cards} + duo ${duoCards} (expect 2 + 2)`);

  const usersBtn = page.locator(".card").first().getByRole("button", { name: "New User" });
  check("New User button in users card head", await usersBtn.count() === 1);
  const newGroupBtn = page.getByRole("button", { name: "New Group" });
  check("New Group button in groups card head", await newGroupBtn.count() === 1);

  // Groups table shows the merged per-group columns.
  for (const col of ["Netdisk path", "Backup path", "Shared-disk path", "Language"]) {
    const hit = await page.locator(`.el-table__header th:has-text("${col}")`).count();
    check(`groups column "${col}"`, hit >= 1, `found ${hit}`);
  }

  const ov = await overflowInfo(page);
  check("no horizontal overflow (desktop)", ov.bodySpill <= 0 && ov.clipped === 0, JSON.stringify(ov));
  await page.screenshot({ path: `${OUT}/desktop-light.png`, fullPage: true });

  // --- Create-user dialog ---
  await page.getByRole("button", { name: "New User" }).click();
  await page.waitForSelector(".el-dialog:visible", { timeout: 5000 });
  const dlgFields = await page.locator(".el-dialog:visible .el-form-item").count();
  check("create-user dialog fields", dlgFields === 6, `found ${dlgFields} (expect 6)`);
  await page.screenshot({ path: `${OUT}/dialog-create-user.png` });
  await page.locator(".el-dialog:visible").getByRole("button", { name: "Cancel" }).click();
  await page.waitForTimeout(400);

  // --- Create group via prompt, then configure it through the new dialog ---
  await newGroupBtn.click();
  await page.waitForSelector(".el-message-box", { timeout: 5000 });
  await page.locator(".el-message-box__input input").fill("research");
  await page.locator(".el-message-box__btns .el-button--primary").click();
  await page.waitForTimeout(800);
  const rowShown = await page.locator(".el-table__row:has-text('research')").count();
  check("group created and listed", rowShown >= 1);

  await page.locator(".el-table__row:has-text('research')").getByRole("button", { name: "Edit" }).click();
  await page.waitForSelector(".el-dialog:visible", { timeout: 5000 });
  const formItems = await page.locator(".el-dialog:visible .el-form-item").count();
  const pathInputs = await page.locator(".el-dialog:visible .el-form-item:has(.el-input__inner) .el-input__inner").count();
  check("group settings dialog fields", formItems === 4 && pathInputs === 3, `${formItems} form items, ${pathInputs} path inputs (expect 4 + 3)`);

  const dlg = page.locator(".el-dialog:visible");
  await dlg.locator(".el-form-item").filter({ hasText: "Netdisk path" }).locator("input").fill("/tmp/mudp-e2e-netdisk/research");
  await dlg.locator(".el-form-item").filter({ hasText: "Backup path" }).locator("input").fill("/tmp/mudp-e2e-backup/research");
  await page.screenshot({ path: `${OUT}/dialog-group-settings.png` });
  await dlg.getByRole("button", { name: "Save" }).click();
  await page.waitForTimeout(1000);
  const savedRow = await page.locator(".el-table__row:has-text('/tmp/mudp-e2e-netdisk/research')").count();
  check("group paths saved via dialog", savedRow >= 1);

  const ov2 = await overflowInfo(page);
  check("no horizontal overflow after save", ov2.bodySpill <= 0 && ov2.clipped === 0, JSON.stringify(ov2));

  // --- Dark mode ---
  await page.locator(".work-header .icon-btn").first().click();
  await page.waitForTimeout(500);
  const isDark = await page.evaluate(() => document.documentElement.getAttribute("data-theme") === "dark");
  check("dark mode toggled", isDark);
  await page.screenshot({ path: `${OUT}/desktop-dark.png`, fullPage: true });

  check("no JS errors (desktop)", errors.length === 0, errors.join(" | "));
  await page.close();
}

// ---------- Phone ----------
{
  const page = await browser.newPage({ viewport: { width: 375, height: 720 } });
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await login(page);
  await page.goto(BASE + "/users");
  await page.waitForSelector(".users-page", { timeout: 20000 });
  await page.waitForTimeout(1500);

  // Mobile tables drop their secondary columns; the folded-in info shows instead.
  const groupsHeaders = await page.locator(".el-table__header th:has-text('Backup path')").count();
  check("phone hides groups backup-path column", groupsHeaders === 0, `found ${groupsHeaders}`);
  const actionsHeaders = await page.locator(".el-table__header th:has-text('Actions')").count();
  check("phone hides actions column", actionsHeaders === 0, `found ${actionsHeaders}`);
  const folded = await page.locator(".el-table__row .secondary-line:has-text('Ports:')").count();
  check("phone folds groups/ports into user cell", folded >= 1, `folded lines: ${folded}`);

  // Tapping a user row opens the bottom action sheet with every action.
  await page.locator(".el-table__row:has-text('admin')").first().click();
  await page.waitForSelector(".action-sheet:visible", { timeout: 5000 });
  const sheetBtns = await page.locator(".action-sheet .sheet-btn:visible").count();
  check("user action sheet opens on row tap", sheetBtns === 4, `buttons: ${sheetBtns} (expect 4; self row hides approve)`);
  const sheetMeta = await page.locator(".action-sheet .sheet-meta-line").count();
  check("sheet shows meta lines", sheetMeta === 3, `meta lines: ${sheetMeta}`);
  await page.screenshot({ path: `${OUT}/phone-sheet.png` });
  await page.locator(".action-sheet .sheet-btn:visible").first().click(); // groups dialog
  await page.waitForTimeout(600);
  const groupsDlg = await page.locator(".el-dialog:visible").count();
  check("sheet 'groups' opens the groups dialog", groupsDlg === 1);
  await page.keyboard.press("Escape");
  await page.waitForTimeout(400);

  // Tapping a group row opens the group-settings dialog directly.
  await page.locator(".el-table__row:has-text('research')").first().click();
  await page.waitForTimeout(600);
  const groupDlgVisible = await page.locator(".el-dialog:visible .el-form-item:has-text('Netdisk path')").count();
  check("group row tap opens settings dialog", groupDlgVisible === 1);
  await page.keyboard.press("Escape");
  await page.waitForTimeout(400);

  const ov = await overflowInfo(page);
  check("no horizontal overflow (phone)", ov.bodySpill <= 0 && ov.clipped === 0, JSON.stringify(ov));
  await page.screenshot({ path: `${OUT}/phone-light.png`, fullPage: true });
  check("no JS errors (phone)", errors.length === 0, errors.join(" | "));
  await page.close();
}

await browser.close();
const failed = results.filter((r) => r.startsWith("FAIL"));
for (const r of results) console.log(r);
console.log(failed.length ? `\n${failed.length} FAILURES` : "\nALL CHECKS PASSED");
process.exit(failed.length ? 1 : 0);

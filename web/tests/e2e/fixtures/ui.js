import { expect } from "@playwright/test";

// Tabs the sidebar renders per role, mirroring render() in web/app.js.
export const ADMIN_TABS = [
  "dashboard", "netdisk", "containers", "mcp", "processes", "usage", "images", "volumes",
  "networks", "forwards", "stacks", "hardware", "users", "audit", "security", "errors", "disks", "database", "settings", "help",
];
export const USER_TABS = [
  "dashboard", "netdisk", "containers", "mcp", "processes", "usage", "images", "volumes",
  "networks", "stacks", "hardware", "settings", "help",
];
// Tabs only an admin may reach.
export const ADMIN_ONLY_TABS = ADMIN_TABS.filter((t) => !USER_TABS.includes(t));

// Buttons the generic "click everything" crawler must not press, shared by the
// admin and user specs. Each is either:
//   - a real host-level side effect with no confirmation dialog to stop it
//     (mount, backup, schedule, SSO/credential writes), or
//   - the bare "Save" language-preference button, which calls location.reload()
//     on success and would otherwise tear down the page mid-crawl.
// Covered explicitly, safely, elsewhere: users/groups creation, and (for admins)
// the language/registry/Feishu forms are exercised with real assertions in the
// dedicated settings/users tests.
export const CRAWLER_SKIP_BUTTONS = [
  "Mount Now",          // runs a real mount(8) against the host
  "Backup Database",    // writes a DB dump to a host path
  "bkRunNow",           // starts a backup of every user's netdisk
  "Save Schedule",      // rewrites the daily backup schedule
  "Save Feishu",        // rewrites SSO credentials
  "saveMountConfig",    // rewrites the stored mount config
  "Create Group",       // covered explicitly in the users test
  "Create User",        // covered explicitly in the users test
  "Save",               // the language-settings Save reloads the page on success
];

// installPage wires the three things every test in this suite needs:
//   * a collector for JS exceptions and 5xx responses,
//   * a native-dialog handler (confirm/prompt) that defaults to "cancel", so a
//     stray click on a Delete button can never destroy anything,
//   * helpers to opt into accepting a dialog for the tests that mean it.
export function installPage(page) {
  const jsErrors = [];
  const serverErrors = [];
  // dialog holds the answer the next native dialog gets. `accept` is false by
  // default: destructive UI paths all go through confirm(), so cancelling makes
  // blind clicking safe.
  const dialog = { accept: false, promptText: "", seen: [] };

  page.on("pageerror", (err) => jsErrors.push(err.message));
  page.on("response", (resp) => {
    if (resp.status() >= 500) serverErrors.push(`${resp.status()} ${resp.request().method()} ${resp.url()}`);
  });
  page.on("dialog", async (d) => {
    dialog.seen.push(d.message());
    if (dialog.accept) await d.accept(dialog.promptText);
    else await d.dismiss();
  });

  return {
    jsErrors,
    serverErrors,
    dialog,
    // withConfirm runs fn with native dialogs auto-accepted (optionally typing
    // `text` into a prompt), then restores the cancel-by-default behaviour.
    withConfirm: async (fn, text = "") => {
      dialog.accept = true;
      dialog.promptText = text;
      try {
        return await fn();
      } finally {
        dialog.accept = false;
        dialog.promptText = "";
      }
    },
    // forgetServerError removes any already-recorded 5xx whose URL contains
    // `urlFragment` — for the rare action (e.g. "test this registry login")
    // where a 5xx is the correct, expected outcome rather than a bug. Call it
    // only after awaiting that specific response (e.g. via page.waitForResponse)
    // so it does not race the response-recording listener.
    forgetServerError: (urlFragment) => {
      for (let i = serverErrors.length - 1; i >= 0; i--) {
        if (serverErrors[i].includes(urlFragment)) serverErrors.splice(i, 1);
      }
    },
    // assertClean fails the test with the collected diagnostics attached.
    assertClean: (context = "") => {
      expect(jsErrors, `JS errors${context ? " during " + context : ""}`).toEqual([]);
      expect(serverErrors, `5xx responses${context ? " during " + context : ""}`).toEqual([]);
    },
  };
}

export async function login(page, username, password) {
  await page.goto("/");
  await expect(page.locator("form.auth-card")).toBeVisible();
  await page.fill("input[name='username']", username);
  await page.fill("input[name='password']", password);
  await fillCaptcha(page);
  await page.click("form.auth-card .auth-submit");
  // The shell only appears once /api/me + refreshAll() resolve; refreshAll fans
  // out 6–9 Docker-backed calls (containers, images, dashboard, …) that, on
  // Docker Desktop for Windows under load, can each take several seconds. The
  // earlier 45s ceiling was intermittently too tight and surfaced as a flaky
  // "aside nav not visible" timeout deep into the netdisk tests.
  await expect(page.locator("aside nav")).toBeVisible({ timeout: 90000 });
}

// Login requires the GIF captcha. The e2e server runs with
// MUDP_CAPTCHA_TEST_ANSWERS=1, so the answer rides the response headers the
// SPA itself reads. The form must submit the answer for the challenge IT is
// showing, so we click the image to refresh and capture that exact response —
// fetching a separate captcha would desynchronize id and answer.
export async function fillCaptcha(page) {
  const [resp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes("/api/captcha")),
    page.click(".captcha-img"),
  ]);
  const answer = resp.headers()["x-mudp-captcha-answer"];
  if (!answer) throw new Error("captcha answer header missing; is MUDP_CAPTCHA_TEST_ANSWERS set for the e2e server?");
  await page.fill("input[name='captcha']", answer);
}

export async function logout(page) {
  await page.click("aside .profile button");
  await expect(page.locator("form.auth-card")).toBeVisible({ timeout: 15000 });
}

// openTab clicks a sidebar entry and waits for the view to paint. Tabs are
// addressed by their data-tab key so the assertions survive i18n changes.
export async function openTab(page, tab) {
  await page.click(`nav button[data-tab="${tab}"]`);
  await expect(page.locator(`nav button[data-tab="${tab}"]`)).toHaveClass(/active/);
  await expect(page.locator(".app-main")).not.toBeEmpty({ timeout: 30000 });
  // Self-fetching views (netdisk, usage, disks, hardware) paint asynchronously.
  await page.waitForTimeout(250);
}

export function modal(page) {
  return page.locator(".el-dialog__wrapper:visible .el-dialog").last();
}

// closeModals dismisses every open modal. Esc is tried first (ui.js binds it
// globally), but some panels — the terminal in particular — hand keyboard
// focus to an embedded widget (xterm.js) that swallows the keypress before it
// reaches document, so each [data-close] button is clicked as a fallback.
export async function closeModals(page) {
  for (let i = 0; i < 8; i++) {
    if ((await page.locator(".el-dialog__wrapper:visible, .el-message-box__wrapper:visible").count()) === 0) return;
    await page.keyboard.press("Escape");
    await page.waitForTimeout(150);
    if ((await page.locator(".el-dialog__wrapper:visible, .el-message-box__wrapper:visible").count()) === 0) return;
    const closeBtn = page.locator(".el-dialog__wrapper:visible .el-dialog__headerbtn, .el-message-box__wrapper:visible .el-message-box__headerbtn").last();
    if ((await closeBtn.count()) > 0 && (await closeBtn.isVisible())) {
      await closeBtn.click();
      await page.waitForTimeout(150);
    }
  }
  await expect(page.locator(".el-dialog__wrapper:visible, .el-message-box__wrapper:visible")).toHaveCount(0);
}

// describeButtons snapshots the clickable buttons inside a scope, keeping a
// stable identity (id, then title, then text) for each so the crawler can find
// them again after a click re-renders the view.
export async function describeButtons(page, scope = ".app-main") {
  return page.$$eval(
    `${scope} button`,
    (els) =>
      els
        .filter((e) => !e.disabled && e.offsetParent !== null)
        .map((e) => ({
          id: e.id || "",
          title: e.getAttribute("title") || "",
          text: (e.textContent || "").trim().slice(0, 60),
        })),
  );
}

// clickEveryButton clicks each button in a scope and closes whatever it opened.
// Native confirms stay cancelled (see installPage), so destructive buttons are
// exercised up to — but never past — their confirmation.
//
// Returns the labels actually clicked, so a test can assert it covered the page.
export async function clickEveryButton(page, { scope = ".app-main", skip = [] } = {}) {
  const buttons = await describeButtons(page, scope);
  const clicked = [];
  for (const b of buttons) {
    const label = b.id || b.title || b.text;
    if (!label) continue;
    if (skip.some((s) => label.includes(s))) continue;

    const locator = b.id
      ? page.locator(`#${b.id}`)
      : b.title
        ? page.locator(`${scope} button[title="${b.title}"]`).first()
        : page.locator(`${scope} button`, { hasText: b.text }).first();

    // The button may have vanished because an earlier click re-rendered the
    // view; that is not a failure, just skip it.
    if ((await locator.count()) === 0) continue;
    if (!(await locator.first().isVisible())) continue;
    if (!(await locator.first().isEnabled())) continue;

    await locator.first().click();
    await page.waitForTimeout(200);
    await closeModals(page);
    clicked.push(label);
  }
  return clicked;
}

// toastText waits for the transient toast banner and returns its text.
export async function toastText(page) {
  const toast = page.locator(".el-message").last();
  await expect(toast).toBeVisible({ timeout: 15000 });
  return (await toast.textContent()) || "";
}

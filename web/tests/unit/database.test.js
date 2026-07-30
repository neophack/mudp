// @vitest-environment jsdom
// Coverage for the admin Database page: it renders the per-table breakdown
// from /api/admin/db/usage and posts a prune request with the chosen age when
// an admin cleans a log table. The allow-list of which tables carry a "clean"
// button is server-supplied, so the test also guards that user tables are shown
// without one.

import { describe, it, expect, vi, beforeEach } from "vitest";

const apiMock = vi.fn();

vi.mock("../../app.js", () => ({
  state: {},
  api: (...args) => apiMock(...args),
  toast: vi.fn(),
  escapeHtml: (s) => String(s ?? ""),
  fmtBytes: (n) => `${n}B`,
  t: (key, params) => {
    if (typeof params === "object" && params !== null) {
      let out = key;
      for (const [k, v] of Object.entries(params)) out = out.replace(`{${k}}`, v);
      return out;
    }
    return key;
  },
}));

vi.mock("../../modules/ui.js", () => ({
  showModal: vi.fn(({ body, foot }) => {
    document.querySelectorAll(".modal-backdrop").forEach((el) => el.remove());
    const wrap = document.createElement("div");
    wrap.className = "modal-backdrop";
    wrap.innerHTML = `<div class="modal-body">${body}</div>` + (foot ? `<div class="modal-foot">${foot}</div>` : "");
    document.body.appendChild(wrap);
  }),
  closeModal: vi.fn(() => {
    document.querySelectorAll(".modal-backdrop").forEach((el) => el.remove());
  }),
}));

const { state } = await import("../../app.js");
const { renderDatabase } = await import("../../modules/database.js");

beforeEach(() => {
  apiMock.mockReset();
  state.dbUsage = null;
  document.body.innerHTML = `<div id="view"></div>`;
});

describe("renderDatabase", () => {
  it("renders tables and only offers Clean on pruneable log tables", async () => {
    apiMock.mockResolvedValueOnce({
      report: {
        fileBytes: 4096,
        walBytes: 0,
        shmBytes: 0,
        freePages: 3,
        byteSizes: true,
        tables: [
          { name: "audit_logs", bytes: 2048, rows: 42, description: "管理操作审计日志" },
          { name: "users", bytes: 1024, rows: 5, description: "用户账号" },
        ],
      },
      prunable: ["audit_logs"],
    });
    await renderDatabase();

    const html = document.querySelector("#view").innerHTML;
    // A log table gets a Clean button.
    expect(html).toContain("audit_logs");
    expect(html).toMatch(/data-db-prune="audit_logs"/);
    // A user table is shown for visibility but has no Clean control.
    expect(html).toContain("users");
    expect(html).not.toMatch(/data-db-prune="users"/);
    // The Purpose column renders each table's backend-supplied description.
    expect(html).toContain("database.colPurpose");
    expect(html).toContain("管理操作审计日志");
    expect(html).toContain("用户账号");
    // Summary tiles render the file size and free-page count.
    expect(html).toContain("database.dbFile");
    expect(html).toContain("3");
  });

  it("posts the chosen age range when cleaning a table", async () => {
    apiMock.mockResolvedValueOnce({
      report: { fileBytes: 0, walBytes: 0, shmBytes: 0, freePages: 0, byteSizes: false, tables: [{ name: "access_logs", bytes: 0, rows: 9 }] },
      prunable: ["access_logs"],
    });
    apiMock.mockResolvedValueOnce({ deleted: { access_logs: 9 } });
    apiMock.mockResolvedValueOnce({ report: { tables: [] }, prunable: [] }); // re-render fetch
    await renderDatabase();

    document.querySelector('[data-db-prune="access_logs"]').click();
    // The prune modal is now open; pick "older than 30 days" and confirm.
    const daysSel = document.querySelector('[name="days"]');
    daysSel.value = "30";
    document.querySelector("#dbPruneConfirm").click();
    // Let the async handler run.
    await new Promise((r) => setTimeout(r, 0));

    const pruneCall = apiMock.mock.calls.find((c) => c[0] === "/api/admin/db/prune");
    expect(pruneCall).toBeTruthy();
    expect(pruneCall[1]).toMatchObject({ method: "POST" });
    expect(JSON.parse(pruneCall[1].body)).toMatchObject({ tables: ["access_logs"], olderThanDays: 30 });
  });
});

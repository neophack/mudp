// @vitest-environment jsdom
// Regression coverage for the notification bell badge: marking a notification
// read (singly, all-at-once, or via delete) must clear the header bell dot
// immediately, without waiting for the next background poll. Prior to this
// fix, the modal handlers refetched state but never called
// updateNotificationBadge(), so the bell stayed lit until the next
// auto-refresh tick or a full tab-switch re-render.

import { describe, it, expect, vi, beforeEach } from "vitest";

const apiMock = vi.fn();

vi.mock("../../app.js", () => ({
  state: { notifications: [], unreadCount: 0, me: { username: "admin", role: "admin" } },
  api: (...args) => apiMock(...args),
  toast: vi.fn(),
  escapeHtml: (s) => String(s ?? ""),
  displayNameForUsername: (u) => u,
  render: vi.fn(),
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
const { renderNotificationBell, openNotificationsModal } = await import("../../modules/notifications.js");

function unreadItem(id) {
  return { id, type: "system_alert", title: `Alert ${id}`, message: `msg ${id}`, read: false, createdAt: new Date().toISOString() };
}

function bellDot() {
  return document.querySelector("#notificationBell .notification-dot");
}

beforeEach(() => {
  apiMock.mockReset();
  state.notifications = [unreadItem(1)];
  state.unreadCount = 1;
  document.body.innerHTML = renderNotificationBell();
});

describe("notification bell badge", () => {
  it("clears as soon as a single notification is marked read", async () => {
    expect(bellDot()).not.toBeNull();

    apiMock.mockImplementation(async (path) => {
      if (path === "/api/notifications/read") {
        state.notifications = state.notifications.map((n) => ({ ...n, read: true }));
        return { ok: true };
      }
      if (path === "/api/notifications") {
        return { notifications: state.notifications, unreadCount: 0 };
      }
      throw new Error(`unexpected call: ${path}`);
    });

    openNotificationsModal();
    await document.querySelector('[data-notification-id="1"]').onclick();

    expect(bellDot()).toBeNull();
  });

  it("clears when all notifications are marked read at once", async () => {
    state.notifications = [unreadItem(1), unreadItem(2)];
    state.unreadCount = 2;
    expect(bellDot()).not.toBeNull();

    apiMock.mockImplementation(async (path) => {
      if (path === "/api/notifications/read") {
        state.notifications = state.notifications.map((n) => ({ ...n, read: true }));
        return { ok: true };
      }
      if (path === "/api/notifications") {
        return { notifications: state.notifications, unreadCount: 0 };
      }
      throw new Error(`unexpected call: ${path}`);
    });

    openNotificationsModal();
    await document.querySelector("#markAllRead").onclick();

    expect(bellDot()).toBeNull();
  });

  it("clears when the last unread notification is deleted", async () => {
    apiMock.mockImplementation(async (path) => {
      if (path === "/api/notifications/delete") {
        state.notifications = [];
        return { ok: true };
      }
      if (path === "/api/notifications") {
        return { notifications: state.notifications, unreadCount: 0 };
      }
      throw new Error(`unexpected call: ${path}`);
    });

    openNotificationsModal();
    await document.querySelector('[data-notification-delete="1"]').onclick({ stopPropagation() {} });

    expect(bellDot()).toBeNull();
  });
});

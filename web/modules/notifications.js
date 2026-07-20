// In-app notification bell + list modal.

import { state, api, toast, escapeHtml, displayNameForUsername, render } from "../app.js";
import { showModal, closeModal } from "./ui.js";

const ICONS = {
  pending_user: `<circle cx="12" cy="8" r="4"/><path d="M4 20c0-4 4-6 8-6s8 2 8 6"/>`,
  user_approved: `<path d="M20 6 9 17l-5-5"/>`,
  user_deactivated: `<path d="M18 6 6 18"/><path d="M6 6l12 12"/>`,
  system_alert: `<path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><path d="M12 9v4"/><path d="M12 17h.01"/>`,
};

export function renderNotificationBell() {
  const count = state.unreadCount || 0;
  return (
    `<button class="icon notification-bell" id="notificationBell" title="Notifications">` +
      `<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` +
        `<path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/>` +
        `<path d="M10.3 21a1.94 1.94 0 0 0 3.4 0"/>` +
      `</svg>` +
      (count > 0 ? `<span class="notification-dot">${count > 99 ? "99+" : count}</span>` : "") +
    `</button>`
  );
}

export async function fetchNotifications() {
  try {
    const res = await api("/api/notifications");
    state.notifications = res.notifications || [];
    state.unreadCount = res.unreadCount || 0;
  } catch {
    state.notifications = [];
    state.unreadCount = 0;
  }
}

// updateNotificationBadge refreshes only the bell's unread-count badge in
// place, so the auto-refresh loop can surface new notifications without a
// full shell re-render.
export function updateNotificationBadge() {
  const bell = document.querySelector("#notificationBell");
  if (!bell) return;
  const count = state.unreadCount || 0;
  let dot = bell.querySelector(".notification-dot");
  if (count > 0) {
    if (!dot) {
      dot = document.createElement("span");
      dot.className = "notification-dot";
      bell.appendChild(dot);
    }
    dot.textContent = count > 99 ? "99+" : String(count);
  } else if (dot) {
    dot.remove();
  }
}

export function openNotificationsModal() {
  const items = state.notifications || [];
  const body = items.length
    ? `<div class="notification-list">${items.map(notificationRow).join("")}</div>`
    : `<div class="empty-state">No notifications yet.</div>`;
  showModal({
    kind: "notifications",
    title: `Notifications`,
    body,
    foot:
      `<button class="ghost" data-close>Close</button>` +
      (items.length
        ? `<button class="ghost" id="markAllRead">Mark all read</button><button class="ghost danger" id="clearNotifications">Clear list</button>`
        : ""),
  });
  document.querySelectorAll("[data-notification-id]").forEach((row) => {
    row.onclick = async () => {
      const id = Number(row.dataset.notificationId);
      const type = row.dataset.notificationType || "";
      // Pending-user notifications should take the admin straight to the Users &
      // Groups page where they can approve the new user.
      if (type === "pending_user") {
        closeModal();
        state.tab = "users";
        render();
      }
      if (!row.classList.contains("read")) {
        try {
          await api("/api/notifications/read", { method: "POST", body: JSON.stringify({ ids: [id] }) });
          await fetchNotifications();
          // Reopen the modal only if we did not navigate away.
          if (type !== "pending_user") {
            openNotificationsModal();
          }
        } catch (err) {
          toast(err.message);
        }
      }
    };
  });
  document.querySelectorAll("[data-notification-delete]").forEach((btn) => {
    btn.onclick = async (e) => {
      e.stopPropagation();
      const id = Number(btn.dataset.notificationDelete);
      try {
        await api("/api/notifications/delete", { method: "POST", body: JSON.stringify({ ids: [id] }) });
        await fetchNotifications();
        openNotificationsModal();
      } catch (err) {
        toast(err.message);
      }
    };
  });
  const markAll = $("#markAllRead");
  if (markAll) {
    markAll.onclick = async () => {
      try {
        await api("/api/notifications/read", { method: "POST", body: JSON.stringify({ all: true }) });
        await fetchNotifications();
        closeModal();
      } catch (err) {
        toast(err.message);
      }
    };
  }
  const clearBtn = $("#clearNotifications");
  if (clearBtn) {
    clearBtn.onclick = async () => {
      try {
        await api("/api/notifications/delete", { method: "POST", body: JSON.stringify({ all: true }) });
        await fetchNotifications();
        closeModal();
      } catch (err) {
        toast(err.message);
      }
    };
  }
}

function notificationRow(n) {
  const icon = ICONS[n.type] || ICONS.system_alert;
  const when = new Date(n.createdAt).toLocaleString();
  const data = n.data || {};
  let detail = "";
  if (n.type === "pending_user" && data.username) {
    detail = `User: ${escapeHtml(displayNameForUsername(data.username))}`;
  } else if (n.type === "user_approved" && data.groups) {
    detail = `Groups: ${escapeHtml((data.groups || []).join(", "))}`;
  }
  return (
    `<div class="notification-item ${n.read ? "read" : "unread"}" data-notification-id="${n.id}" data-notification-type="${escapeHtml(n.type)}">` +
      `<div class="notification-icon"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${icon}</svg></div>` +
      `<div class="notification-body">` +
        `<div class="notification-title">${escapeHtml(n.title)}</div>` +
        `<div class="notification-message">${escapeHtml(n.message)}</div>` +
        (detail ? `<div class="notification-detail hint">${detail}</div>` : "") +
        `<div class="notification-time hint">${escapeHtml(when)}</div>` +
      `</div>` +
      `<button class="icon notification-delete" title="Delete" data-notification-delete="${n.id}">✕</button>` +
    `</div>`
  );
}

function $(selector) {
  return document.querySelector(selector);
}

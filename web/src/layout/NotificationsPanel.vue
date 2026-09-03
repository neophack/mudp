<template>
  <el-dialog :model-value="visible" :title="tt('notif.title')" width="520px" append-to-body @update:model-value="$emit('update:visible', $event)">
    <div v-if="!items.length" class="empty-state">{{ tt("notif.noNotifications") }}</div>
    <div v-else class="notification-list">
      <div
        v-for="n in items"
        :key="n.id"
        class="notification-item"
        :class="n.read ? 'read' : 'unread'"
        @click="open(n)"
      >
        <div class="notification-icon"><v-icon :name="n.type || 'system_alert'" /></div>
        <div class="notification-body">
          <div class="notification-title">{{ n.title }}</div>
          <div class="notification-message">{{ n.message }}</div>
          <div v-if="detail(n)" class="notification-detail hint">{{ detail(n) }}</div>
          <div class="notification-time hint">{{ new Date(n.createdAt).toLocaleString() }}</div>
        </div>
        <el-button link class="danger-text" icon="Close" :title="tt('notif.delete')" @click.stop="removeOne(n.id)" />
      </div>
    </div>
    <template #footer>
      <el-button @click="$emit('update:visible', false)">{{ tt("common.close") }}</el-button>
      <el-button v-if="items.length" @click="markAllRead">{{ tt("notif.markAllRead") }}</el-button>
      <el-button v-if="items.length" type="danger" plain @click="clearAll">{{ tt("notif.clearList") }}</el-button>
    </template>
  </el-dialog>
</template>

<script>
import { ElMessage } from "element-plus";
import { api } from "@/api";
import { store, fetchNotifications, displayNameForUsername } from "@/store";
import { tt } from "@/i18n";
import VIcon from "@/components/VIcon.vue";

export default {
  name: "NotificationsPanel",
  components: { VIcon },
  props: {
    visible: { type: Boolean, default: false },
  },
  computed: {
    items() {
      return store.notifications || [];
    },
  },
  methods: {
    tt,
    // Pending-user notifications take the admin straight to the Users &
    // Groups page where they can approve the new user.
    open(n) {
      if (n.type === "pending_user") {
        this.$emit("update:visible", false);
        this.$router.push("/users");
      }
      if (!n.read) {
        api("/api/notifications/read", { method: "POST", body: JSON.stringify({ ids: [n.id] }) })
          .then(fetchNotifications)
          .catch((err) => ElMessage.error(err.message));
      }
    },
    detail(n) {
      const data = n.data || {};
      if (n.type === "pending_user" && data.username) return `User: ${displayNameForUsername(data.username)}`;
      if (n.type === "user_approved" && data.group) return `Group: ${data.group}`;
      return "";
    },
    removeOne(id) {
      api("/api/notifications/delete", { method: "POST", body: JSON.stringify({ ids: [id] }) })
        .then(fetchNotifications)
        .catch((err) => ElMessage.error(err.message));
    },
    markAllRead() {
      api("/api/notifications/read", { method: "POST", body: JSON.stringify({ all: true }) })
        .then(() => {
          fetchNotifications();
          this.$emit("update:visible", false);
        })
        .catch((err) => ElMessage.error(err.message));
    },
    clearAll() {
      api("/api/notifications/delete", { method: "POST", body: JSON.stringify({ all: true }) })
        .then(() => {
          fetchNotifications();
          this.$emit("update:visible", false);
        })
        .catch((err) => ElMessage.error(err.message));
    },
  },
};
</script>

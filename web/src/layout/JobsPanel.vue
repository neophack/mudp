<template>
  <el-dialog :model-value="visible" :title="tt('jobs.title')" width="560px" append-to-body @update:model-value="$emit('update:visible', $event)">
    <div v-if="!jobs.length" class="empty-state">{{ tt("jobs.noJobs") }}</div>
    <div v-else class="job-list">
      <div v-for="job in sortedJobs" :key="job.id" class="job-item" :class="job.active ? 'job-active' : 'job-inactive'">
        <div class="job-icon"><v-icon :name="job.kind" /></div>
        <div class="job-body">
          <div class="job-headline">
            <span class="job-name" :title="job.name">{{ job.name }}</span>
            <el-tag size="small" :type="statusTag(job.status)">{{ statusText(job.status) }}</el-tag>
          </div>
          <div class="job-meta">
            <span class="job-kind">{{ kindLabel(job.kind) }}</span>
            <span class="job-time">{{ elapsed(job.startedAt) }}</span>
          </div>
          <div class="job-message hint">{{ truncate(job.message, 140) }}</div>
          <el-progress v-if="job.active && typeof job.progress === 'number' && job.total > 0" :percentage="Math.min(100, job.progress)" :stroke-width="4" :show-text="false" />
        </div>
        <div class="job-actions">
          <el-button v-if="!job.active" link class="danger-text" icon="Delete" :title="tt('jobs.removeTitle')" @click="removeJob(job.id)" />
          <el-button v-else-if="job.cancellable !== false" link class="warn-text" :title="tt('jobs.stopTitle')" @click="cancelJob(job.id)">{{ tt("jobs.stop") }}</el-button>
        </div>
      </div>
    </div>
    <template #footer>
      <el-button @click="$emit('update:visible', false)">{{ tt("common.close") }}</el-button>
      <el-button v-if="jobs.some((j) => !j.active)" @click="clearCompletedJobs">{{ tt("jobs.clearCompleted") }}</el-button>
    </template>
  </el-dialog>
</template>

<script>
import { store } from "@/store";
import { tt } from "@/i18n";
import { cancelJob, removeJob, clearCompletedJobs, KIND_LABEL } from "@/jobs";
import VIcon from "@/components/VIcon.vue";

const STATUS_KEY = {
  running: "jobs.msgRunning",
  done: "jobs.msgCompleted",
  error: "jobs.msgFailed",
  cancelled: "jobs.msgCancelled",
};

export default {
  name: "JobsPanel",
  components: { VIcon },
  props: {
    visible: { type: Boolean, default: false },
  },
  computed: {
    jobs() {
      return store.jobs || [];
    },
    sortedJobs() {
      return this.jobs.slice().sort((a, b) => b.startedAt - a.startedAt);
    },
  },
  methods: {
    tt,
    cancelJob,
    removeJob,
    clearCompletedJobs,
    kindLabel(kind) {
      const key = KIND_LABEL[kind];
      return key ? tt(key) : kind;
    },
    statusTag(status) {
      return status === "running" ? "warning" : status === "done" ? "success" : status === "error" ? "danger" : "info";
    },
    statusText(status) {
      const key = STATUS_KEY[status];
      return key ? tt(key) : status[0].toUpperCase() + status.slice(1);
    },
    elapsed(startedAt) {
      const totalSeconds = Math.floor((Date.now() - startedAt) / 1000);
      if (totalSeconds < 60) return `${totalSeconds}s`;
      const minutes = Math.floor(totalSeconds / 60);
      const seconds = totalSeconds % 60;
      if (minutes < 60) return `${minutes}m ${seconds}s`;
      const hours = Math.floor(minutes / 60);
      return `${hours}h ${minutes % 60}m`;
    },
    truncate(text, max) {
      if (!text) return "—";
      return text.length <= max ? text : text.slice(0, max - 1) + "…";
    },
  },
};
</script>

<template>
  <el-drawer
    :model-value="visible"
    direction="btt"
    size="auto"
    :with-header="false"
    append-to-body
    class="action-sheet"
    @update:model-value="$emit('update:visible', $event)"
  >
    <div class="sheet-card">
      <div class="sheet-grab"></div>
      <div class="sheet-head">
        <strong class="sheet-title">{{ title }}</strong>
        <span v-if="subtitle" class="sheet-subtitle">{{ subtitle }}</span>
      </div>
      <slot name="meta"></slot>
      <div class="sheet-grid" :style="gridStyle">
        <button
          v-for="item in items"
          :key="item.key"
          type="button"
          class="sheet-btn"
          :class="{ danger: item.danger }"
          :disabled="item.disabled"
          @click="$emit('select', item)"
        >
          <el-icon :size="20"><component :is="item.icon" /></el-icon>
          <span class="sheet-btn-label">{{ item.label }}</span>
        </button>
      </div>
    </div>
  </el-drawer>
</template>

<script>
// Bottom action sheet for phone-width list rows: the table hides its wide
// action column and a row tap opens this sheet instead. Items are
// { key, label, icon (Element Plus icon name), danger?, disabled? }.
export default {
  name: "ActionSheet",
  props: {
    visible: { type: Boolean, default: false },
    title: { type: String, default: "" },
    subtitle: { type: String, default: "" },
    items: { type: Array, default: () => [] },
    columns: { type: Number, default: 4 },
  },
  emits: ["update:visible", "select"],
  computed: {
    gridStyle() {
      return { gridTemplateColumns: `repeat(${this.columns}, minmax(0, 1fr))` };
    },
  },
};
</script>

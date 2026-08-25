<template>
  <div ref="el" class="echart-box" :style="{ height, width }"></div>
</template>

<script>
// Thin ECharts wrapper: mounts a chart into the div, applies option updates
// in place, and keeps the canvas sized to its container via ResizeObserver.
import * as echarts from "echarts";

export default {
  name: "EChart",
  props: {
    option: { type: Object, required: true },
    height: { type: String, default: "300px" },
    width: { type: String, default: "100%" },
    loading: { type: Boolean, default: false },
  },
  watch: {
    option: {
      deep: true,
      handler(v) {
        if (this.chart) this.chart.setOption(v, { notMerge: true });
      },
    },
    loading(v) {
      if (!this.chart) return;
      if (v) this.chart.showLoading();
      else this.chart.hideLoading();
    },
  },
  mounted() {
    this.chart = echarts.init(this.$refs.el);
    this.chart.setOption(this.option, { notMerge: true });
    if (this.loading) this.chart.showLoading();
    this.ro = new ResizeObserver(() => this.chart && this.chart.resize());
    this.ro.observe(this.$refs.el);
  },
  beforeUnmount() {
    if (this.ro) this.ro.disconnect();
    if (this.chart) {
      this.chart.dispose();
      this.chart = null;
    }
  },
};
</script>

<style scoped>
.echart-box { min-height: 120px; }
</style>

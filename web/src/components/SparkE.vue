<template>
  <!-- Compact ECharts line used for live sparklines (stats dialog, usage
       trends, GPU/sensor history). Axis-free by design; hovering shows the
       sample value through an axis tooltip. -->
  <div ref="el" class="spark-box" :style="{ height }"></div>
</template>

<script>
import * as echarts from "echarts";

export default {
  name: "SparkE",
  props: {
    // Y values; the x axis is implicit sample order.
    series: { type: Array, default: () => [] },
    color: { type: String, default: "#3370ff" },
    height: { type: String, default: "28px" },
    // autoscale: keep 0 as the floor (percentages) instead of the data minimum.
    autoscale: { type: Boolean, default: false },
  },
  computed: {
    option() {
      const data = this.series || [];
      return {
        animation: false,
        grid: { left: 0, right: 0, top: 3, bottom: 3 },
        xAxis: { type: "category", show: false, data: data.map((_, i) => i) },
        yAxis: {
          type: "value",
          show: false,
          scale: !this.autoscale && data.length > 1,
          min: this.autoscale ? 0 : undefined,
        },
        tooltip: {
          trigger: "axis",
          formatter: (params) => {
            const p = params[0];
            const v = Number(p.value);
            return `${typeof v === "number" && Number.isFinite(v) ? Math.round(v * 10) / 10 : v}`;
          },
          confine: true,
          textStyle: { fontSize: 11 },
        },
        series: [{
          type: "line",
          data,
          showSymbol: false,
          smooth: false,
          lineStyle: { width: 1.5, color: this.color },
          itemStyle: { color: this.color },
          areaStyle: { opacity: 0.12, color: this.color },
        }],
      };
    },
  },
  watch: {
    option: {
      deep: true,
      handler(v) {
        if (this.chart) this.chart.setOption(v);
      },
    },
  },
  mounted() {
    this.chart = echarts.init(this.$refs.el);
    this.chart.setOption(this.option);
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
.spark-box { width: 100%; min-height: 0; }
</style>

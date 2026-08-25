<template>
  <div ref="el" class="world-map" :style="{ height }"></div>
</template>

<script>
// ECharts world map, modelled on the geo + scatter + visualMap layout: a geo
// component with roam, one or more scatter series pinned to geoIndex 0 whose
// [lon, lat, count] rows drive a horizontal calculable visualMap scaling
// symbolSize, and (optionally) a map series sharing the geo for a choropleth.
// The country-polygon geojson is fetched once and registered as "world".
import * as echarts from "echarts";

let worldPromise = null;

function ensureWorld() {
  if (!worldPromise) {
    worldPromise = fetch("/world-echarts.json")
      .then((r) => {
        if (!r.ok) throw new Error("world map data unavailable");
        return r.json();
      })
      .then((geoJSON) => {
        echarts.registerMap("world", geoJSON);
      });
  }
  return worldPromise;
}

const AREA_STYLE = {
  areaColor: "#e8edf3",
  borderColor: "#94a3b8",
  borderWidth: 0.5,
};
const EMPHASIS_STYLE = {
  areaColor: "#dbe4ee",
  shadowBlur: 6,
  shadowColor: "rgba(0, 0, 0, 0.2)",
};

// Builds the scatter series entry for one class of points.
function scatterSeries(points, color, borderColor) {
  const data = points
    .filter((p) => p.latitude != null && p.longitude != null)
    .map((p) => [Number(p.longitude), Number(p.latitude), Number(p.count || 0), p]);
  return {
    type: "scatter",
    coordinateSystem: "geo",
    geoIndex: 0,
    encode: {
      // `2` is the dimension index of the count in series.data.
      tooltip: 2,
      label: 2,
    },
    data,
    symbolSize: () => 10,
    itemStyle: {
      color,
      borderWidth: 1,
      borderColor,
      shadowBlur: 6,
      shadowColor: "rgba(0, 0, 0, 0.25)",
    },
  };
}

export default {
  name: "WorldMap",
  props: {
    // [{ latitude, longitude, count, label, city, country, ip, kind, severity }]
    points: { type: Array, default: () => [] },
    // "single": one brand-blue series. "mcp": access green + attack
    // yellow/red series split by kind/severity.
    mode: { type: String, default: "single" },
    // tooltipHtml(point) supplies the hover body for a point.
    tooltipHtml: { type: Function, default: null },
    // Optional choropleth input: [{ name, value }] mapped onto the countries.
    countryData: { type: Array, default: null },
    height: { type: String, default: "480px" },
  },
  data() {
    return { chart: null, ready: false };
  },
  computed: {
    seriesGroups() {
      if (this.mode === "mcp") {
        return [
          { points: this.points.filter((p) => p.kind === "access"), color: "#22c55e", border: "#166534" },
          { points: this.points.filter((p) => p.kind !== "access" && (p.severity || 0) < 2), color: "#f59e0b", border: "#92400e" },
          { points: this.points.filter((p) => p.kind !== "access" && (p.severity || 0) >= 2), color: "#ef4444", border: "#991b1b" },
        ].filter((g) => g.points.length);
      }
      return [{ points: this.points, color: "#3882ff", border: "#1d4ed8" }];
    },
    maxCount() {
      return this.points.reduce((m, p) => Math.max(m, p.count || 0), 0) || 1;
    },
  },
  watch: {
    points() {
      this.apply();
    },
    countryData() {
      this.apply();
    },
  },
  async mounted() {
    this.chart = echarts.init(this.$refs.el);
    this.chart.showLoading();
    try {
      await ensureWorld();
      this.ready = true;
      this.apply();
    } finally {
      this.chart.hideLoading();
    }
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
  methods: {
    apply() {
      if (!this.chart || !this.ready) return;
      const groups = this.seriesGroups;
      const series = groups.map((g) => scatterSeries(g.points, g.color, g.border));
      const visualMap = [];
      // One horizontal calculable visualMap per series, scaling symbolSize by
      // the count dimension (0..max across all points).
      groups.forEach((g, i) => {
        const gMax = g.points.reduce((m, p) => Math.max(m, p.count || 0), 0) || this.maxCount;
        visualMap.push({
          orient: "horizontal",
          calculable: true,
          right: i === 0 ? 10 : undefined,
          left: i === 1 ? 10 : undefined,
          bottom: 10 - i * 34,
          seriesIndex: i,
          min: 0,
          max: gMax,
          dimension: 2,
          text: [String(gMax), "0"],
          inRange: { symbolSize: [6, 26] },
          controller: { inRange: { color: [g.color] } },
          formatter: (v) => String(Math.round(v)),
        });
      });
      // A choropleth (map series sharing the geo component) only makes sense
      // when the caller supplied per-country values.
      if (this.countryData && this.countryData.length) {
        series.push({
          type: "map",
          geoIndex: 0,
          map: "",
          data: this.countryData.map((c) => ({ name: c.name, value: c.value })),
        });
        const cMax = this.countryData.reduce((m, c) => Math.max(m, c.value || 0), 0) || 1;
        visualMap.push({
          orient: "horizontal",
          calculable: true,
          left: "center",
          bottom: 0,
          seriesIndex: series.length - 1,
          min: 0,
          max: cMax,
          dimension: 0,
          inRange: { color: ["#deebf7", "#3182bd"] },
        });
      }
      const tooltipHtml = this.tooltipHtml;
      this.chart.setOption({
        tooltip: {
          trigger: "item",
          backgroundColor: "rgba(15, 23, 42, 0.92)",
          borderWidth: 0,
          textStyle: { color: "#e2e8f0", fontSize: 12 },
          formatter: (params) => {
            if (params.seriesType !== "scatter") {
              const p = params.data || {};
              return p.name ? `${p.name}: ${p.value ?? "—"}` : "";
            }
            const point = params.data && params.data[3];
            if (!point) return "";
            if (tooltipHtml) return tooltipHtml(point);
            return `<strong>${point.label || point.city || ""}</strong><br>${point.count} events`;
          },
        },
        geo: {
          map: "world",
          roam: true,
          itemStyle: AREA_STYLE,
          emphasis: EMPHASIS_STYLE,
          label: { show: false },
          scaleLimit: { min: 0.8, max: 12 },
        },
        series,
        visualMap,
      }, { notMerge: true });
    },
  },
};
</script>

<style scoped>
.world-map { width: 100%; min-height: 300px; }
</style>

// Shared ECharts entry with on-demand registration: only the chart types and
// components this app actually uses are bundled, which cuts the echarts
// payload by more than half versus the full-package import. Always import
// echarts from here (never from "echarts" directly) so every component shares
// one registry — a chart type registered here renders everywhere.
import * as echarts from "echarts/core";
import { LineChart, PieChart, ScatterChart, MapChart } from "echarts/charts";
import {
  TooltipComponent,
  GridComponent,
  LegendComponent,
  GeoComponent,
  VisualMapComponent,
} from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";

echarts.use([
  LineChart,
  PieChart,
  ScatterChart,
  MapChart,
  TooltipComponent,
  GridComponent,
  LegendComponent,
  GeoComponent,
  VisualMapComponent,
  CanvasRenderer,
]);

export default echarts;

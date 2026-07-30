// @vitest-environment jsdom
// Position-correctness coverage for the shared world-map component. The map's
// only job that varies with geography is the lon/lat → pixel projection in
// makeProjector() and the dot placement in drawDots(); the basemap strokes are
// decorative. So these tests feed points for well-known cities and a few
// extremes, capture the canvas arcs the renderer emits, and assert each dot
// lands at the pixel the documented equirectangular contract demands:
//   lon [-180,180] → x [0,W], lat [85,-85] → y [0,H]   (north up).
//
// jsdom ships no <canvas> rasteriser (getContext returns null), so each test
// stubs getContext with a recording context that collects every arc() call.

import { describe, it, expect, beforeEach } from "vitest";
import { renderWorldMap } from "../../modules/worldmap.js";

// Canvas dimensions used by the Security / MCP pages (width/height attributes).
const W = 980;
const H = 480;

// makeMockCtx returns a 2D-context stand-in that records arc() centres. Only
// the methods renderWorldMap touches are implemented; the rest are no-ops.
function makeMockCtx() {
  const arcs = [];
  const ctx = {
    fillStyle: "",
    strokeStyle: "",
    lineJoin: "",
    lineCap: "",
    lineWidth: 0,
    clearRect: () => {},
    beginPath: () => {},
    moveTo: () => {},
    lineTo: () => {},
    stroke: () => {},
    fill: () => {},
    arc: (x, y, r) => arcs.push({ x, y, r }),
    createRadialGradient: () => ({ addColorStop: () => {} }),
  };
  return { ctx, arcs };
}

// Each point produces two arcs at the same centre (a wide glow and a solid
// core), so dedupe by rounded centre to get one entry per plotted point.
function dotCenters(arcs) {
  const seen = new Map();
  for (const a of arcs) {
    const key = `${a.x.toFixed(2)}|${a.y.toFixed(2)}`;
    if (!seen.has(key)) seen.set(key, { x: a.x, y: a.y, r: a.r });
  }
  return [...seen.values()];
}

// nearest finds the plotted dot closest to (x,y) and its distance in pixels.
function nearest(dots, x, y) {
  let best = null;
  let bestDist = Infinity;
  for (const d of dots) {
    const dist = Math.hypot(d.x - x, d.y - y);
    if (dist < bestDist) {
      bestDist = dist;
      best = d;
    }
  }
  return { dot: best, dist: bestDist };
}

// matchCities aligns a list of plotted dots to the cities, one-to-one, by
// snapping each dot to the city whose projected pixel it lands closest to.
// Because distinct cities project to distinct pixels, this recovers the label
// the canvas cannot carry (arc() only sees x/y/r, not the point's data).
function matchCities(dots, cities) {
  return cities.map((c) => {
    const [ex, ey] = expectedPixel(c.longitude, c.latitude);
    const { dot } = nearest(dots, ex, ey);
    return { ...c, dot };
  });
}

// expectedPixel independently implements the documented projection so a sign
// flip or wrong divisor in the module shows up as a mismatch. Written from the
// spec comment, not copied from the module.
function expectedPixel(lon, lat) {
  const maxLat = 85;
  const minLat = -85;
  return [
    ((lon + 180) / 360) * W, // lon [-180,180] → x [0,W]
    ((maxLat - lat) / (maxLat - minLat)) * H, // lat [85,-85] → y [0,H]
  ];
}

// makeCanvas builds a canvas with the recording context already wired in.
function makeCanvas() {
  const canvas = document.createElement("canvas");
  canvas.width = W;
  canvas.height = H;
  const { ctx, arcs } = makeMockCtx();
  canvas.getContext = () => ctx;
  return { canvas, arcs };
}

// render is a thin wrapper that draws points and returns the deduped dot list.
// tooltip is null so the hover handler (which would attach listeners) is skipped.
function render(points) {
  const { canvas, arcs } = makeCanvas();
  renderWorldMap(canvas, null, points);
  return dotCenters(arcs);
}

// Landmark cities spanning every quadrant, used across several assertions.
// count is set so the busiest point defines the size scale.
const CITIES = [
  { label: "New York", latitude: 40.71, longitude: -74.01, count: 50 },
  { label: "London", latitude: 51.51, longitude: -0.13, count: 40 },
  { label: "Tokyo", latitude: 35.68, longitude: 139.69, count: 80 },
  { label: "Sydney", latitude: -33.87, longitude: 151.21, count: 30 },
  { label: "São Paulo", latitude: -23.55, longitude: -46.63, count: 20 },
  { label: "Cairo", latitude: 30.04, longitude: 31.24, count: 10 },
  { label: "Nairobi", latitude: -1.29, longitude: 36.82, count: 5 },
];

beforeEach(() => {
  document.body.innerHTML = "";
});

describe("world map projection — extremes and centre", () => {
  // These four corners plus the two midlines are the strongest checks because
  // their expected pixels are simple exact fractions (0, 1/2, 1) — independent
  // of any floating arithmetic, so they catch inverted axes or wrong ranges.
  it("places the projection corners at the canvas edges", () => {
    const dots = render([
      { label: "west", latitude: 0, longitude: -180, count: 1 },
      { label: "east", latitude: 0, longitude: 180, count: 1 },
      { label: "north", latitude: 85, longitude: 0, count: 1 },
      { label: "south", latitude: -85, longitude: 0, count: 1 },
    ]);
    expect(dots).toHaveLength(4);

    // lon -180 → left edge (x=0), lon 180 → right edge (x=W).
    const xs = dots.map((d) => d.x).sort((a, b) => a - b);
    expect(xs[0]).toBeCloseTo(0, 0);
    expect(xs[3]).toBeCloseTo(W, 0);
    // lat 85 → top edge (y=0), lat -85 → bottom edge (y=H).
    const ys = dots.map((d) => d.y).sort((a, b) => a - b);
    expect(ys[0]).toBeCloseTo(0, 0);
    expect(ys[3]).toBeCloseTo(H, 0);
  });

  it("centres the prime meridian and the equator", () => {
    const dots = render([
      { label: "meridian", latitude: 0, longitude: 0, count: 1 },
    ]);
    expect(dots).toHaveLength(1);
    expect(dots[0].x).toBeCloseTo(W / 2, 0); // lon 0 → horizontal centre
    expect(dots[0].y).toBeCloseTo(H / 2, 0); // lat 0 → vertical centre
  });
});

describe("world map projection — known cities", () => {
  it("drops each city within 1px of its projected lon/lat", () => {
    const dots = render(CITIES);
    expect(dots).toHaveLength(CITIES.length);
    for (const c of CITIES) {
      const [ex, ey] = expectedPixel(c.longitude, c.latitude);
      const { dot, dist } = nearest(dots, ex, ey);
      expect(dot, `no dot near ${c.label}`).not.toBeNull();
      expect(dist, `${c.label} off by ${dist?.toFixed(2)}px`).toBeLessThan(1);
    }
  });

  // Independent of the exact formula: geography has an unambiguous left/right
  // and up/down ordering that any correct world map must preserve.
  it("orders cities west→east along x (Honolulu … Sydney)", () => {
    const set = [
      ...CITIES,
      { label: "Beijing", latitude: 39.9, longitude: 116.4, count: 60 },
      { label: "Honolulu", latitude: 21.31, longitude: -157.86, count: 15 },
    ];
    const dots = render(set);
    const matched = matchCities(dots, set);
    const x = (label) => matched.find((m) => m.label === label).dot.x;
    expect(x("Honolulu")).toBeLessThan(x("New York"));
    expect(x("New York")).toBeLessThan(x("London"));
    expect(x("London")).toBeLessThan(x("Cairo"));
    expect(x("Cairo")).toBeLessThan(x("Beijing"));
    expect(x("Beijing")).toBeLessThan(x("Tokyo"));
    expect(x("Tokyo")).toBeLessThan(x("Sydney"));
  });

  it("orders cities north→south along y (north is up)", () => {
    const set = [
      ...CITIES,
      { label: "Anchorage", latitude: 61.22, longitude: -149.9, count: 8 },
    ];
    const dots = render(set);
    const matched = matchCities(dots, set);
    const y = (label) => matched.find((m) => m.label === label).dot.y;
    expect(y("Anchorage")).toBeLessThan(y("London")); // 61N above 51N
    expect(y("London")).toBeLessThan(y("New York")); // 51N above 40N
    expect(y("New York")).toBeLessThan(y("Cairo")); // 40N above 30N
    expect(y("Cairo")).toBeLessThan(y("Sydney")); // northern above southern
  });

  it("puts each city in the correct screen hemisphere", () => {
    const dots = render(CITIES);
    const matched = matchCities(dots, CITIES);
    for (const m of matched) {
      const { dot, longitude, latitude } = m;
      // Western hemisphere (lon<0) → left half; eastern (lon>0) → right half.
      if (longitude < 0) expect(dot.x).toBeLessThan(W / 2);
      else expect(dot.x).toBeGreaterThan(W / 2);
      // Northern hemisphere (lat>0) → top half; southern (lat<0) → bottom half.
      if (latitude > 0) expect(dot.y).toBeLessThan(H / 2);
      else expect(dot.y).toBeGreaterThan(H / 2);
    }
  });
});

describe("world map dot drawing", () => {
  it("skips points that lack latitude or longitude", () => {
    const dots = render([
      { label: "valid", latitude: 10, longitude: 20, count: 1 },
      { label: "no-lon", latitude: 10, count: 1 }, // longitude undefined → skipped
      { label: "no-lat", longitude: 20, count: 1 }, // latitude undefined → skipped
      { label: "neither", count: 1 },
      { label: "zero-ok", latitude: 0, longitude: 0, count: 1 }, // 0 is a valid coord
    ]);
    expect(dots).toHaveLength(2);
    // (0,0) must not be treated as "missing": it plots at the canvas centre.
    expect(nearest(dots, W / 2, H / 2).dist).toBeLessThan(1);
  });

  it("scales dot radius with count (busiest point is largest)", () => {
    const set = [
      { label: "busy", latitude: 0, longitude: 0, count: 100 },
      { label: "quiet", latitude: 0, longitude: 10, count: 1 },
    ];
    const dots = render(set);
    const matched = matchCities(dots, set);
    const busy = matched.find((m) => m.label === "busy").dot;
    const quiet = matched.find((m) => m.label === "quiet").dot;
    // The recorded radius grows with count, so the busy point's arc is larger.
    expect(busy.r).toBeGreaterThan(quiet.r);
  });

  it("does not throw when the canvas is absent", () => {
    expect(() => renderWorldMap(null, null, CITIES)).not.toThrow();
  });
});

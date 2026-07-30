// Shared world-map component. Both the Security monitor (login access map) and
// the MCP Security page (access + attack map) draw the same Cloudflare-style
// flat coastline basemap and the same scaled, glowing lon/lat dots — only the
// dot colours and the tooltip text differ. This module owns the basemap load
// and the projection; callers supply the points and a colouring rule.
//
// The basemap is world.geojson (high-fidelity coastlines in lon/lat), fetched
// once and cached for the page's lifetime. While it loads, the canvas shows a
// blank ocean so the dots (which do not need the basemap) still render.

// worldCoastlines holds the parsed world.geojson FeatureCollection. null until
// the first load completes (or fails, in which case it stays null and the map
// draws dots only).
let worldCoastlines = null;
let worldCoastlinesLoading = false;

// ensureCoastlines loads and caches the coastline geojson. Concurrent callers
// share one in-flight fetch so a rapid re-render never fires duplicate 2MB
// downloads.
async function ensureCoastlines() {
  if (worldCoastlines) return;
  if (worldCoastlinesLoading) {
    while (worldCoastlinesLoading) {
      await new Promise((r) => setTimeout(r, 30));
    }
    return;
  }
  worldCoastlinesLoading = true;
  try {
    const res = await fetch("/world.geojson");
    if (res.ok) worldCoastlines = await res.json();
  } catch {
    worldCoastlines = null;
  } finally {
    worldCoastlinesLoading = false;
  }
}

// Equirectangular projection covering the geojson's full ±180/±85 extent:
// lon [-180,180] → x [0,W], lat [85,-85] → y [0,H].
function makeProjector(W, H) {
  const maxLat = 85, minLat = -85;
  return (lon, lat) => [
    ((lon + 180) / 360) * W,
    ((maxLat - lat) / (maxLat - minLat)) * H,
  ];
}

// drawWorldMap renders the basemap + dots onto canvas. It is async because the
// basemap geojson loads on first use; the dots are drawn after it resolves.
//
//   canvas   - the <canvas> element (uses its width/height attributes)
//   tooltip  - the tooltip element (positioned on hover), or null for no hover
//   points   - [{ latitude, longitude, count, ... }]
//   opts     - {
//     colorFor(point) -> [glowRgba, dotHex],   // dot appearance
//     pointLabel(point) -> html string,         // tooltip body for a point
//   }
export async function drawWorldMap(canvas, tooltip, points, opts = {}) {
  if (!canvas) return;
  await ensureCoastlines();
  renderWorldMap(canvas, tooltip, points, opts);
}

// renderWorldMap does the synchronous canvas work, split out so a caller with an
// already-loaded basemap (or a re-render) does not re-await the fetch.
export function renderWorldMap(canvas, tooltip, points, opts = {}) {
  if (!canvas) return;
  const ctx = canvas.getContext("2d");
  const W = canvas.width;
  const H = canvas.height;
  const project = makeProjector(W, H);

  ctx.clearRect(0, 0, W, H);

  // Coastline basemap: a set of lon/lat line strings stroked as a soft slate
  // tone with rounded joins — the flat look of the reference / Cloudflare map.
  if (worldCoastlines) {
    ctx.lineJoin = "round";
    ctx.lineCap = "round";
    ctx.strokeStyle = "#94a3b8"; // slate-400
    ctx.lineWidth = 0.6;
    for (const f of worldCoastlines.features) {
      strokeGeometry(ctx, f.geometry, project);
    }
  }

  drawDots(ctx, project, points || [], opts.colorFor);
  if (tooltip) wireHover(canvas, tooltip, project, points || [], opts.pointLabel);
}

// strokeGeometry strokes every line string in a GeoJSON geometry, walking the
// nested coordinate arrays of MultiLineString / LineString.
function strokeGeometry(ctx, geom, project) {
  if (!geom || !geom.coordinates) return;
  const stroke = (coords) => {
    if (!coords.length) return;
    ctx.beginPath();
    coords.forEach(([lon, lat], i) => {
      const [x, y] = project(lon, lat);
      if (i === 0) ctx.moveTo(x, y);
      else ctx.lineTo(x, y);
    });
    ctx.stroke();
  };
  const walk = (node) => {
    if (typeof node[0] === "number") return; // a coordinate pair, not a line
    if (typeof node[0][0] === "number") {
      stroke(node); // a single line string
    } else {
      node.forEach(walk); // a multi-line string
    }
  };
  walk(geom.coordinates);
}

// drawDots plots each point at its lon/lat with a soft radial glow and a solid
// core. Size scales with count relative to the busiest point; colour comes from
// the caller's colorFor (falling back to brand blue when unset).
function drawDots(ctx, project, points, colorFor) {
  const max = points.reduce((m, p) => Math.max(m, p.count || 0), 0) || 1;
  const fallback = ["rgba(56, 130, 255, 0.7)", "#3882ff"];
  for (const p of points) {
    // Compare against null/undefined (not falsiness): latitude 0 (the equator)
    // and longitude 0 (the prime meridian) are valid coordinates and must plot.
    if (p.latitude == null || p.longitude == null) continue;
    const [x, y] = project(p.longitude, p.latitude);
    const r = 3 + 7 * ((p.count || 0) / max);
    const [glow, dot] = colorFor ? colorFor(p) : fallback;
    const grd = ctx.createRadialGradient(x, y, 0, x, y, r * 2.5);
    grd.addColorStop(0, glow);
    grd.addColorStop(1, glow.replace(/[\d.]+\)$/, "0)"));
    ctx.fillStyle = grd;
    ctx.beginPath();
    ctx.arc(x, y, r * 2.5, 0, Math.PI * 2);
    ctx.fill();
    ctx.fillStyle = dot;
    ctx.beginPath();
    ctx.arc(x, y, Math.max(1.5, r / 2), 0, Math.PI * 2);
    ctx.fill();
  }
}

// wireHover shows the tooltip for the nearest point within an 18px tolerance.
function wireHover(canvas, tooltip, project, points, pointLabel) {
  canvas.onmousemove = (e) => {
    const rect = canvas.getBoundingClientRect();
    const W = canvas.width;
    const H = canvas.height;
    const mx = ((e.clientX - rect.left) / rect.width) * W;
    const my = ((e.clientY - rect.top) / rect.height) * H;
    let best = null, bestDist = 18;
    for (const p of points) {
      if (p.latitude == null || p.longitude == null) continue;
      const [px, py] = project(p.longitude, p.latitude);
      const d = Math.hypot(px - mx, py - my);
      if (d < bestDist) { bestDist = d; best = p; }
    }
    if (best) {
      tooltip.style.display = "block";
      tooltip.style.left = Math.min(rect.width - 170, e.clientX - rect.left + 12) + "px";
      tooltip.style.top = (e.clientY - rect.top + 12) + "px";
      tooltip.innerHTML = pointLabel ? pointLabel(best) : escapeHtml(best.label || "");
    } else {
      tooltip.style.display = "none";
    }
  };
  canvas.onmouseleave = () => { tooltip.style.display = "none"; };
}

function escapeHtml(v) {
  return String(v ?? "").replace(/[&<>"']/g, (m) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[m]));
}

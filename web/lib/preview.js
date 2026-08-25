// Extra inline-preview renderers shared by the console ViewerDialog and the
// public share page: CSV/TSV tables, pretty JSON, and Office documents
// (docx via mammoth, xlsx/xls via SheetJS). Framework-independent on purpose
// — it is imported by the Vue app under src/ and loaded by share.js.
//
// The Office decoders are lazy: their UMD bundles (~0.6/0.9 MB) are fetched
// from /vendor/ only the first time a document of that type is previewed.

const loaded = new Map();

function loadScriptOnce(src) {
  if (!loaded.has(src)) {
    loaded.set(
      src,
      new Promise((resolve, reject) => {
        const s = document.createElement("script");
        s.src = src;
        s.onload = () => resolve();
        s.onerror = () => {
          loaded.delete(src);
          reject(new Error(`failed to load ${src}`));
        };
        document.head.appendChild(s);
      }),
    );
  }
  return loaded.get(src);
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

// Parses one CSV/TSV line honouring double-quoted fields with embedded
// separators and escaped quotes (""). Good enough for preview purposes.
function splitDelimited(line, sep) {
  const out = [];
  let cur = "";
  let inQuotes = false;
  for (let i = 0; i < line.length; i++) {
    const c = line[i];
    if (inQuotes) {
      if (c === '"') {
        if (line[i + 1] === '"') { cur += '"'; i++; }
        else inQuotes = false;
      } else cur += c;
    } else if (c === '"') {
      inQuotes = true;
    } else if (c === sep) {
      out.push(cur);
      cur = "";
    } else cur += c;
  }
  out.push(cur);
  return out;
}

// Renders CSV/TSV text as an HTML table. The first row is treated as a
// header; everything is escaped here, so callers may assign with innerHTML.
export function csvToTableHtml(text, sep) {
  const lines = String(text || "").split(/\r?\n/).filter((l) => l.length > 0).slice(0, 501);
  if (!lines.length) return "<p class='preview-empty'>empty file</p>";
  const rows = lines.map((l) => splitDelimited(l, sep));
  const [head, ...body] = rows;
  const th = `<thead><tr>${head.map((c) => `<th>${escapeHtml(c)}</th>`).join("")}</tr></thead>`;
  const trs = body
    .map((r) => `<tr>${r.map((c) => `<td>${escapeHtml(c)}</td>`).join("")}</tr>`)
    .join("");
  const note = lines.length > 500 ? "<p class='preview-note'>showing first 500 rows</p>" : "";
  return `<div class='preview-table-wrap'><table class='preview-table'>${th}<tbody>${trs}</tbody></table></div>${note}`;
}

// Pretty-prints JSON, returning null when the text is not valid JSON (the
// caller then falls back to the plain-text preview).
export function prettyJsonOrNull(text) {
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return null;
  }
}

async function fetchArrayBuffer(url) {
  const res = await fetch(url, { credentials: "same-origin" });
  if (!res.ok) throw new Error(`download failed: ${res.status}`);
  return res.arrayBuffer();
}

function sanitizeInto(html, el) {
  const purifier = window.DOMPurify;
  el.innerHTML = purifier ? purifier.sanitize(html) : html;
}

// docx → HTML via mammoth (headings/paragraphs/lists/tables; images inline
// where the format keeps them).
export async function renderDocxInto(url, el) {
  await loadScriptOnce("/vendor/mammoth.browser.min.js");
  if (!window.mammoth) throw new Error("mammoth unavailable");
  const buf = await fetchArrayBuffer(url);
  const result = await window.mammoth.convertToHtml({ arrayBuffer: buf });
  sanitizeInto(result.value || "<p class='preview-empty'>empty document</p>", el);
}

// xlsx/xls → first sheet as an HTML table via SheetJS.
export async function renderXlsxInto(url, el) {
  await loadScriptOnce("/vendor/xlsx.full.min.js");
  if (!window.XLSX) throw new Error("SheetJS unavailable");
  const buf = await fetchArrayBuffer(url);
  const wb = window.XLSX.read(buf, { type: "array" });
  const sheetName = wb.SheetNames[0];
  if (!sheetName) throw new Error("workbook has no sheets");
  const html = window.XLSX.utils.sheet_to_html(wb.Sheets[sheetName], { editable: false });
  sanitizeInto(html, el);
}

// Maps an extension to the preview kind, or null when unsupported. Kept here
// so the share page and the console agree on which files are previewable.
const KINDS = {
  pdf: "pdf", md: "markdown", markdown: "markdown",
  png: "image", jpg: "image", jpeg: "image", gif: "image", webp: "image", bmp: "image", svg: "image",
  mp3: "audio", wav: "audio", ogg: "audio", m4a: "audio", flac: "audio",
  mp4: "video", webm: "video", m4v: "video", mov: "video",
  yuv: "yuv", raw: "raw",
  csv: "csv", tsv: "csv",
  json: "json",
  docx: "docx",
  xlsx: "xlsx", xls: "xlsx",
};

const TEXT_EXTS = new Set([
  "txt", "log", "ini", "conf", "cfg", "yml", "yaml", "toml", "xml", "html",
  "htm", "css", "js", "mjs", "ts", "go", "py", "rb", "java", "c", "h", "cpp",
  "hpp", "cc", "sh", "bash", "zsh", "sql", "env", "gitignore",
]);

export function previewKind(name) {
  const ext = (name.split(".").pop() || "").toLowerCase();
  if (!ext || ext === name.toLowerCase()) return null;
  if (KINDS[ext]) return KINDS[ext];
  if (TEXT_EXTS.has(ext)) return "text";
  return null;
}

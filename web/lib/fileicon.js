// Netdisk file icons, shared by the console (Netdisk.vue) and the public
// share page (share.js). Every file keeps the same page silhouette; the
// category glyph is knocked out of the page via fill-rule="evenodd" and the
// icon is filled with the category color. Size and fill are baked into the
// markup because the icons land in v-html/innerHTML targets, where scoped
// stylesheet rules don't reach them. Framework-independent on purpose —
// imported by the Vue app under src/ and loaded by share.js.

const PAGE = "M6 3.5h8.4L19 8.1v12.1c0 1-.8 1.8-1.8 1.8H6.8c-1 0-1.8-.8-1.8-1.8V5.3c0-1 .8-1.8 1.8-1.8Z";
const FOLD = "M14 3.8V8h4.2Z";
const FOLDER = "M3 6.8C3 5.8 3.8 5 4.8 5h5.1l2 2.2h7.3c1 0 1.8.8 1.8 1.8v1H3V6.8Z M3 9h18l-1.2 8.2c-.1 1-1 1.8-2 1.8H6.2c-1 0-1.8-.7-2-1.8L3 9Z";
const FOLDER_COLOR = "#e6a23c";
// Unknown extensions render the plain page in a neutral gray that stays
// readable in both the light and dark themes.
const GENERIC_COLOR = "#94a3b8";

// Glyph paths are holes cut into PAGE, so they stay inside x 7–17.7 and
// y 10.7–19.4 — below the folded corner and clear of the page edges.
const CATEGORIES = [
  {
    kind: "image", color: "#8b5cf6",
    exts: "png jpg jpeg gif webp bmp svg ico heic heif tif tiff avif jfif",
    glyph: "M9.7 10.9a1.4 1.4 0 1 0 0 2.8a1.4 1.4 0 1 0 0-2.8Z M7.1 18.7l3.4-4.4 2.3 2.7 1.8-2.2 3.1 3.9H7.1Z",
  },
  {
    kind: "video", color: "#ec4899",
    exts: "mp4 mkv webm mov avi m4v flv wmv mpg mpeg 3gp rmvb vob",
    glyph: "M10.2 11.7l5.3 2.9-5.3 2.9Z",
  },
  {
    kind: "audio", color: "#0d9488",
    exts: "mp3 wav flac ogg oga m4a aac wma opus mid midi aiff aif",
    glyph: "M8.6 17.2a1.9 1.4 0 1 0 3.8 0a1.9 1.4 0 1 0-3.8 0Z M12.4 11h1.3v6.5h-1.3Z M12.4 11l3.2 1.2v1.6l-3.2-1.3Z",
  },
  {
    kind: "pdf", color: "#ef4444",
    exts: "pdf",
    glyph: "M8.3 11.9h7.4v1.3H8.3Z M8.3 15h7.4v1.3H8.3Z",
  },
  {
    kind: "word", color: "#2563eb",
    exts: "doc docx rtf odt md markdown pages",
    glyph: "M8.3 11.2h7.4v1.3H8.3Z M8.3 13.9h7.4v1.3H8.3Z M8.3 16.6h4.6v1.3H8.3Z",
  },
  {
    kind: "excel", color: "#16a34a",
    exts: "xls xlsx csv tsv ods numbers",
    glyph: "M8.2 11.3h7.6v1.2H8.2Z M8.2 15.6h7.6v1.2H8.2Z M11.5 12.5h1.2v3.1h-1.2Z M14.4 12.5h1.2v3.1h-1.2Z",
  },
  {
    kind: "ppt", color: "#ea580c",
    exts: "ppt pptx odp key",
    glyph: "M8.8 15.1h1.5v3.7H8.8Z M11.2 12.7h1.5v6.1h-1.5Z M13.6 13.9h1.5v4.9h-1.5Z",
  },
  {
    kind: "archive", color: "#b45309",
    exts: "zip rar 7z tar gz tgz bz2 xz zst iso cab",
    glyph: "M11.4 10.7h1.2v1.2h-1.2Z M11.4 13.2h1.2v1.2h-1.2Z M11.4 15.7h1.2v1.2h-1.2Z M11.4 18.2h1.2v1.2h-1.2Z",
  },
  {
    kind: "code", color: "#6366f1",
    exts: "js mjs cjs ts tsx jsx json html htm css scss less xml yaml yml toml ini env sql vue svelte go py rb rs java c h cpp hpp cc hh cs php swift kt kts sh bash zsh ps1 pl lua dart scala",
    glyph: "M11.6 11.8 8.4 14.5 11.6 17.2 11.6 16 10.5 14.5 11.6 13Z M12.5 11.8 15.7 14.5 12.5 17.2 12.5 16 13.6 14.5 12.5 13Z",
  },
  {
    kind: "exe", color: "#64748b",
    exts: "exe msi bat cmd apk deb rpm appimage run",
    glyph: "M9 11.6h1.7v1.7H9Z M13.3 11.6h1.7v1.7h-1.7Z M9 15.2h1.7v1.7H9Z M13.3 15.2h1.7v1.7h-1.7Z",
  },
];

const BY_EXT = {};
for (const cat of CATEGORIES) for (const ext of cat.exts.split(" ")) BY_EXT[ext] = cat;

// Returns the full <svg> markup for a list row. Unknown extensions (and
// plain-text files) fall back to the untyped page in the generic gray.
export function fileIconSvg(name, dir) {
  if (dir) {
    return `<svg width="22" height="22" viewBox="0 0 24 24" focusable="false" aria-hidden="true"><path fill="${FOLDER_COLOR}" d="${FOLDER}"/></svg>`;
  }
  const lower = String(name || "").toLowerCase();
  const ext = lower.includes(".") ? lower.split(".").pop() : "";
  const cat = BY_EXT[ext];
  const fill = ` fill="${cat ? cat.color : GENERIC_COLOR}"`;
  return `<svg width="22" height="22" viewBox="0 0 24 24" focusable="false" aria-hidden="true"><path${fill} fill-rule="evenodd" d="${PAGE}${cat ? " " + cat.glyph : ""}"/><path${fill} d="${FOLD}"/></svg>`;
}

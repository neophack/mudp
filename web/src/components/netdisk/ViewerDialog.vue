<template>
  <!-- Inline file preview. PDF via pdf.js, markdown via marked+DOMPurify,
       text/JSON in a <pre>, CSV/TSV as a table, docx/xlsx through the lazily
       loaded mammoth/SheetJS bundles, media via native elements, YUV/RAW
       through the server-side raster decode (lib/yuv.js, lib/raw.js). The
       type dispatch lives in lib/preview.js, shared with the share page. -->
  <el-dialog :model-value="visible" width="860px" top="3vh" append-to-body custom-class="viewer-dialog" @update:model-value="onVisible" @opened="render">
    <!-- Title on the left, fullscreen + download on the right of the same
         row, matching the share page's viewer head. -->
    <template #header>
      <div class="viewer-head">
        <span class="viewer-head-title" :title="name">{{ name }}</span>
        <div class="viewer-head-actions">
          <el-button v-if="supportsFullscreen" size="small" @click="toggleFullscreen">
            ⛶ {{ fullscreen ? tt("netdisk.viewerExitFullscreen") : tt("netdisk.viewerFullscreen") }}
          </el-button>
          <el-button size="small">
            <a :href="dlUrl" :download="name" style="color: inherit; text-decoration: none">⬇ {{ tt("netdisk.download") }}</a>
          </el-button>
        </div>
      </div>
    </template>

    <div v-if="!kind" class="viewer-unsupported">
      <div class="viewer-unsupported-icon">📄</div>
      <p>{{ tt("netdisk.viewerUnsupported") }}</p>
      <p class="hint">{{ tt("netdisk.viewerDownloadToView") }}</p>
      <el-button type="primary" size="small">
        <a :href="dlUrl" :download="name" style="color: #fff; text-decoration: none">⬇ {{ tt("netdisk.download") }}</a>
      </el-button>
    </div>

    <div v-else ref="body" class="viewer-body" :class="{ fullscreen }">
      <div v-if="loading" class="viewer-loading">{{ tt("netdisk.viewerLoading") }}</div>
      <div v-else-if="error" class="viewer-error">{{ error }}</div>
      <img v-else-if="kind === 'image'" ref="media" class="viewer-media" :src="rawUrl" :alt="name" />
      <audio v-else-if="kind === 'audio'" ref="media" class="viewer-media" controls preload="metadata" :src="rawUrl" />
      <!-- Video is deliberately fullscreen-free (no player button via
           controlslist, no header button), so it always plays inline. -->
      <video v-else-if="kind === 'video'" ref="media" class="viewer-media" controls controlslist="nofullscreen" preload="metadata" :src="rawUrl" />
      <pre v-else-if="kind === 'text' || kind === 'json'" class="viewer-text">{{ text }}</pre>
      <!-- csvToTableHtml escapes every cell itself, so v-html is safe here. -->
      <div v-else-if="kind === 'csv'" class="viewer-csv" v-html="csvHtml"></div>
      <div v-else-if="kind === 'markdown'" ref="md" class="viewer-md"></div>
      <div v-else-if="kind === 'pdf'" ref="pdf" class="viewer-canvas-wrap"></div>
      <div v-else-if="kind === 'docx' || kind === 'xlsx'" ref="office" class="viewer-md viewer-doc"></div>
      <div v-else-if="kind === 'yuv' || kind === 'raw'" ref="raster" class="viewer-canvas-wrap"></div>
    </div>
  </el-dialog>
</template>

<script>
import { tt } from "@/i18n";
import { renderMarkdownInto } from "@/lib/viewer.js";
import { openYuvViewer } from "@/lib/yuv.js";
import { openRawViewer } from "@/lib/raw.js";
import { previewKind, csvToTableHtml, prettyJsonOrNull, renderDocxInto, renderXlsxInto } from "@/lib/preview.js";

export default {
  name: "ViewerDialog",
  props: {
    visible: { type: Boolean, default: false },
    path: { type: String, default: "" },
    fromBackup: { type: Boolean, default: false },
  },
  data() {
    return { loading: false, error: "", text: "", csvHtml: "", fullscreen: false, controller: null, pdfDoc: null };
  },
  computed: {
    name() {
      return this.path.split("/").pop() || this.path;
    },
    kind() {
      return previewKind(this.name);
    },
    dlUrl() {
      const base = this.fromBackup ? "/api/netdisk/backup/download" : "/api/netdisk/download";
      return `${base}?path=${encodeURIComponent(this.path)}&ts=${Date.now()}`;
    },
    rawUrl() {
      const base = this.fromBackup ? "/api/netdisk/backup/raw" : "/api/netdisk/raw";
      return `${base}?path=${encodeURIComponent(this.path)}&ts=${Date.now()}`;
    },
    // The server-side YUV/RAW decode endpoint returns a JPEG per frame, so only
    // the compressed image crosses the network, not the raw sensor bytes.
    rasterUrl() {
      const base = this.fromBackup ? "/api/netdisk/backup/raster" : "/api/netdisk/raster";
      return `${base}?path=${encodeURIComponent(this.path)}&ts=${Date.now()}`;
    },
    // Video is deliberately fullscreen-free: the player plays inline only.
    supportsFullscreen() {
      return ["pdf", "image", "yuv", "raw", "csv", "docx", "xlsx"].includes(this.kind);
    },
  },
  watch: {
    fullscreen() {
      this.$nextTick(() => {
        if (this.pdfDoc) this.paintPdf();
        if (this.controller && this.controller.repaint) this.controller.repaint();
      });
    },
  },
  methods: {
    tt,
    onVisible(v) {
      this.$emit("update:visible", v);
      if (!v) this.reset();
    },
    reset() {
      this.text = "";
      this.csvHtml = "";
      this.error = "";
      this.loading = false;
      this.fullscreen = false;
      // The dialog only hides (no destroy-on-close), so media elements keep
      // playing after close unless explicitly stopped and unloaded here.
      const media = this.$refs.media;
      if (media) {
        try {
          media.pause();
          media.removeAttribute("src");
          media.load();
        } catch { /* already gone */ }
      }
      if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
      if (this.controller) {
        if (this.controller.destroy) this.controller.destroy();
        this.controller = null;
      }
      this.pdfDoc = null;
    },
    // Images go through the native Fullscreen API so zoom/pan stays smooth;
    // other kinds use the CSS .fullscreen overlay, which also serves as the
    // fallback when the API is unavailable (e.g. sandboxed iframe without
    // allowfullscreen).
    toggleFullscreen() {
      const native = this.kind === "image" ? this.$refs.media : null;
      this.fullscreen = !this.fullscreen;
      if (this.fullscreen && native && native.requestFullscreen) {
        native.requestFullscreen().catch(() => {});
      } else if (!this.fullscreen && document.fullscreenElement) {
        document.exitFullscreen().catch(() => {});
      }
    },
    // Loads the raw bytes for text-like previews with the same 2 MB cap the
    // old console had, so a huge log can't be pulled into the DOM.
    async fetchText() {
      const res = await fetch(this.rawUrl, { credentials: "same-origin" });
      const len = Number(res.headers.get("Content-Length") || 0);
      if (len > 2 * 1024 * 1024) throw new Error(tt("netdisk.viewerLarge"));
      return res.text();
    },
    async render() {
      const kind = this.kind;
      if (!kind) return;
      this.loading = true;
      this.error = "";
      try {
        if (kind === "text" || kind === "json") {
          const raw = await this.fetchText();
          // Invalid JSON falls back to the plain-text view rather than erroring.
          this.text = kind === "json" ? (prettyJsonOrNull(raw) ?? raw) : raw;
        } else if (kind === "csv") {
          const sep = this.name.toLowerCase().endsWith(".tsv") ? "\t" : ",";
          this.csvHtml = csvToTableHtml(await this.fetchText(), sep);
        } else if (kind === "markdown") {
          this.loading = false;
          await this.$nextTick();
          if (this.$refs.md) renderMarkdownInto(this.rawUrl, this.$refs.md);
          return;
        } else if (kind === "docx") {
          this.loading = false;
          await this.$nextTick();
          if (this.$refs.office) await renderDocxInto(this.dlUrl, this.$refs.office);
          return;
        } else if (kind === "xlsx") {
          this.loading = false;
          await this.$nextTick();
          if (this.$refs.office) await renderXlsxInto(this.dlUrl, this.$refs.office);
          return;
        } else if (kind === "pdf") {
          this.loading = false;
          await this.$nextTick();
          await this.renderPdf();
          return;
        } else if (kind === "yuv") {
          this.loading = false;
          await this.$nextTick();
          if (this.$refs.raster) {
            this.controller = openYuvViewer({ name: this.name, url: this.rawUrl, rasterUrl: this.rasterUrl, bodyEl: this.$refs.raster });
          }
          return;
        } else if (kind === "raw") {
          this.loading = false;
          await this.$nextTick();
          if (this.$refs.raster) {
            this.controller = openRawViewer({ name: this.name, url: this.rawUrl, rasterUrl: this.rasterUrl, bodyEl: this.$refs.raster });
          }
          return;
        }
      } catch (err) {
        this.error = err.message || "Failed to load file";
      } finally {
        this.loading = false;
      }
    },
    async renderPdf() {
      const pdfjs = window.pdfjsLib;
      if (!pdfjs) {
        this.error = tt("netdisk.viewerPdfFail");
        return;
      }
      pdfjs.workerSrc = "/vendor/pdf.worker.min.js";
      this.pdfDoc = await pdfjs.getDocument({ url: this.rawUrl }).promise;
      await this.paintPdf();
    },
    // Lays out each PDF page in a vertical scroll container. The canvas is
    // drawn at device-pixel resolution and sized down via CSS, keeping text
    // crisp on Retina/4K displays.
    async paintPdf() {
      const pdf = this.pdfDoc;
      const pages = this.$refs.pdf;
      if (!pdf || !pages) return;
      pages.innerHTML = "";
      const maxWidth = Math.max(360, pages.clientWidth || 820);
      for (let i = 1; i <= pdf.numPages; i++) {
        const page = await pdf.getPage(i);
        const baseViewport = page.getViewport({ scale: 1 });
        const scale = Math.max(1, maxWidth / baseViewport.width);
        const viewport = page.getViewport({ scale });
        const dpr = window.devicePixelRatio || 1;
        const canvas = document.createElement("canvas");
        canvas.className = "viewer-page";
        canvas.width = Math.floor(viewport.width * dpr);
        canvas.height = Math.floor(viewport.height * dpr);
        canvas.style.width = `${Math.floor(viewport.width)}px`;
        canvas.style.height = `${Math.floor(viewport.height)}px`;
        pages.appendChild(canvas);
        const ctx = canvas.getContext("2d");
        ctx.scale(dpr, dpr);
        await page.render({ canvasContext: ctx, viewport }).promise;
      }
    },
  },
};
</script>

<style scoped>
/* The dialog header keeps its own show-close padding, so the actions row
   already clears the ✕ button; typography matches the app-wide dialog
   title style (14.5px/650). */
.viewer-head { display: flex; align-items: center; gap: 12px; }
.viewer-head-title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 14.5px; font-weight: 650; }
.viewer-head-actions { display: flex; gap: 8px; }
.viewer-body { min-height: 320px; max-height: 70vh; overflow: auto; text-align: center; }
.viewer-body.fullscreen { position: fixed; inset: 0; z-index: 3001; max-height: none; background: #fff; padding: 16px; }
.viewer-media { max-width: 100%; max-height: 66vh; }
.viewer-text { text-align: left; background: var(--fill); border-radius: 8px; padding: 12px; font-size: 12.5px; white-space: pre-wrap; word-break: break-word; overflow: auto; max-height: 66vh; }
.viewer-md { text-align: left; max-height: 66vh; overflow: auto; }
.viewer-canvas-wrap { text-align: center; }
.viewer-loading, .viewer-error { color: var(--muted); padding: 40px 0; }
.viewer-error { color: var(--danger); }
.viewer-unsupported { text-align: center; padding: 30px 0; }
.viewer-unsupported-icon { font-size: 40px; }
>>> .viewer-page { margin: 0 auto 10px; box-shadow: 0 2px 10px rgba(0, 0, 0, 0.12); display: block; }
</style>

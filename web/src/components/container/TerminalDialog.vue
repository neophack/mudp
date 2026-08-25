<template>
  <el-dialog
    :model-value="visible"
    custom-class="terminal-dialog"
    width="900px"
    top="4vh"
    append-to-body
    :close-on-click-modal="false"
    @update:model-value="onVisible"
    @opened="start"
  >
    <div class="term-head">
      <div class="term-title">
        <h2>{{ tt("terminal.title") }}</h2>
        <div class="term-meta hint">{{ meta }}</div>
      </div>
      <div class="term-toolbar">
        <el-dropdown size="small" trigger="click" @command="runCommand">
          <el-button size="small" :title="tt('terminal.commandsTitle')">{{ tt("terminal.commands") }}</el-button>
          <template #dropdown><el-dropdown-menu>
            <el-dropdown-item v-for="c in COMMON_COMMANDS" :key="c.label" :command="c.cmd">
              <code class="mono">{{ c.cmd }}</code>
              <span class="cmd-label">{{ c.label }}</span>
            </el-dropdown-item>
          </el-dropdown-menu></template>
        </el-dropdown>
        <el-button size="small" :title="tt('terminal.clearTitle')" @click="send('\f')">{{ tt("terminal.clear") }}</el-button>
        <el-button size="small" :title="tt('terminal.copyTitle')" @click="copySelection">{{ tt("terminal.copy") }}</el-button>
        <el-button size="small" :title="tt('terminal.pasteTitle')" @click="paste">{{ tt("terminal.paste") }}</el-button>
        <el-button size="small" @click="fullscreen = !fullscreen">{{ fullscreen ? tt("terminal.exitFullscreen") : tt("terminal.fullscreen") }}</el-button>
      </div>
    </div>
    <div ref="box" class="term-box" :class="{ fullscreen }"></div>
    <div class="term-statusbar">
      <span class="term-stat">
        <span class="dot" :class="connClass"></span>{{ connText }}
      </span>
      <span class="term-stat term-keys">{{ tt("terminal.keysHint") }}</span>
      <span class="term-stat term-size">{{ sizeText }}</span>
    </div>
  </el-dialog>
</template>

<script>
import { ElMessage } from "element-plus";
import { tt } from "@/i18n";

// One-line snippets inserted into the prompt via the toolbar. Picked to be
// safe, read-only, and useful when inspecting a container.
const COMMON_COMMANDS = [
  { label: "ls", cmd: "ls -la" },
  { label: "pwd", cmd: "pwd" },
  { label: "whoami", cmd: "whoami" },
  { label: "uname", cmd: "uname -a" },
  { label: "disk", cmd: "df -h" },
  { label: "mem", cmd: "free -h" },
  { label: "cpu", cmd: "nproc && cat /proc/loadavg" },
  { label: "gpu", cmd: "nvidia-smi 2>/dev/null || echo 'no nvidia-smi'" },
  { label: "procs", cmd: "ps aux --sort=-%cpu | head -n 15" },
  { label: "ports", cmd: "ss -tlnp 2>/dev/null || netstat -tlnp 2>/dev/null" },
  { label: "env", cmd: "env | sort" },
  { label: "date", cmd: "date" },
];

// Balanced dark palette (One Dark inspired) with distinct, high-contrast ANSI
// colours so ls/git/gcc/diff output reads well.
const TERM_THEME = {
  background: "#1a1b26",
  foreground: "#a9b1d6",
  cursor: "#c0caf5",
  cursorAccent: "#1a1b26",
  selectionBackground: "rgba(122,134,198,0.35)",
  black: "#32344a",
  red: "#f7768e",
  green: "#9ece6a",
  yellow: "#e0af68",
  blue: "#7aa2f7",
  magenta: "#bb9af7",
  cyan: "#7dcfff",
  white: "#a9b1d6",
  brightBlack: "#9699b8",
  brightRed: "#f7768e",
  brightGreen: "#9ece6a",
  brightYellow: "#e0af68",
  brightBlue: "#7aa2f7",
  brightMagenta: "#bb9af7",
  brightCyan: "#7dcfff",
  brightWhite: "#c0caf5",
};

export default {
  name: "TerminalDialog",
  props: {
    visible: { type: Boolean, default: false },
    id: { type: String, default: "" },
    title: { type: String, default: "" },
  },
  data() {
    return {
      COMMON_COMMANDS,
      meta: tt("terminal.loadingMeta"),
      conn: "opening",
      sizeText: "",
      fullscreen: false,
      term: null,
      ws: null,
      fitAddon: null,
      ro: null,
    };
  },
  computed: {
    connClass() {
      return this.conn === "ok" ? "dot-ok" : this.conn === "error" || this.conn === "closed" ? "dot-danger" : "dot-muted";
    },
    connText() {
      return this.conn === "ok" ? tt("terminal.connected") : this.conn === "error" ? tt("terminal.connError")
        : this.conn === "closed" ? tt("terminal.disconnected") : tt("terminal.opening");
    },
  },
  watch: {
    fullscreen() {
      this.$nextTick(() => this.refit());
    },
  },
  methods: {
    tt,
    onVisible(v) {
      this.$emit("update:visible", v);
      if (!v) this.stop();
    },
    // Fetch container metadata for the header (best-effort; never blocks the PTY).
    async fetchMeta() {
      try {
        const i = await fetch("/api/containers/inspect?id=" + encodeURIComponent(this.id), { credentials: "same-origin" }).then((r) => r.json());
        const parts = [];
        if (i.imageName || i.image) parts.push(`📦 ${i.imageName || i.image}`);
        if (i.gpu && i.gpu !== "none") parts.push(`🎴 GPU ${i.gpu}`);
        parts.push(`state: ${i.state}`);
        this.meta = parts.join("  ·  ");
      } catch {
        // Header stays at "Loading…"; the PTY itself is unaffected.
      }
    },
    start() {
      this.fetchMeta();
      if (!window.Terminal || !window.FitAddon || !window.FitAddon.FitAddon) {
        this.conn = "error";
        this.meta = tt("terminal.libFail");
        return;
      }
      const term = new window.Terminal({
        cursorBlink: true,
        fontSize: 14,
        lineHeight: 1.25,
        fontFamily: "'Cascadia Code', 'Fira Code', 'SFMono-Regular', Consolas, 'Liberation Mono', monospace",
        scrollback: 10000,
        convertEol: true,
        macOptionIsMeta: true,
        allowProposedApi: true,
        theme: TERM_THEME,
      });
      const fitAddon = new window.FitAddon.FitAddon();
      term.loadAddon(fitAddon);
      term.open(this.$refs.box);
      fitAddon.fit();
      this.term = term;
      this.fitAddon = fitAddon;
      this.updateSize();

      const proto = location.protocol === "https:" ? "wss:" : "ws:";
      const url = `${proto}//${location.host}/api/containers/terminal?id=${encodeURIComponent(this.id)}&cols=${term.cols}&rows=${term.rows}`;
      const ws = new WebSocket(url);
      this.ws = ws;
      ws.binaryType = "arraybuffer";
      ws.onopen = () => {
        this.conn = "ok";
        // The terminal may have been refitted between URL construction and the
        // WebSocket opening, so push the current dimensions to the server pty.
        this.sendResize();
      };
      ws.onmessage = (ev) => {
        term.write(typeof ev.data === "string" ? ev.data : new Uint8Array(ev.data));
      };
      ws.onclose = () => { this.conn = "closed"; };
      ws.onerror = () => { this.conn = "error"; };
      term.onData((data) => {
        if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "stdin", data }));
      });

      // Keep the server-side pty in sync with the rendered terminal size.
      const sendResize = this.sendResize.bind(this);
      term.onResize(() => {
        this.updateSize();
        sendResize();
      });
      const onResize = () => {
        try { fitAddon.fit(); } catch { /* not attached yet */ }
      };
      window.addEventListener("resize", onResize);
      this._onWinResize = onResize;
      if (window.ResizeObserver) {
        this.ro = new ResizeObserver(onResize);
        this.ro.observe(this.$refs.box);
      }
      // Re-fit once the dialog's flex layout has settled, then focus.
      requestAnimationFrame(() => {
        try { fitAddon.fit(); } catch { /* ignore */ }
        setTimeout(() => { try { term.focus(); } catch { /* ignore */ } }, 30);
      });
    },
    updateSize() {
      if (this.term) this.sizeText = `${this.term.cols}×${this.term.rows}`;
    },
    sendResize() {
      if (this.ws && this.ws.readyState === WebSocket.OPEN && this.term) {
        this.ws.send(JSON.stringify({ type: "resize", cols: this.term.cols, rows: this.term.rows }));
      }
    },
    refit() {
      try { this.fitAddon && this.fitAddon.fit(); } catch { /* ignore */ }
      try { this.term && this.term.focus(); } catch { /* ignore */ }
    },
    send(text) {
      if (this.ws && this.ws.readyState === WebSocket.OPEN) {
        this.ws.send(JSON.stringify({ type: "stdin", data: text }));
      }
      try { this.term && this.term.focus(); } catch { /* ignore */ }
    },
    runCommand(cmd) {
      // Run the command immediately (append a newline so the shell executes it).
      this.send(cmd + "\r");
    },
    async copySelection() {
      const sel = this.term && this.term.getSelection();
      if (sel) {
        try {
          await navigator.clipboard.writeText(sel);
          ElMessage.success(tt("terminal.selectionCopied"));
        } catch {
          ElMessage.error(tt("terminal.copyFailed"));
        }
      } else {
        ElMessage.info(tt("terminal.selectFirst"));
      }
    },
    async paste() {
      try {
        const text = await navigator.clipboard.readText();
        if (text) this.send(text);
      } catch {
        ElMessage.error(tt("terminal.clipboardBlocked"));
      }
    },
    stop() {
      if (this._onWinResize) window.removeEventListener("resize", this._onWinResize);
      if (this.ro) {
        try { this.ro.disconnect(); } catch { /* ignore */ }
        this.ro = null;
      }
      try { this.ws && this.ws.close(); } catch { /* ignore */ }
      try { this.term && this.term.dispose(); } catch { /* ignore */ }
      this.term = null;
      this.ws = null;
      this.fitAddon = null;
      this.fullscreen = false;
    },
  },
};
</script>

<style scoped>
.term-head { display: flex; align-items: center; gap: 12px; margin-bottom: 8px; flex-wrap: wrap; }
.term-title h2 { margin: 0; font-size: 15px; }
.term-toolbar { margin-left: auto; display: flex; gap: 6px; flex-wrap: wrap; }
.cmd-label { color: #909399; margin-left: 8px; }
.term-box {
  background: #1a1b26;
  border-radius: 8px;
  padding: 6px;
  height: 55vh;
}
.term-box.fullscreen {
  position: fixed;
  inset: 12px;
  z-index: 3000;
  height: auto;
}
.term-statusbar {
  display: flex;
  gap: 16px;
  align-items: center;
  margin-top: 8px;
  font-size: 12px;
  color: #94a3b8;
}
.term-stat .dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 6px; }
.dot-ok { background: var(--ok); }
.dot-danger { background: var(--danger); }
.dot-muted { background: #94a3b8; }
.term-keys { flex: 1; text-align: center; }
</style>

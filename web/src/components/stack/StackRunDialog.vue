<template>
  <!-- Read-only xterm log for stack deploy/down output. Docker pull/build
       progress lines are meant to overwrite the same row; the server strips
       \r, so the overwrite behavior is reconstructed here. -->
  <el-dialog :model-value="visible" :title="title" width="860px" top="4vh" append-to-body @update:model-value="onVisible" @opened="start">
    <div ref="box" class="term-box"></div>
    <div class="term-statusbar">
      <span class="term-stat">
        <span class="dot" :class="statusDot"></span>{{ statusText }}
      </span>
      <span class="term-stat term-keys">{{ tt("stacks.readonlyLog") }}</span>
    </div>
  </el-dialog>
</template>

<script>
import { tt } from "@/i18n";
import { stackRun } from "@/components/stack/stackRun";

// One Dark-inspired palette shared with the container terminal.
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

const PROGRESS_RE = /(?:Downloading|Extracting|Pulling|Pushing|Waiting|Importing|Verifying|Loading|Saving|Uploading).*?\d+(?:\.\d+)?\s*(?:MB|GB|kB|B)/;

export default {
  name: "StackRunDialog",
  props: {
    visible: { type: Boolean, default: false },
    title: { type: String, default: "" },
  },
  data() {
    return { term: null, lastWasProgress: false, consumed: 0 };
  },
  computed: {
    statusDot() {
      if (stackRun.error) return "dot-danger";
      if (stackRun.done) return "dot-ok";
      return "dot-muted";
    },
    statusText() {
      if (stackRun.error) return stackRun.error;
      if (stackRun.done) return tt("stacks.complete");
      return tt("stacks.running");
    },
  },
  watch: {
    // Streamed lines are appended to the module state by the Stacks view; the
    // dialog writes any new ones into the terminal as they land.
    "stackRun.lines"() {
      this.flush();
    },
  },
  methods: {
    tt,
    onVisible(v) {
      this.$emit("update:visible", v);
      if (!v) this.stop();
    },
    writeLine(raw) {
      if (!this.term) return;
      const isProgress = PROGRESS_RE.test(raw);
      if (isProgress) {
        // Move up one line, clear it, then redraw the updated progress.
        if (this.lastWasProgress) this.term.write("\x1b[1A\x1b[2K" + raw + "\r");
        else this.term.write(raw + "\r");
        this.lastWasProgress = true;
      } else {
        if (this.lastWasProgress) this.term.write("\n" + raw + "\r\n");
        else this.term.write(raw + "\r\n");
        this.lastWasProgress = false;
      }
    },
    // Replay the buffered session so reopening shows the same output.
    flush() {
      while (this.consumed < stackRun.lines.length) {
        this.writeLine(String(stackRun.lines[this.consumed]));
        this.consumed++;
      }
    },
    start() {
      if (!window.Terminal || !window.FitAddon || !window.FitAddon.FitAddon) {
        this.$refs.box.textContent = tt("terminal.libFail");
        return;
      }
      const term = new window.Terminal({
        cursorBlink: false,
        fontSize: 13,
        lineHeight: 1.25,
        fontFamily: "'Cascadia Code', 'Fira Code', 'SFMono-Regular', Consolas, 'Liberation Mono', monospace",
        scrollback: 10000,
        convertEol: true,
        disableStdin: true,
        theme: TERM_THEME,
      });
      const fitAddon = new window.FitAddon.FitAddon();
      term.loadAddon(fitAddon);
      term.open(this.$refs.box);
      fitAddon.fit();
      this.term = term;
      this.lastWasProgress = false;
      this.consumed = 0;
      this.flush();
      if (window.ResizeObserver) {
        this.ro = new ResizeObserver(() => {
          try { fitAddon.fit(); } catch { /* detached */ }
        });
        this.ro.observe(this.$refs.box);
      }
    },
    stop() {
      if (this.ro) {
        try { this.ro.disconnect(); } catch { /* ignore */ }
        this.ro = null;
      }
      try { this.term && this.term.dispose(); } catch { /* ignore */ }
      this.term = null;
      this.lastWasProgress = false;
    },
  },
};
</script>

<style scoped>
.term-box {
  background: #1a1b26;
  border-radius: 8px;
  padding: 6px;
  height: 55vh;
}
.term-statusbar { display: flex; gap: 16px; align-items: center; margin-top: 8px; font-size: 12px; color: #94a3b8; }
.term-stat .dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 6px; }
.dot-ok { background: #10b981; }
.dot-danger { background: #ef4444; }
.dot-muted { background: #94a3b8; }
.term-keys { flex: 1; text-align: center; }
</style>

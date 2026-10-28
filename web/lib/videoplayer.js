// Video player for the netdisk viewer dialog (ViewerDialog.vue) and the public
// share page (share.js). The native <video controls> overlay draws a dark
// gradient over the bottom of the frame, which hides exactly the content a
// side-by-side compare clip is meant to show. This player keeps the frame
// unobstructed and puts its own control bar below it (lib/videoplayer.css):
// a transport row and a picture row with brightness / contrast / color
// temperature sliders. The adjustments are display-only filters on the
// <video>; the file itself is never touched.
//
// The adjustments are remembered in localStorage and applied to every video
// opened afterwards, so a look tuned once carries across clips.
//
// The fullscreen button puts the whole player (frame + bar) into native
// fullscreen rather than the <video> alone, which would bring back the
// browser's overlay controls.
//
// mountVideoPlayer({ url, bodyEl }) renders into bodyEl and returns
// { destroy }, which stops playback, leaves fullscreen and unloads the source;
// both hosts call it when the viewer closes or switches files.

import { t } from "./i18n.js";

const ICON_PLAY = `<svg viewBox="0 0 16 16"><path d="M4 2.5v11l9-5.5z"/></svg>`;
const ICON_PAUSE = `<svg viewBox="0 0 16 16"><path d="M4 2.5h3v11H4zm5 0h3v11H9z"/></svg>`;
const ICON_SOUND = `<svg viewBox="0 0 16 16"><path d="M2 6h3l4-3.5v11L5 10H2z"/><path d="M11 5.2a4 4 0 0 1 0 5.6l-.8-.8a2.9 2.9 0 0 0 0-4z"/></svg>`;
const ICON_MUTED = `<svg viewBox="0 0 16 16"><path d="M2 6h3l4-3.5v11L5 10H2z"/><path d="m10.5 5.5 4 4-.8.8-4-4zm4 .8-4 4-.8-.8 4-4z"/></svg>`;
const ICON_ENTER_FS = `<svg viewBox="0 0 16 16"><path d="M2 2h4.5v1.5h-3v3H2zm7.5 0H14v4.5h-1.5v-3h-3zM2 9.5h1.5v3h3V14H2zm10.5 0H14V14H9.5v-1.5h3z"/></svg>`;
const ICON_EXIT_FS = `<svg viewBox="0 0 16 16"><path d="M5 2h1.5v4.5H2V5h3zm4.5 0H11v3h3v1.5H9.5zM2 9.5h4.5V14H5v-3H2zm7.5 0H14V11h-3v3H9.5z"/></svg>`;
const ICON_RESET = `<svg viewBox="0 0 16 16"><path d="M8 2.5a5.5 5.5 0 1 1-5.2 7.3l1.4-.5A4 4 0 1 0 5.2 5.2L7 7H2V2l2.1 2.1A5.5 5.5 0 0 1 8 2.5z"/></svg>`;

const SPEEDS = [0.25, 0.5, 1, 1.5, 2];

// Picture adjustments. Brightness and contrast are CSS filter percentages;
// temperature is the display white point in kelvin, so lower is warmer, as
// on a monitor's color-temperature setting. 6500K (D65) is neutral.
const ADJUSTS = [
  { key: "brightness", label: "video.brightness", min: 0, max: 200, step: 1, neutral: 100, unit: "%" },
  { key: "contrast", label: "video.contrast", min: 0, max: 200, step: 1, neutral: 100, unit: "%" },
  { key: "temperature", label: "video.temperature", min: 2500, max: 10000, step: 100, neutral: 6500, unit: "K" },
];

let filterSeq = 0;

// Per-browser picture settings, { brightness, contrast, temperature }.
const PICTURE_KEY = "mudp:video-picture";

function readPicture() {
  try {
    const parsed = JSON.parse(localStorage.getItem(PICTURE_KEY));
    return parsed && typeof parsed === "object" ? parsed : {};
  } catch (_) {
    return {};
  }
}

function writePicture(values) {
  try {
    localStorage.setItem(PICTURE_KEY, JSON.stringify(values));
  } catch (_) {}
}

function fmtTime(s) {
  if (!Number.isFinite(s) || s < 0) s = 0;
  const m = Math.floor(s / 60);
  const sec = Math.floor(s % 60);
  return `${m}:${String(sec).padStart(2, "0")}`;
}

// kelvinRgb approximates the sRGB color of a blackbody at k kelvin (Tanner
// Helland's fit of the CIE 1964 data), each channel 0..255.
function kelvinRgb(k) {
  const x = k / 100;
  const clamp = (v) => Math.min(255, Math.max(0, v));
  const r = x <= 66 ? 255 : clamp(329.698727446 * Math.pow(x - 60, -0.1332047592));
  const g = x <= 66 ? clamp(99.4708025861 * Math.log(x) - 161.1195681661) : clamp(288.1221695283 * Math.pow(x - 60, -0.0755148492));
  const b = x >= 66 ? 255 : x <= 19 ? 0 : clamp(138.5177312231 * Math.log(x - 10) - 305.0447927307);
  return [r, g, b];
}

// Per-channel gains that move the 6500K white point to k.
function temperatureGains(k) {
  const ref = kelvinRgb(6500);
  return kelvinRgb(k).map((c, i) => c / ref[i]);
}

export function mountVideoPlayer({ url, bodyEl }) {
  const filterId = `vp-wb-${++filterSeq}`;
  bodyEl.innerHTML =
    `<div class="vp-root viewer-media">` +
      // color-interpolation-filters=sRGB: scale the encoded values directly,
      // as the gains were computed, instead of in linear light.
      `<svg class="vp-defs" aria-hidden="true"><filter id="${filterId}" color-interpolation-filters="sRGB">` +
        `<feColorMatrix type="matrix" values="1 0 0 0 0 0 1 0 0 0 0 0 1 0 0 0 0 0 1 0"/>` +
      `</filter></svg>` +
      `<video class="vp-video" preload="metadata" playsinline></video>` +
      `<div class="vp-bar">` +
        `<div class="vp-row">` +
          `<button type="button" class="vp-btn vp-play" title="${t("video.play")}">${ICON_PLAY}</button>` +
          `<span class="vp-time">0:00 / 0:00</span>` +
          `<input type="range" class="vp-seek" min="0" max="0" step="any" value="0" aria-label="${t("video.seek")}" />` +
          `<button type="button" class="vp-btn vp-mute" title="${t("video.mute")}">${ICON_SOUND}</button>` +
          `<select class="vp-speed" title="${t("video.speed")}">` +
            SPEEDS.map((r) => `<option value="${r}"${r === 1 ? " selected" : ""}>${r}×</option>`).join("") +
          `</select>` +
          `<button type="button" class="vp-btn vp-fs" title="${t("video.fullscreen")}">${ICON_ENTER_FS}</button>` +
        `</div>` +
        `<div class="vp-row vp-adjust">` +
          ADJUSTS.map((a) =>
            `<label class="vp-adj vp-adj-${a.key}" title="${t("video.resetHint")}">` +
              `<span class="vp-adj-label">${t(a.label)}</span>` +
              `<input type="range" data-key="${a.key}" min="${a.min}" max="${a.max}" step="${a.step}" value="${a.neutral}" />` +
              `<span class="vp-adj-value">${a.neutral}${a.unit}</span>` +
            `</label>`,
          ).join("") +
          `<button type="button" class="vp-btn vp-reset" title="${t("video.resetPicture")}">${ICON_RESET}</button>` +
        `</div>` +
      `</div>` +
    `</div>`;

  const root = bodyEl.querySelector(".vp-root");
  const video = root.querySelector(".vp-video");
  const matrix = root.querySelector("feColorMatrix");
  const playBtn = root.querySelector(".vp-play");
  const muteBtn = root.querySelector(".vp-mute");
  const time = root.querySelector(".vp-time");
  const seek = root.querySelector(".vp-seek");
  const speed = root.querySelector(".vp-speed");
  const fsBtn = root.querySelector(".vp-fs");
  const adjInputs = [...root.querySelectorAll(".vp-adjust input")];
  let seeking = false;
  let raf = 0;

  const toggle = () => (video.paused || video.ended ? video.play().catch(() => {}) : video.pause());

  // timeupdate fires only ~4×/s, which makes the scrubber stutter on short
  // clips; follow the playhead per animation frame while playing instead.
  const sync = () => {
    if (!seeking) seek.value = video.currentTime;
    paintSeek();
    time.textContent = `${fmtTime(video.currentTime)} / ${fmtTime(video.duration)}`;
  };
  // Range inputs have no cross-browser "filled" part, so the played portion
  // is painted into the track gradient.
  const paintSeek = () => {
    const p = seek.max > 0 ? (seek.value / seek.max) * 100 : 0;
    seek.style.setProperty("--vp-track", `linear-gradient(90deg, #4c8dff ${p}%, #343845 ${p}%)`);
  };
  const tick = () => {
    sync();
    raf = video.paused ? 0 : requestAnimationFrame(tick);
  };

  video.addEventListener("loadedmetadata", () => {
    seek.max = video.duration || 0;
    sync();
  });
  video.addEventListener("play", () => {
    playBtn.innerHTML = ICON_PAUSE;
    playBtn.title = t("video.pause");
    if (!raf) raf = requestAnimationFrame(tick);
  });
  video.addEventListener("pause", () => {
    playBtn.innerHTML = ICON_PLAY;
    playBtn.title = t("video.play");
    sync();
  });
  video.addEventListener("seeked", sync);
  video.addEventListener("volumechange", () => {
    muteBtn.innerHTML = video.muted ? ICON_MUTED : ICON_SOUND;
    muteBtn.title = video.muted ? t("video.unmute") : t("video.mute");
  });
  video.addEventListener("click", toggle);

  playBtn.addEventListener("click", toggle);
  muteBtn.addEventListener("click", () => {
    video.muted = !video.muted;
  });
  seek.addEventListener("input", () => {
    seeking = true;
    video.currentTime = Number(seek.value);
    paintSeek();
    time.textContent = `${fmtTime(video.currentTime)} / ${fmtTime(video.duration)}`;
  });
  seek.addEventListener("change", () => {
    seeking = false;
  });
  speed.addEventListener("change", () => {
    video.playbackRate = Number(speed.value);
  });

  // Hidden where the API is unavailable (e.g. an iframe without
  // allowfullscreen); the hosts' own overlay fullscreen still applies there.
  fsBtn.hidden = !document.fullscreenEnabled;
  fsBtn.addEventListener("click", () => {
    if (document.fullscreenElement === root) document.exitFullscreen().catch(() => {});
    else root.requestFullscreen().catch(() => {});
  });
  // Esc leaves native fullscreen without a click, so the icon follows the event.
  const onFullscreenChange = () => {
    const on = document.fullscreenElement === root;
    fsBtn.innerHTML = on ? ICON_EXIT_FS : ICON_ENTER_FS;
    fsBtn.title = on ? t("video.exitFullscreen") : t("video.fullscreen");
  };
  document.addEventListener("fullscreenchange", onFullscreenChange);

  // The SVG white-balance pass is only referenced off neutral, so an
  // untouched player keeps the plain hardware-composited video path.
  const applyPicture = () => {
    const v = Object.fromEntries(adjInputs.map((i) => [i.dataset.key, Number(i.value)]));
    for (const input of adjInputs) {
      const a = ADJUSTS.find((x) => x.key === input.dataset.key);
      input.nextElementSibling.textContent = `${input.value}${a.unit}`;
    }
    const parts = [];
    if (v.temperature !== 6500) {
      const [r, g, b] = temperatureGains(v.temperature);
      matrix.setAttribute("values", `${r} 0 0 0 0 0 ${g} 0 0 0 0 0 ${b} 0 0 0 0 0 1 0`);
      parts.push(`url(#${filterId})`);
    }
    if (v.brightness !== 100) parts.push(`brightness(${v.brightness}%)`);
    if (v.contrast !== 100) parts.push(`contrast(${v.contrast}%)`);
    video.style.filter = parts.join(" ");
    writePicture(v);
  };
  const resetInput = (input) => {
    input.value = ADJUSTS.find((x) => x.key === input.dataset.key).neutral;
  };
  for (const input of adjInputs) {
    input.addEventListener("input", applyPicture);
    // Double-click a slider to snap just that adjustment back to neutral.
    input.addEventListener("dblclick", () => {
      resetInput(input);
      applyPicture();
    });
  }
  root.querySelector(".vp-reset").addEventListener("click", () => {
    adjInputs.forEach(resetInput);
    applyPicture();
  });
  // Restore the saved look. Assigning to a range input clamps and snaps the
  // value to its min/max/step, so a stale or hand-edited entry stays in range.
  const saved = readPicture();
  for (const input of adjInputs) {
    const value = Number(saved[input.dataset.key]);
    if (Number.isFinite(value)) input.value = value;
  }
  applyPicture();

  video.src = url;

  return {
    destroy() {
      cancelAnimationFrame(raf);
      document.removeEventListener("fullscreenchange", onFullscreenChange);
      if (document.fullscreenElement === root) document.exitFullscreen().catch(() => {});
      video.pause();
      video.removeAttribute("src");
      video.load();
    },
  };
}

// Video player for the netdisk viewer dialog (ViewerDialog.vue) and the public
// share page (share.js). The native <video controls> overlay draws a dark
// gradient over the bottom of the frame, which hides exactly the content a
// side-by-side compare clip is meant to show. This player keeps the frame
// unobstructed and puts its own control bar below it (lib/videoplayer.css).
//
// mountVideoPlayer({ url, bodyEl }) renders into bodyEl and returns
// { destroy }, which stops playback and unloads the source; both hosts call it
// when the viewer closes or switches files.

const ICON_PLAY = `<svg viewBox="0 0 16 16"><path d="M4 2.5v11l9-5.5z"/></svg>`;
const ICON_PAUSE = `<svg viewBox="0 0 16 16"><path d="M4 2.5h3v11H4zm5 0h3v11H9z"/></svg>`;
const ICON_SOUND = `<svg viewBox="0 0 16 16"><path d="M2 6h3l4-3.5v11L5 10H2z"/><path d="M11 5.2a4 4 0 0 1 0 5.6l-.8-.8a2.9 2.9 0 0 0 0-4z"/></svg>`;
const ICON_MUTED = `<svg viewBox="0 0 16 16"><path d="M2 6h3l4-3.5v11L5 10H2z"/><path d="m10.5 5.5 4 4-.8.8-4-4zm4 .8-4 4-.8-.8 4-4z"/></svg>`;

const SPEEDS = [0.25, 0.5, 1, 1.5, 2];

function fmtTime(s) {
  if (!Number.isFinite(s) || s < 0) s = 0;
  const m = Math.floor(s / 60);
  const sec = Math.floor(s % 60);
  return `${m}:${String(sec).padStart(2, "0")}`;
}

export function mountVideoPlayer({ url, bodyEl }) {
  bodyEl.innerHTML =
    `<div class="vp-root viewer-media">` +
      `<video class="vp-video" preload="metadata" playsinline></video>` +
      `<div class="vp-bar">` +
        `<button type="button" class="vp-btn vp-play" title="Play">${ICON_PLAY}</button>` +
        `<span class="vp-time">0:00 / 0:00</span>` +
        `<input type="range" class="vp-seek" min="0" max="0" step="any" value="0" aria-label="Seek" />` +
        `<button type="button" class="vp-btn vp-mute" title="Mute">${ICON_SOUND}</button>` +
        `<select class="vp-speed" title="Playback speed">` +
          SPEEDS.map((r) => `<option value="${r}"${r === 1 ? " selected" : ""}>${r}×</option>`).join("") +
        `</select>` +
      `</div>` +
    `</div>`;

  const root = bodyEl.querySelector(".vp-root");
  const video = root.querySelector(".vp-video");
  const playBtn = root.querySelector(".vp-play");
  const muteBtn = root.querySelector(".vp-mute");
  const time = root.querySelector(".vp-time");
  const seek = root.querySelector(".vp-seek");
  const speed = root.querySelector(".vp-speed");
  let seeking = false;
  let raf = 0;

  const toggle = () => (video.paused || video.ended ? video.play().catch(() => {}) : video.pause());

  // timeupdate fires only ~4×/s, which makes the scrubber stutter on short
  // clips; follow the playhead per animation frame while playing instead.
  const sync = () => {
    if (!seeking) seek.value = video.currentTime;
    time.textContent = `${fmtTime(video.currentTime)} / ${fmtTime(video.duration)}`;
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
    playBtn.title = "Pause";
    if (!raf) raf = requestAnimationFrame(tick);
  });
  video.addEventListener("pause", () => {
    playBtn.innerHTML = ICON_PLAY;
    playBtn.title = "Play";
    sync();
  });
  video.addEventListener("seeked", sync);
  video.addEventListener("volumechange", () => {
    muteBtn.innerHTML = video.muted ? ICON_MUTED : ICON_SOUND;
    muteBtn.title = video.muted ? "Unmute" : "Mute";
  });
  video.addEventListener("click", toggle);

  playBtn.addEventListener("click", toggle);
  muteBtn.addEventListener("click", () => {
    video.muted = !video.muted;
  });
  seek.addEventListener("input", () => {
    seeking = true;
    video.currentTime = Number(seek.value);
    time.textContent = `${fmtTime(video.currentTime)} / ${fmtTime(video.duration)}`;
  });
  seek.addEventListener("change", () => {
    seeking = false;
  });
  speed.addEventListener("change", () => {
    video.playbackRate = Number(speed.value);
  });

  video.src = url;

  return {
    destroy() {
      cancelAnimationFrame(raf);
      video.pause();
      video.removeAttribute("src");
      video.load();
    },
  };
}

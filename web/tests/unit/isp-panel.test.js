// @vitest-environment jsdom
// Unit coverage for the collapsible ISP parameter panel added to the shared
// raster frame viewer (openRasterFrameViewer). The contract under test:
//
//   - the panel renders as a <details> that is collapsed by default
//     (no "open" attribute — the summary must be clicked to expand);
//   - an untouched panel sends no ispX parameters (auto mode: the backend
//     runs its own defaults) — only after the first edit does frameURL carry
//     the manual parameter set;
//   - slider edits coalesce through a 250ms debounce (one server round-trip
//     per burst, not one per "input" tick);
//   - the master ISP checkbox disables the whole panel;
//   - the Gray-world AWB button fetches awb=1 and applies the returned gains;
//   - the RAW viewer seeds the panel with the board-calibrated defaults and
//     exposes the Bayer-only controls; the YUV viewer does not.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { openRasterFrameViewer, ispQuerySuffix, defaultIspPanelValues } from "../../lib/yuv.js";
import { openRawViewer } from "../../lib/raw.js";

const FAKE_BITMAP = { __fakeBitmap: true };

function makeMockCtx() {
  return {
    setTransform: () => {},
    clearRect: () => {},
    drawImage: () => {},
    imageSmoothingEnabled: true,
  };
}

let fetchMock;
let viewer;
let bodyEl;

function installFetch({ totalBytes = 1000, awbGains = null } = {}) {
  fetchMock = vi.fn(async (url) => {
    if (typeof url === "string" && url.includes("awb=1")) {
      return {
        ok: true,
        json: async () => awbGains || { gainR: 2.5, gainB: 0.75 },
      };
    }
    return {
      ok: true,
      blob: async () => new Blob(),
    };
  });
  // probeFileSize issues a Range probe; answer it with the given total size.
  vi.stubGlobal("fetch", async (url, opts) => {
    const range = opts?.headers?.Range || "";
    if (range === "bytes=0-0") {
      return {
        status: 206,
        headers: { get: (k) => (k === "Content-Range" ? `bytes 0-0/${totalBytes}` : null) },
      };
    }
    return fetchMock(url, opts);
  });
}

function frameFetchCalls() {
  return fetchMock.mock.calls.filter(([u]) => typeof u === "string" && u.includes("frame="));
}

function openViewer({ raw = false, name = "frame.yuv" } = {}) {
  bodyEl = document.createElement("div");
  document.body.appendChild(bodyEl);
  const opener = raw
    ? openRawViewer
    : (opts) => openRasterFrameViewer({
        ...opts,
        isp: true,
        frameSize: () => 1000,
        frameURL: (state) =>
          `${opts.rasterUrl}&width=${state.width}&height=${state.height}&frame=${state.frame}` +
          `&format=i420&isp=${state.isp ? 1 : 0}${ispQuerySuffix(state)}`,
        statusLabel: () => "I420",
      });
  viewer = opener({
    name,
    url: "/api/netdisk/raw?path=f",
    rasterUrl: "/api/netdisk/raster?path=f",
    bodyEl,
  });
  return viewer;
}

beforeEach(() => {
  vi.useFakeTimers();
  installFetch();
  vi.stubGlobal("createImageBitmap", vi.fn(async () => FAKE_BITMAP));
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation(() => makeMockCtx());
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  viewer?.destroy();
  viewer = null;
  bodyEl?.remove();
});

describe("ISP panel layout", () => {
  it("renders collapsed by default (no open attribute)", async () => {
    openViewer();
    await vi.runAllTimersAsync();
    const panel = bodyEl.querySelector("#rasterIspPanel");
    expect(panel).toBeTruthy();
    expect(panel.tagName).toBe("DETAILS");
    expect(panel.open).toBe(false);
    expect(panel.querySelector("summary").textContent).toContain("ISP parameters");
  });

  it("shows Bayer-only controls for RAW and hides them for YUV", async () => {
    openViewer({ raw: true, name: "RAW_8x8_12bits_RGGB_Linear.raw" });
    await vi.runAllTimersAsync();
    expect(bodyEl.querySelector("#ispBlcR")).toBeTruthy();
    expect(bodyEl.querySelector("#ispChromaNr")).toBeTruthy();
    expect(bodyEl.querySelector("#ispGray")).toBeTruthy();

    openViewer();
    await vi.runAllTimersAsync();
    expect(bodyEl.querySelector("#ispBlcR")).toBeNull();
    expect(bodyEl.querySelector("#ispChromaNr")).toBeNull();
    expect(bodyEl.querySelector("#ispGray")).toBeNull();
  });

  it("seeds RAW controls with the board-calibrated defaults", async () => {
    openViewer({ raw: true, name: "RAW_8x8_12bits_RGGB_Linear.raw" });
    await vi.runAllTimersAsync();
    expect(bodyEl.querySelector("#ispBlcR").value).toBe("39");
    expect(bodyEl.querySelector("#ispBlcGr").value).toBe("225");
    expect(bodyEl.querySelector("#ispGamma").value).toBe("2.27");
    expect(bodyEl.querySelector("#ispContrast").value).toBe("1.22");
    expect(bodyEl.querySelector("#ispCcmPreset").value).toBe("2");
  });
});

describe("manual mode + parameter transport", () => {
  it("sends no ispX params until a control is touched", async () => {
    openViewer({ raw: true, name: "RAW_8x8_12bits_RGGB_Linear.raw" });
    await vi.runAllTimersAsync();
    const auto = frameFetchCalls().at(-1)[0];
    expect(auto).toContain("&isp=1");
    expect(auto).not.toContain("ispGainR");

    const gamma = bodyEl.querySelector("#ispGamma");
    gamma.value = "2.6";
    gamma.dispatchEvent(new Event("input", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(300);

    const manual = frameFetchCalls().at(-1)[0];
    expect(manual).toContain("ispGamma=2.6");
    expect(manual).toContain("ispGainR=1.07");
    expect(manual).toContain("ispBlc=39,225,225,48");
    expect(manual).toContain("ispChromaNr=3");
    expect(manual).toContain("ispGray=0");
    expect(manual).toContain("ispCcm=");
  });

  it("coalesces a burst of slider input events into one repaint", async () => {
    openViewer();
    await vi.runAllTimersAsync();
    const before = frameFetchCalls().length;
    const gainR = bodyEl.querySelector("#ispGainR");
    for (const v of ["1.2", "1.5", "1.9", "2.4"]) {
      gainR.value = v;
      gainR.dispatchEvent(new Event("input", { bubbles: true }));
      await vi.advanceTimersByTimeAsync(50);
    }
    expect(frameFetchCalls().length).toBe(before); // still inside the debounce
    await vi.advanceTimersByTimeAsync(300);
    expect(frameFetchCalls().length).toBe(before + 1); // one trailing request
    expect(frameFetchCalls().at(-1)[0]).toContain("ispGainR=2.4");
    // Live readout updated on every tick, not only on commit.
    expect(bodyEl.querySelector("#ispGainRVal").textContent).toBe("2.40");
  });

  it("CCM preset select swaps the matrix; manual entries are sent", async () => {
    openViewer();
    await vi.runAllTimersAsync();
    const preset = bodyEl.querySelector("#ispCcmPreset");
    const matrixRow = bodyEl.querySelector("#ispCcmMatrixRow");
    expect(matrixRow.hidden).toBe(true); // identity default, not manual

    preset.value = "0"; // Manual
    preset.dispatchEvent(new Event("change", { bubbles: true }));
    expect(matrixRow.hidden).toBe(false);

    const cell = bodyEl.querySelector("#ispCcm0");
    cell.value = "1.5";
    cell.dispatchEvent(new Event("change", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(300);
    const url = frameFetchCalls().at(-1)[0];
    expect(url).toMatch(/ispCcm=1\.5,/);
  });

  it("sharpen checkbox off sends ispSharpen=0 and disables its sliders", async () => {
    openViewer();
    await vi.runAllTimersAsync();
    const on = bodyEl.querySelector("#ispSharpenOn");
    expect(on.checked).toBe(true);
    on.checked = false;
    on.dispatchEvent(new Event("change", { bubbles: true }));
    expect(bodyEl.querySelector("#ispSharpen").disabled).toBe(true);
    expect(bodyEl.querySelector("#ispSharpenRadius").disabled).toBe(true);
    await vi.advanceTimersByTimeAsync(300);
    expect(frameFetchCalls().at(-1)[0]).toContain("ispSharpen=0");
  });

  it("Reset returns to auto mode (no ispX params on the wire)", async () => {
    openViewer();
    await vi.runAllTimersAsync();
    const gainR = bodyEl.querySelector("#ispGainR");
    gainR.value = "4";
    gainR.dispatchEvent(new Event("input", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(300);
    expect(frameFetchCalls().at(-1)[0]).toContain("ispGainR=4");

    bodyEl.querySelector("#ispResetBtn").click();
    await vi.advanceTimersByTimeAsync(300);
    expect(frameFetchCalls().at(-1)[0]).not.toContain("ispGainR");
    expect(gainR.value).toBe("1"); // YUV neutral default
  });
});

describe("master ISP toggle interplay", () => {
  it("disables every panel control while ISP is off", async () => {
    openViewer({ raw: true, name: "RAW_8x8_12bits_RGGB_Linear.raw" });
    await vi.runAllTimersAsync();
    const master = bodyEl.querySelector("#rasterIsp");
    expect(master.checked).toBe(true); // RAW defaults ISP on
    expect(bodyEl.querySelector("#ispGamma").disabled).toBe(false);

    master.checked = false;
    master.dispatchEvent(new Event("change", { bubbles: true }));
    for (const el of bodyEl.querySelectorAll("#rasterIspPanel input, #rasterIspPanel select, #rasterIspPanel button")) {
      expect(el.disabled).toBe(true);
    }
  });
});

describe("gray-world AWB button", () => {
  it("fetches awb=1 and applies the returned gains", async () => {
    openViewer({ raw: true, name: "RAW_8x8_12bits_RGGB_Linear.raw" });
    await vi.runAllTimersAsync();
    bodyEl.querySelector("#ispAwbBtn").click();
    await vi.advanceTimersByTimeAsync(300);
    const awbCall = fetchMock.mock.calls.find(([u]) => u.includes("awb=1"));
    expect(awbCall).toBeTruthy();
    // The gains from the server land in state and on the next frame request.
    expect(bodyEl.querySelector("#ispGainR").value).toBe("2.5");
    expect(bodyEl.querySelector("#ispGainB").value).toBe("0.75");
    await vi.advanceTimersByTimeAsync(300);
    expect(frameFetchCalls().at(-1)[0]).toContain("ispGainR=2.5");
  });
});

describe("defaultIspPanelValues / ispQuerySuffix", () => {
  it("RAW defaults mirror the board calibration", () => {
    const p = defaultIspPanelValues(true);
    expect(p.blc).toEqual([39, 225, 225, 48]);
    expect(p.gamma).toBe(2.27);
    expect(p.ccmPreset).toBe(2);
    expect(p.sharpenOn).toBe(true);
    expect(p.chromaSupp).toBe(60);
  });

  it("auto mode yields an empty suffix; manual yields every key", () => {
    const autoState = { ispManual: false };
    expect(ispQuerySuffix(autoState)).toBe("");

    const state = {
      ispManual: true,
      rawMode: false,
      ispParams: {
        ...defaultIspPanelValues(false),
        gainR: 2,
        ccm: [...Array(9).keys()],
      },
    };
    const suffix = ispQuerySuffix(state);
    expect(suffix).toContain("ispGainR=2");
    expect(suffix).toContain("ispCcm=0,1,2,3,4,5,6,7,8");
    expect(suffix).not.toContain("ispBlc="); // YUV hides Bayer-only stages
    expect(suffix).not.toContain("ispGray=");

    const rawState = { ...state, rawMode: true, ispParams: { ...state.ispParams, blc: [1, 2, 3, 4], gray: true } };
    expect(ispQuerySuffix(rawState)).toContain("ispBlc=1,2,3,4");
    expect(ispQuerySuffix(rawState)).toContain("ispGray=1");
  });
});

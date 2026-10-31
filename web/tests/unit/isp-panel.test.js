// @vitest-environment jsdom
// Unit coverage for the collapsible ISP parameter panel added to the shared
// raster frame viewer (openRasterFrameViewer). The contract under test:
//
//   - the panel renders as a <details> that is collapsed by default
//     (no "open" attribute — the summary must be clicked to expand);
//   - an untouched panel sends no ispX parameters (auto mode: the backend
//     resolves the file context — ISO tier, sidecar black level, frame-0 AWB)
//     — only after the first edit does frameURL carry the manual parameter
//     set;
//   - the X-Isp-Resolved header on a RAW response re-seeds the panel with
//     the backend's resolved parameters while still in auto mode;
//   - slider edits coalesce through a 250ms debounce (one server round-trip
//     per burst, not one per "input" tick);
//   - the master ISP checkbox disables the whole panel, and the DRC toggle
//     disables its sliders;
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

// installFetch stubs the raster endpoint. resolvedHeader (optional) is served
// as the X-Isp-Resolved header on frame fetches — what the backend reports
// after resolving the RAW file context.
function installFetch({ totalBytes = 1000, awbGains = null, resolvedHeader = null } = {}) {
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
      headers: { get: (k) => (k === "X-Isp-Resolved" ? resolvedHeader : null) },
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
    expect(bodyEl.querySelector("#ispDgain")).toBeTruthy();
    expect(bodyEl.querySelector("#ispDrcOn")).toBeTruthy();
    expect(bodyEl.querySelector("#ispDrcCompress")).toBeTruthy();
    expect(bodyEl.querySelector("#ispLumaNr")).toBeTruthy();
    expect(bodyEl.querySelector("#ispChromaMap")).toBeTruthy();

    openViewer();
    await vi.runAllTimersAsync();
    expect(bodyEl.querySelector("#ispBlcR")).toBeNull();
    expect(bodyEl.querySelector("#ispChromaNr")).toBeNull();
    expect(bodyEl.querySelector("#ispGray")).toBeNull();
    expect(bodyEl.querySelector("#ispDgain")).toBeNull();
    expect(bodyEl.querySelector("#ispDrcOn")).toBeNull();
    expect(bodyEl.querySelector("#ispDrcCompress")).toBeNull();
    expect(bodyEl.querySelector("#ispLumaNr")).toBeNull();
    expect(bodyEl.querySelector("#ispChromaMap")).toBeNull();
  });

  it("seeds RAW controls with the board-calibrated defaults", async () => {
    openViewer({ raw: true, name: "RAW_8x8_12bits_RGGB_Linear.raw" });
    await vi.runAllTimersAsync();
    expect(bodyEl.querySelector("#ispBlcR").value).toBe("0");
    expect(bodyEl.querySelector("#ispGamma").value).toBe("1.43");
    expect(bodyEl.querySelector("#ispContrast").value).toBe("0.985");
    // The fit matrix is shown in manual form (preset 0).
    expect(bodyEl.querySelector("#ispCcmPreset").value).toBe("0");
    expect(bodyEl.querySelector("#ispDgain").value).toBe("1");
    expect(bodyEl.querySelector("#ispChromaNr").value).toBe("5");
    expect(bodyEl.querySelector("#ispLumaNr").value).toBe("0.1");
    expect(bodyEl.querySelector("#ispDrcOn").checked).toBe(true);
    expect(bodyEl.querySelector("#ispChromaMap").checked).toBe(true);
    expect(bodyEl.querySelector("#ispSharpen").value).toBe("1.18");
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
    expect(manual).toContain("ispGainR=1.51");
    expect(manual).toContain("ispBlc=0,0,0,0");
    expect(manual).toContain("ispChromaNr=5");
    expect(manual).toContain("ispGray=0");
    expect(manual).toContain("ispDgain=1");
    expect(manual).toContain("ispDrcOn=1");
    expect(manual).toContain("ispLumaNr=0.1");
    expect(manual).toContain("ispChromaMap=1");
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

  it("DRC toggle off sends ispDrcOn=0 and disables its sliders", async () => {
    openViewer({ raw: true, name: "RAW_8x8_12bits_RGGB_Linear.raw" });
    await vi.runAllTimersAsync();
    const on = bodyEl.querySelector("#ispDrcOn");
    expect(on.checked).toBe(true);
    expect(bodyEl.querySelector("#ispDrcCompress").disabled).toBe(false);
    on.checked = false;
    on.dispatchEvent(new Event("change", { bubbles: true }));
    expect(bodyEl.querySelector("#ispDrcCompress").disabled).toBe(true);
    expect(bodyEl.querySelector("#ispDrcRadius").disabled).toBe(true);
    await vi.advanceTimersByTimeAsync(300);
    expect(frameFetchCalls().at(-1)[0]).toContain("ispDrcOn=0");

    // Reset restores the defaults and drops back to auto mode.
    bodyEl.querySelector("#ispResetBtn").click();
    await vi.advanceTimersByTimeAsync(300);
    expect(bodyEl.querySelector("#ispDrcOn").checked).toBe(true);
    expect(bodyEl.querySelector("#ispDrcCompress").disabled).toBe(false);
    expect(frameFetchCalls().at(-1)[0]).not.toContain("ispDrcOn");
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

describe("resolved-parameter sync (file context)", () => {
  it("re-seeds the panel from the X-Isp-Resolved header in auto mode", async () => {
    const resolved = {
      iso: 155629, dgain: 2, blc: [64, 68, 68, 60], blcFrom: "sidecar",
      gainR: 1.9, gainB: 2.1, gamma: 1.43, saturation: 0.64, contrast: 0.985,
      brightness: -12.29, drcOn: true, drcCompress: 0.646, drcDetail: 0.727,
      drcRadius: 32, drcPivot: 0.214, drcEdge: 4, lumaNr: 19.2, chromaNr: 6,
      chromaMapOn: true, sharpenOn: true, sharpen: 0.48,
    };
    installFetch({ resolvedHeader: JSON.stringify(resolved) });
    openViewer({ raw: true, name: "RAW_8x8_12bits_RGGB_Linear.raw" });
    await vi.runAllTimersAsync();

    // The panel mirrors the backend's resolved values (auto mode, no ispX on
    // the wire yet).
    expect(bodyEl.querySelector("#ispIsoReadout").textContent).toBe("ISO 155629");
    expect(bodyEl.querySelector("#ispBlcR").value).toBe("64");
    expect(bodyEl.querySelector("#ispBlcB").value).toBe("60");
    expect(bodyEl.querySelector("#ispDgain").value).toBe("2");
    expect(bodyEl.querySelector("#ispLumaNr").value).toBe("19.2");
    expect(bodyEl.querySelector("#ispChromaNr").value).toBe("6");
    expect(bodyEl.querySelector("#ispGainR").value).toBe("1.9");
    expect(bodyEl.querySelector("#ispDrcCompress").value).toBe("0.646");
    expect(bodyEl.querySelector("#ispSharpen").value).toBe("0.48");
    expect(bodyEl.querySelector("#ispCcm0").value).toBeTruthy();
    expect(frameFetchCalls().at(-1)[0]).not.toContain("ispGainR");

    // The first edit carries the resolved values for everything else.
    const gamma = bodyEl.querySelector("#ispGamma");
    gamma.value = "2.6";
    gamma.dispatchEvent(new Event("input", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(300);
    const manual = frameFetchCalls().at(-1)[0];
    expect(manual).toContain("ispGamma=2.6");
    expect(manual).toContain("ispGainR=1.9");
    expect(manual).toContain("ispBlc=64,68,68,60");
    expect(manual).toContain("ispDgain=2");
    expect(manual).toContain("ispLumaNr=19.2");
  });

  it("does not clobber manual edits with later resolved headers", async () => {
    const resolved = { iso: 4651, gainR: 1.7, gamma: 1.43 };
    installFetch({ resolvedHeader: JSON.stringify(resolved) });
    openViewer({ raw: true, name: "RAW_8x8_12bits_RGGB_Linear.raw" });
    await vi.runAllTimersAsync();

    const gamma = bodyEl.querySelector("#ispGamma");
    gamma.value = "2.6";
    gamma.dispatchEvent(new Event("input", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(300);
    // A frame change refetches with the same resolved header — the user's
    // gamma must survive it.
    viewer.state.frame = 0;
    viewer.repaint();
    await vi.runAllTimersAsync();
    expect(bodyEl.querySelector("#ispGamma").value).toBe("2.6");
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
    expect(p.blc).toEqual([0, 0, 0, 0]);
    expect(p.gamma).toBe(1.43);
    expect(p.saturation).toBe(2.43);
    expect(p.contrast).toBe(0.985);
    expect(p.ccmPreset).toBe(0);
    expect(p.sharpenOn).toBe(true);
    expect(p.sharpen).toBe(1.18);
    expect(p.chromaSupp).toBe(0);
    expect(p.drcOn).toBe(true);
    expect(p.drcCompress).toBe(0.891);
    expect(p.drcDetail).toBe(1.13);
    expect(p.drcRadius).toBe(96);
    expect(p.drcPivot).toBe(0.214);
    expect(p.drcEdge).toBe(4);
    expect(p.lumaNr).toBe(0.1);
    expect(p.chromaNr).toBe(5);
    expect(p.chromaMapOn).toBe(true);
    expect(p.dgain).toBe(1);
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
    expect(suffix).not.toContain("ispDgain=");
    expect(suffix).not.toContain("ispDrcOn=");

    const rawState = {
      ...state,
      rawMode: true,
      ispParams: {
        ...state.ispParams,
        blc: [1, 2, 3, 4],
        gray: true,
        dgain: 2.5,
        drcOn: false,
        drcCompress: 0.7,
        drcDetail: 1.4,
        drcRadius: 48,
        drcPivot: 0.1,
        drcEdge: 2.5,
        lumaNr: 12.5,
        chromaNr: 6,
        chromaMapOn: false,
      },
    };
    const rawSuffix = ispQuerySuffix(rawState);
    expect(rawSuffix).toContain("ispBlc=1,2,3,4");
    expect(rawSuffix).toContain("ispGray=1");
    expect(rawSuffix).toContain("ispDgain=2.5");
    expect(rawSuffix).toContain("ispDrcOn=0");
    expect(rawSuffix).toContain("ispDrcCompress=0.7");
    expect(rawSuffix).toContain("ispDrcDetail=1.4");
    expect(rawSuffix).toContain("ispDrcRadius=48");
    expect(rawSuffix).toContain("ispDrcPivot=0.1");
    expect(rawSuffix).toContain("ispDrcEdge=2.5");
    expect(rawSuffix).toContain("ispLumaNr=12.5");
    expect(rawSuffix).toContain("ispChromaNr=6");
    expect(rawSuffix).toContain("ispChromaMap=0");
  });
});

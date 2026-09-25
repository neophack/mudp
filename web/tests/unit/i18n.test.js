// @vitest-environment jsdom
import { describe, it, expect, vi } from "vitest";
import { t, switchLanguage, getCurrentLanguage, LANG_CHINESE, LANG_ENGLISH } from "../../lib/i18n.js";

// The optimization pass added several user-facing keys (netdisk partial
// delete results, unhealthy legend, pull validation). These pin their
// presence in BOTH dictionaries and the {placeholder} interpolation the
// message builders rely on, so a typo in one language cannot ship silently.
describe("t() dictionary coverage and interpolation", () => {
  vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true })));

  it("renders the newly added keys in both languages", async () => {
    await switchLanguage(LANG_CHINESE);
    expect(getCurrentLanguage()).toBe(LANG_CHINESE);
    expect(t("containers.filterUnhealthy")).toBe("异常");
    expect(t("containers.loadFailed")).toContain("加载失败");
    expect(t("images.refRequired")).toContain("镜像引用");
    expect(t("netdisk.shareDeleteConfirm", { name: "相册" })).toContain("相册");

    await switchLanguage(LANG_ENGLISH);
    expect(getCurrentLanguage()).toBe(LANG_ENGLISH);
    expect(t("containers.filterUnhealthy")).toBe("Unhealthy");
    expect(t("containers.loadFailed")).toContain("Failed to load");
    expect(t("images.refRequired")).toContain("image reference");
    expect(t("netdisk.shareDeleteConfirm", { name: "album" })).toContain("album");
  });

  it("interpolates {named} placeholders for the partial-delete toast", async () => {
    await switchLanguage(LANG_ENGLISH);
    expect(t("netdisk.deletedNFailed", { ok: 2, err: 1 })).toBe("2 deleted, 1 failed");
    await switchLanguage(LANG_CHINESE);
    expect(t("netdisk.deletedNFailed", { ok: 2, err: 1 })).toBe("已删除 2 项，1 项失败");
  });

  it("falls back to the caller-supplied default for unknown keys", async () => {
    await switchLanguage(LANG_ENGLISH);
    expect(t("totally.bogus.key", "fallback {x}", { x: 7 })).toBe("fallback 7");
    expect(t("totally.bogus.key")).toBe("totally.bogus.key");
  });
});

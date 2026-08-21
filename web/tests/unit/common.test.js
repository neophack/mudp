import { describe, it, expect } from "vitest";
import {
  roleRank,
  isAdminUser,
  canMutateUser,
  escapeHtml,
  fmtBytes,
  fmtMB,
  formatDate,
} from "../../lib/common.js";

describe("role helpers", () => {
  it("returns correct ranks", () => {
    expect(roleRank("admin")).toBe(50);
    expect(roleRank("operator")).toBe(40);
    expect(roleRank("user")).toBe(30);
    expect(roleRank("helpdesk")).toBe(20);
    expect(roleRank("readonly")).toBe(10);
    expect(roleRank("unknown")).toBe(0);
  });

  it("detects admin users", () => {
    expect(isAdminUser({ role: "admin" })).toBe(true);
    expect(isAdminUser({ role: "operator" })).toBe(false);
    expect(isAdminUser(null)).toBe(false);
  });

  it("detects mutating roles", () => {
    expect(canMutateUser({ role: "admin" })).toBe(true);
    expect(canMutateUser({ role: "operator" })).toBe(true);
    expect(canMutateUser({ role: "user" })).toBe(true);
    expect(canMutateUser({ role: "helpdesk" })).toBe(false);
    expect(canMutateUser({ role: "readonly" })).toBe(false);
    expect(canMutateUser(null)).toBe(false);
  });
});

describe("escapeHtml", () => {
  it("escapes HTML special characters", () => {
    expect(escapeHtml("<script>alert('xss')</script>")).toBe(
      "&lt;script&gt;alert(&#39;xss&#39;)&lt;/script&gt;"
    );
  });

  it("handles null/undefined", () => {
    expect(escapeHtml(null)).toBe("");
    expect(escapeHtml(undefined)).toBe("");
  });

  it("leaves safe text unchanged", () => {
    expect(escapeHtml("hello world 123")).toBe("hello world 123");
  });
});

describe("fmtBytes", () => {
  it("formats bytes through units", () => {
    expect(fmtBytes(0)).toBe("0 B");
    expect(fmtBytes(512)).toBe("512 B");
    expect(fmtBytes(1024)).toBe("1.00 KB");
    expect(fmtBytes(1024 * 1024 * 1.5)).toBe("1.50 MB");
    expect(fmtBytes(1024 * 1024 * 1024)).toBe("1.00 GB");
  });

  it("handles invalid input", () => {
    expect(fmtBytes(null)).toBe("-");
    expect(fmtBytes(-1)).toBe("-");
    expect(fmtBytes(Number.NaN)).toBe("-");
  });
});

describe("fmtMB", () => {
  it("formats megabytes", () => {
    expect(fmtMB(0)).toBe("0 MB");
    expect(fmtMB(512)).toBe("512 MB");
    expect(fmtMB(1024)).toBe("1.00 GB");
  });

  it("handles invalid input", () => {
    expect(fmtMB(null)).toBe("-");
    expect(fmtMB(Number.NaN)).toBe("-");
  });
});



describe("formatDate", () => {
  it("formats ISO dates", () => {
    expect(formatDate("2024-01-15T08:30:00Z")).toContain("2024");
  });

  it("handles missing input", () => {
    expect(formatDate("")).toBe("-");
    expect(formatDate(null)).toBe("-");
  });
});

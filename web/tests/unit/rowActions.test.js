import { describe, it, expect } from "vitest";
import { actionsWidth } from "@/rowActions";

describe("actionsWidth", () => {
  it("never drops below the width the column title needs", () => {
    expect(actionsWidth(0)).toBe(76);
    expect(actionsWidth(1)).toBe(76);
    expect(actionsWidth(2)).toBe(78);
  });

  it("grows with the button count", () => {
    expect(actionsWidth(10)).toBe(10 * 26 + 9 * 2 + 24);
  });
});

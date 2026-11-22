import { describe, expect, it } from "vitest";
import { clock, hourTicks, minutesBetween, pct, relative } from "./time";

describe("time helpers", () => {
  it("formats in the facility zone (+05:30)", () => {
    expect(clock("2026-08-04T04:25:00Z")).toBe("09:55");
  });
  it("places and clamps positions on the window", () => {
    expect(pct(50, 0, 100)).toBe(50);
    expect(pct(-10, 0, 100)).toBe(0);
    expect(pct(150, 0, 100)).toBe(100);
    expect(pct(5, 10, 10)).toBe(0);
  });
  it("ticks on whole hours", () => {
    const start = Date.parse("2026-08-04T04:25:00Z");
    const ticks = hourTicks(start, start + 3 * 3_600_000);
    expect(ticks.map((t) => new Date(t).toISOString())).toEqual([
      "2026-08-04T05:00:00.000Z",
      "2026-08-04T06:00:00.000Z",
      "2026-08-04T07:00:00.000Z",
    ]);
  });
  it("describes relative times", () => {
    expect(relative(10 * 60_000, 0)).toBe("in 10 min");
    expect(relative(0, 3 * 60_000)).toBe("3 min ago");
    expect(relative(20_000, 0)).toBe("now");
    expect(minutesBetween("2026-08-04T04:25:00Z", "2026-08-04T05:10:00Z")).toBe(45);
  });
});

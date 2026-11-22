import { describe, expect, it } from "vitest";
import { mergeBlocks } from "./Timeline";

const b = (kind: string, start: string, end?: string) => ({ dockId: "D", kind, reason: "r", start, end });

describe("mergeBlocks", () => {
  it("merges blocks that touch or overlap and keeps separate ones apart", () => {
    const end = Date.parse("2026-08-04T18:00:00Z");
    const bands = mergeBlocks(
      [
        b("SLOT_BLOCKED", "2026-08-04T06:00:00Z", "2026-08-04T07:00:00Z"),
        b("BREAKDOWN", "2026-08-04T04:00:00Z", "2026-08-04T06:00:00Z"),
        b("SLOT_BLOCKED", "2026-08-04T06:30:00Z", "2026-08-04T07:30:00Z"),
        b("MAINTENANCE", "2026-08-04T12:30:00Z"),
      ],
      end,
    );
    expect(bands).toHaveLength(2);
    expect(bands[0].labels).toEqual(["Breakdown", "Slot blocked"]);
    expect(new Date(bands[0].end).toISOString()).toBe("2026-08-04T07:30:00.000Z");
    expect(bands[1].end).toBe(end);
  });
});

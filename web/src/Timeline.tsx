import type { Block, Board, Booking } from "./api";
import { clock, hourTicks, pct } from "./time";

interface Props {
  board: Board;
  now: number;
  selected?: string;
  onSelect: (b: Booking) => void;
}

const BLOCK_LABEL: Record<string, string> = {
  BREAKDOWN: "Breakdown",
  MAINTENANCE: "Maintenance",
  CAPACITY_REDUCTION: "Capacity reduced",
  SLOT_BLOCKED: "Slot blocked",
  SLOT_CLOSED: "Slot closed",
};

export interface Band {
  start: number;
  end: number;
  labels: string[];
  reasons: string[];
}

/** Merges a dock's blocks that touch or overlap into bands, so one stretch shows one label. */
export function mergeBlocks(blocks: Block[], windowEnd: number): Band[] {
  const sorted = blocks
    .map((b) => ({ start: Date.parse(b.start), end: b.end ? Date.parse(b.end) : windowEnd, b }))
    .sort((x, y) => x.start - y.start);
  const out: Band[] = [];
  for (const { start, end, b } of sorted) {
    const label = BLOCK_LABEL[b.kind] ?? b.kind;
    const reason = `${label}: ${b.reason}`.replace(/: $/, "");
    const last = out[out.length - 1];
    if (last && start <= last.end) {
      last.end = Math.max(last.end, end);
      if (!last.labels.includes(label)) last.labels.push(label);
      if (!last.reasons.includes(reason)) last.reasons.push(reason);
    } else {
      out.push({ start, end, labels: [label], reasons: [reason] });
    }
  }
  return out;
}

/** One row per dock across the planning window; bookings as bars, blocks hatched, a line at now. */
export function Timeline({ board, now, selected, onSelect }: Props) {
  const start = Date.parse(board.windowStart);
  const end = Date.parse(board.windowEnd);
  const ticks = hourTicks(start, end);
  const nowPct = pct(now, start, end);

  return (
    <div className="timeline" role="table" aria-label="Dock sequence">
      <div className="tl-row tl-axis" role="row">
        <div className="tl-label" role="columnheader">
          Dock
        </div>
        <div className="tl-lane" role="columnheader">
          {ticks.map((t) => (
            <span key={t} className="tl-tick" style={{ left: `${pct(t, start, end)}%` }}>
              {clock(t)}
            </span>
          ))}
        </div>
      </div>
      {board.docks.map((d) => {
        const bookings = board.bookings.filter((b) => b.dockId === d.dockId);
        const blocks = board.blocks.filter((b) => b.dockId === d.dockId);
        return (
          <div className="tl-row" role="row" key={d.dockId}>
            <div className="tl-label" role="rowheader">
              <strong>{d.dockCode}</strong>
              <span className={`dock-type t-${d.dockType.toLowerCase()}`}>{d.dockType}</span>
              {d.dockStatus !== "ACTIVE" && <span className="dock-status">{d.dockStatus}</span>}
              <span className="dock-meta">≤ {(d.maxWeightKg / 1000).toFixed(0)} t</span>
            </div>
            <div className="tl-lane" role="cell">
              {ticks.map((t) => (
                <span key={t} className="tl-grid" style={{ left: `${pct(t, start, end)}%` }} />
              ))}
              {mergeBlocks(blocks, end).map((band) => {
                const s = pct(band.start, start, end);
                const e = pct(band.end, start, end);
                return (
                  <div
                    key={band.start}
                    className="tl-block"
                    style={{ left: `${s}%`, width: `${Math.max(0.4, e - s)}%` }}
                    title={`${clock(band.start)}–${clock(band.end)}\n${band.reasons.join("\n")}`}
                  >
                    {e - s > 6 && <span>{band.labels.join(" · ")}</span>}
                  </div>
                );
              })}
              {bookings.map((b) => {
                const s = pct(Date.parse(b.slotStart), start, end);
                const e = pct(Date.parse(b.slotEnd), start, end);
                const hold = b.status === "PENDING_CONFIRMATION";
                const cls = [
                  "tl-bar",
                  `p-${b.priorityCode.toLowerCase()}`,
                  hold ? "is-hold" : "",
                  b.flagged ? "is-flagged" : "",
                  b.status === "IN_PROGRESS" ? "is-inprogress" : "",
                  selected === b.appointmentId ? "is-selected" : "",
                ].join(" ");
                return (
                  <button
                    key={b.appointmentId}
                    className={cls}
                    style={{ left: `${s}%`, width: `${Math.max(1.2, e - s)}%` }}
                    onClick={() => onSelect(b)}
                    title={`${b.shipmentId} · ${b.priorityCode} · ${clock(b.slotStart)}–${clock(b.slotEnd)}${hold ? " · driver hold" : ""}${b.flagged ? " · flagged" : ""}`}
                  >
                    <span className="bar-id">{b.shipmentId.replace(/^SHP-?/, "")}</span>
                    {b.predictedWaitMin != null && b.predictedWaitMin > 0 && (
                      <span className="bar-wait">{b.predictedWaitMin}′</span>
                    )}
                  </button>
                );
              })}
              <span className="tl-now" style={{ left: `${nowPct}%` }} aria-hidden="true" />
            </div>
          </div>
        );
      })}
    </div>
  );
}

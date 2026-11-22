import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { api, type Capacity as CapacityData, type CapacitySlot } from "./api";
import { Field } from "./Shipments";
import { clock, date, fromIstInput, toIstInput } from "./time";

const DOCK_STATUSES = ["ACTIVE", "MAINTENANCE", "OUT_OF_SERVICE"];
const EVENT_TYPES = ["BREAKDOWN", "MAINTENANCE", "CAPACITY_REDUCTION", "MANUAL_BLOCK"];
const HOURS_SHOWN = 12;
const words = (s: string) => s.replaceAll("_", " ").toLowerCase();

/** Docks in or out of service, dock events and slot blocks. Every change re-plans the facility. */
export function Capacity({ now, onChanged }: { now: number; onChanged: () => void }) {
  const [data, setData] = useState<CapacityData | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [slot, setSlot] = useState<CapacitySlot | null>(null);

  const load = useCallback(async () => {
    try {
      setData(await api.capacity());
      setError(null);
    } catch (err) {
      setError((err as Error).message);
    }
  }, []);

  useEffect(() => {
    load();
    const poll = setInterval(load, 10_000);
    return () => clearInterval(poll);
  }, [load]);

  async function run(action: () => Promise<unknown>, message: string) {
    setBusy(true);
    setError(null);
    try {
      await action();
      setNote(message);
      await load();
      onChanged();
      return true;
    } catch (err) {
      setError((err as Error).message);
      return false;
    } finally {
      setBusy(false);
    }
  }

  const code = (dockId: string) => data?.docks.find((d) => d.dockId === dockId)?.dockCode ?? dockId;

  // Slots in the next HOURS_SHOWN hours, by dock; a slot overlapping a dock event is unavailable to the solver.
  const lanes = useMemo(() => {
    if (!data) return [];
    const until = now + HOURS_SHOWN * 3_600_000;
    return data.docks.map((d) => ({
      dock: d,
      slots: data.slots
        .filter((s) => s.dockId === d.dockId && Date.parse(s.end) > now && Date.parse(s.start) < until)
        .map((s) => ({
          slot: s,
          event: data.events.find(
            (e) => e.dockId === d.dockId && Date.parse(e.start) < Date.parse(s.end) && (!e.end || Date.parse(e.end) > Date.parse(s.start)),
          ),
        })),
    }));
  }, [data, now]);

  return (
    <section className="page" aria-label="Dock capacity">
      <div className="section-head">
        <h2>Dock capacity</h2>
      </div>
      <p className="hint">
        Take a dock out of service, record a breakdown or maintenance window, or block a single slot. Any open slot that overlaps a dock
        event (including a capacity reduction) is unavailable. Each change re-plans the facility; bookings that no longer fit are moved or
        listed as exceptions.
      </p>
      {note && <p className="note">{note}</p>}
      {error && <p className="error">{error}</p>}
      {!data ? (
        <p className="empty">Loading…</p>
      ) : (
        <div className="capacity">
          <div className="card">
            <h3>Docks</h3>
            <table className="grid">
              <thead>
                <tr>
                  <th>Dock</th>
                  <th>Type</th>
                  <th className="num">Max weight</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                {data.docks.map((d) => (
                  <tr key={d.dockId}>
                    <td>
                      <strong>{d.dockCode}</strong>
                    </td>
                    <td>
                      {d.dockType}
                      {d.refrigerated && <span className="li-meta"> · reefer</span>}
                    </td>
                    <td className="num">{(d.maxWeightKg / 1000).toFixed(0)} t</td>
                    <td>
                      <select
                        aria-label={`Status of ${d.dockCode}`}
                        value={d.dockStatus}
                        disabled={busy}
                        onChange={(e) =>
                          run(
                            () => api.setDockStatus(d.dockId, e.target.value),
                            `${d.dockCode} set to ${words(e.target.value)}; re-planning.`,
                          )
                        }
                      >
                        {DOCK_STATUSES.map((s) => (
                          <option key={s} value={s}>
                            {words(s)}
                          </option>
                        ))}
                      </select>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <div className="card">
            <h3>Dock events</h3>
            {data.events.length === 0 ? (
              <p className="empty">No current or upcoming dock events.</p>
            ) : (
              <ul className="list">
                {data.events.map((e, i) => (
                  <li key={i} className="list-item">
                    <div className="li-head">
                      <strong>
                        {code(e.dockId)} · {words(e.kind)}
                      </strong>
                      <span className="li-time">
                        {date(e.start)} {clock(e.start)}–{e.end ? clock(e.end) : "open-ended"}
                      </span>
                    </div>
                    <div className="li-body">{e.reason}</div>
                  </li>
                ))}
              </ul>
            )}
            <NewEvent
              docks={data.docks.map((d) => [d.dockId, d.dockCode])}
              now={now}
              busy={busy}
              onSubmit={(e, label) => run(() => api.addDockEvent(e), `${label} recorded; re-planning.`)}
            />
          </div>

          <div className="card capacity-slots">
            <h3>Slots, next {HOURS_SHOWN} hours</h3>
            <div className="legend">
              <span className="lg">
                <i className="sw slot-free" />
                open
              </span>
              <span className="lg">
                <i className="sw slot-booked" />
                booked
              </span>
              <span className="lg">
                <i className="sw slot-event" />
                dock event
              </span>
              <span className="lg">
                <i className="sw sw-block" />
                blocked
              </span>
            </div>
            {lanes.map(({ dock, slots }) => (
              <div key={dock.dockId} className="slot-lane">
                <span className="slot-dock">{dock.dockCode}</span>
                <div className="slot-chips">
                  {slots.map(({ slot: s, event }) => {
                    const state = s.status === "BLOCKED" ? "blocked" : event ? "event" : s.shipmentId ? "booked" : "free";
                    return (
                      <button
                        key={s.slotId}
                        className={`slot-chip slot-${state} ${slot?.slotId === s.slotId ? "is-active" : ""}`}
                        title={`${clock(s.start)}–${clock(s.end)} · ${s.reason ?? event?.reason ?? s.shipmentId ?? "open"}`}
                        onClick={() => setSlot(s)}
                      >
                        {clock(s.start)}
                      </button>
                    );
                  })}
                  {slots.length === 0 && <span className="li-meta">no slots</span>}
                </div>
              </div>
            ))}
            {slot && (
              <SlotEditor
                key={slot.slotId}
                slot={slot}
                dockCode={code(slot.dockId)}
                busy={busy}
                onClose={() => setSlot(null)}
                onSubmit={async (status, reason) => {
                  const label = `${code(slot.dockId)} ${clock(slot.start)}`;
                  if (
                    await run(
                      () => api.setSlotStatus(slot.slotId, status, reason),
                      `Slot ${label} ${status === "BLOCKED" ? "blocked" : "reopened"}; re-planning.`,
                    )
                  ) {
                    setSlot(null);
                  }
                }}
              />
            )}
          </div>
        </div>
      )}
    </section>
  );
}

function NewEvent({
  docks,
  now,
  busy,
  onSubmit,
}: {
  docks: [string, string][];
  now: number;
  busy: boolean;
  onSubmit: (e: { dockId: string; type: string; start: string; end?: string; reason: string }, label: string) => void;
}) {
  const [dockId, setDockId] = useState(docks[0]?.[0] ?? "");
  const [type, setType] = useState("BREAKDOWN");
  const [start, setStart] = useState(toIstInput(now));
  const [end, setEnd] = useState(toIstInput(now + 2 * 3_600_000));
  const [reason, setReason] = useState("");

  function submit(e: FormEvent) {
    e.preventDefault();
    const code = docks.find(([id]) => id === dockId)?.[1] ?? dockId;
    onSubmit({ dockId, type, start: fromIstInput(start), end: end ? fromIstInput(end) : undefined, reason }, `${code} ${words(type)}`);
    setReason("");
  }

  return (
    <form className="form-grid" onSubmit={submit} aria-label="New dock event">
      <h4>Record a dock event</h4>
      <Field label="Dock">
        <select value={dockId} onChange={(e) => setDockId(e.target.value)}>
          {docks.map(([id, code]) => (
            <option key={id} value={id}>
              {code}
            </option>
          ))}
        </select>
      </Field>
      <Field label="Type">
        <select value={type} onChange={(e) => setType(e.target.value)}>
          {EVENT_TYPES.map((t) => (
            <option key={t} value={t}>
              {words(t)}
            </option>
          ))}
        </select>
      </Field>
      <Field label="Start (IST)">
        <input type="datetime-local" required value={start} onChange={(e) => setStart(e.target.value)} />
      </Field>
      <Field label="End (IST, empty if unknown)">
        <input type="datetime-local" value={end} onChange={(e) => setEnd(e.target.value)} />
      </Field>
      <Field label="Reason">
        <input
          required
          maxLength={200}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          placeholder="e.g. hydraulic leveller fault"
        />
      </Field>
      <div className="form-actions">
        <button className="primary" disabled={busy}>
          Record event
        </button>
      </div>
    </form>
  );
}

function SlotEditor({
  slot,
  dockCode,
  busy,
  onClose,
  onSubmit,
}: {
  slot: CapacitySlot;
  dockCode: string;
  busy: boolean;
  onClose: () => void;
  onSubmit: (status: string, reason?: string) => void;
}) {
  const [reason, setReason] = useState("");
  const blocked = slot.status === "BLOCKED";
  return (
    <div className="slot-editor">
      <div className="li-head">
        <strong>
          {dockCode} {clock(slot.start)}–{clock(slot.end)}
        </strong>
        <button className="close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </div>
      <p className="li-body">
        {blocked
          ? `Blocked: ${slot.reason ?? "no reason given"}`
          : slot.shipmentId
            ? `Booked by ${slot.shipmentId}. Blocking it moves that truck at the next re-plan.`
            : "Open."}
      </p>
      {blocked ? (
        <button className="primary" disabled={busy} onClick={() => onSubmit("OPEN")}>
          Reopen slot
        </button>
      ) : (
        <form
          className="row"
          onSubmit={(e) => {
            e.preventDefault();
            onSubmit("BLOCKED", reason);
          }}
        >
          <input
            required
            maxLength={200}
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder="Reason, e.g. yard congestion"
            aria-label="Block reason"
          />
          <button className="primary" disabled={busy}>
            Block slot
          </button>
        </form>
      )}
    </div>
  );
}

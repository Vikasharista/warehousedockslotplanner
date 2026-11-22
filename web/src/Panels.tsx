import { useEffect, useState, type ReactNode } from "react";
import { api, ApiError, type Booking, type Change, type Exception, type Hold, type RunSummary, type SlotOption } from "./api";
import { clock, clockSeconds, minutesBetween, relative } from "./time";

const STATUS_LABEL: Record<string, string> = {
  UNASSIGNED: "No safe slot",
  FLAGGED: "Coordinator",
  IN_DOCK: "In dock",
  OUTSIDE_HORIZON: "Beyond 12 h",
};

export function Exceptions({ items, onOfferSlot }: { items: Exception[]; onOfferSlot: (shipmentId: string) => void }) {
  if (items.length === 0) return <p className="empty">Every truck in the horizon has a slot.</p>;
  return (
    <ul className="list">
      {items.map((e) => (
        <li key={e.shipmentId} className="list-item">
          <div className="li-head">
            <strong>{e.shipmentId}</strong>
            <span className={`pill p-${e.priorityCode.toLowerCase()}`}>{e.priorityCode}</span>
            <span className={`pill s-${e.allocationStatus.toLowerCase()}`}>{STATUS_LABEL[e.allocationStatus] ?? e.allocationStatus}</span>
          </div>
          <div className="li-body">
            <code>{e.reasonCode}</code>
            {e.detail && <span> · {e.detail}</span>}
          </div>
          <div className="li-meta">
            {e.originCity} · {(e.loadWeightKg / 1000).toFixed(1)} t{e.earliestReady && <> · ready {clock(e.earliestReady)}</>}
          </div>
          {e.allocationStatus === "UNASSIGNED" && (
            <button className="link" onClick={() => onOfferSlot(e.shipmentId)}>
              Find a slot for the driver →
            </button>
          )}
        </li>
      ))}
    </ul>
  );
}

const ACTION_LABEL: Record<string, string> = { NEW: "Booked", MOVED: "Moved", RELEASED: "Released" };

export function Changes({ items }: { items: Change[] }) {
  if (items.length === 0) return <p className="empty">No committed changes yet.</p>;
  return (
    <ul className="list">
      {items.map((c) => (
        <li key={`${c.runId}-${c.shipmentId}`} className="list-item">
          <div className="li-head">
            <strong>{c.shipmentId}</strong>
            <span className={`pill a-${c.action.toLowerCase()}`}>{ACTION_LABEL[c.action] ?? c.action}</span>
            <span className="li-time">{clock(c.committedAt)}</span>
          </div>
          <div className="li-body">
            {c.fromStart ? `${c.fromDockCode} ${clock(c.fromStart)}` : "no booking"} →{" "}
            {c.toStart ? `${c.toDockCode} ${clock(c.toStart)}` : "unassigned"}
            {c.reasonCode && <code className="li-code">{c.reasonCode}</code>}
          </div>
          <div className="li-meta">
            {c.trigger}
            {c.triggerDetail && ` · ${c.triggerDetail}`}
          </div>
        </li>
      ))}
    </ul>
  );
}

export function Runs({ items }: { items: RunSummary[] }) {
  if (items.length === 0) return <p className="empty">No runs yet; the first one starts within a minute.</p>;
  return (
    <table className="runs">
      <thead>
        <tr>
          <th>As of</th>
          <th>Trigger</th>
          <th>Status</th>
          <th className="num">Placed</th>
          <th className="num">Wait</th>
        </tr>
      </thead>
      <tbody>
        {items.map((r) => (
          <tr key={r.runId} title={[r.triggerDetail, r.outcome].filter(Boolean).join(" · ")}>
            <td>{clock(r.asOf)}</td>
            <td>{r.trigger}</td>
            <td>
              <span className={`pill r-${r.status.toLowerCase()}`}>{r.status}</span>
              {r.outcome && <div className="li-meta">{r.outcome}</div>}
            </td>
            <td className="num">
              {r.assigned}/{r.assigned + r.unassigned}
            </td>
            <td className="num">{r.avgWaitMin.toFixed(0)}′</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** Book a driver's choice: list the slots a truck may hold, hold one, then confirm or release it. */
export function Holds({ shipmentId, setShipmentId, shipments, now, onChanged }: {
  shipmentId: string;
  setShipmentId: (s: string) => void;
  shipments: string[];
  now: number;
  onChanged: () => void;
}) {
  const [options, setOptions] = useState<SlotOption[] | null>(null);
  const [hold, setHold] = useState<Hold | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setOptions(null);
    setError(null);
    if (!shipmentId) return;
    let live = true;
    api.options(shipmentId).then(
      (o) => live && setOptions(o),
      (e: ApiError) => live && setError(e.message),
    );
    return () => {
      live = false;
    };
  }, [shipmentId, hold?.status]);

  async function run(fn: () => Promise<Hold>) {
    setBusy(true);
    setError(null);
    try {
      setHold(await fn());
      onChanged();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const live = hold && hold.status === "PENDING_CONFIRMATION";
  return (
    <div className="holds">
      <label className="field">
        <span>Truck</span>
        <select value={shipmentId} onChange={(e) => (setShipmentId(e.target.value), setHold(null))}>
          <option value="">Choose a shipment…</option>
          {shipments.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
      </label>

      {hold && (
        <div className={`hold-card ${live ? "is-live" : ""}`}>
          <div className="li-head">
            <strong>{hold.appointmentId}</strong>
            <span className="pill">{hold.status.replaceAll("_", " ")}</span>
          </div>
          <div className="li-body">
            {hold.shipmentId} on {hold.slotId}
            {live && (
              <>
                {" "}
                · expires {clockSeconds(hold.expiresAt)} ({relative(Date.parse(hold.expiresAt), now)})
              </>
            )}
          </div>
          {live && (
            <div className="row">
              <button className="primary" disabled={busy} onClick={() => run(() => api.confirm(hold.appointmentId))}>
                Driver confirms
              </button>
              <button disabled={busy} onClick={() => run(() => api.release(hold.appointmentId))}>
                Release
              </button>
            </div>
          )}
        </div>
      )}

      {error && <p className="error">{error}</p>}

      {shipmentId && !live && options && (
        <>
          <p className="hint">
            Free slots {shipmentId} can use now (its earliest arrival, dock type and weight respected). A hold blocks the
            slot for 15 minutes while the driver decides.
          </p>
          {options.length === 0 && <p className="empty">No free slot fits this truck in the next 12 hours.</p>}
          <ul className="options">
            {options.map((o) => (
              <li key={o.slotId}>
                <span>
                  <strong>{o.dockCode}</strong> {clock(o.slotStart)}–{clock(o.slotEnd)}{" "}
                  <span className="li-meta">{minutesBetween(new Date(now).toISOString(), o.slotStart)} min from now</span>
                </span>
                <button disabled={busy} onClick={() => run(() => api.hold(shipmentId, o.slotId))}>
                  Hold
                </button>
              </li>
            ))}
          </ul>
        </>
      )}
    </div>
  );
}

export function Details({ booking, now, onClose, onOfferSlot }: {
  booking: Booking;
  now: number;
  onClose: () => void;
  onOfferSlot: (shipmentId: string) => void;
}) {
  const b = booking;
  const rows: [string, ReactNode][] = [
    ["Slot", `${clock(b.slotStart)}–${clock(b.slotEnd)} · ${b.slotId}`],
    ["Status", `${b.status.replaceAll("_", " ")} · ${b.source.replaceAll("_", " ")}`],
    ["Truck", `${b.vehicleId} from ${b.originCity} · ${(b.loadWeightKg / 1000).toFixed(1)} t · unload ${b.unloadMin} min`],
    ["ETA", b.gateIn ? `at the gate since ${clock(b.gateIn)}` : b.eta ? `${clock(b.eta)} · ${b.etaSource ?? ""} · ${b.etaConfidence ?? ""}` : "—"],
    ["Predicted wait", b.predictedWaitMin != null ? `${b.predictedWaitMin} min` : "—"],
  ];
  if (b.holdExpiresAt) rows.push(["Hold expires", `${clock(b.holdExpiresAt)} (${relative(Date.parse(b.holdExpiresAt), now)})`]);
  if (b.flagged) rows.push(["Flag", "Weight breach: left for a coordinator, never moved automatically"]);
  return (
    <aside className="details" aria-label={`Booking ${b.shipmentId}`}>
      <div className="li-head">
        <strong>{b.shipmentId}</strong>
        <span className={`pill p-${b.priorityCode.toLowerCase()}`}>{b.priorityCode}</span>
        <button className="close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </div>
      <dl>
        {rows.map(([k, v]) => (
          <div key={k}>
            <dt>{k}</dt>
            <dd>{v}</dd>
          </div>
        ))}
      </dl>
      {!b.flagged && b.status !== "IN_PROGRESS" && (
        <button className="link" onClick={() => onOfferSlot(b.shipmentId)}>
          Offer the driver another slot →
        </button>
      )}
    </aside>
  );
}

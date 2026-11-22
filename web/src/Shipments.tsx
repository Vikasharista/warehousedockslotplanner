import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { api, type ShipmentRow } from "./api";
import { clock, fromIstInput, relative, toIstInput } from "./time";

const PRIORITIES = ["CRITICAL", "HIGH", "NORMAL", "LOW"];
const DOCK_TYPES = ["ANY", "STANDARD", "REEFER", "HEAVY"];
const REASONS = ["", "TRAFFIC", "BREAKDOWN", "WEATHER", "LATE_DEPARTURE", "LOADING_DELAY", "ROUTE_ISSUE", "OTHER"];
const words = (s: string) => s.replaceAll("_", " ").toLowerCase();

/** Every open truck request at the facility, with forms to add one, edit it, change its ETA or check it in. */
export function Shipments({ now, onChanged }: { now: number; onChanged: () => void }) {
  const [rows, setRows] = useState<ShipmentRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState("");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [note, setNote] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setRows(await api.shipments());
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

  const shown = useMemo(() => {
    const q = filter.trim().toLowerCase();
    if (!rows) return [];
    if (!q) return rows;
    return rows.filter((r) =>
      [r.shipmentId, r.priorityCode, r.originCity, r.driverName, r.driverPhone, r.dockCode ?? "", r.planStatus ?? ""]
        .join(" ")
        .toLowerCase()
        .includes(q),
    );
  }, [rows, filter]);

  const selected = rows?.find((r) => r.shipmentId === selectedId) ?? null;

  /** Runs a change, then reloads this list and the plan view. */
  async function changed(message: string) {
    setNote(message);
    await load();
    onChanged();
  }

  return (
    <section className="page" aria-label="Shipments and requests">
      <div className="section-head">
        <h2>Shipments and requests</h2>
        <div className="row">
          <input
            type="search"
            placeholder="Filter by id, city, driver, dock…"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            aria-label="Filter shipments"
          />
          <button className="primary" onClick={() => setAdding((a) => !a)}>
            {adding ? "Close" : "New request"}
          </button>
        </div>
      </div>
      <p className="hint">
        Open trucks known to the planner as of the shared clock. A new request, an edit, an operations ETA override or a check-in each start
        a re-plan; a driver-declared ETA re-plans only if it moved more than 30 minutes.
      </p>
      {note && <p className="note">{note}</p>}
      {error && <p className="error">{error}</p>}

      {adding && (
        <NewRequest
          now={now}
          onCreated={async (row) => {
            setAdding(false);
            setSelectedId(row.shipmentId);
            await changed(`${row.shipmentId} added. The solver plans it within a minute (NEW_REQUEST).`);
          }}
        />
      )}

      {selected && (
        <ShipmentEditor key={selected.shipmentId} row={selected} now={now} onClose={() => setSelectedId(null)} onChanged={changed} />
      )}

      {rows === null ? (
        <p className="empty">Loading…</p>
      ) : (
        <div className="table-wrap">
          <table className="grid">
            <thead>
              <tr>
                <th>Shipment</th>
                <th>Priority</th>
                <th>From</th>
                <th>Dock type</th>
                <th className="num">Load</th>
                <th>ETA</th>
                <th>Slot</th>
                <th>Plan</th>
              </tr>
            </thead>
            <tbody>
              {shown.map((r) => (
                <tr
                  key={r.shipmentId}
                  className={r.shipmentId === selectedId ? "is-selected" : ""}
                  onClick={() => setSelectedId(r.shipmentId)}
                  tabIndex={0}
                  onKeyDown={(e) => e.key === "Enter" && setSelectedId(r.shipmentId)}
                >
                  <td>
                    <strong>{r.shipmentId}</strong>
                    <div className="li-meta">{r.driverName}</div>
                  </td>
                  <td>
                    <span className={`pill p-${r.priorityCode.toLowerCase()}`}>{r.priorityCode}</span>
                  </td>
                  <td>{r.originCity}</td>
                  <td>{r.requiredDockType}</td>
                  <td className="num">{(r.loadWeightKg / 1000).toFixed(1)} t</td>
                  <td>
                    {r.gateIn ? (
                      <span className="good">at gate {clock(r.gateIn)}</span>
                    ) : r.eta ? (
                      <>
                        {clock(r.eta)} <span className="li-meta">{relative(Date.parse(r.eta), now)}</span>
                        {r.etaSource && <div className="li-meta">{words(r.etaSource)}</div>}
                      </>
                    ) : (
                      "—"
                    )}
                  </td>
                  <td>
                    {r.dockCode && r.slotStart ? (
                      <>
                        {r.dockCode} {clock(r.slotStart)}
                        {r.bookingStatus && <div className="li-meta">{words(r.bookingStatus)}</div>}
                      </>
                    ) : (
                      <span className="li-meta">none</span>
                    )}
                  </td>
                  <td>
                    {r.planStatus ? words(r.planStatus) : <span className="li-meta">not planned yet</span>}
                    {r.planReason && <div className="li-meta">{words(r.planReason)}</div>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {shown.length === 0 && <p className="empty">No shipment matches.</p>}
        </div>
      )}
    </section>
  );
}

function NewRequest({ now, onCreated }: { now: number; onCreated: (row: ShipmentRow) => void }) {
  const [form, setForm] = useState({
    priorityCode: "NORMAL",
    requiredDockType: "ANY",
    loadWeightKg: 8000,
    unloadMin: 45,
    originCity: "",
    eta: toIstInput(now + 2 * 3_600_000),
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const set = (k: keyof typeof form, v: string | number) => setForm((f) => ({ ...f, [k]: v }));

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      onCreated(await api.createShipment({ ...form, eta: fromIstInput(form.eta) }));
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="card form-grid" onSubmit={submit} aria-label="New truck request">
      <h3>New truck request</h3>
      <Field label="Priority">
        <select value={form.priorityCode} onChange={(e) => set("priorityCode", e.target.value)}>
          {PRIORITIES.map((p) => (
            <option key={p}>{p}</option>
          ))}
        </select>
      </Field>
      <Field label="Dock type">
        <select value={form.requiredDockType} onChange={(e) => set("requiredDockType", e.target.value)}>
          {DOCK_TYPES.map((p) => (
            <option key={p}>{p}</option>
          ))}
        </select>
      </Field>
      <Field label="Load (kg)">
        <input
          type="number"
          min={500}
          max={32000}
          step={100}
          value={form.loadWeightKg}
          onChange={(e) => set("loadWeightKg", Number(e.target.value))}
        />
      </Field>
      <Field label="Unload (min)">
        <input
          type="number"
          min={15}
          max={240}
          step={5}
          value={form.unloadMin}
          onChange={(e) => set("unloadMin", Number(e.target.value))}
        />
      </Field>
      <Field label="Origin city">
        <input
          required
          maxLength={60}
          value={form.originCity}
          onChange={(e) => set("originCity", e.target.value)}
          placeholder="e.g. Delhi"
        />
      </Field>
      <Field label="ETA (IST)">
        <input type="datetime-local" required value={form.eta} onChange={(e) => set("eta", e.target.value)} />
      </Field>
      <div className="form-actions">
        <button className="primary" disabled={busy}>
          {busy ? "Adding…" : "Add request"}
        </button>
        {error && <span className="error">{error}</span>}
      </div>
    </form>
  );
}

function ShipmentEditor({
  row,
  now,
  onClose,
  onChanged,
}: {
  row: ShipmentRow;
  now: number;
  onClose: () => void;
  onChanged: (message: string) => Promise<void>;
}) {
  const [edit, setEdit] = useState({
    priorityCode: row.priorityCode,
    requiredDockType: row.requiredDockType,
    loadWeightKg: row.loadWeightKg,
    unloadMin: row.unloadMin,
  });
  const [eta, setEta] = useState(toIstInput(row.eta ? Date.parse(row.eta) : now));
  const [source, setSource] = useState("OPERATIONS_OVERRIDE");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function run(action: () => Promise<unknown>, message: string) {
    setBusy(true);
    setError(null);
    try {
      await action();
      await onChanged(message);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const id = row.shipmentId;
  return (
    <div className="card" aria-label={`Edit ${id}`}>
      <div className="li-head">
        <h3>
          {id}{" "}
          <span className="li-meta">
            · {row.driverName} · {row.driverPhone}
          </span>
        </h3>
        <button className="close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </div>
      <div className="editor">
        <form
          className="form-grid"
          onSubmit={(e) => {
            e.preventDefault();
            run(() => api.editShipment(id, edit), `${id} updated; re-planning.`);
          }}
        >
          <h4>Request</h4>
          <Field label="Priority">
            <select value={edit.priorityCode} onChange={(e) => setEdit({ ...edit, priorityCode: e.target.value })}>
              {PRIORITIES.map((p) => (
                <option key={p}>{p}</option>
              ))}
            </select>
          </Field>
          <Field label="Dock type">
            <select value={edit.requiredDockType} onChange={(e) => setEdit({ ...edit, requiredDockType: e.target.value })}>
              {DOCK_TYPES.map((p) => (
                <option key={p}>{p}</option>
              ))}
            </select>
          </Field>
          <Field label="Load (kg)">
            <input
              type="number"
              min={500}
              max={32000}
              step={100}
              value={edit.loadWeightKg}
              onChange={(e) => setEdit({ ...edit, loadWeightKg: Number(e.target.value) })}
            />
          </Field>
          <Field label="Unload (min)">
            <input
              type="number"
              min={15}
              max={240}
              step={5}
              value={edit.unloadMin}
              onChange={(e) => setEdit({ ...edit, unloadMin: Number(e.target.value) })}
            />
          </Field>
          <div className="form-actions">
            <button disabled={busy}>Save</button>
          </div>
        </form>

        <form
          className="form-grid"
          onSubmit={(e) => {
            e.preventDefault();
            run(
              () => api.setEta(id, fromIstInput(eta), source, reason),
              source === "OPERATIONS_OVERRIDE"
                ? `New ETA for ${id} recorded; re-planning.`
                : `Driver ETA for ${id} recorded; re-plans if it moved more than 30 minutes.`,
            );
          }}
        >
          <h4>ETA</h4>
          <Field label="New ETA (IST)">
            <input type="datetime-local" required value={eta} onChange={(e) => setEta(e.target.value)} />
          </Field>
          <Field label="Source">
            <select value={source} onChange={(e) => setSource(e.target.value)}>
              <option value="OPERATIONS_OVERRIDE">Operations override</option>
              <option value="DRIVER_DECLARED">Driver declared</option>
            </select>
          </Field>
          <Field label="Reason">
            <select value={reason} onChange={(e) => setReason(e.target.value)}>
              {REASONS.map((r) => (
                <option key={r} value={r}>
                  {r ? words(r) : "—"}
                </option>
              ))}
            </select>
          </Field>
          <div className="form-actions">
            <button disabled={busy}>Record ETA</button>
          </div>
        </form>

        <div className="form-grid">
          <h4>Gate</h4>
          {row.gateIn ? (
            <p className="hint">Checked in at {clock(row.gateIn)}.</p>
          ) : (
            <>
              <p className="hint">Puts the truck at the gate now (shared clock). Early or late is judged at ±30 minutes of its ETA.</p>
              <div className="form-actions">
                <button
                  className="primary"
                  disabled={busy}
                  onClick={() => run(() => api.checkIn(id), `${id} checked in; re-planning (CHECKIN).`)}
                >
                  Check in now
                </button>
              </div>
            </>
          )}
        </div>
      </div>
      {error && <p className="error">{error}</p>}
    </div>
  );
}

export function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="field">
      <span>{label}</span>
      {children}
    </label>
  );
}

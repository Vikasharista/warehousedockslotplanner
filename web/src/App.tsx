import { useCallback, useEffect, useMemo, useState } from "react";
import { api, type Board, type Booking, type Change, type Exception, type RunSummary } from "./api";
import { Changes, Details, Exceptions, Holds, Runs } from "./Panels";
import { Timeline } from "./Timeline";
import { clock, clockSeconds, date, relative } from "./time";

const POLL_MS = 10_000;
type Tab = "exceptions" | "changes" | "runs" | "holds";

export function App() {
  const [board, setBoard] = useState<Board | null>(null);
  const [exceptions, setExceptions] = useState<Exception[]>([]);
  const [changes, setChanges] = useState<Change[]>([]);
  const [runs, setRuns] = useState<RunSummary[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [tab, setTab] = useState<Tab>("exceptions");
  const [selected, setSelected] = useState<Booking | null>(null);
  const [holdShipment, setHoldShipment] = useState("");
  const [replanning, setReplanning] = useState(false);
  const [replanNote, setReplanNote] = useState<string | null>(null);
  // The shared clock: server time at the last poll, advanced locally between polls.
  const [clockBase, setClockBase] = useState<{ server: number; local: number } | null>(null);
  const [tick, setTick] = useState(Date.now());

  const refresh = useCallback(async () => {
    try {
      const [b, e, c, r] = await Promise.all([api.board(), api.exceptions(), api.changes(), api.runs()]);
      setBoard(b);
      setExceptions(e);
      setChanges(c);
      setRuns(r);
      setClockBase({ server: Date.parse(b.now), local: Date.now() });
      setError(null);
    } catch (err) {
      setError((err as Error).message);
    }
  }, []);

  useEffect(() => {
    refresh();
    const poll = setInterval(refresh, POLL_MS);
    const second = setInterval(() => setTick(Date.now()), 1000);
    return () => {
      clearInterval(poll);
      clearInterval(second);
    };
  }, [refresh]);

  const now = clockBase ? clockBase.server + (tick - clockBase.local) : Date.now();
  const last = board?.lastCommitted;
  const latest = board?.lastRun;

  const shipments = useMemo(() => {
    const ids = new Set<string>();
    board?.bookings.forEach((b) => b.status !== "IN_PROGRESS" && !b.flagged && ids.add(b.shipmentId));
    exceptions.forEach((e) => e.allocationStatus === "UNASSIGNED" && ids.add(e.shipmentId));
    return [...ids].sort();
  }, [board, exceptions]);

  function offerSlot(shipmentId: string) {
    setHoldShipment(shipmentId);
    setTab("holds");
    setSelected(null);
  }

  async function replan() {
    setReplanning(true);
    setReplanNote(null);
    try {
      const r = await api.replan();
      const a = r.attempts[r.attempts.length - 1];
      setReplanNote(`${r.status}${a?.outcome ? `: ${a.outcome}` : ""}`);
      await refresh();
    } catch (err) {
      setReplanNote((err as Error).message);
    } finally {
      setReplanning(false);
    }
  }

  return (
    <div className="app">
      <header className="top">
        <div className="brand">
          <h1>Dock plan</h1>
          <span className="sub">Jaipur DC · FAC-JAI-01</span>
        </div>
        <div className="clock" title="The shared simulated clock every service runs on">
          <span className="clock-time">{clockSeconds(now)}</span>
          <span className="sub">{date(now)} · IST</span>
        </div>
        <div className="actions">
          <button className="primary" onClick={replan} disabled={replanning}>
            {replanning ? "Re-planning…" : "Re-plan now"}
          </button>
          {replanNote && <span className="note">{replanNote}</span>}
        </div>
      </header>

      {error && (
        <div className="banner" role="alert">
          Cannot reach the dock API: {error}. Is <code>docker compose up</code> running?
        </div>
      )}

      <section className="kpis" aria-label="Last committed plan">
        <Kpi label="Placed" value={last ? `${last.assigned}/${last.assigned + last.unassigned}` : "—"} />
        <Kpi label="Avg wait" value={last ? `${last.avgWaitMin.toFixed(0)} min` : "—"} />
        <Kpi label="Priority-weighted wait" value={last ? `${last.weightedWaitMin.toFixed(0)} min` : "—"} />
        <Kpi label="Priority inversions" value={last ? String(last.priorityInversions) : "—"} good={last?.priorityInversions === 0} />
        <Kpi label="Dock utilisation" value={last ? `${last.dockUtilisationPct.toFixed(0)}%` : "—"} />
        <div className="kpi kpi-wide">
          <span className="kpi-label">Last commit</span>
          <span className="kpi-value small">
            {last ? (
              <>
                {last.trigger} · {clock(last.committedAt ?? last.asOf)} ({relative(Date.parse(last.committedAt ?? last.asOf), now)})
                {last.triggerDetail && <span className="sub"> · {last.triggerDetail}</span>}
              </>
            ) : (
              "waiting for the first plan…"
            )}
          </span>
          {latest && latest.status === "REJECTED" && (
            <span className="warn">Newest run {clock(latest.asOf)} not committed: {latest.outcome}</span>
          )}
        </div>
      </section>

      <main className="main">
        <section className="board-wrap" aria-label="Dock sequence">
          <div className="section-head">
            <h2>Dock sequence</h2>
            <Legend />
          </div>
          {board ? (
            <Timeline board={board} now={now} selected={selected?.appointmentId} onSelect={setSelected} />
          ) : (
            <p className="empty">Loading…</p>
          )}
          {selected && <Details booking={selected} now={now} onClose={() => setSelected(null)} onOfferSlot={offerSlot} />}
        </section>

        <aside className="side">
          <nav className="tabs" role="tablist">
            {(
              [
                ["exceptions", `Exceptions${exceptions.length ? ` (${exceptions.length})` : ""}`],
                ["changes", "Change log"],
                ["runs", "Runs"],
                ["holds", "Driver holds"],
              ] as [Tab, string][]
            ).map(([id, label]) => (
              <button key={id} role="tab" aria-selected={tab === id} className={tab === id ? "is-active" : ""} onClick={() => setTab(id)}>
                {label}
              </button>
            ))}
          </nav>
          <div className="tab-body">
            {tab === "exceptions" && <Exceptions items={exceptions} onOfferSlot={offerSlot} />}
            {tab === "changes" && <Changes items={changes} />}
            {tab === "runs" && <Runs items={runs} />}
            {tab === "holds" && (
              <Holds shipmentId={holdShipment} setShipmentId={setHoldShipment} shipments={shipments} now={now} onChanged={refresh} />
            )}
          </div>
        </aside>
      </main>
    </div>
  );
}

function Kpi({ label, value, good }: { label: string; value: string; good?: boolean }) {
  return (
    <div className={`kpi ${good ? "is-good" : ""}`}>
      <span className="kpi-label">{label}</span>
      <span className="kpi-value">{value}</span>
    </div>
  );
}

function Legend() {
  return (
    <div className="legend" aria-label="Legend">
      {["critical", "high", "normal", "low"].map((p) => (
        <span key={p} className="lg">
          <i className={`sw p-${p}`} />
          {p}
        </span>
      ))}
      <span className="lg">
        <i className="sw is-hold" />
        driver hold
      </span>
      <span className="lg">
        <i className="sw is-flagged" />
        flagged
      </span>
      <span className="lg">
        <i className="sw sw-block" />
        blocked
      </span>
    </div>
  );
}

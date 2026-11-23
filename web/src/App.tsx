import { useCallback, useEffect, useMemo, useState } from "react";
import { api, isUnauthenticated, type Board, type Booking, type Change, type Exception, type Me, type RunSummary } from "./api";
import { Capacity } from "./Capacity";
import { DriverCases } from "./DriverCases";
import { DriverChat } from "./DriverChat";
import { Changes, Details, Exceptions, Holds, Runs } from "./Panels";
import { Shipments } from "./Shipments";
import { Timeline } from "./Timeline";
import { clock, clockSeconds, date, relative } from "./time";

const POLL_MS = 10_000;
type Tab = "exceptions" | "changes" | "runs" | "holds";
type View = "plan" | "shipments" | "capacity" | "cases" | "chat";
const VIEWS: [View, string][] = [
  ["plan", "Plan"],
  ["shipments", "Shipments"],
  ["capacity", "Capacity"],
  ["cases", "Driver cases"],
  ["chat", "Driver chat"],
];

export function App({ me, onSignOut, onSessionExpired }: { me: Me; onSignOut: () => void; onSessionExpired: () => void }) {
  const [board, setBoard] = useState<Board | null>(null);
  const [exceptions, setExceptions] = useState<Exception[]>([]);
  const [changes, setChanges] = useState<Change[]>([]);
  const [runs, setRuns] = useState<RunSummary[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [tab, setTab] = useState<Tab>("exceptions");
  const [view, setView] = useState<View>(() =>
    VIEWS.some(([v]) => v === location.hash.slice(1)) ? (location.hash.slice(1) as View) : "plan",
  );
  const [selected, setSelected] = useState<Booking | null>(null);
  const [holdShipment, setHoldShipment] = useState("");
  const [replanning, setReplanning] = useState(false);
  const [replanNote, setReplanNote] = useState<string | null>(null);
  const [addCount, setAddCount] = useState(10);
  const [adding, setAdding] = useState(false);
  // The shared clock: server time at the last poll, advanced locally between polls.
  const [clockBase, setClockBase] = useState<{
    server: number;
    local: number;
  } | null>(null);
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
      if (isUnauthenticated(err)) {
        onSessionExpired();
        return;
      }
      setError((err as Error).message);
    }
  }, [onSessionExpired]);

  useEffect(() => {
    refresh();
    const poll = setInterval(refresh, POLL_MS);
    const second = setInterval(() => setTick(Date.now()), 1000);
    return () => {
      clearInterval(poll);
      clearInterval(second);
    };
  }, [refresh]);

  function show(v: View) {
    setView(v);
    history.replaceState(null, "", v === "plan" ? location.pathname : `#${v}`);
  }

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

  /** Queue generated truck requests; wait (up to a minute) for the jobs' loop to create them. */
  async function addTrucks() {
    setAdding(true);
    setReplanNote(`Requesting ${addCount} trucks…`);
    try {
      let job = await api.addTrucks(addCount);
      for (let i = 0; i < 30 && job.status === "PENDING"; i++) {
        await new Promise((r) => setTimeout(r, 2000));
        job = await api.requestJob(job.jobId);
      }
      setReplanNote(
        job.status === "DONE"
          ? `Added ${job.result}. The solver re-plans within a minute (NEW_REQUEST).`
          : job.status === "FAILED"
            ? `Could not add trucks: ${job.result}`
            : "Still queued: is the gps service running?",
      );
      await refresh();
    } catch (err) {
      setReplanNote((err as Error).message);
    } finally {
      setAdding(false);
    }
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
          <div className="add-trucks" role="group" aria-label="Add truck requests">
            <select value={addCount} onChange={(e) => setAddCount(Number(e.target.value))} aria-label="How many trucks">
              {[5, 10, 20, 30].map((n) => (
                <option key={n} value={n}>
                  {n} trucks
                </option>
              ))}
            </select>
            <button onClick={addTrucks} disabled={adding}>
              {adding ? "Adding…" : "Add requests"}
            </button>
          </div>
          <button className="primary" onClick={replan} disabled={replanning}>
            {replanning ? "Re-planning…" : "Re-plan now"}
          </button>
          {replanNote && <span className="note">{replanNote}</span>}
          <span className="user" title={me.email}>
            {me.name}
          </span>
          <button className="link" onClick={onSignOut}>
            Sign out
          </button>
        </div>
      </header>

      {error && (
        <div className="banner" role="alert">
          Cannot reach the dock API: {error}. Is <code>docker compose up</code> running?
        </div>
      )}

      <nav className="views" aria-label="Screens">
        {VIEWS.map(([id, label]) => (
          <button
            key={id}
            className={view === id ? "is-active" : ""}
            aria-current={view === id ? "page" : undefined}
            onClick={() => show(id)}
          >
            {label}
            {id === "plan" && exceptions.length > 0 && <span className="count">{exceptions.length}</span>}
          </button>
        ))}
      </nav>

      {view === "shipments" && <Shipments now={now} onChanged={refresh} />}
      {view === "capacity" && <Capacity now={now} onChanged={refresh} />}
      {view === "cases" && <DriverCases now={now} onChanged={refresh} />}
      {view === "chat" && <DriverChat now={now} onChanged={refresh} />}

      {view === "plan" && (
        <>
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
                <span className="warn">
                  Newest run {clock(latest.asOf)} not committed: {latest.outcome}
                </span>
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
                  <button
                    key={id}
                    role="tab"
                    aria-selected={tab === id}
                    className={tab === id ? "is-active" : ""}
                    onClick={() => setTab(id)}
                  >
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
        </>
      )}
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

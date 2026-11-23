import { useCallback, useEffect, useState } from "react";
import { api, type ApprovalRequest, type ChatMessage, type CoordinatorCase, type DriverCases as Data, type DriverException } from "./api";
import { clock, relative } from "./time";

const words = (s: string) => s.replaceAll("_", " ").toLowerCase();
const minutesLeft = (expiresAt: string, now: number) => Math.max(0, Math.floor((Date.parse(expiresAt) - now) / 60_000));

/**
 * What the driver agent hands to a person: HIGH and CRITICAL slot changes waiting for approval, escalated cases and
 * open driver exceptions. Approve swaps the truck's booking for the reserved slot; reject (or no answer in time) keeps
 * the old booking. Every decision and reply reaches the driver through the agent.
 */
export function DriverCases({ now, onChanged }: { now: number; onChanged: () => void }) {
  const [data, setData] = useState<Data | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [thread, setThread] = useState<{ id: string; title: string; messages: ChatMessage[] } | null>(null);

  const load = useCallback(async () => {
    try {
      setData(await api.driverCases());
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
      await load();
      return false;
    } finally {
      setBusy(false);
    }
  }

  async function openThread(id: string, title: string) {
    try {
      setThread({ id, title, messages: await api.threadMessages(id) });
    } catch (err) {
      setError((err as Error).message);
    }
  }

  const n = (count: number, one: string) => `${count} ${one}${count === 1 ? "" : "s"}`;
  const counts = data
    ? `${n(data.approvals.length, "approval")} · ${n(data.cases.length, "case")} · ${n(data.exceptions.length, "exception")}`
    : "";

  return (
    <section className="page" aria-label="Driver cases">
      <div className="section-head">
        <h2>Driver cases</h2>
        <span className="li-meta">{counts}</span>
      </div>
      <p className="hint">
        The driver agent sends HIGH and CRITICAL slot changes here for approval and hands over what it cannot settle. While a change waits,
        the truck keeps its booking and the new slot is held for it; with no answer before the time runs out, the change is dropped and the
        driver is told. Replies go to the driver through the agent.
      </p>
      {note && <p className="note">{note}</p>}
      {error && <p className="error">{error}</p>}
      {!data ? (
        <p className="empty">Loading…</p>
      ) : (
        <div className="cases">
          <div className="cases-lists">
            <Approvals items={data.approvals} now={now} busy={busy} run={run} />
            <Cases items={data.cases} now={now} busy={busy} run={run} openThread={openThread} />
            <Exceptions items={data.exceptions} now={now} openThread={openThread} />
          </div>
          {thread && (
            <aside className="card conversation" aria-label={`Conversation ${thread.title}`}>
              <div className="li-head">
                <h3>Conversation · {thread.title}</h3>
                <button className="close" aria-label="Close conversation" onClick={() => setThread(null)}>
                  ×
                </button>
              </div>
              {thread.messages.length === 0 ? (
                <p className="empty">No messages yet.</p>
              ) : (
                <div className="chat">
                  {thread.messages.map((m) => (
                    <div key={m.messageId} className={`bubble ${m.senderType === "DRIVER" ? "from-driver" : "from-agent"}`}>
                      <div className="li-meta">
                        {words(m.senderType)} · {clock(m.sentAt)}
                        {m.requiresHumanReview && " · needs review"}
                      </div>
                      {m.text}
                    </div>
                  ))}
                </div>
              )}
            </aside>
          )}
        </div>
      )}
    </section>
  );
}

type Run = (action: () => Promise<unknown>, message: string) => Promise<boolean>;

function Approvals({ items, now, busy, run }: { items: ApprovalRequest[]; now: number; busy: boolean; run: Run }) {
  const [notes, setNotes] = useState<Record<string, string>>({});
  return (
    <div className="card">
      <h3>Slot changes waiting for approval</h3>
      {items.length === 0 ? (
        <p className="empty">Nothing waiting.</p>
      ) : (
        <ul className="list">
          {items.map((r) => {
            const left = minutesLeft(r.expiresAt, now);
            const from = r.fromDockCode && r.fromStart ? `dock ${r.fromDockCode} at ${clock(r.fromStart)}` : "no booking";
            return (
              <li key={r.requestId} className="list-item" data-request={r.requestId}>
                <div className="li-head">
                  <strong>{r.shipmentId}</strong>
                  <span className={`pill p-${r.priorityCode.toLowerCase()}`}>{r.priorityCode}</span>
                  <span className="li-meta">{r.driverName}</span>
                  <span className={`li-time ${left < 15 ? "warn" : ""}`}>{left} min left</span>
                </div>
                <div className="li-body">
                  Move from {from} to <strong>dock {r.toDockCode} at {clock(r.toStart)}</strong>
                </div>
                <div className="row">
                  <input
                    aria-label={`Note to the driver for ${r.shipmentId}`}
                    placeholder="Note to the driver (optional)"
                    maxLength={500}
                    value={notes[r.requestId] ?? ""}
                    onChange={(e) => setNotes({ ...notes, [r.requestId]: e.target.value })}
                  />
                  <button
                    className="primary"
                    disabled={busy}
                    onClick={() =>
                      run(
                        () => api.approve(r.requestId, notes[r.requestId]),
                        `Approved: ${r.shipmentId} now has dock ${r.toDockCode} at ${clock(r.toStart)}; the driver is told.`,
                      )
                    }
                  >
                    Approve
                  </button>
                  <button
                    disabled={busy}
                    onClick={() =>
                      run(() => api.reject(r.requestId, notes[r.requestId]), `Rejected: ${r.shipmentId} keeps ${from}; the driver is told.`)
                    }
                  >
                    Reject
                  </button>
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

function Cases({
  items,
  now,
  busy,
  run,
  openThread,
}: {
  items: CoordinatorCase[];
  now: number;
  busy: boolean;
  run: Run;
  openThread: (id: string, title: string) => void;
}) {
  const [replies, setReplies] = useState<Record<string, string>>({});
  return (
    <div className="card">
      <h3>Cases from the agent</h3>
      {items.length === 0 ? (
        <p className="empty">No open cases.</p>
      ) : (
        <ul className="list">
          {items.map((c) => (
            <li key={c.caseId} className="list-item" data-case={c.caseId}>
              <div className="li-head">
                <span className={`pill u-${c.urgency.toLowerCase()}`}>{c.urgency}</span>
                <strong>{words(c.reasonCode)}</strong>
                <span className="li-meta">
                  {c.shipmentId ? `${c.shipmentId} · ${c.driverName ?? ""}` : `unknown number ${c.phone ?? ""} (${words(c.channel)})`}
                </span>
                <span className="li-time">{relative(Date.parse(c.createdAt), now)}</span>
              </div>
              <div className="li-body">{c.summary}</div>
              <div className="li-meta">
                {c.status === "ASSIGNED" ? `With ${c.assignedTo}` : "Not taken yet"}
                {c.threadId && (
                  <>
                    {" · "}
                    <button className="link" onClick={() => openThread(c.threadId!, c.shipmentId ?? c.caseId)}>
                      Open conversation
                    </button>
                  </>
                )}
              </div>
              <div className="row">
                <input
                  aria-label={`Reply to the driver for ${c.caseId}`}
                  placeholder="Reply to the driver (optional)"
                  maxLength={1000}
                  value={replies[c.caseId] ?? ""}
                  onChange={(e) => setReplies({ ...replies, [c.caseId]: e.target.value })}
                />
                {c.status === "OPEN" && (
                  <button disabled={busy} onClick={() => run(() => api.assignCase(c.caseId), `You took ${words(c.reasonCode)}.`)}>
                    Take
                  </button>
                )}
                <button
                  className="primary"
                  disabled={busy}
                  onClick={() =>
                    run(
                      () => api.resolveCase(c.caseId, replies[c.caseId]),
                      replies[c.caseId] ? "Resolved; your reply goes to the driver." : "Resolved.",
                    )
                  }
                >
                  Resolve
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function Exceptions({
  items,
  now,
  openThread,
}: {
  items: DriverException[];
  now: number;
  openThread: (id: string, title: string) => void;
}) {
  return (
    <div className="card">
      <h3>Open driver exceptions</h3>
      {items.length === 0 ? (
        <p className="empty">No open exceptions.</p>
      ) : (
        <ul className="list">
          {items.map((x) => (
            <li key={x.exceptionId} className="list-item">
              <div className="li-head">
                <span className={`pill sev-${x.severity.toLowerCase()}`}>{x.severity}</span>
                <strong>{words(x.type)}</strong>
                <span className="li-meta">
                  {x.shipmentId} · {x.driverName}
                </span>
                <span className="li-time">{relative(Date.parse(x.reportedAt), now)}</span>
              </div>
              <div className="li-body">
                {x.description}
                {x.delayMin ? ` · ${x.delayMin} min late` : null}
                {x.declaredEta && ` · says ${clock(x.declaredEta)}`}
                {x.latestAcceptable && ` · must finish by ${clock(x.latestAcceptable)}`}
              </div>
              <div className="li-meta">
                {words(x.status)} ·{" "}
                <button className="link" onClick={() => openThread(x.threadId, x.shipmentId ?? x.exceptionId)}>
                  Open conversation
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

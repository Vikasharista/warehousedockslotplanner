import { useEffect, useRef, useState, type FormEvent } from "react";
import { api, type AgentShipment, type FeedChange, type SlotOption } from "./api";
import { clock, relative } from "./time";

type Line = { who: "driver"; text: string } | { who: "agent"; text: string; call: string; options?: SlotOption[] };

const DELAYS = [30, 60, 90, 120];
const REASONS = ["TRAFFIC", "BREAKDOWN", "WEATHER", "LATE_DEPARTURE", "ROUTE_ISSUE", "OTHER"];

/**
 * A test console for the driver agent: plays a driver's side of a chat and calls the same /api/agent/* routes a
 * WhatsApp agent would (signed in here, so no agent key is needed). Each reply shows the call behind it.
 */
export function DriverChat({ now, onChanged }: { now: number; onChanged: () => void }) {
  const [phone, setPhone] = useState("");
  const [drivers, setDrivers] = useState<{ phone: string; label: string }[]>([]);
  const [shipment, setShipment] = useState<AgentShipment | null>(null);
  const [lines, setLines] = useState<Line[]>([]);
  const [busy, setBusy] = useState(false);
  const [delay, setDelay] = useState(60);
  const [reason, setReason] = useState("TRAFFIC");
  const [cursor, setCursor] = useState(0);
  const end = useRef<HTMLDivElement>(null);

  useEffect(() => {
    api
      .shipments()
      .then((rows) =>
        setDrivers(
          rows
            .filter((r) => !r.gateIn)
            .map((r) => ({ phone: r.driverPhone, label: `${r.driverName} · ${r.shipmentId} · ${r.priorityCode}` })),
        ),
      )
      .catch(() => undefined);
    // Start the change feed at its current end, so "Any changes?" shows only what happens from now on.
    api.agent.changes(0, 1).then(
      (page) => setCursor(page.last),
      () => undefined,
    );
  }, []);

  // Braces matter: newer browsers return a Promise from scrollIntoView, which React would take for a cleanup.
  useEffect(() => {
    end.current?.scrollIntoView({ block: "nearest" });
  }, [lines]);

  const say = (l: Line) => setLines((ls) => [...ls, l]);

  async function turn(
    driverText: string,
    call: string,
    action: () => Promise<Omit<Extract<Line, { who: "agent" }>, "who" | "call">>,
    changes = false,
  ) {
    say({ who: "driver", text: driverText });
    setBusy(true);
    try {
      const reply = await action();
      say({ who: "agent", call, ...reply });
      if (changes) onChanged();
    } catch (err) {
      say({ who: "agent", call, text: `Sorry, that did not work: ${(err as Error).message}` });
    } finally {
      setBusy(false);
    }
  }

  function start(e: FormEvent) {
    e.preventDefault();
    const p = phone.trim();
    if (!p) return;
    setLines([]);
    setShipment(null);
    turn(`Hi, this is ${p}`, `GET /api/agent/drivers/${p}/shipments`, async () => {
      const list = await api.agent.byPhone(p);
      const first = list[0];
      setShipment(first);
      const more = list.length > 1 ? ` (you have ${list.length} open trips; showing ${first.shipmentId})` : "";
      return { text: `Namaste ${first.driverName}${more}. ${first.message}` };
    });
  }

  const id = shipment?.shipmentId ?? "";
  const refresh = async () => {
    const s = await api.agent.shipment(id);
    setShipment(s);
    return s;
  };

  const whereIsMySlot = () =>
    turn("Where is my slot?", `GET /api/agent/shipments/${id}`, async () => ({ text: (await refresh()).message }));

  function runningLate() {
    const base = shipment?.eta ? Date.parse(shipment.eta) : now;
    const eta = new Date(Math.max(base, now) + delay * 60_000);
    turn(
      `I'm running about ${delay} minutes late (${reason.replaceAll("_", " ").toLowerCase()}).`,
      `POST /api/agent/shipments/${id}/eta`,
      async () => {
        const s = await api.agent.reportEta(id, eta.toISOString(), reason);
        setShipment(s);
        // The planner uses the effective ETA, which prefers a live GPS estimate over a driver's report.
        const used = s.eta ? Date.parse(s.eta) : eta.getTime();
        const gpsWins = s.etaSource === "GPS" && Math.abs(used - eta.getTime()) > 5 * 60_000;
        return {
          text: gpsWins
            ? `Thanks, noted ${clock(eta)}. Your GPS position puts you here at ${clock(used)}, so the planner uses that. ${s.message}`
            : `Thanks, noted your new arrival time ${clock(eta)}. If that moves you more than 30 minutes from your slot, the planner re-plans within a minute and I will tell you the new slot. ${s.message}`,
        };
      },
      true,
    );
  }

  const otherSlots = () =>
    turn("Can I get another slot?", `GET /api/agent/shipments/${id}/options?limit=4`, async () => {
      const options = await api.agent.options(id);
      return options.length
        ? { text: "These slots are free for your truck. Pick one and I will hold it for 15 minutes:", options }
        : { text: "There is no other free slot for your truck in the next 12 hours. Your current plan stays." };
    });

  const hold = (o: SlotOption) =>
    turn(
      `${o.dockCode} at ${clock(o.slotStart)} please`,
      "POST /api/agent/holds",
      async () => {
        const h = await api.agent.hold(id, o.slotId);
        await refresh();
        return {
          text: `Held ${o.dockCode} ${clock(o.slotStart)}–${clock(o.slotEnd)} for you until ${clock(h.expiresAt)}. Reply YES to confirm or NO to let it go.`,
        };
      },
      true,
    );

  const confirm = (holdId: string) =>
    turn(
      "YES",
      `POST /api/agent/holds/${holdId}/confirm`,
      async () => {
        const h = await api.agent.confirm(holdId);
        if (h.outcome === "AWAITING_APPROVAL") {
          return {
            text: `This is a priority load, so a coordinator approves the change. Your current slot stands until then. ${(await refresh()).message}`,
          };
        }
        return { text: `Confirmed. ${(await refresh()).message}` };
      },
      true,
    );

  const release = (holdId: string) =>
    turn(
      "NO",
      `POST /api/agent/holds/${holdId}/release`,
      async () => {
        await api.agent.release(holdId);
        return { text: `Released it. ${(await refresh()).message}` };
      },
      true,
    );

  const anyChanges = () =>
    turn("Any changes to my slot?", `GET /api/agent/changes?after=${cursor}`, async () => {
      const { changes, next } = await readFeed(cursor);
      setCursor(next);
      const mine = changes.filter((c) => c.shipmentId === id);
      return {
        text: mine.length
          ? mine.map((c) => c.message).join(" ")
          : `No change to your booking since I last checked (${changes.length} change${changes.length === 1 ? "" : "s"} to other trucks).`,
      };
    });

  const liveHold = shipment?.hold;

  return (
    <section className="page" aria-label="Driver chat test console">
      <div className="section-head">
        <h2>Driver chat (test console)</h2>
      </div>
      <p className="hint">
        Play a driver. Replies come from the agent API a WhatsApp bot would call with its <code>X-Agent-Key</code>; the call behind each
        reply is shown under it. See <code>docs/agent-api.md</code>.
      </p>
      <form className="row chat-start" onSubmit={start}>
        <input
          list="driver-phones"
          value={phone}
          onChange={(e) => setPhone(e.target.value)}
          placeholder="Driver phone, e.g. +91 98…"
          aria-label="Driver phone"
        />
        <datalist id="driver-phones">
          {drivers.map((d) => (
            <option key={d.phone + d.label} value={d.phone}>
              {d.label}
            </option>
          ))}
        </datalist>
        <button className="primary" disabled={busy || !phone.trim()}>
          Start chat
        </button>
      </form>

      <div className="chat" aria-live="polite">
        {lines.length === 0 && <p className="empty">Pick a driver's phone number to start.</p>}
        {lines.map((l, i) =>
          l.who === "driver" ? (
            <div key={i} className="bubble from-driver">
              {l.text}
            </div>
          ) : (
            <div key={i} className="bubble from-agent">
              <div>{l.text}</div>
              {l.options && (
                <div className="chat-options">
                  {l.options.map((o) => (
                    <button key={o.slotId} disabled={busy || !!liveHold} onClick={() => hold(o)}>
                      {o.dockCode} {clock(o.slotStart)}
                    </button>
                  ))}
                </div>
              )}
              <code className="chat-call">{l.call}</code>
            </div>
          ),
        )}
        <div ref={end} />
      </div>

      {shipment && (
        <div className="chat-actions">
          <div className="li-meta">
            {shipment.shipmentId} · {shipment.priorityCode}
            {shipment.booking && ` · ${shipment.booking.dockCode} ${clock(shipment.booking.start)}`}
            {shipment.eta && ` · ETA ${clock(shipment.eta)}`}
            {liveHold?.expiresAt &&
              ` · hold on ${liveHold.dockCode} ${clock(liveHold.start)} expires ${relative(Date.parse(liveHold.expiresAt), now)}`}
          </div>
          <div className="row">
            <button disabled={busy} onClick={whereIsMySlot}>
              Where is my slot?
            </button>
            <span className="add-trucks">
              <select value={delay} onChange={(e) => setDelay(Number(e.target.value))} aria-label="Delay">
                {DELAYS.map((d) => (
                  <option key={d} value={d}>
                    {d} min
                  </option>
                ))}
              </select>
              <select value={reason} onChange={(e) => setReason(e.target.value)} aria-label="Reason">
                {REASONS.map((r) => (
                  <option key={r} value={r}>
                    {r.replaceAll("_", " ").toLowerCase()}
                  </option>
                ))}
              </select>
              <button disabled={busy || !!shipment.gateIn} onClick={runningLate}>
                Running late
              </button>
            </span>
            <button disabled={busy || !!liveHold || !!shipment.gateIn} onClick={otherSlots}>
              Another slot?
            </button>
            {liveHold && (
              <>
                <button className="primary" disabled={busy} onClick={() => confirm(liveHold.appointmentId)}>
                  YES (confirm hold)
                </button>
                <button disabled={busy} onClick={() => release(liveHold.appointmentId)}>
                  NO (release)
                </button>
              </>
            )}
            <button disabled={busy} onClick={anyChanges}>
              Any changes?
            </button>
          </div>
        </div>
      )}
    </section>
  );
}

/** Reads the change feed from a cursor to its end, as an agent would; restarts at 0 if the feed was reset. */
async function readFeed(cursor: number): Promise<{ changes: FeedChange[]; next: number }> {
  const all: FeedChange[] = [];
  for (let i = 0; i < 50; i++) {
    const page = await api.agent.changes(cursor, 500);
    if (cursor > page.last) {
      cursor = 0; // the nightly reset started the ids again
      continue;
    }
    all.push(...page.changes);
    cursor = page.next;
    if (cursor >= page.last) break;
  }
  return { changes: all, next: cursor };
}

"""End-to-end race harness: drivers, the solver and a coordinator all change bookings at once, then the invariants
are checked in the database.

Against a running stack (docker compose up), for --seconds:
  * driver threads hold a free slot through the agent API, then confirm or release it;
  * same-slot bursts: several drivers try to hold ONE slot at the same instant (exactly one may win);
  * a solver thread asks for a re-plan and commit in a loop;
  * a coordinator thread blocks and reopens free slots through dock-api;
  * an approver thread decides HIGH and CRITICAL changes waiting for approval (phase 7: a driver's confirm of such a
    truck answers 202 and reserves the slot), approving some and rejecting others.
Afterwards (every change still waiting is decided, then one quiet re-plan) it checks:
  I1 no slot has two active bookings;           I2 no truck has two current active bookings;
  I3 no two active bookings overlap on a dock;  I4 every committed run passed the validator;
  I5 every rejected run says why;               I6 every same-slot burst had at most one winner;
  I7 no request failed with a server error (5xx);
  I8 every reservation was released or decided, and each hold matches its request's outcome.

    python -m setuhaul_jobs.race --database-url postgresql://postgres:postgres@localhost:55432/setuhaul \\
        [--api http://localhost:8082] [--solver http://localhost:8081] [--seconds 60] [--drivers 6] [--json out.json]

The CI-sized run is --seconds 15 --drivers 3. Exit code 1 when an invariant fails. It changes the database it runs
against (holds, confirmations, blocks, re-plans): use the local demo stack, never a deployment.
"""
from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import random
import sys
import threading
import time
import urllib.error
import urllib.request
from collections import Counter
from dataclasses import dataclass, field

import psycopg

FACILITY = "FAC-JAI-01"
LOCAL_AGENT_KEY = "local-agent-key-for-development-only"
LOCAL_ACCOUNT = ("demo@setuhaul.local", "setuhaul-demo-2026")


@dataclass
class Tally:
    """Outcome counts per action, thread-safe."""
    counts: Counter = field(default_factory=Counter)
    errors: list[str] = field(default_factory=list)
    bursts: list[dict] = field(default_factory=list)
    lock: threading.Lock = field(default_factory=threading.Lock)

    def add(self, key: str, error: str | None = None) -> None:
        with self.lock:
            self.counts[key] += 1
            if error and len(self.errors) < 50:
                self.errors.append(error)


class Http:
    def __init__(self, base: str, headers: dict[str, str] | None = None):
        self.base = base.rstrip("/")
        self.headers = headers or {}

    def call(self, method: str, path: str, body: dict | None = None) -> tuple[int, dict | list | None]:
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.base + path, data=data, method=method,
                                     headers={**self.headers, **({"Content-Type": "application/json"} if data else {})})
        try:
            with urllib.request.urlopen(req, timeout=180) as res:
                raw = res.read()
                return res.status, json.loads(raw) if raw else None
        except urllib.error.HTTPError as e:
            raw = e.read()
            try:
                return e.code, json.loads(raw) if raw else None
            except ValueError:
                return e.code, {"error": raw[:200].decode(errors="replace")}


def login(api: str, email: str, password: str) -> Http:
    """A dock-api client signed in as a coordinator (session cookie)."""
    req = urllib.request.Request(api.rstrip("/") + "/api/auth/login", method="POST",
                                 data=json.dumps({"email": email, "password": password}).encode(),
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as res:
        cookie = res.headers.get("Set-Cookie", "").split(";")[0]
    if not cookie:
        raise RuntimeError("sign-in returned no session cookie")
    return Http(api, {"Cookie": cookie})


def open_shipments(conn: psycopg.Connection) -> list[str]:
    rows = conn.execute(
        """SELECT s.shipment_id FROM shipments s
           LEFT JOIN facility_checkins fc ON fc.shipment_id = s.shipment_id AND fc.gate_in_ts IS NOT NULL
           WHERE s.destination_facility_id = %s AND s.created_at <= setuhaul_now()
             AND s.current_status NOT IN ('COMPLETED', 'CANCELLED') AND fc.shipment_id IS NULL
           ORDER BY s.shipment_id""", (FACILITY,)).fetchall()
    return [r[0] for r in rows]


def _status(code: int) -> str:
    return "5xx" if code >= 500 else str(code)


def driver(agent: Http, shipments: list[str], stop: threading.Event, tally: Tally, rng: random.Random) -> None:
    while not stop.is_set():
        sid = rng.choice(shipments)
        code, options = agent.call("GET", f"/api/agent/shipments/{sid}/options?limit=6")
        if code != 200 or not options:
            tally.add(f"options {_status(code) if code != 200 else 'none'}",
                      f"options {sid}: {code} {options}" if code >= 500 else None)
            time.sleep(0.05)  # most trucks have no free slot on a full day; do not spin
            continue
        slot = rng.choice(options)["slotId"]
        code, hold = agent.call("POST", "/api/agent/holds", {"shipmentId": sid, "slotId": slot})
        reason = (hold or {}).get("code", "") if isinstance(hold, dict) else ""
        tally.add(f"hold {_status(code)} {reason}".strip(), f"hold {sid} {slot}: {code} {hold}" if code >= 500 else None)
        if code != 201:
            continue
        time.sleep(rng.uniform(0, 0.5))
        action = "confirm" if rng.random() < 0.6 else "release"
        code, out = agent.call("POST", f"/api/agent/holds/{hold['appointmentId']}/{action}")
        reason = (out or {}).get("code", "") if isinstance(out, dict) else ""
        tally.add(f"{action} {_status(code)} {reason}".strip(),
                  f"{action} {hold['appointmentId']}: {code} {out}" if code >= 500 else None)


def burst(agent: Http, shipments: list[str], tally: Tally, rng: random.Random, size: int) -> None:
    """Several drivers hold the same free slot at the same instant."""
    picks = rng.sample(shipments, min(len(shipments), size * 3))
    by_slot: dict[str, list[str]] = {}
    for sid in picks:
        code, options = agent.call("GET", f"/api/agent/shipments/{sid}/options?limit=10")
        for o in options or [] if code == 200 else []:
            by_slot.setdefault(o["slotId"], []).append(sid)
    contested = [(slot, sids) for slot, sids in by_slot.items() if len(sids) >= 2]
    if not contested:
        tally.add("burst skipped: no slot free for two trucks")
        return
    slot, sids = max(contested, key=lambda x: len(x[1]))
    sids = sids[:size]
    gate = threading.Barrier(len(sids))
    results: list[tuple[str, int, str]] = []
    lock = threading.Lock()

    def one(sid: str) -> None:
        gate.wait()
        code, out = agent.call("POST", "/api/agent/holds", {"shipmentId": sid, "slotId": slot})
        with lock:
            results.append((sid, code, (out or {}).get("code", "") if isinstance(out, dict) else ""))

    threads = [threading.Thread(target=one, args=(s,)) for s in sids]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    winners = [r for r in results if r[1] == 201]
    tally.bursts.append({"slot": slot, "drivers": len(sids), "winners": len(winners),
                         "outcomes": sorted(f"{c} {k}".strip() for _, c, k in results)})
    tally.add(f"burst {len(sids)} drivers, {len(winners)} winner(s)")
    for _, code, _ in results:
        if code >= 500:
            tally.add("burst 5xx", f"burst on {slot}: {results}")


def solver_loop(solver: Http, stop: threading.Event, tally: Tally) -> None:
    while not stop.is_set():
        code, out = solver.call("POST", "/replan", {"facilityId": FACILITY, "trigger": "MANUAL",
                                                    "detail": "race harness"})
        status = (out or {}).get("status", "?") if isinstance(out, dict) else "?"
        tally.add(f"replan {_status(code)} {status}", f"replan: {code} {out}" if code >= 500 else None)


def coordinator(coord: Http, conn_url: str, stop: threading.Event, tally: Tally, rng: random.Random) -> None:
    with psycopg.connect(conn_url, autocommit=True) as conn:
        while not stop.is_set():
            row = conn.execute(
                """SELECT slot_id FROM appointment_slots WHERE facility_id = %s AND slot_status = 'OPEN'
                     AND slot_start_ts > setuhaul_now() + interval '1 hour'
                   ORDER BY random() LIMIT 1""", (FACILITY,)).fetchone()
            if not row:
                time.sleep(1)
                continue
            slot = row[0]
            code, out = coord.call("POST", f"/api/slots/{slot}/status", {"status": "BLOCKED", "reason": "race harness"})
            tally.add(f"block {_status(code)}", f"block {slot}: {code} {out}" if code >= 500 else None)
            time.sleep(rng.uniform(1, 3))
            code, out = coord.call("POST", f"/api/slots/{slot}/status", {"status": "OPEN"})
            tally.add(f"reopen {_status(code)}", f"reopen {slot}: {code} {out}" if code >= 500 else None)
            time.sleep(rng.uniform(0.5, 2))


def decide(coord: Http, tally: Tally, rng: random.Random, all_pending: bool = False) -> int:
    """Approves (60 %) or rejects the changes waiting on the Driver cases screen: one, or all of them."""
    code, view = coord.call("GET", f"/api/facilities/{FACILITY}/driver-cases")
    if code != 200:
        tally.add(f"driver-cases {_status(code)}", f"driver-cases: {code} {view}" if code >= 500 else None)
        return 0
    pending = view.get("approvals", []) if isinstance(view, dict) else []
    for req in pending if all_pending else pending[:1]:
        action = "approve" if rng.random() < 0.6 else "reject"
        code, out = coord.call("POST", f"/api/approvals/{req['requestId']}/{action}", {})
        reason = (out or {}).get("code", "") if isinstance(out, dict) and code != 200 else ""
        if code == 409 and action == "approve":  # the slot was blocked meanwhile: a coordinator rejects instead
            code, out = coord.call("POST", f"/api/approvals/{req['requestId']}/reject", {"note": "slot no longer free"})
            action, reason = "reject after 409", (out or {}).get("code", "") if code != 200 else ""
        tally.add(f"{action} {_status(code)} {reason}".strip(),
                  f"{action} {req['requestId']}: {code} {out}" if code >= 500 else None)
    return len(pending)


def approver(coord: Http, stop: threading.Event, tally: Tally, rng: random.Random) -> None:
    while not stop.is_set():
        decide(coord, tally, rng)
        time.sleep(rng.uniform(0.5, 2))


INVARIANTS = {
    "I1 no slot has two active bookings": """
        SELECT slot_id, count(*) FROM appointments
        WHERE appointment_status IN ('PENDING_CONFIRMATION', 'CONFIRMED', 'IN_PROGRESS')
        GROUP BY slot_id HAVING count(*) > 1""",
    "I2 no truck has two current active bookings": """
        SELECT shipment_id, count(*) FROM appointments
        WHERE is_current = 1 AND appointment_status IN ('PENDING_CONFIRMATION', 'CONFIRMED', 'IN_PROGRESS')
        GROUP BY shipment_id HAVING count(*) > 1""",
    "I3 no two active bookings overlap on a dock": """
        WITH b AS (
            SELECT a.appointment_id, a.shipment_id, sl.dock_id, sl.slot_start_ts AS s,
                   sl.slot_start_ts + greatest(sl.slot_end_ts - sl.slot_start_ts,
                       make_interval(mins => COALESCE(sh.expected_unload_min, f.default_unload_min))) AS e
            FROM appointments a
            JOIN appointment_slots sl ON sl.slot_id = a.slot_id
            JOIN shipments sh ON sh.shipment_id = a.shipment_id
            JOIN facilities f ON f.facility_id = sl.facility_id
            WHERE a.appointment_status IN ('PENDING_CONFIRMATION', 'CONFIRMED')
              AND sl.facility_id = %(facility)s AND sl.slot_start_ts >= setuhaul_now()
              -- a weight-breach booking is left for a coordinator and may overrun on purpose
              AND sh.load_weight_kg <= (SELECT max_vehicle_weight_kg FROM docks d WHERE d.dock_id = sl.dock_id)
        )
        SELECT x.appointment_id, y.appointment_id, x.dock_id FROM b x JOIN b y
          ON x.dock_id = y.dock_id AND x.appointment_id < y.appointment_id AND x.s < y.e AND y.s < x.e
         AND x.shipment_id <> y.shipment_id""",
    "I4 every committed run passed the validator": """
        SELECT run_id FROM solver_run WHERE facility_id = %(facility)s AND status = 'COMMITTED'
          AND created_at >= %(since)s AND (violations IS NOT NULL OR NOT feasible)""",
    "I5 every rejected run says why": """
        SELECT run_id FROM solver_run WHERE facility_id = %(facility)s AND status = 'REJECTED'
          AND created_at >= %(since)s AND COALESCE(outcome, '') = ''""",
    # A request still PENDING, a decided request whose hold still blocks a slot, or an approved one whose hold never
    # became a booking. (An approved booking the solver moved later is CANCELLED with a re-plan reason: fine.)
    "I8 every reservation was released or decided": """
        SELECT r.request_id, r.request_status, h.appointment_status FROM slot_change_requests r
        JOIN appointments h ON h.appointment_id = r.hold_appointment_id
        WHERE r.facility_id = %(facility)s AND r.requested_at >= (SELECT setuhaul_now()) - interval '1 day'
          AND (r.request_status = 'PENDING'
               OR (r.request_status IN ('REJECTED', 'EXPIRED', 'WITHDRAWN') AND h.appointment_status
                   IN ('PENDING_CONFIRMATION', 'CONFIRMED', 'IN_PROGRESS'))
               OR (r.request_status = 'APPROVED' AND (h.appointment_status = 'PENDING_CONFIRMATION'
                   OR h.confirmed_at IS NULL)))""",
}


def check(conn: psycopg.Connection, since: dt.datetime, tally: Tally) -> dict[str, list]:
    out: dict[str, list] = {}
    for name, sql in INVARIANTS.items():
        rows = conn.execute(sql, {"facility": FACILITY, "since": since}).fetchall()
        out[name] = [list(map(str, r)) for r in rows]
    out["I6 every same-slot burst had at most one winner"] = [b for b in tally.bursts if b["winners"] > 1]
    out["I7 no request failed with a server error (5xx)"] = list(tally.errors)
    return out


def run(database_url: str, api: str, solver_url: str, seconds: int, drivers: int, burst_size: int,
        agent_key: str, account: tuple[str, str], seed: int = 1) -> dict:
    rng = random.Random(seed)
    agent = Http(api, {"X-Agent-Key": agent_key})
    solver = Http(solver_url)
    coord = login(api, *account)
    tally = Tally()
    with psycopg.connect(database_url, autocommit=True) as conn:
        (since,) = conn.execute("SELECT now()").fetchone()
        shipments = open_shipments(conn)
        if len(shipments) < 2:
            raise RuntimeError("need at least two open shipments: is the demo seeded?")
        stop = threading.Event()
        threads = [threading.Thread(target=driver, args=(agent, shipments, stop, tally, random.Random(seed * 100 + i)))
                   for i in range(drivers)]
        threads.append(threading.Thread(target=solver_loop, args=(solver, stop, tally)))
        threads.append(threading.Thread(target=coordinator, args=(coord, database_url, stop, tally,
                                                                   random.Random(seed * 7))))
        threads.append(threading.Thread(target=approver, args=(coord, stop, tally, random.Random(seed * 11))))
        started = time.monotonic()
        for t in threads:
            t.start()
        while time.monotonic() - started < seconds:
            burst(agent, shipments, tally, rng, burst_size)
            time.sleep(rng.uniform(1, 3))
        stop.set()
        for t in threads:
            t.join()
        # Every change still waiting for approval is decided, so each reservation ends one way or the other.
        decide(coord, tally, random.Random(seed * 13), all_pending=True)
        # One quiet re-plan, so the last coordinator change is planned around, then the checks.
        code, out = solver.call("POST", "/replan", {"facilityId": FACILITY, "trigger": "MANUAL",
                                                    "detail": "race harness: final"})
        tally.add(f"final replan {_status(code)} {(out or {}).get('status', '?') if isinstance(out, dict) else '?'}")
        failures = check(conn, since, tally)
        runs = dict(conn.execute(
            """SELECT status, count(*) FROM solver_run WHERE facility_id = %s AND created_at >= %s GROUP BY status""",
            (FACILITY, since)).fetchall())
        # Why runs were rejected: the outcome up to its first detail ("STALE", "INVALID: V3", "INFEASIBLE").
        rejected = dict(conn.execute(
            """SELECT COALESCE(substring(outcome FROM '^(STALE|INVALID: V[0-9, V]+|INFEASIBLE)'), outcome) AS why,
                      count(*) FROM solver_run
               WHERE facility_id = %s AND created_at >= %s AND status = 'REJECTED' GROUP BY 1 ORDER BY 2 DESC""",
            (FACILITY, since)).fetchall())
    return {
        "seconds": seconds, "drivers": drivers, "burstSize": burst_size,
        "actions": dict(sorted(tally.counts.items())), "runs": runs, "rejectedBecause": rejected,
        "bursts": tally.bursts,
        "invariants": {name: ("pass" if not rows else rows) for name, rows in failures.items()},
        "passed": all(not rows for rows in failures.values()),
    }


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--database-url", default=os.environ.get("DATABASE_URL"))
    p.add_argument("--api", default="http://localhost:8082")
    p.add_argument("--solver", default="http://localhost:8081")
    p.add_argument("--seconds", type=int, default=60)
    p.add_argument("--drivers", type=int, default=6)
    p.add_argument("--burst", type=int, default=5, help="drivers per same-slot burst")
    p.add_argument("--agent-key", default=os.environ.get("SETUHAUL_AGENT_KEY", LOCAL_AGENT_KEY))
    p.add_argument("--account", default=os.environ.get("SETUHAUL_ACCOUNT", ":".join(LOCAL_ACCOUNT)),
                   help="coordinator sign-in as email:password")
    p.add_argument("--seed", type=int, default=1)
    p.add_argument("--json")
    args = p.parse_args(argv)
    if not args.database_url:
        p.error("--database-url or DATABASE_URL is required")
    email, _, password = args.account.partition(":")
    report = run(args.database_url, args.api, args.solver, args.seconds, args.drivers, args.burst,
                 args.agent_key, (email, password), args.seed)
    text = json.dumps(report, indent=2, default=str)
    if args.json:
        with open(args.json, "w") as f:
            f.write(text + "\n")
    print(text)
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())

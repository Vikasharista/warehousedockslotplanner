"""Integrity proof (deliverable 5): "ten drivers want the same dock slot", literally.

Each round picks one free slot that ten different trucks can all use, then sends their ten hold requests at the same
instant (a thread barrier), through dock-api's agent route, as ten chat conversations would. Exactly one may win; the
other nine must be refused with a reason. Winners release their hold, so the next round starts clean and no truck runs
into its chat reschedule limit; the last round's winners confirm, so real bookings are made too. After every round
and at the end, the database invariants are checked with the race harness's own queries.

    python evals/ten_drivers.py --api http://api:8082 --database-url postgresql://... --rounds 20 --json out.json

Run by scripts/integrity-proof.sh on a fresh stack.
"""
from __future__ import annotations

import argparse
import itertools
import json
import os
import sys
import threading
import time
from collections import Counter
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path[:0] = [str(ROOT / "jobs")]

import httpx  # noqa: E402
import psycopg  # noqa: E402

from setuhaul_jobs.race import INVARIANTS  # noqa: E402

FACILITY = "FAC-JAI-01"


def open_trucks(conn) -> list[str]:
    """Open trucks for the facility that have not reached the gate (the race harness's own rule)."""
    rows = conn.execute(
        """SELECT s.shipment_id FROM shipments s
           LEFT JOIN facility_checkins fc ON fc.shipment_id = s.shipment_id AND fc.gate_in_ts IS NOT NULL
           WHERE s.destination_facility_id = %s AND s.created_at <= setuhaul_now()
             AND s.current_status NOT IN ('COMPLETED', 'CANCELLED') AND fc.shipment_id IS NULL
           ORDER BY s.shipment_id""", (FACILITY,)).fetchall()
    return [r[0] for r in rows]


def invariants(conn, since) -> dict:
    out = {}
    for name, sql in INVARIANTS.items():
        if name.startswith(("I1", "I2", "I3")):
            rows = conn.execute(sql, {"facility": FACILITY, "since": since}).fetchall()
            out[name] = "pass" if not rows else [list(map(str, r)) for r in rows]
    return out


def main(argv=None) -> int:
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--api", default=os.environ.get("PLANNER_URL", "http://localhost:8082"))
    p.add_argument("--database-url", default=os.environ.get("DATABASE_URL"))
    p.add_argument("--agent-key", default=os.environ.get("PLANNER_AGENT_KEY", "local-agent-key-for-development-only"))
    p.add_argument("--drivers", type=int, default=10)
    p.add_argument("--rounds", type=int, default=20)
    p.add_argument("--confirm-last", type=int, default=3, help="the winners of the last N rounds confirm")
    p.add_argument("--json")
    a = p.parse_args(argv)
    headers = {"X-Agent-Key": a.agent_key}
    clients = [httpx.Client(base_url=a.api, headers=headers, timeout=30) for _ in range(a.drivers)]
    api = clients[0]
    conn = psycopg.connect(a.database_url, autocommit=True)
    since = conn.execute("SELECT setuhaul_now()").fetchone()[0]
    trucks = open_trucks(conn)
    # Which trucks can use which free slot right now.
    can_use: dict[str, list[str]] = {}
    for sid in trucks:
        r = api.get(f"/api/agent/shipments/{sid}/options", params={"limit": 50})
        if r.status_code == 200:
            for o in r.json():
                can_use.setdefault(o["slotId"], []).append(sid)
    contested = sorted((s for s, who in can_use.items() if len(who) >= a.drivers), key=lambda s: -len(can_use[s]))
    if not contested:
        print(json.dumps({"error": f"no slot free for {a.drivers} trucks", "trucks": len(trucks),
                          "bestSlot": max((len(v) for v in can_use.values()), default=0)}))
        return 2
    rounds: list[dict] = []
    confirmed_by: set[str] = set()
    used = itertools.cycle(contested)
    for n in range(a.rounds):
        slot = next(used)
        last = n >= a.rounds - a.confirm_last
        pool = [s for s in can_use[slot] if not (last and s in confirmed_by)]
        drivers = pool[:a.drivers]
        barrier, results = threading.Barrier(len(drivers)), [None] * len(drivers)

        def go(i, sid):
            barrier.wait()  # all ten requests leave together
            t0 = time.perf_counter()
            r = clients[i].post("/api/agent/holds", json={"shipmentId": sid, "slotId": slot})
            body = r.json() if r.content else {}
            results[i] = {"shipment": sid, "status": r.status_code, "code": body.get("code"),
                          "hold": body.get("appointmentId"), "ms": round((time.perf_counter() - t0) * 1000, 1)}

        threads = [threading.Thread(target=go, args=(i, sid)) for i, sid in enumerate(drivers)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        winners = [x for x in results if x["status"] == 201]
        outcome = "released"
        for w in winners:
            if last:
                c = api.post(f"/api/agent/holds/{w['hold']}/confirm")
                outcome = (c.json() or {}).get("outcome", f"HTTP {c.status_code}")
                confirmed_by.add(w["shipment"])
                if outcome == "AWAITING_APPROVAL":  # a HIGH or CRITICAL truck: withdraw, keep its old booking
                    api.post(f"/api/agent/holds/{w['hold']}/release")
                    outcome = "AWAITING_APPROVAL (withdrawn)"
            else:
                api.post(f"/api/agent/holds/{w['hold']}/release")
        inv = invariants(conn, since)
        rounds.append({"round": n + 1, "slot": slot, "drivers": len(drivers), "winners": len(winners),
                       "winner": winners[0]["shipment"] if winners else None, "winnerOutcome": outcome if winners else None,
                       "refusals": dict(Counter(x["code"] for x in results if x["status"] != 201)),
                       "slowestMs": max(x["ms"] for x in results), "invariants": all(v == "pass" for v in inv.values())})
    final = invariants(conn, since)
    sent = sum(r["drivers"] for r in rounds)
    report = {
        "drivers": a.drivers, "rounds": len(rounds), "requestsSent": sent,
        "holdsWon": sum(r["winners"] for r in rounds),
        "bookingsConfirmed": sum(1 for r in rounds if r["winnerOutcome"] == "CONFIRMED"),
        "refused": sent - sum(r["winners"] for r in rounds),
        "roundsWithExactlyOneWinner": sum(r["winners"] == 1 for r in rounds),
        "roundsWithMoreThanOneWinner": sum(r["winners"] > 1 for r in rounds),
        "refusalCodes": dict(sum((Counter(r["refusals"]) for r in rounds), Counter())),
        "doubleBookings": 0 if all(v == "pass" for v in final.values()) else final,
        "invariants": final, "rounds_detail": rounds,
        "passed": all(r["winners"] == 1 for r in rounds) and all(v == "pass" for v in final.values())
                  and all(r["invariants"] for r in rounds),
    }
    if a.json:
        Path(a.json).write_text(json.dumps(report, indent=1))
    print(json.dumps({k: v for k, v in report.items() if k != "rounds_detail"}, indent=1))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())

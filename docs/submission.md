# Mini Project 2 submission: SetuHaul Logistics

The assignment's template, filled in. Each link opens the deliverable in this repository.

**Name:** _(your name)_
**Repo link:** https://github.com/Vikasharista/warehousedockslotplanner (public)
**Live demo link (if deployed):** not deployed. One command runs it locally; [docs/deploy.md](deploy.md) puts the
same stack on any Linux VM with HTTPS and demo accounts.
**Stack used:**
- PostgreSQL 16;
- solver: Java 21 with Timefold Solver 2.8.0 on Quarkus 3.40;
- dock-api: Go 1.24 with GORM;
- driver agent and jobs: Python 3.12 (httpx; Claude for optional message reading);
- frontend: React 18, TypeScript and Vite;
- Docker Compose to run it.

Exact versions are in the [README](../README.md#library-versions).

---

**1. Problem Definition Sheet and data audit:** [docs/problem-definition.md](problem-definition.md) and
[docs/data-audit.md](data-audit.md). The audit has 14 findings, each proved by a query in [db/audit/](../db/audit/).

**Top data issue you found:** dock D3 broke down at 09:15 (DEVT001), yet its 09:00 slot is still OPEN, with SHP1005
confirmed on it ([A1](data-audit.md#a1)). Read naively, the data would keep promising a broken dock. Any slot that
overlaps a dock event is now unavailable, and the booking on it is re-planned.

**2. Decision register:** [docs/decision-register.md](decision-register.md), covering all 13 decisions, each with an
answer, a reason and evidence.

**How you order competing requests:**
- **Between drivers in chat:** the first valid hold wins. Holds are decided one at a time under a per-facility lock,
  with a unique index behind it, and the loser is told the slot has gone and why.
- **Across the facility:** the solver plans by priority, weighting each minute of waiting CRITICAL 27, HIGH 9,
  NORMAL 3, LOW 1. It keeps confirmed promises unless they no longer work. A CRITICAL load may bump one LOW or NORMAL
  booking more than 2 hours out, and HIGH and CRITICAL chat changes need a coordinator.

**3. Prototype, how to run it (one command):**

```bash
docker compose up --build
```

Then open http://localhost:5173 and sign in with `demo@setuhaul.local` / `setuhaul-demo-2026` (local only). The
screens are Plan, Shipments, Capacity, Driver cases and a Driver chat console.

**4. Scenario run (link):** [docs/scenario-run/README.md](scenario-run/README.md). It is a script, as the template
allows: `./scripts/scenario-run.sh`.
- It drives the real agent against the running planner and records every planner call.
- Six deterministic evals and the database invariants score it, with the brief's measures and a two-layer review.
- All 14 conversations are in [conversations.md](scenario-run/conversations.md).

**Which case escalates and why:** SHP1015, a HIGH reefer load, sends the snapshot's THR005 message: "Evening
traffic. ETA 6:30. Can the reefer unload tonight?"
- D5, the only reefer dock, is in planned maintenance from 18:00 to 22:00.
- The truck cannot be ready before 19:00, and no new start is allowed after 21:00, so no feasible slot exists.
- The agent invents nothing. It opens an URGENT `NO_FEASIBLE_SLOT` case with the window searched, and tells the
  driver a coordinator is checking.

**5. Integrity test (link):** [docs/integrity-proof.md](integrity-proof.md), run with
`./scripts/integrity-proof.sh`.

**Result (requests sent, bookings made, double bookings):**
- **Ten drivers, one slot:** 20 rounds of ten simultaneous hold requests for the same free slot.
  - 200 requests sent; 20 holds won, exactly one per round, never two.
  - 180 refused with a reason: 165 "slot taken", 15 "truck already holds a slot".
  - The last three winners confirmed: 3 bookings made.
  - **0 double bookings.**
- **Race harness:** 60 seconds of 8 drivers, same-slot bursts, the solver re-planning and a coordinator blocking
  slots.
  - 37,001 hold requests: 55 holds won, 29 confirms, 8 approvals.
  - 56 plans committed; 10 rejected with a reason (stale, or a slot blocked mid-solve).
  - All 8 invariants pass, so **0 double bookings**.

**6. Client readout (link):** [docs/client-readout.md](client-readout.md), one page covering policy, limits and risks.

**Optional scheduling tool (link, or not attempted):** attempted. See [solver/README.md](../solver/README.md), with
the [baseline comparison](results/phase6/baseline.md) against first come, first served and priority first.
- At noon on the 70-truck day, it places 47 of 66 trucks with 108 minutes' average wait and no priority inversions.
- First come, first served places 43, with 213 minutes' wait and 146 inversions.

**What does not work yet:**
- **WhatsApp is not verified end to end.** It needs a public HTTPS webhook; Telegram and the simulator are tested.
- **Labour and equipment are not modelled.**
- **GPS is simulated on three lanes** at a fixed 45 km/h, so its ETAs can be an hour off.
- **Gate-in to dock-in waiting is not observed.** The simulation has no real dock-in times, so waits are the planner's
  predictions.
- **Not built:** a rate limit per phone, and a retention period for stored messages.
- **Open with operations:** a LOW driver who holds first keeps the slot against a later HIGH request (first come,
  first served in chat). This is a policy choice to confirm, not a bug.

# Integrity proof

Deliverable 5: competing requests never double-book a slot. The brief puts it as "ten drivers want the same dock
slot", so the first test does exactly that. One command reproduces everything on a fresh stack:

```bash
./scripts/integrity-proof.sh
```

## 1. Ten drivers, one slot

[`evals/ten_drivers.py`](../evals/ten_drivers.py) works in rounds:
- **Setup:** it picks a free slot that ten different trucks can all use, and sends their ten hold requests at the same
  instant (a thread barrier), through dock-api's agent route, as ten chat conversations would.
- **Pass condition:** exactly one may win, and the other nine must be refused with a reason.
- **Between rounds:** winners release their hold, so no truck runs into its chat reschedule limit.
- **Last rounds:** the winners confirm, so real bookings are made too.
- **Checks:** the database invariants run after every round.

Setting: the snapshot plus 12 generated trucks known at 09:55. The snapshot alone has 13 open trucks, and at most 9 of
them can use any one slot. The full 70-truck day leaves no slot free for even two.

| Result | Value |
| --- | --- |
| Rounds, drivers per round | 20, 10 (the last round had 9, because a truck that had confirmed sat it out) |
| Requests sent | **199**, all within about 41 ms per round |
| Holds won | **20**: exactly one winner in every round, never two |
| Refused | **179**: `SLOT_UNAVAILABLE` 164 (someone else holds it), `HOLD_CONFLICT` 15 (that truck already holds another slot) |
| Bookings made at the end | The last three winners confirmed: 1 booking at once (NORMAL); 2 HIGH changes went to coordinator approval, as policy says, and were withdrawn |
| Double bookings | **0**: invariants I1 (no slot with two active bookings), I2 (no truck with two current bookings) and I3 (no two unloads overlapping on a dock) passed after every round |

Raw results: [results/phase8/ten-drivers.json](results/phase8/ten-drivers.json).

## 2. The race harness: everything at once

[`python -m setuhaul_jobs.race`](../jobs/setuhaul_jobs/race.py) runs, for 60 seconds against the running stack:
- 8 drivers holding, confirming and releasing at random;
- bursts of 6 drivers on one slot;
- the solver re-planning in a loop;
- a coordinator blocking and reopening slots and deciding approvals.

Then it checks eight invariants in the database.

| Run | Requests | What happened | Invariants |
| --- | --- | --- | --- |
| Phase 6, seed 1 (6 drivers) | 271 holds, 146 confirms, 125 releases | Solver: 29 runs committed, 25 rejected (12 stale, 13 slot blocked mid-solve) | all 7 pass |
| Phase 6, seed 2 (8 drivers, bursts of 6) | 368 holds, 217 confirms, 151 releases | Solver: 25 runs committed, 29 rejected (16 stale, 13 slot blocked mid-solve) | all 7 pass |
| **Phase 8**, seed 3 (8 drivers, bursts of 6) | **40,659 hold requests** | 45 holds won, 27 confirms, 6 approvals and 7 rejections by the coordinator, 18 releases. 31,471 refused by the two-change chat limit (phase 7) and 9,141 because the truck already held a slot: drivers who exhaust their changes keep hammering, and the planner keeps saying no. Solver: 51 runs committed, 11 rejected, each with its reason (6 stale, 5 failed validator V6 after a coordinator blocked a slot mid-solve) | **all 8 pass** |

The phase 8 run shows a property the phase 6 runs could not: once the chat limit applies, sustained hammering still
breaks nothing. The ten-drivers test above shows the pure contention case cleanly.

Raw results: [race-seed1.json](results/phase6/race-seed1.json), [race-seed2.json](results/phase6/race-seed2.json),
[race-phase8.json](results/phase8/race-phase8.json).

## 3. Why it holds

1. **Showing is not reserving.** An options list writes nothing, so two drivers may see the same slot.
2. **One lock per facility.** A hold takes `pg_advisory_xact_lock('setuhaul.bookings:<facility>')`, re-runs the
   feasibility check for that slot inside the transaction, then inserts. The solver's commit takes the same lock.
3. **A unique index behind the lock.** At most one active appointment (pending, confirmed or in progress) per slot, and
   one current one per truck, even if the lock were bypassed.
4. **The solver commits only what is still true.** It runs the independent validator (V1 to V8), then a stale check,
   in one transaction. Otherwise the run is rejected with a reason and re-planned.
5. **Every check counts a booking's whole unload.** The first full race run in phase 6 failed I3: a hold sat next to
   a 65-minute unload. Holds, the solver's view, validator V3 and the stale check were fixed together, and each
   regression test fails on the old code.

## 4. Tests that pin it

- **Database level.** `jobs/tests/test_integrity.py`:
  - `test_competing_bookings_for_one_free_slot_leave_exactly_one_winner`;
  - `test_second_active_booking_on_an_occupied_slot_is_rejected`;
  - `test_a_pending_hold_also_blocks_the_slot`.
- **dock-api.** `TestConcurrentHoldsOnOneSlotOneWins` and `TestALongUnloadNextDoorMakesASlotUnavailable`.
- **Solver.** In `CommitIT`:
  - `aSlotTakenAfterSolvingIsStaleAndTheReplanRetries`;
  - `aRunPlannedBeforeNewerInputsIsStale`;
  - `aHoldMadeWhileSolvingWhoseUnloadRunsIntoANewAllocationMakesTheRunStale`.
- **End to end through the agent.** The [scenario run](scenario-run/README.md#evals) checks invariants I1 to I5
  after its 14 conversations. Two drivers there are shown the same slot, the first hold wins, and the other driver
  hears why.

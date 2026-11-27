# Decision register

This document answers the 13 decisions in the SetuHaul project brief ("Decisions you must make",
`docs/Requirements.pdf`, page 8). Each decision has an answer, the reason for it, the evidence that already exists
in this repository, and the evidence the brief asks for together with its status.

- **Source.** The register was written from the code (phase 8), the tests, the
  [scenario run](scenario-run/README.md) and `CLAUDE.md`. The plan of record (claude.ai artifact
  `7F818ubM84vjsf6qZ4DowC`) could not be opened when this was written, so a statement here that differs from it is a
  question to settle, not an override.
- **Times** are in IST (Asia/Kolkata, +05:30). "The planner" means dock-api and the solver together; "the agent" is
  the driver chat agent in `agent/`.
- **Evidence status:** **Done** means the evidence the brief asks for exists in the repository, in the form the brief
  asks for. Most of it is the [scenario run](scenario-run/README.md), which drives the real agent against the running
  planner, records every planner call, and scores the traces with deterministic evals.

## Summary

| # | Decision | Answer in one line | Evidence |
| --- | --- | --- | --- |
| 1 | What to collect before showing options | Only what the planner cannot know: which trip, the arrival time, and the driver's earliest and latest acceptable times | **Done**: three recorded chats |
| 2 | Linking a chat to driver, shipment and appointment | The channel's phone number, matched to open trips, then the driver confirms the trip; the booking is read fresh every turn | **Done**: message-to-rows trace |
| 3 | Revised ETA and how sure it is | A fixed rule from the driver's words with a confidence; the planner's effective ETA (override, then GPS, then the report) is what the plan uses, and the driver is told so | **Done**: the rule on recorded messages |
| 4 | What makes a slot feasible | One SQL rule shared with the solver: dock type, weight, readiness, hours, dock events and every other booking's whole unload | **Done**: written check on three shipments |
| 5 | What "available" means while another driver decides | Shown is not reserved: a slot is available only until someone holds it, and the hold is decided under one lock with a unique index behind it | **Done** |
| 6 | Hold, request, reservation, booking | Shown, then held for 15 minutes, then confirmed; HIGH and CRITICAL changes wait up to 60 minutes for a coordinator | **Done** |
| 7 | Ordering requests when capacity runs short | Chat holds are first come, first served; the solver plans everyone by priority (1, 3, 9, 27) and moves confirmed bookings only when they no longer work, or to place a CRITICAL truck | **Done**: two trucks competing, recorded |
| 8 | When the schedule recalculates, what is fixed, the objective | On check-in, a new request, an ETA move over 30 minutes, coordinator changes and every 15 minutes; feasible bookings, holds and work in the dock stay fixed | **Done**: one recorded replay |
| 9 | Stale options, cancellations, duplicates, retries | Each one is re-checked or ignored by key, never trusted from memory | **Done**: one replayed case each |
| 10 | No feasible slot | Hand off to a coordinator with a complete case; never invent a slot | **Done**: message and hand-off record |
| 11 | What to say when the preferred slot is not granted | Say why in plain words and give what still stands or the next options | **Done**: recorded refusals |
| 12 | Which decisions need a person | A fixed list of reason codes, each with its trigger, including money and contradictory data | **Done** |
| 13 | Proving no double booking | Database indexes, one lock per facility, a commit-time stale check, a race harness, and the invariants after the scenario run | **Done** |

---

## Understand

### 1. What must we collect before showing useful options?

**Answer.** The agent asks the driver only for facts the planner cannot hold, and looks everything else up.

| Ask the driver | Look up in the planner (never ask) |
| --- | --- |
| Which trip this is, as a Yes/No on the trip found for their phone, or a pick from a list | The shipment, priority, dock type, load weight, destination |
| How late, or what time they will arrive. For a breakdown: is the truck fixed, and is the time the total delay or only the repair time | The current booking and whether it still works (`canMakeBooking`) |
| The earliest time they want ("anything after 7"), if they said one | The planner's effective ETA and where it came from |
| The latest time they can finish unloading, asked once; "no limit" is accepted | Facility hours, the last allowed start, the gate buffer, gate contacts |
| Whether they want to see options or only report the delay. A request for options alone skips the delay questions | Free slots that fit the truck |

Some rules apply on top of the table:

- The agent stops asking when it has an arrival time. It asks for the latest finish only when the current booking
  will not work or the driver wants another time.
- After two clarification turns without an answer (`max_clarifications = 2`), a coordinator takes over (decision 12).
  The driver is never sent round in circles.

**Reason.** Missing inputs produce wrong options, but every extra question costs a driver who is on the road. The
brief's rule is to split what we must ask from what we must look up. The latest finish decides which options are
any use, so we ask for it, but only once and only when options will follow.

**Evidence in the repository.**
- The order of questions: `_progress_delay` in [agent/setuhaul_agent/agent.py](../agent/setuhaul_agent/agent.py)
  (steps 1 to 5 in the code). The verification step is `_auth_flow` in the same file.
- The brief's example message is read into structured fields by `test_rules_read_the_brief_example` in
  [agent/tests/test_units.py](../agent/tests/test_units.py): breakdown, 90 minutes, after 7, another pickup, near
  Neemrana.
- A booking that still works ends the conversation without options: `test_w2_still_works_follows_the_planners_rule`
  in [agent/tests/test_live.py](../agent/tests/test_live.py).

**Evidence the brief asks for:** three example chats with the fields asked for. **Done:**
- [S1](scenario-run/README.md#s1-delay-clarify-compare): the brief's own message. The agent asks two questions,
  "fixed?" and "total or repair time?", then the latest finish.
- [L2-missing-facts-THR003](scenario-run/conversations.md#l2-missing-facts-thr003): "I am late by one hour." No ETA
  question is needed.
- [S2b](scenario-run/README.md#s2b-low-truck-wins-the-race): THR011, an options-only request.

The [golden message set](../evals/golden_messages.jsonl) pins the fields read from all 11 snapshot driver messages and
the brief's phrases, and runs in CI.

### 2. How does a conversation link to the right driver, shipment and appointment?

**Answer.**

1. **Identity is the channel's phone number.** WhatsApp gives the sender's number on every message. Telegram asks
   the driver to share their contact, and the contact must be the sender's own.
2. **The number is matched to open trips** on its last 10 digits (`GET /api/agent/drivers/{phone}/shipments`).
3. **The driver confirms the trip:**
   - one open trip: the driver answers Yes or No to that trip;
   - several open trips: the driver picks one by number;
   - a No, or no trip at all: a coordinator case opens (`SHIPMENT_MISMATCH`, `NO_OPEN_TRIP` or `INVALID_PHONE`).
4. **The conversation is bound to one shipment and one thread**, the shipment's open `chat_threads` row.
5. **The appointment is never stored in the agent.** The agent reads the trip, with its current booking and any
   hold, from the planner on every turn.
6. **Verification is redone** when the trip completes or is cancelled, or after 24 hours of planner time
   (`session_ttl_hours`).

**Reason.** One driver can hold two shipments, as the seeded driver with SHP1004 and SHP1020 does. Guessing the trip
would attach a delay to the wrong load. Reading the booking every turn means the agent never acts on a booking the
solver has since moved.

**Evidence in the repository.**
- `test_a_driver_with_two_trips_picks_one_and_gets_the_planners_answer` and `test_an_unknown_number_becomes_a_case`
  in `agent/tests/test_live.py`.
- `test_a_contact_must_be_the_senders_own` in `agent/tests/test_units.py`.
- `TestShipmentsByPhoneMatchesTheLastTenDigits` and `TestGuideCasesAmbiguousDriverAndCancelledShipment` (the
  guide's ambiguous driver sees SHP1004 and SHP1020; the SHP1019 driver has no open trip) in
  [services/dock-api/internal/store/phase5_test.go](../services/dock-api/internal/store/phase5_test.go).

**Evidence the brief asks for:** a message-to-rows trace for an unclear case. **Done:**
[message-to-rows.md](scenario-run/message-to-rows.md). The THR010 driver has two open trips. The trace covers every
`chat_messages`, `driver_exceptions`, `eta_updates` and `appointments` row the conversation wrote, read back from
Postgres after the run.

### 3. How do we set the revised ETA and show how sure it is?

**Answer.** The agent turns the driver's words into an ETA and a confidence with a fixed rule. The planner then
decides which ETA the plan uses.

| The driver says | ETA recorded | Confidence |
| --- | --- | --- |
| A clock time ("reaching 18:30") | That time | HIGH; MEDIUM if they also said the truck is not fixed |
| A total delay ("90 minutes late", not a breakdown) | Current plan ETA + the delay | MEDIUM |
| A breakdown with a delay | First asks "fixed?" and "total or repair time?". Total: plan ETA + the delay | MEDIUM if total; LOW if not fixed |
| A breakdown where the time is only the repair time | Asks for the arrival time instead of guessing | From the answer |
| Only wants options | The planner's current ETA, unchanged | The planner's |

The planner's **effective ETA** ([`v_effective_eta`](../db/gps_tables.sql)) chooses in this order:

1. an operations override;
2. otherwise the GPS estimate, or the later of GPS and the driver's ETA while the driver has an open exception;
3. otherwise the latest report.

A LOW-confidence ETA adds 15 minutes before the truck counts as ready (`low_confidence_margin_min`). The planner
refuses an ETA more than 1 hour in the past or more than 48 hours ahead, and the agent then asks again.

When the effective ETA differs from what the driver said by 5 minutes or more, the reply names the source, for
example: "Your GPS position puts you here at {time}, so the plan uses that."

**Reason.** A repair time is not an arrival time, so the agent asks before adding numbers. A driver's report must
not silently override better evidence (GPS or the dock office). The driver should also never be surprised by which
time the plan used.

**Worked examples, derived from the rule and not yet run.** The scenario run should replace these with recorded
chats.

1. *"Tyre damaged near Neemrana. Around 90 minutes late."* This reads as a breakdown with 90 minutes, so the agent
   asks: "Is the truck fixed and moving now? Is 90 min your total delay to reach Jaipur DC, or only the repair
   time?"
   - "Fixed, total": ETA = plan ETA + 90 minutes, confidence MEDIUM.
   - "Not fixed, total": the same ETA, confidence LOW, so the truck counts as ready 15 minutes later.
   - "That's just the repair": the agent asks what time they expect to arrive.
2. *"Will reach by 18:30."* ETA = 18:30 today on the planner's clock, confidence HIGH. If GPS puts the truck there at
   18:50 and the driver has an open exception, the plan uses 18:50 (the later of the two), and the reply says so.

**Evidence in the repository.**
- The rule in code: step 1 of `_progress_delay` in `agent/setuhaul_agent/agent.py`.
- `test_w1_the_reply_names_the_eta_the_plan_uses` in `agent/tests/test_live.py`.
- `test_effective_eta_prefers_gps_but_respects_the_rules` in `jobs/tests/test_gps.py`.
- `lateEtaAndMultipleUpdatesSHP1006UseTheNewestEta` and `uncertainEtasSHP1013AndSHP1017GetTheLowConfidenceMargin`
  in `SeededCasesIT`.

**Evidence the brief asks for:** the ETA rule applied to two messages. **Done**, recorded in the scenario run:
- **S1** ("90 minutes late", then "total"): the plan ETA + 90 minutes, MEDIUM.
- **S2a** (THR001, "Reaching around 11:20"): 11:20, HIGH.
- **G4** ("already reached" while the GPS says otherwise): the plan keeps the GPS time and says so, and a coordinator
  checks the difference.

---

## Decide

### 4. What makes a slot feasible for this shipment?

**Answer.** A slot is feasible for a truck when **all** of these hold. The same rules sit in the solver (H1 to H14),
in dock-api's options query (`optionsSQL` in
[services/dock-api/internal/store/holds.go](../services/dock-api/internal/store/holds.go)) and in the independent
validator ([db/validate/](../db/validate/), V1 to V8).

1. The slot is `OPEN`, on an `ACTIVE` dock of the truck's destination facility.
2. **The dock fits the load:** the right dock type (a reefer load needs a reefer dock, a heavy load a heavy dock) and
   a weight limit at or above the load.
3. **The truck can be there.** The slot starts no earlier than the truck is ready: its gate-in time, or its effective
   ETA + the 30-minute gate buffer (+15 minutes for a LOW-confidence ETA).
4. The slot starts no later than the facility's last allowed start (for example RULE005: no new start after 21:00 at
   Jaipur) and inside the 12-hour planning horizon.
5. **No dock event overlaps the unload.** Breakdown, maintenance, manual block and capacity reduction all count.
6. **Nobody else is using it.** No active booking is on the slot, and no other truck's booking or hold on that dock
   has an unload running into this one. A truck occupies its dock for the longer of its unload and its slot, so a
   75-minute unload in the 18:00 slot also blocks 19:00.

For the agent, the driver's own limits come on top: not before the time they asked for, and finished by their latest
finish.

**Reason.** Many rules apply at once, and an option that passes some of them but not all becomes a broken promise.
Keeping one rule set across the solver, the API and the validator means the driver is never offered a slot the
solver would refuse.

**Evidence in the repository.**
- `TestOptionsFollowTheSolverRules` and `TestALongUnloadNextDoorMakesASlotUnavailable` in
  `services/dock-api/internal/store/store_test.go`.
- `test_w3_w4_a_long_unload_gets_exactly_the_planners_options` in `agent/tests/test_live.py`.
- In `SeededCasesIT`: `reeferHeavyAndUnloadDurationsSHP1010SHP1016SHP1011`,
  `reeferDockUnavailableSHP1015HasNoFeasibleSlotAndGoesToACoordinator` and
  `operatingHourLimitRULE005NoNewStartAfter2100`.

**Evidence the brief asks for:** a written check run on three shipments. **Done:**
[feasibility.md](scenario-run/feasibility.md) applies every rule, one by one, to three shipments:
- SHP1009, a CRITICAL standard load: 30 feasible slots;
- SHP1011, a heavy load: one dock, 4 slots;
- SHP1015, a reefer load: its only dock is in maintenance, so 0 slots and a coordinator.

### 5. What does "available" mean while another driver is deciding?

**Answer.** **Showing a slot does not reserve it.**

- A slot is *available to a truck* only if the options query returns it at that moment.
- A list shown to a driver is a snapshot. Nothing is written for it, and another driver may see the same slot.
- The slot stops being available to everyone else the moment one driver **holds** it. The hold is a
  `PENDING_CONFIRMATION` appointment, which the active-appointment unique index counts.

Holding works like this:

1. Take the facility's booking lock (`pg_advisory_xact_lock` on `setuhaul.bookings:<facility>`). The solver's commit
   takes the same lock.
2. Re-run the feasibility check for that one slot inside the transaction.
3. Insert the hold. The partial unique index on `appointments(slot_id)` refuses a second active booking even if the
   lock were bypassed.

If two drivers pick the same slot, the first hold wins. The second gets `SLOT_UNAVAILABLE` and hears that the slot was
just taken (decision 11).

**The state this needs:** `appointments` (status, `is_current`, `booked_at`), the hold expiry (15 minutes), the
approval reservation's expiry on `slot_change_requests`, and the per-facility advisory lock.

**Reason.** Two readers can see one slot. Making "available" mean "nobody has written a hold yet", and deciding that
write under a lock with a unique index behind it, means only one promise can ever be made.

**Evidence in the repository.**
- `CreateHold` in `services/dock-api/internal/store/holds.go`.
- `test_a_pending_hold_also_blocks_the_slot` in `jobs/tests/test_integrity.py`.
- `TestConcurrentHoldsOnOneSlotOneWins` in `store_test.go`.
- The W6 part of `test_w5_w6_w7_holds_last_15_minutes_survive_a_refused_switch_and_stale_buttons` in `test_live.py`:
  a rival takes the slot first, and the driver keeps their earlier hold.

**Evidence the brief asks for:** a definition and the state it needs. **Done in this section.** In the scenario run,
two drivers are both shown D2 14:00. The first hold wins, and the second driver hears it has gone
([S2a](scenario-run/README.md#s2a-high-truck-loses-the-race)).

---

## Commit

### 6. When does an option become a hold, request, reservation or confirmed booking?

**Answer.**

| State | Stored as | Who moves it | What the driver sees |
| --- | --- | --- | --- |
| **Shown** | Nothing in the planner, only the agent's session | The agent, from the options query | "Free slots at {facility}: 1) Dock D2, 18:00-19:00 … Reply with the option number, or 'none'." |
| **Held** | `appointments` row `PENDING_CONFIRMATION`, `DRIVER_CHAT`. If the truck already has a booking, the hold sits beside it (`is_current = 0`) and the old booking stays | The driver picks an option; the agent calls `POST /api/agent/holds` | "Holding Dock D2 18:00-19:00 for you until {time}. Confirm this slot?" |
| **Confirmed** (LOW or NORMAL) | The hold becomes the `CONFIRMED` current booking, and the old one is cancelled | The driver says Yes; `confirm` swaps them under the lock | "Confirmed: Dock D2 18:00-19:00 at {facility}. Please be at the gate by {time}. Your old slot … is released." |
| **Requested and reserved** (HIGH or CRITICAL changing an existing booking) | `slot_change_requests` row `PENDING`. The held slot is reserved for 60 minutes (`approval_reserve_min`), the old booking stays current, and nobody else (the solver included) may take the reserved slot | The driver says Yes; `confirm` answers 202 `AWAITING_APPROVAL` | "Requested Dock D2 18:00-19:00. This is a HIGH priority load, so a coordinator approves the change. A coordinator decides by {time}. Until then your slot stays dock D4 at {time}." |
| **Approved / rejected / expired** | Approve swaps the bookings. Reject or expiry releases the reservation and keeps the old booking | A coordinator on the Driver cases screen, or the expiry sweeper | The outcome arrives on the change feed (`kind: APPROVAL`) |
| **Released / expired hold** | The hold is cancelled, and the old booking stands | The driver says No, picks another option, or 15 minutes pass | "OK, released." and fresh options, or "The hold on that slot ran out." |
| **Solver booking** | `CONFIRMED`, `SCHEDULING_TOOL` | The solver's commit | The change feed (`kind: BOOKING`): "{shipment} moved from dock … to dock … because …" |

Two limits apply. A truck may change its booking by chat at most twice (`max_chat_reschedules = 2`). A truck that is
already unloading cannot hold.

**Reason.** Showing is not reserving, and reserving is not confirming. The 15-minute hold is long enough for a driver
who has stopped to read the message, and short enough that an abandoned hold does not starve others. HIGH and
CRITICAL loads carry customer commitments, so moving them is a coordinator's decision (decision 12). Meanwhile the
truck keeps the booking it has.

**Evidence in the repository.**
- In `store_test.go`: `TestSideHoldThenConfirmReplacesTheBooking`, `TestAnUnbookedTruckGetsACurrentHold` and
  `TestExpiredHoldsCannotBeConfirmedAndAreSwept`.
- In [phase7_test.go](../services/dock-api/internal/store/phase7_test.go):
  `TestCriticalChangeWaitsForApprovalThenApproveSwaps`, `TestRejectKeepsTheOldBooking`,
  `TestAReservationExpiresAfterSixtyMinutes`, `TestReleasingAWaitingHoldWithdrawsTheRequest` and
  `TestLowAndNormalChangesConfirmAtOnceUpToTheChatLimit`.
- In `test_live.py`: `test_a_high_load_change_waits_for_a_coordinator_and_the_outcome_reaches_the_driver` and
  `test_the_chat_limit_hands_the_third_change_to_a_coordinator`.

**Evidence the brief asks for:** the states, who moves each one, and what the driver sees. **Done in this table**,
and every state appears in the recorded run: shown, held, confirmed (S1, S2b), requested and approved (S2a), and
refused at the re-check (L5).

### 7. How do we order simultaneous requests when capacity runs short?

**Answer.** Two rules, at two levels.

1. **Between drivers in chat: first valid hold wins.** Holds are decided one at a time under the facility lock, in
   the order they arrive. The agent never ranks drivers. The only exception is that a HIGH or CRITICAL change needs a
   coordinator before it takes effect.
2. **Across the whole facility: the solver plans by priority.** Each waiting minute is weighted LOW 1, NORMAL 3,
   HIGH 9, CRITICAL 27. The score is compared level by level, so a better earlier level always wins:
   1. hard rules, never broken;
   2. trucks left without a slot, weighted by priority;
   3. priority inversions (a lower-priority truck served before a higher one that could have had the slot);
   4. next-pickup inversions (a truck with a next pickup scheduled served after one without, at equal priority);
   5. weighted waiting minutes plus a penalty for moving a confirmed booking (240 for a move it did not need, 30 for
      one it did).

**Fairness rules:**
- A feasible `CONFIRMED` booking is pinned. The solver does not take a promised slot away to improve the score.
- One exception: a CRITICAL truck may bump **one** LOW or NORMAL booking that starts more than 2 hours after the
  plan. It never bumps a truck at the gate, a flagged booking or a driver's hold.
- The bumped truck is re-planned, or goes to a coordinator as `BUMPED_FOR_CRITICAL`.

**Reason.** Fairness and service levels conflict. First come, first served is easy to explain to drivers and cannot
be gamed by the agent. Priority still has to win when the dock is short, and that ranking is a facility-wide
decision, so it belongs to the solver, which sees every truck. Pinning keeps faith with drivers who were already told
a slot. The bump is narrow on purpose: one CRITICAL load, a lower-priority victim, and enough notice to re-plan them.

**Evidence in the repository.**
- In `SeededCasesIT`: `simultaneousCompetitionSHP1006SHP1012SHP1013SHP1014GetsDistinctSlotsHighestPriorityFirst` and
  `priorityConflictSHP1009CriticalIsPlacedWithNoInversion`.
- `CommitIT.aCriticalRequestBumpsOneLowBookingAndAHighOneDoesNot`.
- [docs/results/phase6/baseline.md](results/phase6/baseline.md). On the 70-truck day at 12:00, the solver places
  47/66 trucks with 0 priority inversions. First come, first served places 43/66 with 146 inversions, and the
  priority-first rule places 43/66 with 0.
- The settings: [solver-defaults.properties](../solver/core/src/main/resources/solver-defaults.properties).

**Evidence the brief asks for:** a policy tested on competing trucks. **Done.** In
[S2](scenario-run/README.md#s2a-high-truck-loses-the-race), a LOW truck and a HIGH truck want the same slot. The LOW
driver holds first and keeps it. The HIGH driver hears why, takes another slot, and the change waits for a coordinator.
That is the first-come rule working as designed, with the trade-off stated below.

**Trade-off to state openly.** A LOW-priority driver who chats first can hold a slot that a HIGH truck would later
want. The solver will not take it back while it stays feasible, unless a CRITICAL truck bumps it. Operations should
confirm they accept this.

### 8. (Optional) When does the facility schedule recalculate, which work is fixed, and what is the objective?

**Answer.**

**When.** The solver service checks every 60 seconds and re-plans when one of these is due, in this order:

| Trigger | When it fires |
| --- | --- |
| `CHECKIN` | A truck reached the gate |
| `NEW_REQUEST` | A truck request was made (from its `created_at`) |
| `ETA_DELAY` | A booked truck's effective ETA moved more than 30 minutes, later or earlier, from the ETA its slot was planned on |
| `SCHEDULED` | No run for 15 minutes |
| `MANUAL` | A coordinator changed something on the Shipments or Capacity screens |

Event triggers wait 2 minutes after the last run, so a plan that cannot commit does not re-run every scan. The
horizon is the next 12 hours.

**Fixed work:**
- trucks in a dock (assumed to need 15 more minutes when they overrun);
- feasible `CONFIRMED` bookings;
- driver holds, for their 15 minutes;
- approval reservations, until they expire;
- bookings flagged to a coordinator (weight breach, start after the last allowed start), left exactly where they are.

A truck that becomes 30 minutes or more early is released to take a free earlier slot. Nobody else moves for it.

**Objective, in plain words:** place as many trucks as possible with the most important loads first, serve them in
priority order, cut weighted waiting, and change as few promised slots as possible.

**Commit.** The plan is written only if it passes the independent validator and a stale check (no newer input, no
slot taken since solving), in one transaction under the facility lock. Otherwise it is rejected with a reason and
re-planned.

**Reason.** Changes ripple through the whole day, so the solver re-plans only on events that change what is
possible. The 30-minute ETA threshold keeps small GPS jitter from moving trucks. Pinning keeps re-plans from
reshuffling drivers who were already told a slot.

**Evidence in the repository.**
- In [CommitIT](../solver/service/src/test/java/com/setuhaul/solver/service/CommitIT.java):
  - `triggersFollowTheClockEtasAndCheckins`;
  - `etaShiftsReplanOnlyPastThirtyMinutesAndAnEarlyTruckMovesAlone` (+29 minutes does nothing, +31 re-plans);
  - `replanCommitsValidBookingsAndASecondReplanChangesNothing`;
  - `aRequestExistsOnlyFromWhenItWasMadeAndTriggersARePlan`.
- `BaselineIT.theSameSnapshotSeedAndStepLimitGiveTheSamePlan` (determinism).

**Evidence the brief asks for:** a rule, an objective and one replay. **Done.** In
[S3](scenario-run/README.md#s3-check-in-replan) a coordinator checks a truck in. The first re-plan is rejected as STALE,
because it was planned before the check-in. The retry passes the validator and is committed. The run table lists every
solver run during the scenario.

### 9. How do we handle stale options, cancellations, duplicate messages and retries?

**Answer.**

| Case | What happens |
| --- | --- |
| **Stale option** | Each option button carries its slot id. A tap on an older list never holds a different slot: the agent refreshes the list, or keeps the live hold in focus. Every hold re-checks feasibility at that moment, and every confirm checks the hold has not expired. A solver plan made before newer inputs is rejected as `STALE` at commit. |
| **Cancellation** | A cancelled or completed shipment has no open trip. The agent closes the thread and starts again, and the planner refuses ETA reports for it. A cancelled booking frees its slot for others. |
| **Duplicate message** | The agent logs each (channel, external message id) and ignores one it has already handled. dock-api stores a re-sent message once (`"duplicate": true`). A second exception for the same shipment and type on the same day from another thread is stored as `DUPLICATE`, pointing at the live one. |
| **Retry** | A message counts as handled only after the reply is produced, so a failure is handled again when the channel retries. A failed send does not lose the reply or other notices. The Telegram poller retries an update three times. The Claude reader has a timeout and one retry, then falls back to the rules. |

**Reason.** Chat channels repeat messages, and drivers tap old buttons. Every one of these cases is decided against
the planner's current state, never against what the agent remembers.

**Evidence in the repository.**
- W7 in `test_w5_w6_w7_holds_last_15_minutes_survive_a_refused_switch_and_stale_buttons` (`test_live.py`).
- In `CommitIT`: `aRunPlannedBeforeNewerInputsIsStale` and `aSlotTakenAfterSolvingIsStaleAndTheReplanRetries`.
- In `SeededCasesIT`: `cancellationSHP1008FreesItsSlotForOthers`.
- `TestGuideCasesAmbiguousDriverAndCancelledShipment`.
- `TestThreadsMessagesExceptionsAndCases` (`phase7_test.go`).
- In `test_units.py`: `test_store_keeps_sessions_handled_messages_and_the_cursor`,
  `test_a_failed_send_does_not_stop_the_bot_and_the_reply_is_sent_on_retry`,
  `test_a_failing_update_is_handled_again_on_the_next_poll`,
  `test_the_poller_gives_up_on_an_update_after_three_attempts` and
  `test_a_stalled_or_failing_claude_falls_back_to_rules`.

**Evidence the brief asks for:** one replayed case for each. **Done:**
- **Stale:** [L5](scenario-run/conversations.md#l5-world-changes). A slot is blocked after it was shown. The pick is
  refused with a reason, and the old button does nothing.
- **Duplicate:** [S2a](scenario-run/README.md#s2a-high-truck-loses-the-race). THR001 is re-sent with the same message
  id (THR009) and gets no second reply.
- **Cancellation:** the SHP1019 driver has no open trip, and the planner refuses an ETA for the cancelled load
  (`TestGuideCasesAmbiguousDriverAndCancelledShipment`).
- **Retries:** the unit tests listed above.

---

## Fail safely

### 10. What happens when no feasible slot exists?

**Answer.** The agent never invents a slot. When the options query, after the driver's own limits, returns nothing:

1. The agent releases any hold the driver had.
2. It opens a coordinator case, `NO_FEASIBLE_SLOT`. The case is `URGENT` for a HIGH or CRITICAL load and `NORMAL`
   otherwise.
3. The thread and exception become `ESCALATED`.
4. It tells the driver: "There is no free slot for your truck at {facility} {window}. A coordinator is checking now
   and will message you here. Ref {case id}." The window is, for example, "after 19:00 finishing by 23:00", or "in the
   next 12 hours" when the driver gave no limits.
5. Further messages from the driver are added to the case. A coordinator's reply reaches the driver on the change
   feed (`kind: REPLY`).

**The hand-off record** (`coordinator_cases`, linked to the thread and the exception) holds:
- the reason code, urgency, shipment, driver phone, channel and thread;
- a summary with the dock type, priority, the window searched, the ETA, the latest finish and where the truck is;
- the full conversation, through the linked thread;
- the declared ETA and the earliest and latest acceptable times, on the linked exception.

On the solver side, a truck the solver cannot place stays unassigned and appears on the plan view's exceptions list
with its reason.

**Reason.** An invented answer is worse than an escalation. The coordinator gets everything needed to act without
asking the driver again.

**Evidence in the repository.**
- `_offer_options` and `_escalate` in `agent/setuhaul_agent/agent.py`.
- The case table in [db/phase7_tables.sql](../db/phase7_tables.sql).
- `reeferDockUnavailableSHP1015HasNoFeasibleSlotAndGoesToACoordinator` in `SeededCasesIT`.
- `test_a_coordinators_reply_ends_the_hand_over` in `test_units.py`.

**Evidence the brief asks for:** the escalation message and the hand-off record. **Done.** The scenario run ends here:
[S4](scenario-run/README.md#s4-no-slot-escalates) shows THR005's message, the agent's reply, and the record as the
coordinator sees it on the Driver cases screen. The live test
`test_no_free_reefer_slot_hands_over_with_a_complete_case` repeats it, and eval E6 checks every hand-off record.

### 11. What do we say when the preferred slot is not granted?

**Answer.** Say why in plain words, then say what still stands or what comes next. These are the agent's messages,
from `agent/setuhaul_agent/agent.py`:

| Situation | Message |
| --- | --- |
| Someone held the slot first, and the driver still has an earlier hold | "Sorry, {Dock D2 18:00-19:00} was just taken. You still hold {Dock D4 19:00-20:00} until {time}. Confirm it?" |
| Someone held the slot first, and the driver has no hold | "Sorry, {slot} is no longer available. Free slots at {facility}: …" |
| The slot went between hold and confirm | "That slot was taken before we could confirm. Here are the slots free now. …" |
| The hold ran out | "The hold on that slot ran out. Here are the slots free now. …" |
| The chat change limit was reached | "This trip has already been rescheduled the maximum number of times by chat. A coordinator will plan your slot and message you here. Ref {case id}." |
| A HIGH or CRITICAL change waits for approval | "Requested {slot}. This is a {HIGH} priority load, so a coordinator approves the change. A coordinator decides by {time}. Until then your slot stays dock {D4} at {time}. You will get the answer here." |
| The driver turned down every option | "OK. A coordinator will look for another arrangement and message you here. Ref {case id}." |

**Reason.** A refusal without a reason costs trust. Each message says what happened, what the driver still has, and
what happens next. None blames the driver or promises something the planner has not confirmed.

**Evidence in the repository.**
- W6 in `test_w5_w6_w7_…` asserts "was just taken" and "You still hold".
- `test_the_chat_limit_hands_the_third_change_to_a_coordinator`.
- `test_a_high_load_change_waits_for_a_coordinator_and_the_outcome_reaches_the_driver`.

**Evidence the brief asks for:** two refusal messages in plain words. **Done in this table**, and recorded in the
scenario run: "Sorry, Dock D2 14:00-15:00 is no longer available. Free slots at …" (S2a), and the blocked slot in L5.

### 12. Which decisions need human approval or takeover?

**Answer.** The agent hands over, with a case on the Driver cases screen, on these triggers. It never decides them
itself.

| Reason code | Trigger | Urgency |
| --- | --- | --- |
| `SAFETY` | The message mentions an accident, injury or danger, or a leaking, spilling or hazardous load. Always checked first, whatever else is happening, and it overrides any model reading | URGENT |
| `COMMERCIAL_QUERY` | Penalties, detention or waiting charges, payments, claims, damaged goods. Money is a person's decision; it overrides any model reading | NORMAL |
| `DATA_CONFLICT` | The driver says they are here (or nearly) while GPS puts the truck 60 minutes or more away (`conflict_min`). The plan keeps the GPS time; a coordinator checks; the conversation goes on | NORMAL |
| `NO_FEASIBLE_SLOT` | No slot fits the truck and the driver's limits | URGENT for HIGH or CRITICAL, else NORMAL |
| *Approval request* | A HIGH or CRITICAL truck changes its booking (`approval_priorities`) | Waits up to 60 minutes |
| `RESCHEDULE_LIMIT` | A third chat change for the same truck | NORMAL |
| `NO_ACCEPTABLE_OPTION` | The driver refuses every slot offered | NORMAL |
| `ETA_UNKNOWN` | No usable arrival time after the clarification questions | NORMAL |
| `UNCLEAR_CONVERSATION` | More than 2 turns the agent could not understand | NORMAL |
| `DRIVER_REQUESTED_HUMAN` | The driver asks for a person | NORMAL |
| `GENERAL_QUESTION` | A question other than facility hours, contacts or directions | LOW |
| `SHIPMENT_MISMATCH` | The driver says the trip on record is not theirs | NORMAL |
| `NO_OPEN_TRIP`, `INVALID_PHONE` | The number has no open trip, or cannot be read | NORMAL |
| *Flagged by the solver* | A load above the dock's weight limit, or a start after the facility's last allowed start: flagged, never moved | Plan view exceptions |

**Reason.** Safety, penalties, disputes and no-slot cases carry risk. The brief puts the following outside what the
agent decides alone:
- promising shared capacity;
- whether a vehicle fits a dock;
- how scarce capacity is ranked;
- whether a booking committed.

In this design the planner's code decides all of these, and a person decides the exceptions above.

**What is still not automated.** Penalties and money (`COMMERCIAL_QUERY`), contradictory data (`DATA_CONFLICT`) and
hazardous loads in trouble (`SAFETY`) now have explicit triggers. Two kinds of contradiction are still left to the
coordinators' screens rather than the chat:
- a driver's account against the gate record;
- regulated-load paperwork.

**Evidence in the repository.**
- `_escalate` and its callers in `agent/setuhaul_agent/agent.py`.
- `test_a_safety_message_escalates_and_the_coordinators_reply_comes_back`.
- `test_the_chat_limit_hands_the_third_change_to_a_coordinator`.
- `test_an_unknown_number_becomes_a_case`.
- The approval tests listed under decision 6.

**Evidence the brief asks for:** a list with the trigger for each. **Done in this table.** Every trigger has a test:
[agent/tests/test_guardrails.py](../agent/tests/test_guardrails.py) (money, safety, data conflict, no ETA, a general
question), `test_live.py` (safety, the chat limit, no slot, an unknown number), and the scenario run (no slot, a refused
list, money, safety, a hostile message, contradictory data, an unknown number).

---

## Prove

### 13. How do we prove the system did not double-book capacity?

**Answer.** Four layers, then a test that attacks all of them at once.

1. **Database.** Partial unique indexes allow one active appointment per slot (pending, confirmed or in progress)
   and one current active appointment per shipment.
2. **One lock per facility.** Driver holds and solver commits take the same advisory lock, and each re-checks the
   slot inside it.
3. **Commit-time stale check and validator.** A solver plan is refused if anything changed after it was solved, or if
   the independent SQL checks V1 to V8 find an overlap. Holds and the validator both count a booking's whole unload,
   not only its slot.
4. **The race harness** (`python -m setuhaul_jobs.race`). It runs, all at once against the running stack:
   - drivers holding, confirming and releasing through the agent API;
   - bursts of drivers on the same slot;
   - the solver re-planning in a loop;
   - a coordinator blocking slots.

   It then checks seven invariants in the database:
   - I1: no slot has two active bookings;
   - I2: no truck has two current active bookings;
   - I3: no two active bookings overlap on a dock;
   - I4: every committed run passed the validator;
   - I5: every rejected run says why;
   - I6: every same-slot burst had at most one winner;
   - I7: no request failed with a server error.

**Results.** First, the brief's own case, ten drivers on one slot (`evals/ten_drivers.py`, all in
[integrity-proof.md](integrity-proof.md)):
- 20 rounds of ten simultaneous hold requests, 200 in total;
- exactly one winner every round;
- 180 refused with a reason;
- 0 double bookings: I1 to I3 held after every round.

Then the race harness:

| Run | Holds / confirms / releases | Solver runs committed / rejected | Invariants |
| --- | --- | --- | --- |
| 60 s, 6 drivers, seed 1 | 271 / 146 / 125 | 29 / 25 | All 7 pass |
| 60 s, 8 drivers, bursts of 6, seed 2 | 368 / 217 / 151 | 25 / 29 | All 7 pass |
| Phase 8: 60 s, 8 drivers, bursts of 6, seed 3 | 37,001 hold requests: 55 won, 27,239 refused by the chat limit, 9,700 because the truck already held a slot / 29 / 26 | 56 / 10 | All 8 pass |

The first full run **failed I3**. A hold beside a 65-minute unload overlapped the next slot. The fix made every check
count a booking's whole unload, and each regression test fails on the old code.

**Reason.** The business trusts the allocation only with evidence. The harness tries hardest to break the system:
concurrent drivers, the solver and a coordinator, all at once.

**Evidence in the repository.**
- `test_competing_bookings_for_one_free_slot_leave_exactly_one_winner` in `jobs/tests/test_integrity.py`.
- `TestConcurrentHoldsOnOneSlotOneWins`.
- In `CommitIT`: `aSlotTakenAfterSolvingIsStaleAndTheReplanRetries` and
  `aHoldMadeWhileSolvingWhoseUnloadRunsIntoANewAllocationMakesTheRunStale`.
- [race-seed1.json](results/phase6/race-seed1.json), [race-seed2.json](results/phase6/race-seed2.json),
  [race-phase8.json](results/phase8/race-phase8.json) and [ten-drivers.json](results/phase8/ten-drivers.json).
- [verification.md](verification.md).

**Evidence the brief asks for:** a concurrency test and its result. **Done.**

---

## Where this design departs from the brief

These are choices, not accidents. The client readout should explain them.

| The brief | This design | Why |
| --- | --- | --- |
| "0 live GPS feeds. ETAs come from the plan or from the driver." | GPS is simulated on three fixed lanes into Jaipur, kept in its own table, and it outranks a driver's report | A recorded project decision (`CLAUDE.md`). With no GPS the planner falls back to the latest report, as the brief describes |
| The scheduling tool is optional, and the agent calls it as a controlled tool | The solver runs on its own triggers and commits valid plans by itself. The agent reads its results and holds free slots, but does not call it | Proposed reason, to confirm: a facility-wide plan should not depend on which driver happens to be chatting. Coordinator inputs ask for a re-plan in the background |
| "Start with a simple dispatch rule … move to constraint programming only if you can explain why." | Timefold Solver, starting from the priority-first rule | The baseline comparison shows the solver placing more trucks than first come, first served and priority first on the 70-truck day, never worse on the score |
| A WhatsApp-style chat | Telegram first; a WhatsApp Cloud API adapter exists but needs a public HTTPS webhook | Telegram is the channel set up for the demo. WhatsApp needs Meta to reach the webhook over public HTTPS (`agent/setuhaul_agent/channels/whatsapp.py`) |

## Open points

1. **The plan of record could not be read** when this register was written. Check it against this register.
2. **First come, first served between drivers in chat (decision 7).** Confirm with operations that a LOW driver who
   holds first keeps the slot against a later HIGH request. The scenario run shows exactly that case (S2).
3. **The warm start at 09:55 on the 70-truck day.** The production solver shows 5 priority inversions and 3
   next-pickup inversions, where the cold start shows 0 and 0, with the same 48 trucks placed (`baseline.md`). The
   score ranks trucks left without a slot above inversions, so this may come from placing a different set of trucks.
   Explain it in the readout, or look into it.
4. **Not built yet:** rate limiting per phone, a retention period for stored message text, and contradictions
   against the gate record (decision 12).
5. **WhatsApp is not verified end to end.** It needs a public HTTPS webhook. Telegram and the simulator are the
   tested channels.

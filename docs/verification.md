# Verification matrix

Every row of the plan's test and validation plan, the test or command that covers it, and its result at the end of
phase 6. The findings, for the head of operations and the warehouse planner, are in the
[phase 6 readout](https://claude.ai/artifact/5Hawn7Pmg39pbhSTFByMFs). Run the suites as in each module's README; `scripts/break-and-defend.sh` regenerates the comparison and scale
tables in [results/phase6](results/phase6/).

## Test and validation plan

| Plan row | What must hold | Evidence | Result |
| --- | --- | --- | --- |
| Constraint unit tests | Each of H1-H14 and each score level, one violating and one passing case | `solver/core` `DockConstraintsTest` (ConstraintVerifier) | 27 pass |
| Snapshot run | Every snapshot shipment gets a feasible plan or a named reason | `SnapshotSolveIT` (09:55 and 16:30) | pass |
| Seeded cases | The database guide's cases, one test each, expected slot written out | `SeededCasesIT` (16 planning cases), dock-api `TestGuideCasesAmbiguousDriverAndCancelledShipment`; the rest mapped below | pass |
| Plan validator | Independent of the solver, run before every commit | `db/validate/*.sql` (V1-V8), `jobs/tests/test_validate.py`, `CommitIT` | pass |
| Integrity proof | Parallel writes to one slot: exactly one wins, the rest fail and re-plan | `jobs/tests/test_integrity.py` (unique index), dock-api `TestConcurrentHoldsOnOneSlotOneWins`, `CommitIT` stale and race tests, **race harness** (below) | pass |
| Re-run trigger | +29 min does nothing, +31 min re-plans | `CommitIT.etaShiftsReplanOnlyPastThirtyMinutesAndAnEarlyTruckMovesAlone` | pass |
| Determinism | Same snapshot, seed and step limit give the same plan | `BaselineIT.theSameSnapshotSeedAndStepLimitGiveTheSamePlan` (warm and cold start) | pass |
| Scale | 240 shipments, 32 docks, 6 facilities inside the time limit | `ScaleIT` with `SETUHAUL_SCALE_JDBC_URL`; [results/phase6/scale.md](results/phase6/scale.md) | pass: slowest facility 3.6 s of 60 s |
| Baseline comparison | Weighted wait and inversions against first come first served and an earliest-slot rule | `BaselineIT` (solver never worse on the score); [results/phase6/baseline.md](results/phase6/baseline.md) | see the readout |

## GPS and early-arrival rows

| Plan row | Evidence | Result |
| --- | --- | --- |
| An ETA move of 9 min writes no estimate; 11 min writes one | `test_gps.py::test_a_fresh_estimate_is_written_only_when_it_moved_10_minutes_or_more` | pass |
| A driver's breakdown ETA is not overwritten by an earlier GPS ETA while the exception is open | `test_gps.py::test_effective_eta_prefers_gps_but_respects_the_rules` | pass |
| No ping for 30 min: the ETA holds, confidence LOW, +15 min margin | `test_gps.py::test_a_silent_device_is_marked_stale_once_and_keeps_its_last_eta`, `SnapshotSolveIT.aSilentGpsDeviceKeepsItsEtaButAddsTheLowConfidenceMargin` | pass |
| 35 min early moves to a free earlier slot; no other booking moves | `CommitIT.etaShiftsReplanOnlyPastThirtyMinutesAndAnEarlyTruckMovesAlone` (phase 6 implements the release: `early_release_min`) | pass |
| 20 min early keeps its slot, no re-plan | same test | pass |
| Simulated pings replay deterministically | `test_gps.py::test_simulation_is_repeatable_and_replaces_its_own_pings` | pass |

## Race harness (end to end)

`python -m setuhaul_jobs.race` against the running stack: driver holds, confirms and releases through the agent API,
same-slot bursts, the solver re-planning in a loop and a coordinator blocking slots, all at once; then invariants I1-I7
in the database. Results: [race-seed1.json](results/phase6/race-seed1.json),
[race-seed2.json](results/phase6/race-seed2.json).

| Run | Holds / confirms / releases | Solver runs (committed / rejected) | Invariants |
| --- | --- | --- | --- |
| 60 s, 6 drivers, seed 1 | 271 / 146 / 125 | 29 / 25 (12 stale, 13 slot blocked mid-solve) | all 7 pass |
| 60 s, 8 drivers, bursts of 6, seed 2 | 368 / 217 / 151 | 25 / 29 (16 stale, 13 slot blocked mid-solve) | all 7 pass |

The first full run failed I3: a driver held D4 19:00 while another truck's hold on D4 18:00 had a 65-minute unload.
Holds, the solver's view of side holds, validator V3 and the commit's stale check now all count a booking's whole
unload, not only its slot (`TestALongUnloadNextDoorMakesASlotUnavailable`, `CommitIT.aHoldMadeWhileSolving...`,
`test_validate.py::test_a_side_hold_occupies_the_dock_for_its_whole_unload`; each fails on the old code).

## The database guide's seeded cases

| Case | Covered by |
| --- | --- |
| Normal appointment (SHP1002), Unload overrun (DEVT003) | `SeededCasesIT.normalAppointmentSHP1002...` |
| Early arrival (SHP1003), Early truck does not automatically win | `SeededCasesIT.earlyArrivalSHP1003...` |
| Late arrival already at yard (SHP1004) | `SeededCasesIT.lateArrivalAtTheYardSHP1004...` |
| Late ETA reported, Multiple ETA updates (SHP1006) | `SeededCasesIT.lateEtaAndMultipleUpdatesSHP1006...` |
| Uncertain ETA (SHP1013, SHP1017) | `SeededCasesIT.uncertainEtas...` |
| Dock breakdown (SHP1005, DEVT001) | `SeededCasesIT.dockBreakdownDEVT001...` |
| Appointment cancellation frees capacity (SHP1008) | `SeededCasesIT.cancellationSHP1008...` |
| No-show (SHP1018) | `SeededCasesIT.noShowSHP1018...` |
| Reefer compatibility, Heavy vehicle, Different unloading durations | `SeededCasesIT.reeferHeavyAndUnloadDurations...` |
| Reefer dock unavailable, No feasible slot (SHP1015) | `SeededCasesIT.reeferDockUnavailableSHP1015...` |
| Simultaneous slot competition | `SeededCasesIT.simultaneousCompetition...` |
| Priority conflict (SHP1009, SHP1014) | `SeededCasesIT.priorityConflictSHP1009...` |
| Race condition protection | Integrity row above |
| Appointment history (APT1012A, APT1016A) | `SeededCasesIT.appointmentHistory...` |
| Warehouse confirmation pending (APT1013A, APT1014A) | `SeededCasesIT.pendingWarehouseConfirmation...` |
| Operating-hour limit (RULE005) | `SeededCasesIT.operatingHourLimitRULE005...`, validator V6 |
| Multi-facility dataset | `SeededCasesIT.multiFacilityPlansJaipurAlone`, scale test |
| Ambiguous shipment (DRV004), Cancelled shipment message (SHP1019) | dock-api `TestGuideCasesAmbiguousDriverAndCancelledShipment` |
| Duplicate driver message, Ask-only conversation, Communication failure, Missing contact data | Phase 7 below: a retried message is stored and answered once (dock-api `TestThreadsMessagesExceptionsAndCases`, agent `test_store_keeps_sessions_handled_messages_and_the_cursor`); the agent asks before holding; a failed send or planner call is retried (`test_a_failed_send_does_not_stop_the_bot...`, `test_a_failing_update_is_handled_again...`); an unknown number becomes a case (`test_an_unknown_number_becomes_a_case`) |
| No real-time tracking | Superseded by the decision to simulate GPS on three lanes (phase 3) |

## Phase 7: the driver agent and coordinator approvals

The agent review's 4 critical findings and 12 warnings, each with its test
([plan](https://claude.ai/artifact/CEhBRBoLMYFSsm8Yce24ny)). The agent's live tests run against the planner on the real
snapshot (`scripts/agent-suite.sh`); dock-api store tests and the solver's ITs need the seeded test database.

| Finding | What must hold | Evidence | Result |
| --- | --- | --- | --- |
| C1 agent wrote planner data in its own SQLite | No SQL against planner tables in `agent/`; agent holds are planner holds the solver sees | agent `test_the_agent_holds_no_planner_data`; race harness with agent-style holds and approvals: I1-I8 | pass |
| C2 approvals stored as 15-minute holds | A HIGH or CRITICAL change keeps its slot reserved 60 minutes, past the 15-minute sweep, in dock-api, the solver and V3 | dock-api `TestCriticalChangeWaitsForApprovalThenApproveSwaps`, `TestAReservationExpiresAfterSixtyMinutes`; solver `ReservationIT`; jobs `test_a_reservation_waiting_for_approval_outlasts_the_hold_expiry` | pass |
| C3 reject left the truck with no booking | Approve swaps the bookings; reject and expiry keep the old booking current | dock-api `TestRejectKeepsTheOldBooking`, `TestAReservationExpiresAfterSixtyMinutes`, `TestApprovalIsRefusedWhenTheSlotWasBlockedMeanwhile`; browser `web/e2e/driver-cases.mjs` (bookings checked in the database) | pass |
| C4 one failed send stopped the Telegram bot | A failed send or update leaves the poller running and is answered on retry; a message is marked handled only after it is answered | agent `test_a_failed_send_does_not_stop_the_bot...`, `test_a_failing_update_is_handled_again...`, `test_the_poller_gives_up_on_an_update_after_three_attempts` | pass |
| W1 ETA ignored GPS | The reply names the ETA the plan uses | agent `test_w1_the_reply_names_the_eta_the_plan_uses` (SHP1012, GPS lane) | pass |
| W2 "still works" used a grace period | The answer follows the planner's `canMakeBooking` | agent `test_w2_still_works_follows_the_planners_rule`; dock-api `TestTripStatusCarriesThePlannersClockAndRules` | pass |
| W3, W4 long unloads, reefer docks | Options are exactly the planner's | agent `test_w3_w4_a_long_unload_gets_exactly_the_planners_options` (SHP1009, 75-minute unload) | pass |
| W5 5-minute holds | A hold lasts the planner's 15 minutes | agent `test_w5_w6_w7_holds...` | pass |
| W6 a failed hold dropped the existing one | A refused switch leaves the first hold live | agent `test_w5_w6_w7_holds...` | pass |
| W7 stale buttons picked another slot | Buttons carry the slot id; an older button never holds another slot | agent `test_slot_buttons_carry_the_slot_id_one_per_row`, `test_w5_w6_w7_holds...` | pass |
| W8 wall clock | Times come from the planner (`asOf`); nothing plans on the wall clock | agent `test_nothing_plans_on_the_wall_clock`, `test_a_driver_with_two_trips...` (verified on 4 Aug 2026) | pass |
| W9 model output unchecked, no timeout | Out-of-range fields dropped; 10-second timeout, one retry, then rules | agent `test_out_of_range_readings_are_dropped`, `test_the_claude_client_gets_a_timeout_and_one_retry`, `test_a_stalled_or_failing_claude_falls_back_to_rules` | pass |
| W10 replies in group chats | Only private chats are answered | agent `test_group_chats_get_no_reply` | pass |
| W11 agent moved early trucks itself | An early arrival is reported only | agent `test_w11_an_early_truck_is_reported_never_moved_by_the_agent` | pass |
| W12 tested only on invented data | The agent suite runs against the Compose stack on the real snapshot | `scripts/agent-suite.sh`: 27 passed | pass |
| Approvals and replies reach the driver | APPROVAL, REPLY and BOOKING notices go to the right chat within one poll | agent `test_a_high_load_change_waits...`, `test_a_safety_message_escalates...`, `test_a_plan_change_reaches_the_driver_within_one_poll` | pass |
| Access | An agent key opens the agent's routes only; coordinators act with a session, recorded by email | dock-api `TestPhase7AccessMatrix`, `TestCoordinatorRoutes` | pass |
| Chat reschedule limit | A third chat change is refused and handed to a coordinator | dock-api `TestLowAndNormalChangesConfirmAtOnceUpToTheChatLimit`; agent `test_the_chat_limit_hands_the_third_change_to_a_coordinator` | pass |

## Agent fixes: trip stage, one case per conversation, Telegram workers

Run on a freshly seeded snapshot in Postgres 16: dock-api `go test ./...` (all packages pass; 29 store tests, none
skipped) and the whole agent suite against a running planner, solver and GPS service (36 passed: 23 unit, 13 live,
none skipped).

| Finding | What must hold | Evidence | Result |
| --- | --- | --- | --- |
| A truck in its dock was offered slots, then refused the hold (`HOLD_CONFLICT`) and offered slots again | Options, hold and confirm answer 409 `NOT_RESCHEDULABLE` once the truck has reached its dock (status IN_DOCK, a dock-in time or a booking IN_PROGRESS); the trip carries `canReschedule`; the agent answers with the trip status instead of the slot flow. A truck at the gate or in the yard can still move | dock-api `TestATruckInTheDockCannotReschedule`, `TestErrorsMapToStatusesAndHideInternals` (in dock); agent `test_a_truck_in_its_dock_gets_its_status_not_slot_options` (unit and live), `test_the_planners_refusal_ends_the_slot_flow...` | pass |
| Every safety message opened a new case, even while one was open | A thread keeps one open case: a later hand-over is appended to it and raises its urgency and reason, never lowers them, and puts the thread back to `ESCALATED`; a resolved case lets the next hand-over open a new one | dock-api `TestAThreadKeepsOneOpenCase`; agent `test_a_safety_message_on_an_open_case_raises_that_case` (live) | pass |
| One slow Telegram reply held up every driver | Each driver's updates go to their own queue, drained by a pool of workers (`TELEGRAM_WORKERS`, 8): other drivers are answered meanwhile, one driver's messages stay in order, a failed update is retried | agent `test_one_slow_driver_does_not_hold_up_the_others`, `test_one_drivers_messages_stay_in_order`, `test_a_failed_update_is_retried_with_workers_too` | pass |

## Phase 8: the submission (the brief's boundaries, evals, measures)

Rows added for the FDE brief's hand-in. They come from the brief's boundaries and measures, and from the course's eval
method: start from real failures, write the ideal path, and count where a trace leaves it.

| Row | What must hold | Evidence | Result |
| --- | --- | --- | --- |
| Money goes to a person | Penalties, detention charges, payments and claims reach a coordinator; the chat answers nothing about them | agent `test_money_questions_are_read_as_commercial_and_breakdowns_are_not`, `test_a_money_question_goes_to_a_coordinator_and_nothing_is_booked`; scenario run G1 | pass |
| Contradictory data goes to a person | "Already here" against a GPS time 60+ minutes away keeps the GPS time and opens a `DATA_CONFLICT` case | agent `test_a_driver_claim_the_gps_contradicts_is_flagged_and_the_plan_keeps_the_gps_time`, `test_no_conflict_case_without_gps_evidence`; scenario run G4 | pass |
| Hazardous load in trouble | A leak or spill is a safety case | agent `test_a_leaking_load_is_a_safety_case`; golden SAFE-2 | pass |
| PII gate | No phone number, vehicle number or email reaches the model | agent `test_personal_data_is_masked_before_the_model`, `test_the_claude_reader_only_ever_sends_masked_text` | pass |
| Hostile messages | An instruction inside a message picks, holds or confirms nothing | agent `test_a_hostile_message_cannot_pick_or_confirm_a_slot`; golden HOSTILE-1 to 3; scenario run G3 | pass |
| "The second one works, but I need to leave by 9" | A pick is checked against a limit given with it before anything is held | agent `test_a_pick_that_ends_after_the_driver_must_leave_is_not_held`; golden BRIEF-LEAVE9 | pass |
| Every hand-off trigger tested | `ETA_UNKNOWN`, `GENERAL_QUESTION`, `NO_FEASIBLE_SLOT`, `NO_ACCEPTABLE_OPTION` reach a coordinator | agent `test_no_usable_arrival_time...`, `test_a_question_the_agent_cannot_answer...`, live `test_no_free_reefer_slot_hands_over_with_a_complete_case`; scenario run L3, S4 | pass |
| Golden message set | Every snapshot driver message, the brief's phrases, Hinglish, money, safety and hostile messages read as labelled | `evals/test_evals.py` on [golden_messages.jsonl](../evals/golden_messages.jsonl). The first run failed 5 cases (MSG011, MSG012, MSG014, MSG019, BRIEF-LEAVE9); the reader was fixed and all now pass | pass |
| Deterministic trace checks | E1 approval before every commit, E2 no invented availability, E3 every claim backed, E4 ideal change path, E5 HIGH/CRITICAL wait for a coordinator, E6 complete hand-offs | `evals/checks.py` on every scenario-run trace, and on hand-made traces that break each rule (`evals/test_evals.py`) | pass |
| Ten drivers, one slot | Ten simultaneous holds on one free slot: exactly one wins, every round | `evals/ten_drivers.py` ([integrity-proof.md](integrity-proof.md)): 20 rounds, 200 requests, 20 winners, 0 double bookings; race harness phase 8: all 8 invariants | pass |
| Scenario run | The brief's 7 items, ending in one escalation; every conversation acceptable; invariants I1-I5 after the run | [scenario-run/README.md](scenario-run/README.md) | pass |
| Data audit A11-A14 | Two open trips per driver, repeated messages, a failed notice, driver-only ETAs | `jobs/tests/test_audit.py` | pass |
| CI | The agent's unit and guardrail tests, the golden set and the trace checks run on every pull request | [.github/workflows/evals.yml](../.github/workflows/evals.yml) | added |

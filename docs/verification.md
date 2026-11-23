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
| Duplicate driver message, Ask-only conversation, Communication failure, Missing contact data | Out of scope: the planner does not read chat messages, e-mails or contacts. A driver agent asks before holding (options never hold), and only `confirm` makes a booking |
| No real-time tracking | Superseded by the decision to simulate GPS on three lanes (phase 3) |

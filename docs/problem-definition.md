# Problem Definition Sheet

Deliverable 1 of the SetuHaul brief, with the [data audit](data-audit.md). It separates what the data proves (facts),
what the brief tells us (estimates), what we chose to believe in order to build (assumptions), and what nobody knows
yet (unknowns). Every snapshot figure below comes from a query on the supplied SQLite file, which is never modified.
Every data issue links to the query that proves it.

## 1. The problem

> How might SetuHaul give drivers a conversational way to report delays, ask questions and consider revised
> appointments, while many simultaneous requests meet limited warehouse capacity without infeasible or conflicting
> promises?

It has two layers, and the answer must cover both:

- **The driver.** A late driver needs a fast, honest answer: is my slot still good, and if not, what is? Today that
  answer needs a person.
- **The many drivers underneath.** A wave of delays (rain, a road closure, a loading backlog) sends 20 to 35 drivers
  to chat in half an hour. They all compete for a handful of replacement slots on 32 docks. Two conversations can read
  the same free slot, and only one promise can be true.

**Problem statement in the team's form.** On the snapshot morning at the Jaipur DC:
- 13 of 20 shipments ran later than planned, by 40 minutes on average and 90 at most;
- 8 were more than 30 minutes late, so their slots needed re-planning;
- at 09:55 three trucks were waiting in the yard, for 30, 50 and 95 minutes, each for a different reason: an early
  arrival, a late arrival and a broken dock.

Unplanned waiting and late slot decisions are the cost the service must cut. The snapshot is one curated morning,
though. Converting it into "X shipments a day, Y minutes each" at SetuHaul's scale needs the measurements in section
6.

## 2. Who has the problem, and what each needs

| Party | What it knows | What it needs during an exception | How this design serves it |
| --- | --- | --- | --- |
| Driver | Where the truck is, the breakdown, the traffic | Clear choices and a confirmation, without being asked what the system already knows | Chat agent: verifies the trip by phone, asks only for missing facts, offers only real slots, holds one, confirms |
| Dispatcher or carrier | Vehicle and driver availability | The revised plan and the escalation status | The change feed and the case reference the driver receives |
| SetuHaul operations (5 coordinators per shift) | Plan, customer commitments, open exceptions | One consistent view across many requests, and only the cases that need a person | Driver cases screen: approvals, escalations and open exceptions, each with a complete record |
| Warehouse planner | Docks, labour, local rules | Feasible arrivals without overbooking | Solver plus one feasibility rule shared by the solver, the API and an independent validator |
| Consignee or customer | Stock dependency | A reliable revised arrival | Priority weights (CRITICAL 27, HIGH 9) and coordinator approval for HIGH and CRITICAL changes |

## 3. Facts (proved on the snapshot)

| # | Fact | Proof |
| --- | --- | --- |
| F1 | 2 facilities, 9 docks, 21 shipments, 106 slots, as of 4 August 2026 (IST) | Row counts in the [database guide](../data/setuhaul/setuhaul_database_guide.md), re-checked by the seed loader |
| F2 | Jaipur has 6 docks: 4 standard (20 t or 25 t), 1 reefer (D5, 22 t), 1 heavy (D6, 35 t) | `docks` |
| F3 | Reefer and heavy loads have one dock each, so one dock event can leave them nowhere to go: D5 is in maintenance 18:00 to 22:00 and SHP1015 (reefer) has no slot | `dock_status_events` DEVT002; `SeededCasesIT.reeferDockUnavailableSHP1015HasNoFeasibleSlotAndGoesToACoordinator` |
| F4 | 13 of 20 Jaipur shipments have a later ETA than planned, 40 min on average, 90 at most; 8 are more than 30 min late | `shipments.latest_eta_ts` vs `original_eta_ts` |
| F5 | Every one of the 12 ETA updates comes from the driver; none from the warehouse, an override or GPS | Audit [A14](data-audit.md#a14) |
| F6 | 3 trucks wait in the yard at 09:55 (30, 50, 95 min); the 2 that docked waited 7 and 20 min | `facility_checkins` |
| F7 | 10 driver exceptions on 8 shipments: 4 still need information, 1 escalated, 1 a duplicate | `driver_exceptions` |
| F8 | The chat channel repeats messages: THR001's text arrives again as THR009 | Audit [A12](data-audit.md#a12) |
| F9 | 4 drivers have two open trips; one of the second trips is at another facility | Audit [A11](data-audit.md#a11) |
| F10 | A warehouse notice failed (OM004, the reefer exception) and another is still queued | Audit [A13](data-audit.md#a13) |
| F11 | The database already stops two active bookings on one slot, and two current bookings for one truck (partial unique indexes). It does not stop overlapping unloads on one dock, a booking on a broken dock, or a weight breach | Audit [A1](data-audit.md#a1), [A3](data-audit.md#a3), [A4](data-audit.md#a4) |

## 4. Estimates (the brief's operating profile: design for these, test on the snapshot)

| Measure | Figure | What it means for the design |
| --- | --- | --- |
| Loads coordinated a day | 280 to 360 | |
| Facilities, dock doors | 6, 32 | The scale test plans 240 shipments on 32 docks in 6 facilities: slowest facility 3.6 s of a 60 s limit ([results](results/phase6/scale.md)) |
| Receiving appointments a day | 190 to 240 | |
| Driver exception messages | 15 to 25 a normal day; 20 to 35 in 30 minutes in a spike | The agent answers in milliseconds per turn. Contention, not volume, is the risk, so the race harness fires bursts of 6 drivers on one slot |
| Coordinators per shift | 5 | Every case that reaches a person must be worth their time and complete |
| Live GPS feeds | 0 | ETAs come from the plan or the driver. The demo adds simulated GPS on three lanes; without it, the latest report stands |

## 5. Assumptions (chosen in order to build; each is configuration, so it can change)

| # | Assumption | Why | Where it lives |
| --- | --- | --- | --- |
| A-1 | One 30-minute gate buffer for every facility; +15 min when an ETA is LOW confidence | The brief's gate-entry time; LOW ETAs were wrong before | `gate_buffer_min`, `low_confidence_margin_min` |
| A-2 | A driver's hold lasts 15 minutes; a HIGH or CRITICAL change is reserved for 60 minutes while a coordinator decides | Long enough to answer from a stopped truck, short enough not to starve others | `hold_expiry_min`, `approval_reserve_min` |
| A-3 | Priority weights LOW 1, NORMAL 3, HIGH 9, CRITICAL 27 on waiting minutes | Each level outweighs the one below by more than any realistic wait gap | `priority_weight.*` |
| A-4 | Confirmed bookings stay put unless they no longer work, except that a CRITICAL truck may bump one LOW or NORMAL booking more than 2 hours out | Drivers plan around a promise | `pin_feasible_confirmed`, `bump_*` |
| A-5 | Re-plan when the effective ETA moves more than 30 minutes, on check-in, on a new request, and every 15 minutes | The brief's 30-minute rule; small GPS jitter must not move trucks | Solver trigger settings |
| A-6 | A driver may change a slot by chat twice; after that a coordinator plans it | Stops a driver from churning the plan | `max_chat_reschedules` |
| A-7 | 45 km/h lane speed for simulated GPS ETAs | A fixed, explainable speed; the trucks move at 35 to 76 km/h, so GPS ETAs can be an hour off | `route_lanes.avg_speed_kmph` |
| A-8 | The snapshot's "now" is 09:55 IST on 4 August 2026 | It is the latest consistent moment in the file; audit [A8](data-audit.md#a8) shows the file mixes times | `app_clock` |

## 6. Unknowns (what the snapshot cannot tell us, and how we would find out)

| Unknown | Why it matters | How to measure it in production (the tables already record it) |
| --- | --- | --- |
| How many driver delay or breakdown requests arrive per day, and how they bunch | Sizes the coordinator load and the spike | `driver_exceptions` per day, by hour |
| Average driver waiting time per day | The cost the service must cut (detention) | `facility_checkins`: gate-in to dock-in, per truck per day |
| Coordinator man-hours a day spent on driver queries | The saving the client will ask about | `coordinator_cases`: created to resolved, per case |
| Average reschedules or slot changes per truck | Churn hurts trust and labour planning | `appointments.replaced_appointment_id` chains per shipment (none in the snapshot) |
| How accurate driver and GPS ETAs are | Decides how much to trust each source | Effective ETA vs gate-in time, by source and confidence |
| Unloading labour and equipment limits per shift | A free dock without a crew is not capacity | Not in the data; ask the warehouse planner |
| Status history | The snapshot stores only current statuses (audit [A8](data-audit.md#a8)) | An events table, or Postgres history on the status columns |
| Whether drivers adopt chat, and in which language | Adoption, not technology, usually decides the result | Share of exceptions reported by chat vs phone; Hinglish share in messages |
| Detention and penalty rules per customer | Money decisions belong to a person | Contract data, out of scope for the agent |

## 7. The data audit, in one table

The full report, with every query and its rows, is [data-audit.md](data-audit.md). It is generated by
`python -m setuhaul_jobs.audit` from the queries in [db/audit/](../db/audit/), and a test fails if the report and the
queries drift apart.

| # | Issue | Decision it drove |
| --- | --- | --- |
| A1 | **Top issue:** D3 broke at 09:15, yet its 09:00 slot is still OPEN with SHP1005 confirmed on it | Any slot overlapping a dock event is unavailable. A booking on it is re-planned, never trusted |
| A2 | A capacity reduction on D2 removed no slots | CAPACITY_REDUCTION counts as a dock event too |
| A3 | Two bookings exceed their dock's weight limit | Flagged to a coordinator, never moved automatically |
| A4 | Four unloads run longer than their 60-minute slot | A truck occupies the dock for the longer of its unload and its slot, in every check |
| A5 | No open slot starts after the last allowed start (compliant) | Kept as a check: RULE005, no new start after 21:00 |
| A6 | Slot coverage differs from opening hours | Plan on slots, not on hours |
| A7 | The brief's "pending on a blocked slot" rule matches nothing | A pending booking is a driver's hold for 15 minutes |
| A8 | The snapshot mixes times of day | One shared clock (`setuhaul_now()`), and loading "as of" a time |
| A9 | Two ETAs are LOW confidence | +15 minutes before the truck counts as ready |
| A10 | Cancelled loads still carry appointment history | History stays; only current bookings are planned |
| A11 | Four drivers have two open trips | The agent asks which trip; it never guesses |
| A12 | The chat channel repeats messages | A retried message is handled once; a second report becomes DUPLICATE |
| A13 | A warehouse notice failed, another is queued | Hand-offs live on the Driver cases screen, not in email |
| A14 | Every ETA comes from the driver | A driver's ETA is evidence with a confidence; an override or GPS outranks it |

## 8. What success looks like (the brief's measures, and where they are measured)

| Measure | Target for the pilot | Evidence |
| --- | --- | --- |
| Conflicting or duplicate allocations | 0, always | [Integrity proof](integrity-proof.md) and [scenario run](scenario-run/README.md#evals) invariants |
| Options later found infeasible | 0 confirmed. A slot that goes between showing and booking is refused at the re-check, with a reason | Scenario run measures |
| No-slot escalations handled correctly | Every one with a complete hand-off record | Eval E6 |
| Priority-policy violations | 0 inversions in the plan, 0 HIGH or CRITICAL changes without a coordinator | Plan KPI, eval E5 |
| Driver clarification turns | Asked only for missing facts: about 1 question per resolved case | Scenario run measures |
| Time from first message to usable outcome | A few driver turns, under a second of system time | Scenario run measures |
| Share resolved without manual takeover | High for plain delays. Safety, money, disputes and no-slot cases go to a person by design | Scenario run measures |
| Average driver waiting after rescheduling, slot utilisation | Lower waiting than first come first served at equal utilisation | [Baseline comparison](results/phase6/baseline.md) |

## 9. Scope

- **In scope:** the driver conversation, slot holds and bookings, re-planning one facility at a time, coordinator
  approvals and escalations, and the integrity proof.
- **Out of scope (the brief's boundaries):** network-wide transport optimisation, carrier selection and freight
  rates, autonomous driver-safety decisions, customs and hazardous-material workflows, and commercial penalty
  approval. The agent hands money, safety and disputed data to a person
  ([decision register](decision-register.md#12-which-decisions-need-human-approval-or-takeover)).

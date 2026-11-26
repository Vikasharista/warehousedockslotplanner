# Client readout: driver delays and dock slots

**For:** the head of operations and the warehouse planner, Jaipur DC. One page: policy, limits, risks.

**What it does.** A late driver messages SetuHaul on chat. The service finds the trip from the phone number and asks
only what it cannot look up: how late, and the latest time they can finish. It then either confirms the slot still
works, or offers real free slots, holds one for 15 minutes and books it when the driver says yes. When no safe answer
exists, a coordinator gets the case with everything needed to act. The dock plan re-plans itself whenever an ETA moves
by more than 30 minutes, a truck checks in, a new request arrives, or every 15 minutes.

## Policy: what you are agreeing to

1. **No promise without a free slot.** Every option has passed the planner's rules: dock type, weight, arrival plus
   the 30-minute gate buffer, hours, breakdowns and maintenance. If two drivers want one slot, the first to hold it
   gets it, and the other is told why.
2. **Short capacity goes by priority.** Waiting counts CRITICAL ×27, HIGH ×9, NORMAL ×3, LOW ×1. Promised bookings
   stay put unless they no longer work. One exception: a CRITICAL load may take a LOW or NORMAL slot booked more than
   2 hours ahead, and that truck is re-planned.
3. **A coordinator approves HIGH and CRITICAL changes.** The old slot stands, and the new one is reserved for 60
   minutes.
4. **People own the risky cases.** The chat never decides these:

| Case | Owner (to name before go-live) | Within |
| --- | --- | --- |
| Accident, injury, leaking or hazardous load (the driver is told to call 112) | Shift supervisor | At once |
| No feasible slot for a HIGH or CRITICAL load | Duty coordinator | 15 min |
| Penalties, detention charges, payments, claims | Commercial desk | Same shift |
| A driver's account against the GPS | Duty coordinator | Before the slot |
| HIGH or CRITICAL change, a third reschedule, anything unclear | Duty coordinator | 60 min |

## Evidence

- **0 double bookings.** Ten drivers asked for the same slot at the same instant, 20 times; each time exactly one got
  it. A 60-second stress test of 40,000+ requests, with re-planning and slot blocking throughout, broke nothing.
- **The scripted day:** 14 of 14 conversations reached an acceptable result. No invented slots, no booking without a
  yes, no HIGH load moved without a coordinator.
- **Better than first come, first served.** On a busy 70-truck day at noon, the planner places 47 of 66 trucks with
  108 minutes' average wait and no priority inversions. First come, first served places 43, with 213 minutes and 146
  inversions. 240 shipments across 6 sites plan in under 4 seconds per site.

## Limits

- **It does not know crews or equipment.** A free dock without a crew still looks free.
- **GPS is simulated on three lanes**, and its ETAs can be an hour off.
- **WhatsApp needs a public address and Meta approval**; Telegram works now.
- **Waiting figures are planned, not measured**, because there are no real dock-in times yet.
- **Not built:** a rate limit per phone, and a retention period for stored messages.

## Risks

| Risk | Mitigation |
| --- | --- |
| Drivers keep phoning | Measure the chat share weekly; keep the phone line; Hinglish is understood |
| Bad data, bad promises (the snapshot had a broken dock with a booking still on it) | Dock events block their slots; 14 data checks run by query |
| A delay wave swamps 5 coordinators | Only safety, money, disputes and no-slot cases reach a person, each as one complete record |
| Personal data (DPDP Act) | Phone and vehicle numbers are masked before any AI model sees a message |
| In chat, a LOW truck can beat a later HIGH one | Your call: keep first come, first served, or queue HIGH requests for a coordinator |

## What we need from you

1. **Confirm the chat policy for competing drivers.**
2. **Name an owner and a phone number** for each escalation row.
3. **Approve a 4-week Jaipur pilot**, measured by:
   - double bookings (target 0);
   - the share of cases resolved without a person;
   - gate-in to dock-in waiting;
   - coordinator hours spent on driver calls.

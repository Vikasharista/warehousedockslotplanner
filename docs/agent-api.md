# Driver agent API

The routes a driver-facing agent (for example a WhatsApp bot) calls to answer a driver: find their trip, tell them
their slot, take a delay report, offer and hold another slot, and hear about plan changes. They live in
[dock-api](../services/dock-api/) under `/api/agent/`.

The plan view's **Driver chat** screen is a test console for these routes: it plays the driver's side and shows the
call behind every reply.

## Access

An agent sends its key in the `X-Agent-Key` header. Keys are configured on dock-api as `APP_AGENT_KEYS`:
`name:key` entries separated by commas, each key at least 32 characters (`openssl rand -hex 24`).

| Where | Key |
| --- | --- |
| `docker compose up` (local) | `local-agent-key-for-development-only` (agent `local-agent`); override with `SETUHAUL_AGENT_KEYS` |
| Deployment | `SETUHAUL_AGENT_KEYS` in `.env`; unset means no agent keys |

- A key opens `/api/agent/*` only. It never opens the plan view's other routes (they answer 401).
- A wrong key answers 401, even if a session cookie is also present.
- Signed-in users (the Driver chat console) can call the same routes with their session instead of a key.

```bash
curl -H 'X-Agent-Key: local-agent-key-for-development-only' \
  http://localhost:8082/api/agent/drivers/+91-9000010007/shipments
```

## Routes

| Route | Result |
| --- | --- |
| `GET /api/agent/drivers/{phone}/shipments` | The driver's open trips (matched on the phone's last 10 digits), each as a shipment status below; 404 if none |
| `GET /api/agent/shipments/{id}` | One shipment status |
| `GET /api/agent/shipments/{id}/options?limit=` | Free slots the truck could hold now (same rules as the solver) |
| `POST /api/agent/shipments/{id}/eta` `{"eta", "reason"?, "confidence"?, "note"?}` | Records a driver-declared ETA and returns the updated status |
| `POST /api/agent/holds` `{"shipmentId", "slotId"}` | Holds a slot for 15 minutes; 201 with the hold, 409 `SLOT_UNAVAILABLE` or `HOLD_CONFLICT` |
| `POST /api/agent/holds/{id}/confirm` | The hold becomes the truck's booking; 409 `HOLD_EXPIRED` after 15 minutes |
| `POST /api/agent/holds/{id}/release` | Lets the hold go |
| `GET /api/agent/changes?after={cursor}&limit=` | Booking changes after the cursor, oldest first: `{"changes": [...], "next": <cursor>, "last": <newest id>}` |

Times are RFC 3339. Errors are `{"code", "error"}`: 400 bad input, 401 no or wrong key, 404 unknown, 409 conflict.

### Shipment status

```json
{
  "shipmentId": "SHP-G0012", "driverName": "Manoj Khan", "driverPhone": "+91-6300001212",
  "priorityCode": "NORMAL", "originCity": "Delhi", "status": "IN_TRANSIT",
  "eta": "2026-08-04T05:57:00Z", "etaSource": "GPS", "etaConfidence": "HIGH",
  "booking": {"appointmentId": "APT-…", "slotId": "SLOT-JAI-045", "dockCode": "D3",
              "start": "2026-08-04T08:30:00Z", "end": "2026-08-04T09:30:00Z", "status": "CONFIRMED", "source": "SCHEDULING_TOOL"},
  "hold": null,
  "planStatus": "ALLOCATED", "predictedWaitMin": 0,
  "message": "Your slot is dock D3 at 14:00. We expect you at 11:27."
}
```

- `message` is a ready-to-send sentence in plain words (times in IST). It covers: at the gate, holding a slot,
  booked, with a coordinator (`FLAGGED`, e.g. a weight breach), no safe slot yet (`UNASSIGNED`) and not planned yet.
- `booking` is the current booking; `hold` is a pending hold beside it, with `expiresAt`.
- `eta` is the **effective** ETA the planner uses, and `etaSource` says where it came from.

### Delay reports and GPS

A driver's report is stored (`eta_updates`, source `DRIVER_DECLARED`), but the planner's effective ETA prefers a live
GPS estimate: an operations override first, then GPS (the later of GPS and the driver's ETA while the driver has an
open exception), then the latest report. So after `POST …/eta`, compare the returned `eta` with what the driver said;
if `etaSource` is `GPS` and differs, tell the driver the GPS time is what the plan uses. A report re-plans only when
the effective ETA moves more than 30 minutes from the ETA the slot was planned on (the ETA trigger).

Reasons: `TRAFFIC`, `BREAKDOWN`, `WEATHER`, `LATE_DEPARTURE`, `LOADING_DELAY`, `ROUTE_ISSUE`, `OTHER`.
Confidence: `LOW`, `MEDIUM` (default), `HIGH`. The ETA must lie between 1 hour ago and 48 hours ahead on the shared
clock.

### Offering another slot

1. `GET …/options` lists free slots that fit the truck (dock type, weight, not before it can be ready, not after the
   last start time, inside 12 hours).
2. `POST /api/agent/holds` holds one. A truck with a booking keeps it until the driver confirms; the hold sits beside it.
3. `confirm` swaps the booking for the hold; `release` (or 15 minutes passing) drops the hold and keeps the old booking.

### Change feed

Every committed plan writes one row per booking it creates, moves or releases (`booking_changes`). Poll the feed with
the `next` cursor from the previous answer, starting from `after=0`; a page holds up to `limit` (default 100, at most
500) changes. Each change has `shipmentId`, `driverPhone`, `action` (`NEW`, `MOVED`, `RELEASED`), the old and new dock
and time, the reason code and a `message` for the driver, for example:

> SHP1003 moved from dock D1 at 09:00 to dock D1 at 10:00 because your slot started before you could reach it.

`last` is the newest change id. Keep reading while `next` is below `last`. The demo's nightly reset (03:00 IST)
starts the ids again from 1, so a saved cursor above `last` means the feed was reset: start again from `after=0`.
To start at the current end (ignore history), read `last` from any answer and use it as the cursor.

## What the agent does not do

- It cannot change a booking directly; it can only hold and confirm free slots, like a driver.
- A weight breach or a start after the last allowed start time goes to a coordinator, never to the driver as a new slot.
- Operations overrides, check-ins and capacity changes are for coordinators (the Shipments and Capacity screens).

# Telegram driver bot: planner integration guide

Last updated 23 Nov 2026. The shared, commentable copy of this guide is the
[Claude Doc](https://claude.ai/artifact/3A4TEtx6ZUU6RJweCNSH3y); the route reference is [agent-api.md](agent-api.md).

> **Phase 7.** The full driver agent now lives in this repository: [agent/](../agent/README.md) (Telegram, WhatsApp
> optional, approvals, coordinator cases, plan-change notices). This guide stays as the minimal bot on the same routes;
> it handles the two phase 7 changes below (202 approvals, feed rows of kind APPROVAL and REPLY).

The Telegram bot lets a truck driver ask for their dock slot, report a delay, swap to another free slot and hear when
the plan moves them, all in a chat. It does this through eight HTTP routes on the planner's API (dock-api, under
`/api/agent/`), authenticated with one agent key. The bot never books a slot by itself: it can only hold a free slot
for 15 minutes and confirm it when the driver says yes, and the planner re-plans everything else.

## How it fits

```text
Driver (Telegram app) <--messages--> Telegram Bot API <--long polling--> Your bot (chat to phone, feed cursor)
                                                                              |
                                              calls /api/agent/* with X-Agent-Key
                                              polls the change feed every 30 s
                                                                              |
Solver (re-plans, commits) <--writes plans--> Postgres <--reads, holds--> dock-api (/api/agent/* routes)
```

The bot never touches the database or the solver. It reads trips, holds slots and polls the change feed through
dock-api, and the solver's re-plans reach drivers only through that feed.

## Set up

Run the planner on your own machine and point the bot at `http://localhost:8082`; no hosting is needed while building.

1. Install Docker Desktop (or Docker Engine with Compose v2), then:

   ```bash
   git clone https://github.com/Vikasharista/warehousedockslotplanner.git
   cd warehousedockslotplanner
   docker compose up --build
   ```

2. Wait until the plan view at `http://localhost:5173` shows bookings (sign in as `demo@setuhaul.local` /
   `setuhaul-demo-2026`). The first build takes several minutes.
3. Configure the bot:

   | Setting | Local value |
   | --- | --- |
   | Base URL | `http://localhost:8082` |
   | Auth header | `X-Agent-Key: local-agent-key-for-development-only` |
   | Content type for POST | `application/json` |
   | Times | RFC 3339 with offset, e.g. `2026-08-04T12:30:00+05:30`; responses are UTC (`Z`) |

4. Run the bot with Telegram long polling (`getUpdates`), not a webhook: the bot then needs no public URL and can call
   `localhost`.

The planner runs on a simulated clock that starts at 4 Aug 2026, 09:55 IST and moves at real speed. Always show times
from the API, never the phone's clock. `docker compose down -v` starts the day over.

The local key works only on your machine. When the demo is deployed, it gets its own key (`SETUHAUL_AGENT_KEYS`),
shared with you privately, never in a chat or the repository.

## Who is chatting

The planner knows drivers by phone number, so the bot's first job is a verified number for each Telegram chat.

1. On `/start`, send a reply keyboard with one button, `KeyboardButton("Share my phone number", request_contact=True)`.
2. When the contact arrives, accept it only if `message.contact.user_id == message.from_user.id`. Otherwise a driver
   could forward someone else's contact card and read their bookings.
3. Call `GET /api/agent/drivers/{phone}/shipments` with the number as Telegram sends it (often `919000010007`, no
   `+`). The planner matches the last 10 digits, so any format works.
4. Store `chat_id → phone` and `phone → chat_id` in the bot's own storage (SQLite or Redis). The planner keeps no
   Telegram data; the second map is what lets you push plan changes.

| Answer | Meaning | Bot reply |
| --- | --- | --- |
| 200, one trip | The usual case | Greet by `driverName` and send that trip's `message` |
| 200, two or more trips | The driver has several open trips (in the demo, `+91-9000010004` has SHP1004 and SHP1020) | Inline buttons, one per `shipmentId`, and remember the choice for the chat |
| 404 `RECORD_NOT_FOUND` | No open trip for this number (finished, cancelled, or not a driver) | "I can't find an open trip for this number. Please call the dock office." |
| 400 | Fewer than 10 digits | Ask them to share the contact again |

Check trips again on each new conversation, not once per chat: trips finish, and new ones appear during the day.

## API calls

Eight routes cover everything the bot does; every one needs the `X-Agent-Key` header.

| Route | Body | Success | Use it to |
| --- | --- | --- | --- |
| `GET /api/agent/drivers/{phone}/shipments` | none | 200, list of trip statuses | Find the driver's open trips after they share their contact |
| `GET /api/agent/shipments/{id}` | none | 200, trip status | Answer "where is my slot?" |
| `GET /api/agent/shipments/{id}/options?limit=4` | none | 200, list of free slots (default 8, at most 50) | Offer other slots |
| `POST /api/agent/shipments/{id}/eta` | `{"eta", "reason"?, "confidence"?, "note"?}` | 200, updated trip status | Record a delay the driver reports |
| `POST /api/agent/holds` | `{"shipmentId", "slotId"}` | 201, the hold | Hold a chosen slot for 15 minutes |
| `POST /api/agent/holds/{holdId}/confirm` | none | 200, the confirmed booking | The driver says yes |
| `POST /api/agent/holds/{holdId}/release` | none | 200, the released hold | The driver says no |
| `GET /api/agent/changes?after={cursor}&limit=100` | none | 200, `{changes, next, last}` | Poll for plan changes to push (up to 500 a page) |

**Trip status** (from the first four routes). Send `message` as the reply; the other fields are for buttons and logic.

```json
{
  "shipmentId": "SHP-G0012", "driverName": "Manoj Khan", "driverPhone": "+91-6300001212",
  "priorityCode": "NORMAL", "originCity": "Delhi", "status": "IN_TRANSIT",
  "eta": "2026-08-04T05:57:00Z", "etaSource": "GPS", "etaConfidence": "HIGH",
  "booking": {"appointmentId": "APT-...", "slotId": "SLOT-JAI-045", "dockCode": "D3",
              "start": "2026-08-04T08:30:00Z", "end": "2026-08-04T09:30:00Z", "status": "CONFIRMED"},
  "hold": null,
  "planStatus": "ALLOCATED",
  "message": "Your slot is dock D3 at 14:00. We expect you at 11:27."
}
```

- `booking` is the current booking; `hold` is a pending hold beside it, with `expiresAt`.
- `eta` is the ETA the planner uses, and `etaSource` says where it came from.
- `message` covers every state: at the gate, holding a slot, booked, with a coordinator, no slot yet, not planned yet.
  Times in it are IST.

**Slot option**: `{"slotId", "dockId", "dockCode", "dockType", "slotStart", "slotEnd"}`. Show `dockCode` and
`slotStart` in IST on a button, and keep `slotId` in the button's callback data.

**Hold**: `{"appointmentId", "shipmentId", "slotId", "status", "expiresAt", ...}`. `appointmentId` is the `{holdId}`
for confirm and release.

**Errors**: `{"code", "error"}` with 400 bad input, 401 missing or wrong key, 404 unknown trip or hold, 409 conflict
(codes in Rules and edge cases), 500 server fault.

```bash
curl -s -H 'X-Agent-Key: local-agent-key-for-development-only' \
  http://localhost:8082/api/agent/drivers/+91-9000010007/shipments
```

## Conversation flows

Every reply can be built from the API's own `message`, plus a few words of the bot's; keep menus to buttons, since
drivers type little on the road.

| Driver | Bot calls | Bot replies |
| --- | --- | --- |
| `/start`, shares contact | `GET /drivers/{phone}/shipments` | "Namaste {driverName}." + `message`, then the menu: Where is my slot? · Running late · Another slot |
| Where is my slot? | `GET /shipments/{id}` | `message` |
| Running late → picks 30 / 60 / 90 / 120 min → picks a reason | `POST /shipments/{id}/eta` with `eta` = the status's `eta` + the delay, and `reason` | "Noted, {new time}." Then, if the returned `etaSource` is `GPS` and its `eta` differs: "Your GPS position puts you here at {eta}, so the plan uses that." Then `message` |
| Another slot | `GET /shipments/{id}/options?limit=4` | One button per option, "D4 13:00"; none: "No other free slot fits your truck in the next 12 hours; your slot stays." |
| Taps a slot | `POST /holds` | "Held until {expiresAt}. Keep this slot?" with Yes / No buttons |
| Yes | `POST /holds/{holdId}/confirm` | "Confirmed." + `message` from a fresh `GET /shipments/{id}` |
| No | `POST /holds/{holdId}/release` | "Released; your old slot stays." + `message` |

Working out the new ETA:

- Use the planner's `eta` from the latest trip status, not the phone's clock: the demo runs on a simulated day
  (4 Aug 2026).
- The planner accepts an ETA from 1 hour before to 48 hours after its own clock, so a 30–120 minute delay on its `eta`
  always fits.
- Reasons: `TRAFFIC`, `BREAKDOWN`, `WEATHER`, `LATE_DEPARTURE`, `LOADING_DELAY`, `ROUTE_ISSUE`, `OTHER`.
- A truck already at the gate (`gateIn` set, no `eta`) has nothing to report: hide Running late.

While a hold is open (`hold` in the status), hide Another slot and show only Yes / No. The planner refuses a second
hold anyway (409 `HOLD_CONFLICT`).

The plan view's Driver chat screen runs exactly these flows against the same routes. Use it to see the expected
replies before writing the bot.

## The bot's code: which planner call to make

The bot needs no language model: a small router turns each button or typed message into one Python function, and each
function makes one planner API call. This code was run against the local planner: 8 tests passed (finding drivers,
delays from buttons and typed text, hold then release, hold then confirm, two drivers racing for one slot, the change
feed, a wrong key), and the Telegram app builds with its 5 handlers and the 30-second poller.

| Driver input | Router decides | Planner calls |
| --- | --- | --- |
| Shares their own contact | `on_contact` | `trips_for_phone` |
| Taps a trip (two-trip drivers) | `trip:<id>` | `trip` |
| Where is my slot? button, or any text the router does not recognise | `where` | `trip` |
| Running late button | `late` → minutes buttons → reason buttons | `trip`, then `report_delay` |
| Typed "stuck in traffic, 45 min late" | `late` with minutes and reason read from the words | `report_delay` straight away |
| Another slot button, or "can I get another slot?" | `another` (skipped when flagged, unassigned or already holding) | `trip`, `options` |
| Taps a slot | `hold:<slotId>` | `hold` |
| Yes / No | `yes:<holdId>` / `no:<holdId>` | `confirm` or `release`, then `trip` |
| Every 30 seconds | `push_changes` | `changes` |

Refusals become driver words in one place (`_explain`): `HOLD_EXPIRED`, `SLOT_UNAVAILABLE`, `HOLD_CONFLICT`, and 404
for a closed trip. Anything else goes to the error handler, which tells the driver to retry in a minute and logs a 401
as a key problem.

Requirements: Python 3.10+, `httpx==0.28.1`, `python-telegram-bot[job-queue]==21.10`.

**`planner_tools.py`**: the planner calls (tools) and the router. No Telegram code, so it can be tested on its own.

```python
"""Planner tools for the Telegram driver bot: one Python function per planner action, and a router that decides
which one to call from the button a driver taps or the words they type. Talks to the planner only through its
agent API (never the database), so every booking rule, lock and re-plan stays in the planner."""
from __future__ import annotations

import datetime as dt
import re
from dataclasses import dataclass, field

import httpx

IST = dt.timezone(dt.timedelta(hours=5, minutes=30))
REASONS = {  # words a driver might type -> the planner's delay reason
    "TRAFFIC": ("traffic", "jam", "congestion"),
    "BREAKDOWN": ("breakdown", "broke", "puncture", "tyre", "tire", "engine"),
    "WEATHER": ("rain", "weather", "fog", "storm", "flood"),
    "LATE_DEPARTURE": ("left late", "late start", "departed late"),
    "LOADING_DELAY": ("loading",),
    "ROUTE_ISSUE": ("road", "route", "diversion", "detour", "closed"),
}


class PlannerError(Exception):
    """An error answer from the planner: HTTP status, the planner's code (e.g. HOLD_EXPIRED) and its text."""

    def __init__(self, status: int, code: str, message: str):
        super().__init__(f"{status} {code}: {message}")
        self.status, self.code, self.message = status, code, message


# ---------------------------------------------------------------- the tools: one function per planner action

class Planner:
    def __init__(self, base_url: str, agent_key: str, timeout: float = 10.0):
        self._http = httpx.AsyncClient(base_url=base_url.rstrip("/"), timeout=timeout,
                                       headers={"X-Agent-Key": agent_key})

    async def _call(self, method: str, path: str, body: dict | None = None):
        try:
            r = await self._http.request(method, "/api/agent" + path, json=body)
        except httpx.TransportError:  # planner down or slow: one retry, then give up
            r = await self._http.request(method, "/api/agent" + path, json=body)
        if r.status_code >= 400:
            try:
                err = r.json()
            except ValueError:
                err = {}
            raise PlannerError(r.status_code, err.get("code", ""), err.get("error", r.text[:200]))
        return r.json()

    async def trips_for_phone(self, phone: str) -> list[dict]:
        """The driver's open trips (matched on the last 10 digits of the phone)."""
        return await self._call("GET", f"/drivers/{phone}/shipments")

    async def trip(self, shipment_id: str) -> dict:
        """One trip's status; `message` is a ready-to-send answer."""
        return await self._call("GET", f"/shipments/{shipment_id}")

    async def options(self, shipment_id: str, limit: int = 4) -> list[dict]:
        """Free slots this truck could hold now."""
        return await self._call("GET", f"/shipments/{shipment_id}/options?limit={limit}")

    async def report_delay(self, shipment_id: str, minutes: int, reason: str = "OTHER") -> dict:
        """Record a driver-declared delay on top of the ETA the planner uses now; returns the updated trip."""
        status = await self.trip(shipment_id)
        base = dt.datetime.fromisoformat(status["eta"].replace("Z", "+00:00"))
        eta = (base + dt.timedelta(minutes=minutes)).isoformat()
        return await self._call("POST", f"/shipments/{shipment_id}/eta", {"eta": eta, "reason": reason})

    async def hold(self, shipment_id: str, slot_id: str) -> dict:
        """Hold a free slot for 15 minutes."""
        return await self._call("POST", "/holds", {"shipmentId": shipment_id, "slotId": slot_id})

    async def confirm(self, hold_id: str) -> dict:
        return await self._call("POST", f"/holds/{hold_id}/confirm")

    async def release(self, hold_id: str) -> dict:
        return await self._call("POST", f"/holds/{hold_id}/release")

    async def changes(self, after: int, limit: int = 100) -> dict:
        """Plan changes after a cursor: {"changes": [...], "next": int, "last": int}."""
        return await self._call("GET", f"/changes?after={after}&limit={limit}")

    async def close(self) -> None:
        await self._http.aclose()


# ---------------------------------------------------------------- the router: which tool to call

@dataclass
class Reply:
    text: str
    buttons: list[list[tuple[str, str]]] = field(default_factory=list)  # rows of (label, callback data)


@dataclass
class Chat:
    """What the bot remembers per Telegram chat (keep it in SQLite or Redis, not only in memory)."""
    phone: str | None = None
    shipment: str | None = None
    pending_delay: int | None = None  # minutes chosen, waiting for the reason


MENU = [[("Where is my slot?", "where")], [("Running late", "late"), ("Another slot", "another")]]


def ist(ts: str) -> str:
    return dt.datetime.fromisoformat(ts.replace("Z", "+00:00")).astimezone(IST).strftime("%H:%M")


def intent_of(text: str) -> str:
    """Free text to an intent: 'late', 'another', or 'where' (the safe default)."""
    t = text.lower()
    if re.search(r"\b(late|delay|delayed|stuck|traffic|breakdown|jam)\b", t):
        return "late"
    if re.search(r"\b(another|other|change|earlier|later|different)\b.*\b(slot|time|dock)\b|\breschedul", t):
        return "another"
    return "where"


def minutes_in(text: str) -> int | None:
    """'1 hour', '45 min', '1.5 hrs', '90' -> minutes."""
    m = re.search(r"(\d+(?:\.\d+)?)\s*(h|hr|hrs|hour|hours)\b", text.lower())
    if m:
        return round(float(m.group(1)) * 60)
    m = re.search(r"(\d+)\s*(m|min|mins|minute|minutes)?\b", text.lower())
    return int(m.group(1)) if m else None


def reason_in(text: str) -> str | None:
    t = text.lower()
    for code, words in REASONS.items():
        if any(w in t for w in words):
            return code
    return None


async def on_contact(p: Planner, chat: Chat, phone: str) -> Reply:
    """After the driver shares their own contact."""
    try:
        trips = await p.trips_for_phone(phone)
    except PlannerError as e:
        if e.status == 404:
            return Reply("I can't find an open trip for this number. Please call the dock office.")
        raise
    chat.phone = phone
    if len(trips) == 1:
        chat.shipment = trips[0]["shipmentId"]
        return Reply(f"Namaste {trips[0]['driverName']}. {trips[0]['message']}", MENU)
    return Reply("You have more than one trip. Which one is this about?",
                 [[(f"{t['shipmentId']} from {t['originCity']}", f"trip:{t['shipmentId']}")] for t in trips])


async def on_button(p: Planner, chat: Chat, data: str) -> Reply:
    """A tapped inline button; `data` is its callback data."""
    if chat.shipment is None and not data.startswith("trip:"):
        return Reply("Please share your phone number first with /start.")
    kind, _, arg = data.partition(":")
    try:
        if kind == "trip":
            chat.shipment = arg
            return Reply((await p.trip(arg))["message"], MENU)
        if kind == "where":
            return Reply((await p.trip(chat.shipment))["message"], MENU)
        if kind == "late":
            status = await p.trip(chat.shipment)
            if status.get("gateIn") or not status.get("eta"):
                return Reply("You are already at the gate; no delay to report. " + status["message"], MENU)
            return Reply("How late will you be?", [[(f"{m} min", f"delay:{m}") for m in (30, 60, 90, 120)]])
        if kind == "delay":
            chat.pending_delay = int(arg)
            return Reply("What is the reason?", [[("Traffic", "reason:TRAFFIC"), ("Breakdown", "reason:BREAKDOWN")],
                                                  [("Weather", "reason:WEATHER"), ("Other", "reason:OTHER")]])
        if kind == "reason":
            return await _delay(p, chat, chat.pending_delay or 60, arg)
        if kind == "another":
            status = await p.trip(chat.shipment)
            if status.get("planStatus") in ("FLAGGED", "UNASSIGNED"):
                return Reply(status["message"], MENU)  # a coordinator decides; no slot offers
            if status.get("hold"):
                h = status["hold"]
                return Reply(f"You already hold dock {h['dockCode']} at {ist(h['start'])}. Keep it?", _yes_no(h["appointmentId"]))
            options = await p.options(chat.shipment)
            if not options:
                return Reply("No other free slot fits your truck in the next 12 hours; your slot stays.", MENU)
            return Reply("These slots are free. Pick one and I'll hold it for 15 minutes:",
                         [[(f"{o['dockCode']} {ist(o['slotStart'])}", f"hold:{o['slotId']}")] for o in options])
        if kind == "hold":
            h = await p.hold(chat.shipment, arg)
            return Reply(f"Held until {ist(h['expiresAt'])}. Keep this slot?", _yes_no(h["appointmentId"]))
        if kind == "yes":
            h = await p.confirm(arg)
            if h.get("outcome") == "AWAITING_APPROVAL":  # HIGH and CRITICAL changes wait for a coordinator
                return Reply("Requested. A coordinator approves changes for priority loads; your current slot stays "
                             "until then and I'll tell you the answer. " + (await p.trip(chat.shipment))["message"], MENU)
            return Reply("Confirmed. " + (await p.trip(chat.shipment))["message"], MENU)
        if kind == "no":
            await p.release(arg)
            return Reply("Released; your old slot stays. " + (await p.trip(chat.shipment))["message"], MENU)
    except PlannerError as e:
        return await _explain(p, chat, e)
    return Reply("Sorry, I didn't get that.", MENU)


async def on_text(p: Planner, chat: Chat, text: str) -> Reply:
    """Typed words: decide the intent; a delay with minutes in it is reported straight away."""
    if chat.shipment is None:
        return Reply("Please share your phone number first with /start.")
    intent = intent_of(text)
    if intent == "late":
        minutes = minutes_in(text)
        if minutes:
            return await _delay(p, chat, minutes, reason_in(text) or "OTHER")
    return await on_button(p, chat, intent)


async def _delay(p: Planner, chat: Chat, minutes: int, reason: str) -> Reply:
    chat.pending_delay = None
    try:
        before = await p.trip(chat.shipment)
        said = ist((dt.datetime.fromisoformat(before["eta"].replace("Z", "+00:00"))
                    + dt.timedelta(minutes=minutes)).isoformat())
        after = await p.report_delay(chat.shipment, minutes, reason)
    except PlannerError as e:
        return await _explain(p, chat, e)
    if after.get("etaSource") == "GPS" and ist(after["eta"]) != said:
        return Reply(f"Noted, {said}. Your GPS position puts you here at {ist(after['eta'])}, so the plan uses that. "
                     + after["message"], MENU)
    return Reply(f"Noted, {said}. If that misses your slot, the planner re-plans within a minute and I'll tell you. "
                 + after["message"], MENU)


async def _explain(p: Planner, chat: Chat, e: PlannerError) -> Reply:
    """Planner refusals in driver words; anything else is re-raised for the bot's error handler."""
    if e.code == "HOLD_EXPIRED":
        return Reply("That hold ran out after 15 minutes. Tap Another slot to see what's free now.", MENU)
    if e.code == "SLOT_UNAVAILABLE":
        return Reply("That slot was just taken. Tap Another slot for the ones still free.", MENU)
    if e.code == "HOLD_CONFLICT":
        return Reply(e.message, MENU)
    if e.status == 404:
        chat.shipment = None
        return Reply("That trip is closed. Please call the dock office if that's wrong.")
    raise e


def _yes_no(hold_id: str) -> list[list[tuple[str, str]]]:
    return [[("Yes, keep it", f"yes:{hold_id}"), ("No", f"no:{hold_id}")]]


# ---------------------------------------------------------------- pushing plan changes

def last10(phone: str) -> str:
    return re.sub(r"\D", "", phone)[-10:]


async def poll_changes(p: Planner, cursor: int | None, chat_for_phone) -> tuple[int, list[tuple[int, str]]]:
    """One poll of the change feed. Returns the new cursor and (chat_id, text) pairs to send.
    `chat_for_phone(last10)` returns a chat id or None. Start with cursor=None: history is skipped."""
    page = await p.changes(cursor or 0, limit=1)
    if cursor is None or cursor > page["last"]:  # first start, or the feed was reset (nightly demo reset)
        return page["last"], []
    newest: dict[str, dict] = {}
    while True:
        page = await p.changes(cursor, limit=100)
        for c in page["changes"]:
            if c.get("kind", "BOOKING") == "BOOKING":
                newest[c["shipmentId"]] = c  # several moves of one trip in a page: only the newest is sent
            else:
                newest[f"{c['kind']}:{c['changeId']}"] = c  # approval outcomes and coordinator replies: every one
        cursor = page["next"]
        if cursor >= page["last"]:
            break
    out = []
    for c in newest.values():
        chat_id = chat_for_phone(last10(c.get("driverPhone") or ""))
        if chat_id is not None:
            out.append((chat_id, c["message"]))
    return cursor, out
```

**`bot.py`**: the Telegram wiring. Long polling, so no public URL is needed.

```python
"""Telegram wiring for the driver bot (python-telegram-bot 21). Run: python bot.py
Settings from the environment: TELEGRAM_TOKEN, PLANNER_URL (default http://localhost:8082),
PLANNER_AGENT_KEY (default: the local development key), ALLOW_PHONE_OVERRIDE (default false)."""
import logging
import os

from telegram import InlineKeyboardButton, InlineKeyboardMarkup, KeyboardButton, ReplyKeyboardMarkup, ReplyKeyboardRemove, Update
from telegram.ext import Application, CallbackQueryHandler, CommandHandler, ContextTypes, MessageHandler, filters

import planner_tools as pt

log = logging.getLogger("driver-bot")
planner = pt.Planner(os.environ.get("PLANNER_URL", "http://localhost:8082"),
                     os.environ.get("PLANNER_AGENT_KEY", "local-agent-key-for-development-only"))
ALLOW_OVERRIDE = os.environ.get("ALLOW_PHONE_OVERRIDE", "false").lower() == "true"
chats: dict[int, pt.Chat] = {}  # chat id -> state. Use SQLite or Redis in a real bot so a restart keeps it.
cursor: dict[str, int | None] = {"value": None}  # change-feed cursor: persist it too


def chat_of(update: Update) -> pt.Chat:
    return chats.setdefault(update.effective_chat.id, pt.Chat())


async def send(update: Update, reply: pt.Reply) -> None:
    markup = InlineKeyboardMarkup([[InlineKeyboardButton(label, callback_data=data) for label, data in row]
                                   for row in reply.buttons]) if reply.buttons else None
    await update.effective_chat.send_message(reply.text, reply_markup=markup)


async def start(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    button = KeyboardButton("Share my phone number", request_contact=True)
    await update.message.reply_text("Namaste! Share your phone number so I can find your trip.",
                                    reply_markup=ReplyKeyboardMarkup([[button]], one_time_keyboard=True, resize_keyboard=True))


async def contact(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    c = update.message.contact
    if c.user_id != update.effective_user.id:  # a forwarded contact card: someone else's number
        await update.message.reply_text("Please share your own number with the button.")
        return
    await update.message.reply_text("Thanks.", reply_markup=ReplyKeyboardRemove())
    await send(update, await pt.on_contact(planner, chat_of(update), c.phone_number))


async def act_as(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    """/as +91-9000010009 -- development only: act as a demo driver."""
    if not ALLOW_OVERRIDE or not context.args:
        await update.message.reply_text("Not available.")
        return
    await send(update, await pt.on_contact(planner, chat_of(update), context.args[0]))


async def button(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    await update.callback_query.answer()
    await send(update, await pt.on_button(planner, chat_of(update), update.callback_query.data))


async def text(update: Update, context: ContextTypes.DEFAULT_TYPE) -> None:
    await send(update, await pt.on_text(planner, chat_of(update), update.message.text))


async def push_changes(context: ContextTypes.DEFAULT_TYPE) -> None:
    """Every 30 s: send each affected driver the planner's own words about their new slot."""
    by_phone = {pt.last10(s.phone): chat_id for chat_id, s in chats.items() if s.phone}
    cursor["value"], messages = await pt.poll_changes(planner, cursor["value"], by_phone.get)
    for chat_id, message in messages:
        await context.bot.send_message(chat_id, message)


async def on_error(update: object, context: ContextTypes.DEFAULT_TYPE) -> None:
    log.error("update failed", exc_info=context.error)
    err = context.error
    if isinstance(err, pt.PlannerError) and err.status == 401:
        log.critical("the planner refused the agent key: check PLANNER_AGENT_KEY")
    if isinstance(update, Update) and update.effective_chat:
        await update.effective_chat.send_message("The planner is busy, please try again in a minute.")


def build(token: str) -> Application:
    app = Application.builder().token(token).build()
    app.add_handler(CommandHandler("start", start))
    app.add_handler(CommandHandler("as", act_as))
    app.add_handler(MessageHandler(filters.CONTACT, contact))
    app.add_handler(CallbackQueryHandler(button))
    app.add_handler(MessageHandler(filters.TEXT & ~filters.COMMAND, text))
    app.add_error_handler(on_error)
    app.job_queue.run_repeating(push_changes, interval=30, first=5)
    return app


if __name__ == "__main__":
    logging.basicConfig(level=logging.INFO)
    build(os.environ["TELEGRAM_TOKEN"]).run_polling()  # long polling: no public URL needed
```

To run it, put both files in one folder and start it:

```bash
pip install "httpx==0.28.1" "python-telegram-bot[job-queue]==21.10"
TELEGRAM_TOKEN=<token from @BotFather> ALLOW_PHONE_OVERRIDE=true python bot.py
```

Before going live: keep `chats` and `cursor` in SQLite or Redis, so a restart remembers drivers and sends no
duplicates, and set `ALLOW_PHONE_OVERRIDE=false`.

## Pushing plan changes to drivers

The planner does not call the bot. The bot polls `GET /api/agent/changes` every 30 seconds and messages each affected
driver.

```json
{"changeId": 41, "shipmentId": "SHP1003", "driverPhone": "+91-9000010003", "action": "MOVED",
 "fromDockCode": "D1", "fromStart": "2026-08-04T03:30:00Z", "toDockCode": "D1", "toStart": "2026-08-04T04:30:00Z",
 "reasonCode": "SLOT_ALREADY_STARTED", "changedAt": "2026-08-04T04:25:18Z",
 "message": "SHP1003 moved from dock D1 at 09:00 to dock D1 at 10:00 because your slot started before you could reach it."}
```

- `kind` is `BOOKING` (the solver), `APPROVAL` (a coordinator decided a priority truck's change) or `REPLY` (a
  coordinator answered a case). A reply to an unknown number has no `shipmentId` or driver phone; this bot skips it.
- `action` is `NEW` (a slot was booked), `MOVED` (to another slot) or `RELEASED` (no slot now; a coordinator will
  follow up); `APPROVED`, `REJECTED` or `EXPIRED` for approvals; `REPLY` for replies.
- `message` is ready to send, in plain words and IST.

The loop:

1. On first start, call once and set the cursor to the answer's `last`. That skips history, so drivers aren't flooded
   with old changes.
2. Every 30 seconds: `GET /api/agent/changes?after={cursor}&limit=100`.
3. For each change, find the chat by `driverPhone`, matching on the last 10 digits. If the driver never started the
   bot, skip the change.
4. When one page has several changes for the same shipment, send only the newest.
5. Save `next` as the cursor after sending, in the bot's storage, so a restart resumes where it stopped.
6. Repeat while `next < last`. Then wait 30 seconds.

**The nightly reset.** A deployed demo resets every night at 03:00 IST, and change ids start again from 1. When your
saved cursor is above `last`, the feed was reset: set the cursor to `last`. Don't replay from 0: the reset rebuilds
the whole day, and replaying would message every driver at 03:00. Locally the same happens after
`docker compose down -v`.

## Rules and edge cases

The planner enforces every rule itself; the bot's job is to explain the outcome in plain words, never to work around it.

| Situation | What the planner does | What the bot should do |
| --- | --- | --- |
| Driver reports a delay but GPS tracks the truck | Stores the report; the plan keeps using the GPS ETA (only an operations override beats GPS) | Say which time the plan uses; don't promise a new slot |
| Delay moves the ETA 30+ min from the planned one | Re-plans within about a minute; the change arrives on the feed | Tell the driver to expect an update; the push loop sends it |
| Truck now 30+ min early | Moves it to a free earlier slot if there is one; nobody else moves | Nothing; the push loop sends `MOVED` with "you are arriving early" |
| Hold not confirmed within 15 min | Releases it automatically; the old booking stays | Offer the slots again; on confirm, 409 `HOLD_EXPIRED` means "That hold ran out; here are the free slots now." |
| Slot taken between offer and tap | 409 `SLOT_UNAVAILABLE` | "That slot was just taken", then fresh options |
| Driver already holds a slot, or the truck is unloading | 409 `HOLD_CONFLICT` | Show the existing hold's Yes / No, or say unloading is under way |
| Load heavier than the booked dock allows, or start after 21:00 | Flags it to a coordinator, never moves it (`planStatus` `FLAGGED`) | Send `message` ("a coordinator will contact you"); hide Another slot |
| No safe slot at all | `planStatus` `UNASSIGNED`; a coordinator decides | Send `message`; don't offer options (there are none) |
| A critical truck needs a slot | May bump one LOW or NORMAL booking 2+ hours ahead | The bumped driver gets `MOVED` or `RELEASED` ("an urgent delivery needed that slot") |
| Trip finished or cancelled | Lookups return 404 | "That trip is closed. Please call the dock office if that's wrong." |

Errors and retries:

- **401:** the key is wrong or missing. Stop and alert the bot's owner; retrying won't help.
- **400:** a bug in the bot's request. Log it with the `error` text.
- **500 or no answer:** retry once, then tell the driver "The planner is busy, please try again in a minute."
- **Holds:** never retry automatically; a second hold returns 409.

Privacy: store only `chat_id`, phone and the chosen shipment. Never log the agent key, and never put it in the
repository or a chat.

## Testing checklist

Test against a freshly started local stack (`docker compose down -v`, then `docker compose up --build`), so every
driver starts from the same 09:55 plan.

**Acting as a demo driver.** The share-contact button sends your real number, and no demo driver has it. The
development-only command `/as +91-9000010009` (in `bot.py`) skips the contact step; it works only with
`ALLOW_PHONE_OVERRIDE=true`.

| Demo phone | Trips at 09:55 | Good for |
| --- | --- | --- |
| +91-9000010009 | SHP1009, critical, on the Ahmedabad GPS lane | Where is my slot, running late (GPS wins); no free alternative on the busy day |
| +91-9000010004 | SHP1004 (at the yard) and SHP1020 | Choosing between two trips |
| +91-9000010005 | SHP1005, weight breach | Flagged to a coordinator: no slot offers |
| +91-9000010015 | SHP1015, reefer, no compatible slot | No safe slot yet |
| +91-9000010008 | Both trips cancelled | 404: no open trip |
| any other number | none | 404: not a driver |

For Another slot, hold and confirm, use a truck with a free alternative. At 09:55 that is SHP1003, SHP1004 or SHP1007
(drivers +91-9000010003, -0004 and -0007, each of whom has two trips). The 70 generated trucks have numbers starting
`+91-6300`. The plan view's Shipments screen lists every driver's phone, and Driver chat suggests them as you type.

- [ ] `/start` shows the share-contact button; a forwarded contact (not the sender's own) is refused
- [ ] Each demo phone in the table gets the reply its row describes
- [ ] Where is my slot? returns the same text the Driver chat screen shows for that driver
- [ ] Running late 60 min on +91-9000010009 says GPS sets the time (the truck is on a GPS lane)
- [ ] Another slot → tap → Yes moves the booking; the plan view shows the driver on the new dock
- [ ] Another slot → tap → No keeps the old booking
- [ ] Waiting 15 minutes after a hold: confirm replies that the hold ran out (409 `HOLD_EXPIRED`) and re-offers slots
- [ ] Two phones tapping the same slot: one gets the hold, the other "just taken" (409 `SLOT_UNAVAILABLE`)
- [ ] Blocking a booked slot on the Capacity screen pushes a `MOVED` or `RELEASED` message to that driver within about
      a minute
- [ ] Restarting the bot sends no duplicate change messages (the cursor was saved)
- [ ] After `docker compose down -v` and up, the bot notices the reset (cursor above `last`) and does not message
      every driver
- [ ] A wrong agent key gives 401, and the bot stops instead of retrying

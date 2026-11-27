"""Driver exception agent: a fixed state machine with message reading at the edge and the planner behind it.

    inbound message
      -> handled before? (channel, external message id) -> nothing to do
      -> session (channel, chat)
      -> verify: phone -> the planner's open trips for it -> the driver confirms the trip
      -> understand: NLU extracts intent and fields (rules, or Claude when a key is set), range-checked
      -> act: planner calls only (trip status, ETA report, options, hold, confirm, exceptions, cases)
      -> reply, or hand over to a coordinator (a case on the Driver cases screen)

The model reads the message; code decides what happens; the planner decides what is possible. The agent keeps no
planner data and has no clock: every time comes from the planner (a trip's asOf), every slot from its options.
Origin: the team's setuhaul-agent prototype, rewired in phase 7 (docs/agent-api.md, agent/README.md).
"""
from __future__ import annotations

import logging
import re
from datetime import datetime, timedelta

from .channels.base import Button, InboundMessage, OutboundMessage
from .config import Policy
from .nlu import Extraction, build_nlu, checked
from .planner import Planner, PlannerError
from .store import Session, Store
from .timeutil import day_label, fmt, hhmm, parse, resolve_clock

log = logging.getLogger(__name__)

AUTH_STATES = ("NEW", "AWAITING_CONTACT", "AWAITING_SHIPMENT_CONFIRM", "AWAITING_SHIPMENT_PICK", "UNVERIFIED")
AWAITING = {"AWAITING_SHIPMENT_CONFIRM": "SHIPMENT_CONFIRM", "AWAITING_SHIPMENT_PICK": "SHIPMENT_PICK",
            "AWAITING_DELAY_DETAILS": "DELAY_DETAILS", "AWAITING_LATEST_FINISH": "LATEST_FINISH",
            "AWAITING_OPTION_PICK": "OPTION_PICK", "AWAITING_HOLD_CONFIRM": "HOLD_CONFIRM"}
YES_NO = [Button("yes", "Yes"), Button("no", "No")]
MENU = ("I can help you:\n- report a delay or breakdown\n- get a new unloading slot\n- check your current slot\n"
        "Just type what happened.")
SLOT_BUTTON = "slot:"
DONE_STATUSES = ("COMPLETED", "CANCELLED")
NOT_RESCHEDULABLE = "NOT_RESCHEDULABLE"  # the planner's 409 once the truck has reached its dock


def normalize_phone(raw: str | None) -> str | None:
    """+91XXXXXXXXXX for an Indian mobile number in any common format, else None."""
    if not raw:
        return None
    digits = re.sub(r"\D", "", raw)
    if digits.startswith("00"):
        digits = digits[2:]
    if len(digits) == 11 and digits.startswith("0"):
        digits = digits[1:]
    if len(digits) == 12 and digits.startswith("91"):
        digits = digits[2:]
    if len(digits) != 10 or digits[0] not in "6789":
        return None
    return "+91" + digits


def describe(o: dict) -> str:
    return f"Dock {o['dock_code']} {hhmm(o['slot_start'])}-{hhmm(o['slot_end'])}"


class Agent:
    def __init__(self, planner: Planner, store: Store, nlu=None, policy: Policy | None = None):
        self.planner = planner
        self.store = store
        self.policy = policy or Policy()
        self.nlu = nlu or build_nlu()

    # ================================================================== entry point
    def handle(self, msg: InboundMessage) -> list[OutboundMessage]:
        """Replies to one inbound message. The message counts as handled only once this returns, so a failure (the
        planner down, a bug) leaves it to be handled again when the channel retries it."""
        if self.store.was_handled(msg.channel, msg.external_message_id):
            return []  # webhook retry or duplicate update
        s = self.store.session(msg.channel, msg.channel_user_id)
        s.chat_id = msg.chat_id or s.chat_id or msg.channel_user_id
        out = self._route(s, msg)
        self.store.save(s)
        for o in out:
            self._log(s, "AGENT", o.text)
        self.store.mark_handled(msg.channel, msg.external_message_id)
        return out

    def _log(self, s: Session, sender: str, text: str, **kw) -> None:
        if not s.thread_id:
            return
        try:
            self.planner.log_message(s.thread_id, sender, text, **kw)
        except PlannerError as e:  # the record is secondary to answering the driver
            log.warning("could not log a %s message on %s: %s", sender, s.thread_id, e)

    def _reset(self, s: Session) -> None:
        s.state, s.shipment_id, s.thread_id, s.verified_at, s.ctx = "NEW", None, None, None, {}

    # ================================================================== routing
    def _route(self, s: Session, msg: InboundMessage) -> list[OutboundMessage]:
        if msg.phone:
            s.phone = normalize_phone(msg.phone) or msg.phone
        if msg.contact_mismatch:
            return [OutboundMessage("Please share your own number using the button below.", request_contact=True)]
        trip = None
        if s.shipment_id and s.state not in AUTH_STATES:
            trip = self._current_trip(s)
        if trip is None:
            return self._auth_flow(s, msg)
        if msg.message_type == "audio":
            self._log(s, "DRIVER", "[voice note]", external_id=msg.external_message_id, needs_review=True)
            return [OutboundMessage("Voice notes are not supported yet. Please type your message.")]
        return self._verified_flow(s, trip, msg)

    def _current_trip(self, s: Session) -> dict | None:
        """The bound trip, or None (after resetting the session) when it closed or the verification is too old."""
        try:
            trip = self.planner.trip(s.shipment_id)
        except PlannerError as e:
            if e.status != 404:
                raise
            trip = None
        stale = (trip and s.verified_at and parse(trip["asOf"]) - parse(s.verified_at)
                 > timedelta(hours=self.policy.session_ttl_hours))
        if trip and trip["status"] not in DONE_STATUSES and not stale:
            return trip
        if s.thread_id:
            try:
                self.planner.update_thread(s.thread_id, status="CLOSED")
            except PlannerError as e:
                log.warning("could not close thread %s: %s", s.thread_id, e)
        self._reset(s)
        return None

    # ================================================================== verification
    def _auth_flow(self, s: Session, msg: InboundMessage) -> list[OutboundMessage]:
        text = (msg.text or "").strip()
        if s.state == "AWAITING_SHIPMENT_CONFIRM":
            ex = self._read(text, {"awaiting": "SHIPMENT_CONFIRM"}, options=1)
            if ex.intent == "AFFIRM" or ex.option_number == 1:
                return self._bind(s, s.ctx["candidates"][0])
            if ex.intent == "DENY":
                c = s.ctx["candidates"][0]
                case = self.planner.open_case(
                    reason="SHIPMENT_MISMATCH", urgency="NORMAL", channel=s.channel, channel_user_id=s.chat_id,
                    phone=s.phone, shipment_id=c["shipmentId"],
                    summary=f"Driver on {s.phone} says the open trip on record ({c['orderReference']}) is not theirs.")
                s.state, s.ctx["escalation_id"] = "UNVERIFIED", case["caseId"]
                return [OutboundMessage("Thanks. Our records do not match your trip, so a coordinator will contact you "
                                        f"to fix it. Ref {case['caseId']}.")]
            return [self._trip_question(s.ctx["candidates"][0], again=True)]
        if s.state == "AWAITING_SHIPMENT_PICK":
            cands = s.ctx["candidates"]
            ex = self._read(text, {"awaiting": "SHIPMENT_PICK"}, options=len(cands))
            if ex.option_number:
                return self._bind(s, cands[ex.option_number - 1])
            return [self._trip_list(cands, again=True)]

        # NEW / AWAITING_CONTACT / UNVERIFIED
        if text and msg.message_type in ("text", "button"):
            s.ctx["pending"] = (s.ctx.get("pending", []) + [text])[-3:]
        if not s.phone:
            s.state = "AWAITING_CONTACT"
            return [OutboundMessage("Namaste. This is the SetuHaul driver helpline. To find your trip, please share your "
                                    "phone number using the button below.", request_contact=True)]
        phone = normalize_phone(s.phone)
        trips = self.planner.trips_for_phone(phone) if phone else []
        if not trips:
            return self._auth_failed(s, "INVALID_PHONE" if not phone else "NO_OPEN_TRIP")
        cands = [{k: t.get(k) for k in ("shipmentId", "orderReference", "vehicleRegistration", "originCity",
                                        "facilityName", "driverName")} for t in trips]
        s.ctx["candidates"] = cands
        if len(cands) == 1:
            s.state = "AWAITING_SHIPMENT_CONFIRM"
            return [self._trip_question(cands[0], name=cands[0]["driverName"])]
        s.state = "AWAITING_SHIPMENT_PICK"
        return [self._trip_list(cands, name=cands[0]["driverName"])]

    def _auth_failed(self, s: Session, outcome: str) -> list[OutboundMessage]:
        text = {"INVALID_PHONE": "We could not read this phone number. Please contact your dispatcher.",
                "NO_OPEN_TRIP": "We could not find an open trip for this number. A coordinator has been informed and "
                                "will contact you."}[outcome]
        s.state = "UNVERIFIED"
        if not s.ctx.get("escalation_id"):  # one case per conversation, not per message
            pending = " | ".join(s.ctx.get("pending", []))
            case = self.planner.open_case(
                reason=outcome, urgency="NORMAL", channel=s.channel, channel_user_id=s.chat_id, phone=s.phone,
                summary=f"No trip found for {s.channel} user with phone {s.phone}. Messages: {pending or '-'}")
            s.ctx["escalation_id"] = case["caseId"]
        return [OutboundMessage(f"{text} Ref {s.ctx['escalation_id']}.")]

    @staticmethod
    def _trip_line(t: dict) -> str:
        return (f"{t.get('vehicleRegistration') or 'your truck'}, {t['originCity']} to {t['facilityName']}, "
                f"order {t['orderReference']}")

    def _trip_question(self, t: dict, name: str | None = None, again: bool = False) -> OutboundMessage:
        hello = f"Hi {name.split()[0]}. " if name else ""
        q = "Please reply Yes or No. " if again else ""
        return OutboundMessage(f"{hello}{q}Is this your current trip?\n{self._trip_line(t)}", buttons=YES_NO)

    def _trip_list(self, cands: list[dict], name: str | None = None, again: bool = False) -> OutboundMessage:
        hello = f"Hi {name.split()[0]}. " if name else ""
        lines = [f"{i}) {self._trip_line(c)}" for i, c in enumerate(cands, 1)]
        return OutboundMessage(f"{hello}{'Please reply with a number. ' if again else ''}"
                               "You have more than one open trip. Which one is this about?\n" + "\n".join(lines),
                               buttons=[Button(str(i), c["orderReference"]) for i, c in enumerate(cands, 1)])

    def _bind(self, s: Session, cand: dict) -> list[OutboundMessage]:
        trip = self.planner.trip(cand["shipmentId"])
        s.shipment_id = cand["shipmentId"]
        s.thread_id = self.planner.open_thread(s.shipment_id)["threadId"]
        s.verified_at = trip["asOf"]
        s.state = "IDLE"
        pending = s.ctx.pop("pending", [])
        s.ctx = {}
        self._log(s, "SYSTEM", f"Driver verified by {s.channel} phone; trip confirmed {cand['orderReference']}")
        # If the first message already described a problem, act on it now.
        substantive = [p for p in pending
                       if self.nlu.extract(p, {}).intent not in ("GREETING", "UNKNOWN", "AFFIRM", "DENY")]
        if substantive:
            out = self._verified_flow(s, trip, InboundMessage(s.channel, s.channel_user_id, "", " ".join(substantive)))
            return [OutboundMessage("Thanks, trip confirmed.")] + out
        return [OutboundMessage("Thanks, trip confirmed. " + MENU)]

    # ================================================================== understanding
    def _read(self, text: str, context: dict, options: int = 0) -> Extraction:
        return checked(self.nlu.extract(text, context), options)

    def _nlu_context(self, s: Session, trip: dict) -> dict:
        c = {"awaiting": AWAITING.get(s.state), "now": trip["asOf"], "last_question": s.ctx.get("last_question")}
        if s.ctx.get("options"):
            c["options_shown"] = [f"{o['n']}) {describe(o)}" for o in s.ctx["options"]]
        return c

    # ================================================================== verified conversation
    def _verified_flow(self, s: Session, trip: dict, msg: InboundMessage) -> list[OutboundMessage]:
        text = (msg.text or "").strip()
        s.ctx["last_driver_text"] = text
        if text.startswith(SLOT_BUTTON):
            self._log(s, "DRIVER", text, external_id=msg.external_message_id or None, parsed_intent="CHOOSE_OPTION")
            if s.state in ("AWAITING_OPTION_PICK", "AWAITING_HOLD_CONFIRM"):
                return self._slot_button(s, trip, text[len(SLOT_BUTTON):])
            return [OutboundMessage("That button is from an earlier message. " + MENU)]
        ex = self._read(text, self._nlu_context(s, trip), options=len(s.ctx.get("options") or []))
        self._log(s, "DRIVER", text, external_id=msg.external_message_id or None, parsed_intent=ex.intent,
                  needs_review=s.state == "ESCALATED" or ex.intent == "SAFETY")
        if ex.intent == "SAFETY":
            return self._escalate(s, trip, "SAFETY", "URGENT", f"Safety message from driver: {text}",
                                  "Please stay safe. If anyone is hurt, call 112 now. A coordinator has been alerted "
                                  "and will call you.")
        if s.state == "ESCALATED":
            if not self._escalation_open(s):
                s.state = "IDLE"
            else:
                return [OutboundMessage(f"Your case {s.ctx.get('escalation_id', '')} is with a coordinator. They will "
                                        "reply here. Your message has been added to the case.")]
        if ex.intent == "HUMAN_REQUEST":
            return self._escalate(s, trip, "DRIVER_REQUESTED_HUMAN", "NORMAL", f"Driver asked for a person: {text}",
                                  "OK, a coordinator will message you here shortly.")
        handler = {"IDLE": self._idle, "AWAITING_DELAY_DETAILS": self._delay_details,
                   "AWAITING_LATEST_FINISH": self._latest_finish, "AWAITING_OPTION_PICK": self._option_pick,
                   "AWAITING_HOLD_CONFIRM": self._hold_confirm}.get(s.state, self._idle)
        return handler(s, trip, ex)

    def _escalation_open(self, s: Session) -> bool:
        case_id = s.ctx.get("escalation_id")
        if not case_id:
            return False
        try:
            return self.planner.case(case_id)["status"] != "RESOLVED"
        except PlannerError as e:
            return e.status != 404

    # ---------------------------------------------------------------- IDLE
    def _idle(self, s: Session, trip: dict, ex: Extraction) -> list[OutboundMessage]:
        if ex.intent in ("REPORT_DELAY", "EARLY_ARRIVAL", "ASK_SLOT_OPTIONS") and trip.get("canReschedule") is False:
            self._thread(s, intent=ex.intent)
            return self._at_dock(s, trip)
        if ex.intent in ("REPORT_DELAY", "EARLY_ARRIVAL"):
            self._new_draft(s, trip, ex.exception_type or ("EARLY_ARRIVAL" if ex.intent == "EARLY_ARRIVAL" else "DELAY"))
            self._thread(s, intent=ex.intent)
            self._merge(s, trip, ex)
            return self._progress_delay(s, trip)
        if ex.intent == "ASK_SLOT_OPTIONS":
            self._new_draft(s, trip, "DELAY")
            s.ctx["draft"]["slot_request_only"] = True
            self._thread(s, intent="ASK_SLOT_OPTIONS")
            self._merge(s, trip, ex)
            return self._progress_delay(s, trip)
        if ex.intent == "CHECK_STATUS":
            self._thread(s, intent="CHECK_STATUS")
            return [self._status(trip)]
        if ex.intent == "GENERAL_QUESTION":
            self._thread(s, intent="GENERAL_QUESTION")
            if ex.facility_topic:
                return [self._facility_answer(trip, ex.facility_topic)]
            return self._escalate(s, trip, "GENERAL_QUESTION", "LOW", f"Driver question: {s.ctx.get('last_driver_text')}",
                                  "I have passed your question to a coordinator. They will reply here.")
        return self._unclear(s, trip, MENU)

    def _thread(self, s: Session, status: str | None = None, intent: str | None = None) -> None:
        if s.thread_id:
            try:
                self.planner.update_thread(s.thread_id, status=status, intent=intent)
            except PlannerError as e:
                log.warning("thread %s: %s", s.thread_id, e)

    def _exception_status(self, s: Session, status: str) -> None:
        if s.ctx.get("exception_id"):
            try:
                self.planner.set_exception_status(s.ctx["exception_id"], status)
            except PlannerError as e:
                log.warning("exception %s: %s", s.ctx["exception_id"], e)

    def _unclear(self, s: Session, trip: dict, prompt: str, buttons=None) -> list[OutboundMessage]:
        s.ctx["unclear"] = s.ctx.get("unclear", 0) + 1
        if s.ctx["unclear"] > self.policy.max_clarifications:
            return self._escalate(s, trip, "UNCLEAR_CONVERSATION", "NORMAL",
                                  f"Could not understand the driver after {s.ctx['unclear']} tries. "
                                  f"Last: {s.ctx.get('last_driver_text')}",
                                  "Sorry, I am not able to help with this. A coordinator will message you here.")
        prefix = "Sorry, I did not understand. " if s.ctx["unclear"] > 1 or s.state != "IDLE" else ""
        return [OutboundMessage(prefix + prompt, buttons=buttons or [])]

    # ---------------------------------------------------------------- delay flow
    def _new_draft(self, s: Session, trip: dict, exc_type: str) -> None:
        s.ctx["draft"] = {"exception_type": exc_type, "base_eta": trip.get("eta") or trip["asOf"], "asked": []}
        s.ctx.pop("options", None)
        s.ctx["unclear"] = 0

    def _merge(self, s: Session, trip: dict, ex: Extraction) -> None:
        d = s.ctx.setdefault("draft", {"exception_type": "DELAY", "asked": [], "base_eta": trip.get("eta") or trip["asOf"]})
        now = parse(trip["asOf"])
        if ex.exception_type and ex.exception_type not in ("UNKNOWN", "DELAY"):
            d["exception_type"] = ex.exception_type
        for k in ("delay_reason_code", "delay_min", "delay_is_total", "issue_resolved", "location_text"):
            v = getattr(ex, k)
            if v is not None:
                d[k] = v
        if ex.has_other_commitment:
            d["other_commitment"] = True
        if ex.no_time_limit:
            d["no_time_limit"] = True
        for k in ("declared_eta", "requested_after", "latest_finish"):
            t = resolve_clock(getattr(ex, k), now)
            if t:
                d[k] = fmt(t)

    def _delay_details(self, s: Session, trip: dict, ex: Extraction) -> list[OutboundMessage]:
        if ex.intent == "CHECK_STATUS":
            return [self._status(trip), OutboundMessage(s.ctx.get("last_question", MENU))]
        self._merge(s, trip, ex)
        return self._progress_delay(s, trip)

    def _ask(self, s: Session, key: str, text: str, state: str = "AWAITING_DELAY_DETAILS") -> list[OutboundMessage]:
        s.ctx["draft"]["asked"].append(key)
        s.ctx["last_question"] = text
        s.state = state
        self._thread(s, status="WAITING_FOR_DRIVER")
        return [OutboundMessage(text)]

    def _progress_delay(self, s: Session, trip: dict) -> list[OutboundMessage]:
        d = s.ctx["draft"]
        fac = trip["facilityName"]
        now = parse(trip["asOf"])
        base_eta = parse(d["base_eta"])
        asked = d["asked"]
        too_many = len(asked) >= self.policy.max_clarifications + 1
        early = d["exception_type"] == "EARLY_ARRIVAL"

        # 1) The arrival time the driver gives us, without asking for anything the planner knows.
        eta, confidence = None, "MEDIUM"
        if d.get("declared_eta"):
            eta = parse(d["declared_eta"])
            confidence = "HIGH" if d.get("issue_resolved") is not False else "MEDIUM"
        elif d.get("slot_request_only") and not d.get("delay_min"):
            eta = parse(trip["eta"]) if trip.get("eta") else now
            confidence = trip.get("etaConfidence") or "MEDIUM"
        elif d.get("delay_min") is not None:
            breakdown = d["exception_type"] == "BREAKDOWN"
            if breakdown and "breakdown" not in asked and not too_many and (
                    d.get("issue_resolved") is None or d.get("delay_is_total") is None):
                parts = []
                if d.get("issue_resolved") is None:
                    parts.append("Is the truck fixed and moving now?")
                if d.get("delay_is_total") is None:
                    parts.append(f"Is {d['delay_min']} min your total delay to reach {fac}, or only the repair time?")
                return self._ask(s, "breakdown", "Sorry to hear that. " + " ".join(parts))
            if d.get("delay_is_total") is False:
                if "eta" not in asked and not too_many:
                    return self._ask(s, "eta", f"What time do you expect to reach {fac}? (for example 18:30)")
            else:
                eta = base_eta + timedelta(minutes=d["delay_min"])
                confidence = "MEDIUM" if d.get("delay_is_total") or not breakdown else "LOW"
                if d.get("issue_resolved") is False:
                    confidence = "LOW"
        if eta is None and early:
            eta, confidence = now, "MEDIUM"  # "already reached"
        if eta is None:
            if too_many or "eta" in asked:
                self._log_exception(s, None, base_eta, status="NEEDS_INFORMATION")
                return self._escalate(s, trip, "ETA_UNKNOWN", "NORMAL",
                                      f"Driver reported {d['exception_type']} but gave no usable arrival time after "
                                      f"{len(asked)} questions.",
                                      "Thanks. I could not get a clear arrival time, so a coordinator will call you to "
                                      "plan the slot.")
            return self._ask(s, "eta", f"How late will you be, or what time do you expect to reach {fac}? "
                                       "(for example 'about 1 hour' or '18:30')")

        # 2) Tell the planner what the driver said: the exception first (while it is open, the planner uses the later
        # of GPS and the driver's ETA), then the ETA. Its answer has the ETA the plan uses (W1).
        self._log_exception(s, eta, base_eta)
        said_eta = bool(d.get("declared_eta") or d.get("delay_min") is not None or early)
        if said_eta and d.get("eta_recorded") != fmt(eta):
            note = "via driver chat" + (f"; near {d['location_text']}" if d.get("location_text") else "")
            try:
                trip = self.planner.report_eta(s.shipment_id, fmt(eta), d.get("delay_reason_code") or "OTHER",
                                               confidence, note)
            except PlannerError as e:
                if e.status != 400:
                    raise
                d.pop("declared_eta", None)  # outside the planner's window (1 hour ago to 48 hours ahead)
                return self._ask(s, "eta", f"That time does not look right. What time do you expect to reach {fac}?")
            d["eta_recorded"] = fmt(eta)
        d["eta"] = fmt(eta)
        plan_eta = parse(trip["eta"]) if trip.get("eta") else eta
        gps_note = ""
        if said_eta and trip.get("etaSource") in ("GPS", "OPERATIONS_OVERRIDE") and abs((plan_eta - eta).total_seconds()) >= 300:
            source = "Your GPS position" if trip["etaSource"] == "GPS" else "The dock office"
            gps_note = f" {source} puts you here at {hhmm(plan_eta)}, so the plan uses that."

        # 3) Early: report only. The planner moves an early truck itself when an earlier slot is free (W11).
        booking = trip.get("booking")
        if early:
            self._exception_status(s, "OPEN")
            self._thread(s, status="RESOLVED")
            s.state = "IDLE"
            s.ctx.pop("draft", None)
            stays = (f" Until then your slot stays dock {booking['dockCode']} at {hhmm(booking['start'])}."
                     if booking else "")
            return [OutboundMessage(f"Noted, you can be here at {hhmm(eta)}.{gps_note} If an earlier slot is free, the "
                                    f"planner moves you to it and I will tell you here.{stays}")]

        # 4) Does the booking still work? The planner's rule decides (W2), not a grace period of the agent's.
        wants_change = bool(d.get("requested_after") or d.get("slot_request_only"))
        if booking and trip.get("canMakeBooking") and not wants_change:
            self._exception_status(s, "RESOLVED")
            self._thread(s, status="RESOLVED")
            s.state = "IDLE"
            s.ctx.pop("draft", None)
            return [OutboundMessage(f"Noted, new arrival time {day_label(plan_eta, parse(trip['asOf']))}.{gps_note} "
                                    f"Your slot dock {booking['dockCode']} {hhmm(booking['start'])}-"
                                    f"{hhmm(booking['end'])} still works. Please report at the gate by "
                                    f"{hhmm(trip['reportGateBy'])}.")]
        if trip.get("planStatus") == "FLAGGED":
            s.state = "IDLE"
            s.ctx.pop("draft", None)
            return [OutboundMessage(f"Noted, arrival around {hhmm(plan_eta)}.{gps_note} " + trip["message"])]

        # 5) It does not (or the driver wants another time): ask the latest finish once, then offer slots.
        if not d.get("latest_finish") and not d.get("no_time_limit") and "latest" not in asked and not too_many:
            lead = (f"Noted, arrival around {hhmm(plan_eta)}.{gps_note} Your current slot (dock {booking['dockCode']} "
                    f"{hhmm(booking['start'])}) will not work. " if booking and not trip.get("canMakeBooking")
                    else f"Noted, arrival around {hhmm(plan_eta)}.{gps_note} ")
            extra = "You mentioned another pickup. " if d.get("other_commitment") else ""
            return self._ask(s, "latest", lead + extra + "What is the latest time you can finish unloading? Reply with "
                                                         "a time, or 'no limit'.", state="AWAITING_LATEST_FINISH")
        return self._offer_options(s, trip)

    def _latest_finish(self, s: Session, trip: dict, ex: Extraction) -> list[OutboundMessage]:
        if ex.intent == "CHOOSE_OPTION" and ex.option_number and not ex.latest_finish:
            ex.latest_finish = str(ex.option_number)  # "9" read as an option number
        self._merge(s, trip, ex)
        d = s.ctx["draft"]
        if not d.get("latest_finish") and not d.get("no_time_limit") and ex.intent != "DENY":
            if s.ctx.get("unclear", 0) < 1:
                s.ctx["unclear"] = 1
                return [OutboundMessage("Please reply with a time like 21:00, or 'no limit'.")]
        return self._offer_options(s, trip)

    def _log_exception(self, s: Session, eta: datetime | None, base_eta: datetime, status: str | None = None) -> None:
        d = s.ctx["draft"]
        delay = int((eta - base_eta).total_seconds() // 60) if eta else d.get("delay_min")
        if delay is not None:
            delay = min(max(delay, 0), 1440)
        desc = f"{d['exception_type']} reported via chat" + (f" near {d['location_text']}" if d.get("location_text") else "")
        x = self.planner.report_exception(s.thread_id, d["exception_type"], desc, delay_min=delay,
                                          declared_eta=fmt(eta) if eta else None, earliest=d.get("requested_after"),
                                          latest=d.get("latest_finish"), status=status)
        s.ctx["exception_id"] = x["exceptionId"]

    # ---------------------------------------------------------------- options, hold, confirm
    def _fits(self, o: dict, d: dict) -> bool:
        """The driver's own limits on top of the planner's options: not before the time they asked for, finished by
        the latest time they gave."""
        start, end = parse(o["slotStart"]), parse(o["slotEnd"])
        if d.get("requested_after") and start < parse(d["requested_after"]):
            return False
        if d.get("latest_finish") and end > parse(d["latest_finish"]):
            return False
        return True

    def _at_dock(self, s: Session, trip: dict) -> list[OutboundMessage]:
        """The truck has reached its dock: its slot can no longer change by chat, so the slot flow ends here."""
        self._release(s)
        for key in ("draft", "options", "last_question"):
            s.ctx.pop(key, None)
        self._exception_status(s, "RESOLVED")
        s.state = "IDLE"
        return [OutboundMessage("You are already at the dock, so your slot can no longer change here. "
                                + trip["message"])]

    def _offer_options(self, s: Session, trip: dict, intro: str = "") -> list[OutboundMessage]:
        d = s.ctx.get("draft") or {}
        try:
            listed = self.planner.options(s.shipment_id, self.policy.options_searched)
        except PlannerError as e:
            if e.code != NOT_RESCHEDULABLE:
                raise
            return self._at_dock(s, trip)
        found = [o for o in listed if self._fits(o, d)]
        found = found[: self.policy.max_options]
        if not found:
            limits = []
            if d.get("requested_after"):
                limits.append(f"after {hhmm(d['requested_after'])}")
            if d.get("latest_finish"):
                limits.append(f"finishing by {hhmm(d['latest_finish'])}")
            window = " ".join(limits) or "in the next 12 hours"
            urgency = "URGENT" if trip["priorityCode"] in ("HIGH", "CRITICAL") else "NORMAL"
            return self._escalate(s, trip, "NO_FEASIBLE_SLOT", urgency,
                                  f"No free slot for {s.shipment_id} ({trip['requiredDockType']}, {trip['priorityCode']}) "
                                  f"{window}.",
                                  intro + f"There is no free slot for your truck at {trip['facilityName']} {window}. A "
                                  "coordinator is checking now and will message you here.")
        opts = [{"n": i, "slot_id": o["slotId"], "dock_code": o["dockCode"], "slot_start": o["slotStart"],
                 "slot_end": o["slotEnd"]} for i, o in enumerate(found, 1)]
        s.ctx["options"] = opts
        self._exception_status(s, "SLOT_OPTIONS_SHARED")
        self._thread(s, status="WAITING_FOR_DRIVER")
        now = parse(trip["asOf"])
        lines = [f"{o['n']}) Dock {o['dock_code']}, {day_label(parse(o['slot_start']), now)}-{hhmm(o['slot_end'])}"
                 for o in opts]
        note = ""
        if trip["priorityCode"] in self.policy.approval_hint_priorities:
            note = f"\nThis is a {trip['priorityCode']} priority load, so a coordinator approves the change."
        text = (intro + f"Free slots at {trip['facilityName']}:\n" + "\n".join(lines) + note
                + "\nReply with the option number, or 'none' if none of these work.")
        s.ctx["last_question"] = text
        s.state = "AWAITING_OPTION_PICK"
        s.ctx["unclear"] = 0
        return [OutboundMessage(text, buttons=self._option_buttons(opts))]

    @staticmethod
    def _option_buttons(opts: list[dict]) -> list[Button]:
        # The button carries the slot id, so a tap on an older list can never pick a different slot (W7).
        return [Button(SLOT_BUTTON + o["slot_id"], f"{o['dock_code']} {hhmm(o['slot_start'])}") for o in opts]

    def _slot_button(self, s: Session, trip: dict, slot_id: str) -> list[OutboundMessage]:
        opt = next((o for o in s.ctx.get("options") or [] if o["slot_id"] == slot_id), None)
        if opt is None:
            hold = s.ctx.get("hold")
            if hold:  # keep the live hold in focus
                return [OutboundMessage(f"That button is from an older list. You are holding {describe(hold)} until "
                                        f"{hhmm(hold['expires_at'])}. Confirm this slot?", buttons=YES_NO)]
            return self._offer_options(s, trip, intro="That list is out of date. ")
        return self._hold(s, trip, opt)

    def _option_pick(self, s: Session, trip: dict, ex: Extraction) -> list[OutboundMessage]:
        opts = s.ctx.get("options", [])
        if ex.intent == "CHOOSE_OPTION" and ex.option_number:
            return self._hold(s, trip, opts[ex.option_number - 1])
        if ex.intent == "DENY":
            return self._escalate(s, trip, "NO_ACCEPTABLE_OPTION", "NORMAL",
                                  "Driver turned down every offered slot: " + ", ".join(describe(o) for o in opts),
                                  "OK. A coordinator will look for another arrangement and message you here.")
        if ex.intent in ("REPORT_DELAY", "ASK_SLOT_OPTIONS"):
            self._merge(s, trip, ex)  # a new ETA or new limits: work it out again
            return self._progress_delay(s, trip)
        if ex.intent == "CHECK_STATUS":
            return [self._status(trip), OutboundMessage(s.ctx["last_question"])]
        return self._unclear(s, trip, f"Please reply with a number from 1 to {len(opts)}, or 'none'.",
                             buttons=self._option_buttons(opts))

    def _hold(self, s: Session, trip: dict, opt: dict) -> list[OutboundMessage]:
        previous = s.ctx.get("hold")
        if previous and previous["slot_id"] == opt["slot_id"]:
            return [OutboundMessage(f"You are holding {describe(opt)} until {hhmm(previous['expires_at'])}. "
                                    "Confirm this slot?", buttons=YES_NO)]
        if previous:  # the planner allows one hold per truck: let the old one go, and take it back if this one fails
            self._release(s)
        try:
            h = self.planner.hold(s.shipment_id, opt["slot_id"])
        except PlannerError as e:
            if e.code == NOT_RESCHEDULABLE:
                return self._at_dock(s, trip)
            kept = self._rehold(s, previous) if previous else None
            if e.code == "RESCHEDULE_LIMIT":
                return self._escalate(s, trip, "RESCHEDULE_LIMIT", "NORMAL", e.message,
                                      "This trip has already been rescheduled the maximum number of times by chat. "
                                      "A coordinator will plan your slot and message you here.")
            if e.code not in ("SLOT_UNAVAILABLE", "HOLD_CONFLICT"):
                raise
            if kept:
                return [OutboundMessage(f"Sorry, {describe(opt)} was just taken. You still hold {describe(kept)} until "
                                        f"{hhmm(kept['expires_at'])}. Confirm it?", buttons=YES_NO)]
            return self._offer_options(s, trip, intro=f"Sorry, {describe(opt)} is no longer available. ")
        return self._held(s, opt, h)

    def _held(self, s: Session, opt: dict, h: dict) -> list[OutboundMessage]:
        s.ctx["hold"] = {**opt, "hold_id": h["appointmentId"], "expires_at": h["expiresAt"]}
        s.ctx["last_question"] = "confirm hold"
        s.state = "AWAITING_HOLD_CONFIRM"
        self._exception_status(s, "WAITING_CONFIRMATION")
        return [OutboundMessage(f"Holding {describe(opt)} for you until {hhmm(h['expiresAt'])}. Confirm this slot?",
                                buttons=YES_NO)]

    def _rehold(self, s: Session, previous: dict) -> dict | None:
        try:
            h = self.planner.hold(s.shipment_id, previous["slot_id"])
        except PlannerError:
            return None
        s.ctx["hold"] = {**previous, "hold_id": h["appointmentId"], "expires_at": h["expiresAt"]}
        s.state = "AWAITING_HOLD_CONFIRM"
        return s.ctx["hold"]

    def _release(self, s: Session) -> None:
        hold = s.ctx.pop("hold", None)
        if hold:
            try:
                self.planner.release(hold["hold_id"])
            except PlannerError as e:  # already expired or confirmed elsewhere
                log.info("release %s: %s", hold["hold_id"], e)

    def _hold_confirm(self, s: Session, trip: dict, ex: Extraction) -> list[OutboundMessage]:
        hold = s.ctx.get("hold") or {}
        if ex.intent == "AFFIRM" and hold:
            old = trip.get("booking")
            try:
                h = self.planner.confirm(hold["hold_id"])
            except PlannerError as e:
                s.ctx.pop("hold", None)
                if e.code == NOT_RESCHEDULABLE:
                    self._release_quietly(hold)
                    return self._at_dock(s, trip)
                if e.code == "RESCHEDULE_LIMIT":
                    self._release_quietly(hold)
                    return self._escalate(s, trip, "RESCHEDULE_LIMIT", "NORMAL", e.message,
                                          "This trip has already been rescheduled the maximum number of times by chat. "
                                          "A coordinator will plan your slot and message you here.")
                why = {"HOLD_EXPIRED": "The hold on that slot ran out. ",
                       "SLOT_UNAVAILABLE": "That slot was taken before we could confirm. "}.get(e.code)
                if why is None and e.code != "HOLD_CONFLICT":
                    raise
                return self._offer_options(s, trip, intro=(why or "") + "Here are the slots free now. ")
            return self._after_confirm(s, hold, h, old)
        if ex.intent == "DENY":
            self._release(s)
            return self._offer_options(s, trip, intro="OK, released. ")
        if ex.intent == "CHOOSE_OPTION" and ex.option_number:
            return self._hold(s, trip, s.ctx["options"][ex.option_number - 1])
        return self._unclear(s, trip, "Please reply Yes to confirm the held slot, or No to see other options.", YES_NO)

    def _release_quietly(self, hold: dict) -> None:
        try:
            self.planner.release(hold["hold_id"])
        except PlannerError:
            pass

    def _after_confirm(self, s: Session, hold: dict, h: dict, old: dict | None) -> list[OutboundMessage]:
        s.ctx.pop("hold", None)
        s.ctx.pop("options", None)
        s.ctx.pop("last_question", None)
        trip = self.planner.trip(s.shipment_id)
        if h.get("outcome") == "AWAITING_APPROVAL":
            req = h.get("approvalRequest") or {}
            s.ctx["approval_request"] = req.get("requestId")
            self._exception_status(s, "WAITING_CONFIRMATION")
            self._thread(s, status="WAITING_FOR_WAREHOUSE")
            s.state = "IDLE"
            s.ctx.pop("draft", None)
            stands = f" Until then your slot stays dock {old['dockCode']} at {hhmm(old['start'])}." if old else ""
            until = f" A coordinator decides by {hhmm(req['expiresAt'])}." if req.get("expiresAt") else ""
            return [OutboundMessage(f"Requested {describe(hold)}. This is a {trip['priorityCode']} priority load, so a "
                                    f"coordinator approves the change.{until}{stands} You will get the answer here.")]
        self._exception_status(s, "RESOLVED")
        self._thread(s, status="RESOLVED")
        s.state = "IDLE"
        s.ctx.pop("draft", None)
        released = f" Your old slot (dock {old['dockCode']} {hhmm(old['start'])}) is released." if old else ""
        gate = f" Please be at the gate by {hhmm(trip['reportGateBy'])}." if trip.get("reportGateBy") else ""
        return [OutboundMessage(f"Confirmed: {describe(hold)} at {trip['facilityName']}.{gate}{released}")]

    # ---------------------------------------------------------------- answers
    def _status(self, trip: dict) -> OutboundMessage:
        lines = [trip["message"]]
        if trip.get("eta"):
            source = {"GPS": "GPS", "DRIVER_DECLARED": "your update", "OPERATIONS_OVERRIDE": "the dock office",
                      "ORIGINAL_PLAN": "the plan"}.get(trip.get("etaSource"), "the plan")
            lines.append(f"Arrival time the plan uses: {day_label(parse(trip['eta']), parse(trip['asOf']))} ({source}).")
        if trip.get("reportGateBy") and not trip.get("gateIn"):
            lines.append(f"Please be at the gate by {hhmm(trip['reportGateBy'])}.")
        return OutboundMessage("\n".join(lines))

    def _facility_answer(self, trip: dict, topic: str) -> OutboundMessage:
        f = self.planner.facility(trip["facilityId"])
        gate = next((c for c in f["contacts"] if "gate" in (c["role"] or "").lower()), None) or \
            (f["contacts"][0] if f["contacts"] else None)
        if topic == "HOURS":
            t = f"{f['facilityName']} receives trucks {f['openTime']}-{f['closeTime']}."
            if f.get("lastStartTime"):
                t += f" The last unloading starts at {f['lastStartTime']}."
            return OutboundMessage(t + f" Please reach the gate {f['gateBufferMin']} min before your slot.")
        if topic == "CONTACT" and gate:
            return OutboundMessage(f"{gate['role']} at {f['facilityName']}: {gate['name']}, {gate['phone']}.")
        return OutboundMessage(f"{f['facilityName']}, {f['city']}. For directions call "
                               f"{gate['phone'] if gate else 'your dispatcher'}.")

    # ---------------------------------------------------------------- hand-over
    def _escalate(self, s: Session, trip: dict, reason: str, urgency: str, summary: str,
                  reply: str) -> list[OutboundMessage]:
        self._release(s)
        d = s.ctx.get("draft") or {}
        detail = []
        if d.get("eta"):
            detail.append(f"ETA {hhmm(d['eta'])}")
        if d.get("latest_finish"):
            detail.append(f"latest finish {hhmm(d['latest_finish'])}")
        if d.get("location_text"):
            detail.append(f"near {d['location_text']}")
        case = self.planner.open_case(reason=reason, urgency=urgency, channel=s.channel, channel_user_id=s.chat_id,
                                      phone=s.phone, shipment_id=s.shipment_id, thread_id=s.thread_id,
                                      exception_id=s.ctx.get("exception_id"),
                                      summary=summary + (f" [{'; '.join(detail)}]" if detail else ""))
        s.ctx["escalation_id"] = case["caseId"]
        s.state = "ESCALATED"
        return [OutboundMessage(f"{reply} Ref {case['caseId']}.")]

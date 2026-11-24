"""The planner's agent API, one method per route (docs/agent-api.md). The agent reads and writes planner data only
through these calls: no SQL, no copy of the bookings, no clock of its own. Every booking rule, lock and re-plan stays
in the planner.

Errors come back as PlannerError with the planner's code (SLOT_UNAVAILABLE, HOLD_CONFLICT, HOLD_EXPIRED,
RESCHEDULE_LIMIT, ...). A network failure is retried once, then raised as PlannerUnavailable.
"""
from __future__ import annotations

import logging
from urllib.parse import quote

import httpx

log = logging.getLogger(__name__)


class PlannerError(Exception):
    """An error answer from the planner: HTTP status, the planner's code and its text."""

    def __init__(self, status: int, code: str, message: str):
        super().__init__(f"{status} {code}: {message}")
        self.status, self.code, self.message = status, code, message


class PlannerUnavailable(Exception):
    """The planner did not answer (down, or slower than the timeout)."""


def _q(value: str) -> str:
    return quote(value, safe="")


class Planner:
    def __init__(self, base_url: str, agent_key: str, timeout: float = 10.0, transport: httpx.BaseTransport | None = None):
        self._http = httpx.Client(base_url=base_url.rstrip("/") + "/api/agent", timeout=timeout,
                                  headers={"X-Agent-Key": agent_key}, transport=transport)

    def close(self) -> None:
        self._http.close()

    def _call(self, method: str, path: str, body: dict | None = None):
        for attempt in (1, 2):
            try:
                r = self._http.request(method, path, json=body)
                break
            except httpx.TransportError as e:
                if attempt == 2:
                    raise PlannerUnavailable(str(e)) from e
                log.warning("planner %s %s failed (%s); retrying once", method, path, e)
        if r.status_code >= 400:
            try:
                err = r.json()
            except ValueError:
                err = {}
            raise PlannerError(r.status_code, err.get("code", ""), err.get("error", r.text[:200]))
        return r.json() if r.content else None

    # ---------------------------------------------------------------- trips
    def trips_for_phone(self, phone: str) -> list[dict]:
        """The driver's open trips (matched on the phone's last 10 digits); [] when there is none."""
        try:
            return self._call("GET", f"/drivers/{_q(phone)}/shipments")
        except PlannerError as e:
            if e.status == 404:
                return []
            raise

    def trip(self, shipment_id: str) -> dict:
        """One trip: booking, hold, effective ETA and its source, plan status, approval request, the planner's clock
        (asOf), canMakeBooking, reportGateBy and a ready-to-send message."""
        return self._call("GET", f"/shipments/{_q(shipment_id)}")

    def options(self, shipment_id: str, limit: int = 20) -> list[dict]:
        """Free slots this truck could hold now, by the solver's rules, earliest first."""
        return self._call("GET", f"/shipments/{_q(shipment_id)}/options?limit={limit}")

    def facility(self, facility_id: str) -> dict:
        return self._call("GET", f"/facilities/{_q(facility_id)}")

    def report_eta(self, shipment_id: str, eta: str, reason: str = "OTHER", confidence: str = "MEDIUM",
                   note: str | None = None) -> dict:
        """Record the driver's ETA (RFC 3339). Returns the updated trip: its `eta` is the effective ETA the plan uses,
        which may be GPS rather than what the driver said."""
        body = {"eta": eta, "reason": reason, "confidence": confidence}
        if note:
            body["note"] = note[:500]
        return self._call("POST", f"/shipments/{_q(shipment_id)}/eta", body)

    # ---------------------------------------------------------------- holds
    def hold(self, shipment_id: str, slot_id: str) -> dict:
        return self._call("POST", "/holds", {"shipmentId": shipment_id, "slotId": slot_id})

    def confirm(self, hold_id: str) -> dict:
        """The hold's `outcome` is CONFIRMED, or AWAITING_APPROVAL with `approvalRequest` (HIGH and CRITICAL loads)."""
        return self._call("POST", f"/holds/{_q(hold_id)}/confirm")

    def release(self, hold_id: str) -> dict:
        return self._call("POST", f"/holds/{_q(hold_id)}/release")

    # ---------------------------------------------------------------- conversation records
    def open_thread(self, shipment_id: str, intent: str | None = None) -> dict:
        body = {"shipmentId": shipment_id}
        if intent:
            body["intent"] = intent
        return self._call("POST", "/threads", body)

    def update_thread(self, thread_id: str, status: str | None = None, intent: str | None = None) -> dict:
        body = {k: v for k, v in (("status", status), ("intent", intent)) if v}
        return self._call("PATCH", f"/threads/{_q(thread_id)}", body)

    def log_message(self, thread_id: str, sender: str, text: str, external_id: str | None = None,
                    parsed_intent: str | None = None, needs_review: bool = False) -> dict:
        body: dict = {"senderType": sender, "text": text[:4000] or "-", "requiresHumanReview": needs_review}
        if external_id:
            body["externalMessageId"] = external_id[:200]
        if parsed_intent:
            body["parsedIntent"] = parsed_intent
        return self._call("POST", f"/threads/{_q(thread_id)}/messages", body)

    def report_exception(self, thread_id: str, exception_type: str, description: str, *, delay_min: int | None = None,
                         declared_eta: str | None = None, earliest: str | None = None, latest: str | None = None,
                         status: str | None = None) -> dict:
        """Records the thread's exception, or updates its live one; the planner sets severity and the duplicate flag."""
        body: dict = {"threadId": thread_id, "type": exception_type, "description": description[:1000]}
        for key, value in (("delayMin", delay_min), ("declaredEta", declared_eta), ("earliestAcceptable", earliest),
                           ("latestAcceptable", latest), ("status", status)):
            if value is not None:
                body[key] = value
        return self._call("POST", "/exceptions", body)

    def set_exception_status(self, exception_id: str, status: str) -> dict:
        return self._call("PATCH", f"/exceptions/{_q(exception_id)}", {"status": status})

    def open_case(self, *, reason: str, urgency: str, summary: str, channel: str, shipment_id: str | None = None,
                  thread_id: str | None = None, exception_id: str | None = None, channel_user_id: str | None = None,
                  phone: str | None = None) -> dict:
        body = {"reasonCode": reason, "urgency": urgency, "summary": summary[:1000], "channel": channel}
        for key, value in (("shipmentId", shipment_id), ("threadId", thread_id), ("exceptionId", exception_id),
                           ("channelUserId", channel_user_id), ("phone", phone)):
            if value:
                body[key] = value
        return self._call("POST", "/cases", body)

    def case(self, case_id: str) -> dict:
        return self._call("GET", f"/cases/{_q(case_id)}")

    # ---------------------------------------------------------------- the change feed
    def changes(self, after: int, limit: int = 100) -> dict:
        """{"changes": [...], "next": int, "last": int}: booking moves, approval outcomes and coordinator replies."""
        return self._call("GET", f"/changes?after={after}&limit={limit}")

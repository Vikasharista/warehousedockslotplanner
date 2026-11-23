"""Plan-change notices: the agent polls the planner's change feed and tells each affected driver.

  BOOKING   the solver booked, moved or released a truck (only the newest change per truck in one poll is sent)
  APPROVAL  a coordinator approved or rejected a HIGH or CRITICAL truck's change, or it expired
  REPLY     a coordinator answered a case; the conversation leaves the ESCALATED state

A change goes to the chat whose session is bound to the shipment, else to a chat verified with the driver's phone,
else (a reply to an unknown number) back to the chat that opened the case. The cursor is kept in the agent's own
store; the first start skips history, and a cursor past the feed's end (the nightly demo reset) jumps to the end.

WhatsApp: outside 24 hours of the driver's last message, a business-initiated message needs an approved template.
This sends plain text, which works inside that window and on Telegram at any time.
"""
from __future__ import annotations

import logging
import re

from .channels.base import OutboundMessage
from .planner import Planner
from .store import Session, Store

log = logging.getLogger(__name__)
CURSOR = "feed_cursor"


def _last10(phone: str | None) -> str:
    return re.sub(r"\D", "", phone or "")[-10:]


def _target(sessions: list[Session], c: dict) -> Session | None:
    if c.get("kind") == "REPLY" and c.get("caseId"):
        for s in sessions:
            if s.ctx.get("escalation_id") == c["caseId"]:
                return s
        for s in sessions:
            if c.get("channel") == s.channel and c.get("channelUserId") in (s.chat_id, s.channel_user_id):
                return s
    if c.get("shipmentId"):
        for s in sessions:
            if s.shipment_id == c["shipmentId"]:
                return s
    phone = _last10(c.get("driverPhone"))
    if phone:
        for s in sessions:
            if s.shipment_id and _last10(s.phone) == phone:
                return s
    return None


def poll(planner: Planner, store: Store) -> list[tuple[Session, OutboundMessage]]:
    """One poll of the feed: returns (session, message) pairs to send and moves the cursor past what it read."""
    raw = store.get(CURSOR)
    first = planner.changes(int(raw) if raw else 0, limit=1)
    if raw is None or int(raw) > first["last"]:
        store.put(CURSOR, str(first["last"]))  # first start or the feed was reset: skip history
        return []
    cursor = int(raw)
    picked: list[dict] = []
    newest_booking: dict[str, int] = {}
    while True:
        page = planner.changes(cursor, limit=100)
        for c in page["changes"]:
            if c.get("kind", "BOOKING") == "BOOKING":
                newest_booking[c["shipmentId"]] = len(picked)
            picked.append(c)
        cursor = page["next"]
        if not page["changes"] or cursor >= page["last"]:
            break
    sessions = store.sessions()
    out: list[tuple[Session, OutboundMessage]] = []
    for i, c in enumerate(picked):
        if c.get("kind", "BOOKING") == "BOOKING" and newest_booking.get(c["shipmentId"]) != i:
            continue  # several moves of one truck in one poll: only the latest is news
        s = _target(sessions, c)
        if s is None:
            continue
        if c.get("kind") in ("REPLY", "APPROVAL"):
            s = store.session(s.channel, s.channel_user_id)  # re-read just before writing: a message may have moved it on
            if c["kind"] == "REPLY" and s.state in ("ESCALATED", "UNVERIFIED"):
                s.state = "IDLE" if s.shipment_id else "NEW"
                s.ctx.pop("escalation_id", None)
            if c["kind"] == "APPROVAL":
                s.ctx.pop("approval_request", None)
            store.save(s)
        out.append((s, OutboundMessage(c["message"])))
    store.put(CURSOR, str(cursor))
    return out


def deliver(planner: Planner, store: Store, senders: dict) -> int:
    """Polls once and sends each notice through its channel's sender (channel -> fn(chat_id, message)). A notice
    for a channel this process does not run (e.g. WhatsApp not configured) is logged and skipped."""
    sent = 0
    for s, msg in poll(planner, store):
        send = senders.get(s.channel)
        if send is None:
            log.info("no sender for %s; notice for %s skipped", s.channel, s.shipment_id)
            continue
        try:
            send(s.chat_id or s.channel_user_id, msg)
        except Exception:  # one failed send must not lose the other notices (the cursor has moved on)
            log.exception("could not send a notice to %s on %s", s.chat_id, s.channel)
            continue
        if s.thread_id:
            try:
                planner.log_message(s.thread_id, "AGENT", msg.text)
            except Exception:
                log.warning("could not log the notice on %s", s.thread_id, exc_info=True)
        sent += 1
    return sent

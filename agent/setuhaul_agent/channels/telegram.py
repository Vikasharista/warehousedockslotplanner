"""Telegram adapter (Bot API, long polling: no public URL or open port needed).

Identity: Telegram does not share a user's phone number. The bot asks for it with a request_contact keyboard button,
and accepts a shared contact only when contact.user_id == from.id, so a driver cannot pass someone else's number.

Reliability (phase 7, review findings C4 and W10):
  * every update is handled in its own try: one bad update or one failed send never stops the poller;
  * an update that failed is tried again on the next polls (up to 3 times), and replies that could not be sent are
    kept and sent again, so the driver still gets an answer;
  * the agent marks a message handled only after answering it (agent.handle), so a retry is answered, not skipped;
  * only private chats are answered: the bot ignores groups and channels.
"""
from __future__ import annotations

import logging
import time
from dataclasses import dataclass

from .base import InboundMessage, OutboundMessage
from .http import post_json

log = logging.getLogger(__name__)
CHANNEL = "TELEGRAM"
MAX_ATTEMPTS = 3


def parse_update(update: dict, allow_phone_override: bool = False) -> InboundMessage | None:
    """The update as an agent message, or None when the bot should not answer it (not a private chat, no sender)."""
    uid = str(update.get("update_id"))
    if "callback_query" in update:
        cq = update["callback_query"]
        chat = (cq.get("message") or {}).get("chat") or {}
        if chat.get("type") != "private":
            return None
        return InboundMessage(CHANNEL, str(cq["from"]["id"]), f"tg-cb-{cq['id']}", text=cq.get("data", ""),
                              message_type="button", chat_id=str(chat["id"]), raw=update)
    m = update.get("message") or update.get("edited_message")
    if not m or "from" not in m or (m.get("chat") or {}).get("type") != "private":
        return None
    chat_id, user_id = str(m["chat"]["id"]), str(m["from"]["id"])
    base = dict(channel=CHANNEL, channel_user_id=user_id, external_message_id=f"tg-{uid}", chat_id=chat_id, raw=update)
    if "contact" in m:
        c = m["contact"]
        own = str(c.get("user_id")) == user_id
        return InboundMessage(text="", phone=c["phone_number"] if own else None, message_type="contact",
                              contact_mismatch=not own, **base)
    if "voice" in m or "audio" in m:
        return InboundMessage(message_type="audio", **base)
    text = m.get("text")
    if text is None:
        return InboundMessage(message_type="other", **base)
    if allow_phone_override and text.startswith("/as "):
        # Development only (ALLOW_PHONE_OVERRIDE=true): act as a seeded demo driver, e.g. /as +91-9000010009.
        return InboundMessage(text="", phone=text[4:].strip(), message_type="contact", **base)
    return InboundMessage(text=text, **base)


def render(out: OutboundMessage, chat_id: str) -> dict:
    payload: dict = {"chat_id": chat_id, "text": out.text[:4096]}
    if out.request_contact:
        payload["reply_markup"] = {"keyboard": [[{"text": "Share my phone number", "request_contact": True}]],
                                   "one_time_keyboard": True, "resize_keyboard": True}
    elif out.buttons:
        # Slot buttons one per row (their labels are long); short answers (Yes / No, 1 / 2) side by side.
        rows = [[b] for b in out.buttons] if any(b.id.startswith("slot:") for b in out.buttons) else [out.buttons]
        payload["reply_markup"] = {"inline_keyboard": [[{"text": b.label, "callback_data": b.id[:64]} for b in row]
                                                       for row in rows]}
    else:
        payload["reply_markup"] = {"remove_keyboard": True}
    return payload


class BotApi:
    """The Telegram Bot API over HTTPS. Tests pass a stub with the same call()."""

    def __init__(self, token: str):
        self.base = f"https://api.telegram.org/bot{token}"

    def call(self, method: str, payload: dict, timeout: float = 40) -> dict:
        return post_json(f"{self.base}/{method}", payload, timeout=timeout)


@dataclass
class _Pending:
    update: dict
    attempts: int = 0


class TelegramBot:
    def __init__(self, api, agent, allow_phone_override: bool = False, poll_timeout: int = 30):
        self.api, self.agent = api, agent
        self.allow_phone_override = allow_phone_override
        self.poll_timeout = poll_timeout
        self.offset = 0
        self.retry: list[_Pending] = []
        self.outbox: list[tuple[str, OutboundMessage, int]] = []
        self.stopped = False

    def send(self, chat_id: str, out: OutboundMessage) -> None:
        self.api.call("sendMessage", render(out, chat_id))

    def deliver(self, chat_id: str, out: OutboundMessage) -> None:
        """Sends now, or keeps the message to send again on the next poll."""
        try:
            self.send(chat_id, out)
        except Exception:
            log.exception("sendMessage to %s failed; will retry", chat_id)
            self.outbox.append((chat_id, out, 1))

    def _handle(self, update: dict) -> None:
        if "callback_query" in update:
            try:
                self.api.call("answerCallbackQuery", {"callback_query_id": update["callback_query"]["id"]})
            except Exception:
                log.warning("answerCallbackQuery failed", exc_info=True)
        msg = parse_update(update, self.allow_phone_override)
        if msg is None:
            return
        for out in self.agent.handle(msg):
            self.deliver(msg.chat_id, out)

    def _flush_outbox(self) -> None:
        pending, self.outbox = self.outbox, []
        for chat_id, out, attempts in pending:
            try:
                self.send(chat_id, out)
            except Exception:
                if attempts + 1 < MAX_ATTEMPTS:
                    self.outbox.append((chat_id, out, attempts + 1))
                else:
                    log.error("gave up sending a reply to %s after %d attempts", chat_id, MAX_ATTEMPTS)

    def poll_once(self) -> None:
        """One round: resend what failed, fetch new updates, handle each (in order) on its own."""
        self._flush_outbox()
        res = self.api.call("getUpdates", {"offset": self.offset, "timeout": self.poll_timeout,
                                           "allowed_updates": ["message", "edited_message", "callback_query"]},
                            timeout=self.poll_timeout + 10)
        work, self.retry = self.retry, []
        for u in res.get("result", []):
            self.offset = max(self.offset, u["update_id"] + 1)
            work.append(_Pending(u))
        for p in work:
            try:
                self._handle(p.update)
            except Exception:
                p.attempts += 1
                log.exception("update %s failed (attempt %d)", p.update.get("update_id"), p.attempts)
                if p.attempts < MAX_ATTEMPTS:
                    self.retry.append(p)

    def run(self) -> None:
        log.info("Telegram polling started")
        while not self.stopped:
            try:
                self.poll_once()
            except Exception:
                log.exception("getUpdates failed; retrying in 3 s")
                time.sleep(3)

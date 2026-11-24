"""WhatsApp Cloud API adapter (webhook in, Graph API out).

Identity: every inbound message carries wa_id, the sender's phone number, so the
driver is matched without asking for anything.

Security: Meta signs each webhook body with the app secret (X-Hub-Signature-256).
Unsigned or wrongly signed requests are rejected before reaching the agent.

24-hour rule: free-form replies are only allowed within 24 hours of the driver's last
message. Anything the business starts later (e.g. "your slot moved") must use an
approved template; send_template() covers that.

Run with the agent (python -m setuhaul_agent) and WHATSAPP_TOKEN, WHATSAPP_PHONE_NUMBER_ID, WHATSAPP_APP_SECRET and
WHATSAPP_VERIFY_TOKEN set; Meta must reach the webhook over public HTTPS, so it is optional until the demo is deployed.
"""
from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import logging
import os
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

from .base import InboundMessage, OutboundMessage
from .http import post_json

log = logging.getLogger(__name__)
CHANNEL = "WHATSAPP"
GRAPH = "https://graph.facebook.com/v21.0"


def verify_signature(body: bytes, header: str | None, app_secret: str) -> bool:
    if not header or not header.startswith("sha256="):
        return False
    expected = hmac.new(app_secret.encode(), body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, header[7:])


def parse_webhook(payload: dict) -> list[InboundMessage]:
    out = []
    for entry in payload.get("entry", []):
        for change in entry.get("changes", []):
            value = change.get("value", {})
            for m in value.get("messages", []):
                wa_id = m.get("from")
                base = dict(channel=CHANNEL, channel_user_id=wa_id, external_message_id=m.get("id"),
                            phone="+" + wa_id if wa_id and not wa_id.startswith("+") else wa_id, raw=m)
                t = m.get("type")
                if t == "text":
                    out.append(InboundMessage(text=m["text"]["body"], **base))
                elif t == "interactive":
                    i = m["interactive"]
                    reply = i.get("button_reply") or i.get("list_reply") or {}
                    out.append(InboundMessage(text=reply.get("id", ""), message_type="button", **base))
                elif t == "button":  # quick-reply button on a template
                    out.append(InboundMessage(text=m["button"].get("payload") or m["button"].get("text", ""),
                                              message_type="button", **base))
                elif t in ("audio", "voice"):
                    out.append(InboundMessage(message_type="audio", **base))
                else:
                    out.append(InboundMessage(message_type="other", **base))
            # value["statuses"] (sent/delivered/read/failed) could update operational_messages.
    return out


def render(out: OutboundMessage, to: str) -> dict:
    to = to.lstrip("+")
    if out.template:
        return {"messaging_product": "whatsapp", "to": to, "type": "template",
                "template": {"name": out.template, "language": {"code": "en"},
                             "components": [{"type": "body", "parameters": [
                                 {"type": "text", "text": p} for p in out.template_params]}]}}
    if out.buttons and len(out.buttons) <= 3:
        return {"messaging_product": "whatsapp", "to": to, "type": "interactive",
                "interactive": {"type": "button", "body": {"text": out.text[:1024]},
                                "action": {"buttons": [{"type": "reply", "reply": {"id": b.id, "title": b.label[:20]}}
                                                       for b in out.buttons]}}}
    if out.buttons:
        return {"messaging_product": "whatsapp", "to": to, "type": "interactive",
                "interactive": {"type": "list", "body": {"text": out.text[:1024]},
                                "action": {"button": "Choose", "sections": [{"title": "Options", "rows": [
                                    {"id": b.id, "title": b.label[:24]} for b in out.buttons[:10]]}]}}}
    return {"messaging_product": "whatsapp", "to": to, "type": "text", "text": {"body": out.text[:4096]}}


class WhatsAppClient:
    def __init__(self, token: str, phone_number_id: str):
        self.url = f"{GRAPH}/{phone_number_id}/messages"
        self.headers = {"Authorization": f"Bearer {token}"}

    def send(self, out: OutboundMessage, to: str) -> dict:
        return post_json(self.url, render(out, to), self.headers)


def make_handler(agent, client: WhatsAppClient, app_secret: str, verify_token: str):
    user_locks: dict[str, threading.Lock] = {}
    guard = threading.Lock()

    def lock_for(uid: str) -> threading.Lock:
        with guard:
            return user_locks.setdefault(uid, threading.Lock())

    def process(msgs: list[InboundMessage]) -> None:
        for msg in msgs:
            with lock_for(msg.channel_user_id):  # one user's messages in order
                try:
                    # A message is marked handled only once answered, so Meta's retry of a failed one is answered.
                    for out in agent.handle(msg):
                        client.send(out, msg.channel_user_id)
                except Exception:
                    log.exception("Failed handling WhatsApp message %s", msg.external_message_id)

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):  # webhook verification handshake
            q = parse_qs(urlparse(self.path).query)
            if q.get("hub.mode", [""])[0] == "subscribe" and q.get("hub.verify_token", [""])[0] == verify_token:
                self.send_response(200)
                self.end_headers()
                self.wfile.write(q.get("hub.challenge", [""])[0].encode())
            else:
                self.send_response(403)
                self.end_headers()

        def do_POST(self):
            body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
            if not verify_signature(body, self.headers.get("X-Hub-Signature-256"), app_secret):
                self.send_response(401)
                self.end_headers()
                return
            self.send_response(200)  # ack fast; Meta retries slow responses, dedupe absorbs retries
            self.end_headers()
            msgs = parse_webhook(json.loads(body or b"{}"))
            threading.Thread(target=process, args=(msgs,), daemon=True).start()

        def log_message(self, *a):
            pass

    return Handler

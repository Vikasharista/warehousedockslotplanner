"""Simulator channel: drives the agent from scripts, tests and the CLI.
The phone is trusted as if the channel proved it (like WhatsApp's wa_id)."""
from __future__ import annotations

import itertools

from .base import InboundMessage, OutboundMessage

_ids = itertools.count(1)


class SimDriver:
    def __init__(self, agent, phone: str, name: str | None = None, channel: str = "SIM",
                 share_phone: bool = True):
        self.agent = agent
        self.phone = phone
        self.name = name or phone
        self.channel = channel
        self.share_phone = share_phone
        self.transcript: list[tuple[str, str]] = []

    def say(self, text: str, message_type: str = "text") -> list[OutboundMessage]:
        msg = InboundMessage(channel=self.channel, channel_user_id=self.phone,
                             external_message_id=f"sim-{next(_ids)}", text=text,
                             phone=self.phone if self.share_phone else None, message_type=message_type)
        self.transcript.append((self.name, text))
        out = self.agent.handle(msg)
        for o in out:
            self.transcript.append(("Agent", render(o)))
        return out

    def last(self) -> str:
        return self.transcript[-1][1]


def render(o: OutboundMessage) -> str:
    t = o.text
    if o.buttons:
        t += "\n  [" + "] [".join(f"{b.label}" for b in o.buttons) + "]"
    if o.request_contact:
        t += "\n  [Share my phone number]"
    return t

"""Channel-neutral message types. Adapters convert to and from these; the agent never
knows which app a message came from."""
from __future__ import annotations

from dataclasses import dataclass, field


@dataclass
class Button:
    id: str        # what comes back as the driver's reply text, e.g. "2", "yes"
    label: str     # what the driver sees (WhatsApp limits reply-button titles to 20 chars)


@dataclass
class InboundMessage:
    channel: str                       # WHATSAPP | TELEGRAM | SIM
    channel_user_id: str               # wa_id / Telegram from.id / simulator id
    external_message_id: str
    text: str = ""
    phone: str | None = None           # set when the channel proves the number
    message_type: str = "text"         # text | button | contact | audio | other
    contact_mismatch: bool = False     # Telegram: shared a contact that is not their own
    chat_id: str | None = None         # where replies go (Telegram chat id); defaults to channel_user_id
    raw: dict = field(default_factory=dict, repr=False)


@dataclass
class OutboundMessage:
    text: str
    buttons: list[Button] = field(default_factory=list)
    request_contact: bool = False      # Telegram: show the "share my number" keyboard
    template: str | None = None        # WhatsApp: approved template name for business-initiated sends
    template_params: list[str] = field(default_factory=list)

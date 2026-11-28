"""Settings, all from the environment. Planning rules (hold length, gate buffer, which loads need approval, the chat
reschedule limit) are the planner's and are not repeated here."""
from __future__ import annotations

import os
from dataclasses import dataclass


@dataclass(frozen=True)
class Policy:
    max_options: int = 3            # slots offered at once (WhatsApp allows 3 reply buttons)
    options_searched: int = 20      # slots read from the planner before the driver's own limits are applied
    max_clarifications: int = 2     # failed clarification turns before a coordinator takes over
    session_ttl_hours: int = 24     # re-verify the trip after this long (planner time)
    conflict_min: int = 60          # a driver's "I am here" this far from the GPS time goes to a coordinator to check
    # Only for wording ("a coordinator approves the change"); the planner decides, and its 202 is what counts.
    approval_hint_priorities: tuple = ("HIGH", "CRITICAL")


@dataclass(frozen=True)
class Settings:
    planner_url: str
    agent_key: str
    state_db: str
    telegram_token: str | None
    whatsapp: dict | None
    feed_every_seconds: float
    allow_phone_override: bool
    telegram_workers: int = 8

    @staticmethod
    def from_env() -> "Settings":
        wa = None
        if os.environ.get("WHATSAPP_TOKEN"):
            wa = {k: os.environ[k.upper()] for k in ("whatsapp_token", "whatsapp_phone_number_id",
                                                      "whatsapp_app_secret", "whatsapp_verify_token")}
            wa["port"] = int(os.environ.get("WHATSAPP_PORT", "8090"))
        return Settings(
            planner_url=os.environ.get("PLANNER_URL", "http://localhost:8082"),
            agent_key=os.environ.get("PLANNER_AGENT_KEY", "local-agent-key-for-development-only"),
            state_db=os.environ.get("AGENT_STATE_DB", "agent-state.db"),
            telegram_token=os.environ.get("TELEGRAM_TOKEN") or None,
            whatsapp=wa,
            feed_every_seconds=float(os.environ.get("FEED_EVERY_SECONDS", "30")),
            allow_phone_override=os.environ.get("ALLOW_PHONE_OVERRIDE", "false").lower() == "true",
            telegram_workers=int(os.environ.get("TELEGRAM_WORKERS", "8")),
        )

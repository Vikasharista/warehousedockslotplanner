"""The agent's own state, in a private SQLite file that nothing else reads: conversation sessions, the log of inbound
messages already handled (so a retried webhook or Telegram update is answered once) and the change-feed cursor.

It holds no planner data. Bookings, holds, ETAs, threads, exceptions and cases live in the planner and are reached
only through planner.py. This is the only module in agent/ that uses SQL.
"""
from __future__ import annotations

import json
import sqlite3
import threading
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone

SCHEMA = """
CREATE TABLE IF NOT EXISTS sessions (
    channel TEXT NOT NULL,
    channel_user_id TEXT NOT NULL,
    data TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (channel, channel_user_id)
);
CREATE TABLE IF NOT EXISTS handled (
    channel TEXT NOT NULL,
    external_message_id TEXT NOT NULL,
    handled_at TEXT NOT NULL,
    PRIMARY KEY (channel, external_message_id)
);
CREATE TABLE IF NOT EXISTS kv (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
"""


@dataclass
class Session:
    """One chat's conversation state. `chat_id` is where replies go (Telegram chat id, WhatsApp number)."""
    channel: str
    channel_user_id: str
    chat_id: str | None = None
    state: str = "NEW"
    phone: str | None = None
    shipment_id: str | None = None
    thread_id: str | None = None
    verified_at: str | None = None
    ctx: dict = field(default_factory=dict)


def _now() -> str:
    # Bookkeeping only (when a row was written), never used for planning.
    return datetime.now(timezone.utc).isoformat()


class Store:
    def __init__(self, path: str = ":memory:"):
        self._db = sqlite3.connect(path, check_same_thread=False, isolation_level=None)
        self._db.execute("PRAGMA journal_mode=WAL") if path != ":memory:" else None
        self._lock = threading.Lock()
        with self._lock:
            self._db.executescript(SCHEMA)

    def close(self) -> None:
        self._db.close()

    # ---------------------------------------------------------------- sessions
    def session(self, channel: str, channel_user_id: str) -> Session:
        with self._lock:
            row = self._db.execute("SELECT data FROM sessions WHERE channel = ? AND channel_user_id = ?",
                                   (channel, channel_user_id)).fetchone()
        if not row:
            return Session(channel, channel_user_id)
        return Session(**json.loads(row[0]))

    def save(self, s: Session) -> None:
        with self._lock:
            self._db.execute("""INSERT INTO sessions VALUES (?, ?, ?, ?)
                                ON CONFLICT (channel, channel_user_id) DO UPDATE SET data = excluded.data,
                                    updated_at = excluded.updated_at""",
                             (s.channel, s.channel_user_id, json.dumps(asdict(s), default=str), _now()))

    def sessions(self) -> list[Session]:
        with self._lock:
            rows = self._db.execute("SELECT data FROM sessions").fetchall()
        return [Session(**json.loads(r[0])) for r in rows]

    # ---------------------------------------------------------------- handled messages
    def was_handled(self, channel: str, external_message_id: str) -> bool:
        with self._lock:
            return self._db.execute("SELECT 1 FROM handled WHERE channel = ? AND external_message_id = ?",
                                    (channel, external_message_id)).fetchone() is not None

    def mark_handled(self, channel: str, external_message_id: str) -> None:
        """Called only after a message was handled, so a failure leaves it to be handled again on a retry."""
        with self._lock:
            self._db.execute("INSERT OR IGNORE INTO handled VALUES (?, ?, ?)", (channel, external_message_id, _now()))

    # ---------------------------------------------------------------- small values (the feed cursor)
    def get(self, key: str) -> str | None:
        with self._lock:
            row = self._db.execute("SELECT value FROM kv WHERE key = ?", (key,)).fetchone()
        return row[0] if row else None

    def put(self, key: str, value: str) -> None:
        with self._lock:
            self._db.execute("INSERT INTO kv VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value",
                             (key, value))

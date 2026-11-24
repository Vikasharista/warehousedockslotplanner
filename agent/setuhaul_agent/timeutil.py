"""Time helpers. Times are shown to drivers in IST. The agent has no clock of its own: "now" is the planner's
(a trip's asOf), and a driver's "7pm" is read relative to a planner time (resolve_clock)."""
from __future__ import annotations

import re
from datetime import datetime, timedelta, timezone

IST = timezone(timedelta(hours=5, minutes=30))


def parse(ts: str | None) -> datetime | None:
    if not ts:
        return None
    dt = datetime.fromisoformat(ts)
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=IST)
    return dt.astimezone(IST)


def fmt(dt: datetime) -> str:
    return dt.astimezone(IST).replace(microsecond=0).isoformat()


def hhmm(dt: datetime | str | None) -> str:
    if isinstance(dt, str):
        dt = parse(dt)
    return dt.strftime("%H:%M") if dt else "-"


def day_label(dt: datetime, ref: datetime) -> str:
    """'19:30' for today, '19:30 (5 Aug)' otherwise."""
    if dt.date() == ref.date():
        return hhmm(dt)
    return f"{hhmm(dt)} ({dt.day} {dt.strftime('%b')})"


_CLOCK_RE = re.compile(r"^\s*(\d{1,2})(?:[:.](\d{2}))?\s*(am|pm|a\.m\.|p\.m\.)?\s*$", re.I)


def resolve_clock(text: str | None, not_before: datetime) -> datetime | None:
    """Turn a driver's clock time ('7', '7pm', '19:30', '7.30') into a datetime.

    Without am/pm, '7' could be 07:00 or 19:00: take the earliest reading that is not
    more than 30 minutes before `not_before`, rolling to the next day if needed.
    """
    if not text:
        return None
    m = _CLOCK_RE.match(text)
    if not m:
        return None
    h, mins, ampm = int(m.group(1)), int(m.group(2) or 0), (m.group(3) or "").lower()
    if h > 23 or mins > 59:
        return None
    if ampm.startswith("p") and h < 12:
        h += 12
    elif ampm.startswith("a") and h == 12:
        h = 0
    hours = [h] if (ampm or h >= 13 or h == 0) else [h, h + 12 if h < 12 else h]
    base = not_before.replace(second=0, microsecond=0)
    floor = base - timedelta(minutes=30)
    for day_offset in (0, 1):
        for hour in sorted(set(hours)):
            cand = base.replace(hour=hour % 24, minute=mins) + timedelta(days=day_offset)
            if cand >= floor:
                return cand
    return None

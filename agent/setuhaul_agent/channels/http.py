"""Tiny JSON-over-HTTPS helper on the standard library (no extra dependency)."""
from __future__ import annotations

import json
import urllib.request


def post_json(url: str, payload: dict, headers: dict | None = None, timeout: float = 40) -> dict:
    req = urllib.request.Request(url, data=json.dumps(payload).encode(), method="POST",
                                 headers={"Content-Type": "application/json", **(headers or {})})
    with urllib.request.urlopen(req, timeout=timeout) as r:
        body = r.read()
    return json.loads(body) if body else {}

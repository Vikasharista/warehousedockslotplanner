"""Unit tests that need no planner: message reading (W9), the Telegram bot's reliability (C4, W10), buttons (W7),
the WhatsApp adapter, the private store and the no-SQL rule (C1)."""
import hashlib
import hmac
import re
import time
from pathlib import Path
from types import SimpleNamespace

from setuhaul_agent.channels import telegram, whatsapp
from setuhaul_agent.channels.base import Button, OutboundMessage
from setuhaul_agent.nlu import ClaudeNLU, Extraction, RuleNLU, checked
from setuhaul_agent.store import Session, Store

AGENT = Path(__file__).resolve().parents[1] / "setuhaul_agent"
BRIEF_MESSAGE = ("Tyre damaged near Neemrana. Around 90 minutes late. Can I get something after 7? "
                 "I also have another pickup tonight.")


# ---------------------------------------------------------------- message reading
def test_rules_read_the_brief_example():
    ex = RuleNLU().extract(BRIEF_MESSAGE)
    assert ex.intent == "REPORT_DELAY" and ex.exception_type == "BREAKDOWN"
    assert ex.delay_min == 90 and ex.requested_after == "7"
    assert ex.has_other_commitment and ex.location_text == "Neemrana"


def test_out_of_range_readings_are_dropped():  # W9
    ex = checked(Extraction(intent="REPORT_DELAY", delay_min=5000, option_number=7, declared_eta="tomorrow-ish!!",
                            exception_type="ALIENS", delay_reason_code="MAGIC", facility_topic="WEATHER",
                            location_text="x" * 200, no_time_limit="yes"), options=3)
    assert ex.delay_min is None and ex.option_number is None and ex.declared_eta is None
    assert ex.exception_type is None and ex.delay_reason_code is None and ex.facility_topic is None
    assert ex.location_text is None and ex.no_time_limit is False
    assert checked(Extraction(intent="CHOOSE_OPTION", option_number=4), options=3).intent == "UNKNOWN"
    assert checked(Extraction(intent="CHOOSE_OPTION", option_number=2), options=3).option_number == 2
    assert checked(Extraction(intent="SELL_TRUCK")).intent == "UNKNOWN"
    assert checked(Extraction(intent="REPORT_DELAY", delay_min=True)).delay_min is None, "a bool is not a number"
    assert checked(Extraction(intent="REPORT_DELAY", delay_min=1440)).delay_min == 1440


def _claude(create) -> ClaudeNLU:
    nlu = ClaudeNLU.__new__(ClaudeNLU)
    nlu.model, nlu.fallback = "test", RuleNLU()
    nlu.client = SimpleNamespace(messages=SimpleNamespace(create=create))
    return nlu


def test_claude_reading_is_mapped_and_safety_always_wins():
    block = SimpleNamespace(type="tool_use", input={"intent": "REPORT_DELAY", "exception_type": "BREAKDOWN",
                                                    "delay_min": 90, "confidence": 0.9})
    ex = _claude(lambda **kw: SimpleNamespace(content=[block])).extract("tyre gaya, 1.5 ghante late")
    assert ex.intent == "REPORT_DELAY" and ex.delay_min == 90 and ex.source == "claude"
    ex = _claude(lambda **kw: SimpleNamespace(content=[block])).extract("accident ho gaya, 1 hour late")
    assert ex.intent == "SAFETY"


def test_a_stalled_or_failing_claude_falls_back_to_rules():  # W9
    def stalled(**kw):
        raise TimeoutError("read timed out after 10 s")
    ex = _claude(stalled).extract("traffic, 30 min late")
    assert ex.source == "rules" and ex.delay_min == 30


def test_the_claude_client_gets_a_timeout_and_one_retry(monkeypatch):  # W9
    seen = {}

    class Fake:
        def __init__(self, **kw):
            seen.update(kw)
    import anthropic
    monkeypatch.setattr(anthropic, "Anthropic", Fake)
    ClaudeNLU()
    assert seen == {"timeout": 10.0, "max_retries": 1}


# ---------------------------------------------------------------- Telegram
def update(uid, text, chat_type="private", user=5, chat=5):
    return {"update_id": uid, "message": {"message_id": uid, "from": {"id": user}, "chat": {"id": chat, "type": chat_type},
                                          "text": text}}


class StubTelegram:
    """The Bot API: getUpdates returns the queued updates once; sendMessage can be made to fail."""

    def __init__(self, updates, fail_sends=0):
        self.updates, self.fail_sends, self.sent = list(updates), fail_sends, []

    def call(self, method, payload, timeout=40):
        if method == "getUpdates":
            out, self.updates = [u for u in self.updates if u["update_id"] >= payload["offset"]], []
            return {"ok": True, "result": out}
        if method == "sendMessage":
            if self.fail_sends > 0:
                self.fail_sends -= 1
                raise OSError("telegram unreachable")
            self.sent.append(payload)
        return {"ok": True}


class EchoAgent:
    """Answers every message; marks it handled only once answered, like the real agent."""

    def __init__(self, fail_first=0):
        self.handled, self.fail_first = set(), fail_first

    def handle(self, msg):
        if self.fail_first > 0:
            self.fail_first -= 1
            raise RuntimeError("planner unavailable")
        if msg.external_message_id in self.handled:
            return []
        self.handled.add(msg.external_message_id)
        return [OutboundMessage(f"echo {msg.text}")]


def test_a_failed_send_does_not_stop_the_bot_and_the_reply_is_sent_on_retry():  # C4
    api = StubTelegram([update(1, "late"), update(2, "where")], fail_sends=1)
    bot = telegram.TelegramBot(api, EchoAgent(), poll_timeout=0)
    bot.poll_once()
    assert [p["text"] for p in api.sent] == ["echo where"], "the second update is handled although the first send failed"
    bot.poll_once()
    assert [p["text"] for p in api.sent] == ["echo where", "echo late"], "the failed reply is sent on the next poll"


def test_a_failing_update_is_handled_again_on_the_next_poll():  # C4
    api = StubTelegram([update(1, "late")])
    bot = telegram.TelegramBot(api, EchoAgent(fail_first=1), poll_timeout=0)
    bot.poll_once()
    assert api.sent == []
    bot.poll_once()
    assert [p["text"] for p in api.sent] == ["echo late"]


def test_the_poller_gives_up_on_an_update_after_three_attempts():
    api = StubTelegram([update(1, "late")])
    bot = telegram.TelegramBot(api, EchoAgent(fail_first=99), poll_timeout=0)
    for _ in range(5):
        bot.poll_once()
    assert bot.retry == [] and api.sent == []


def test_group_chats_get_no_reply():  # W10
    api = StubTelegram([update(1, "hello", chat_type="group", chat=-100), update(2, "hi", chat_type="supergroup")])
    bot = telegram.TelegramBot(api, EchoAgent(), poll_timeout=0)
    bot.poll_once()
    assert api.sent == []
    assert telegram.parse_update({"update_id": 3, "callback_query": {"id": "q", "from": {"id": 5}, "data": "yes",
                                  "message": {"chat": {"id": -100, "type": "group"}}}}) is None


def test_a_contact_must_be_the_senders_own():
    own = {"update_id": 1, "message": {"from": {"id": 5}, "chat": {"id": 5, "type": "private"},
                                       "contact": {"phone_number": "919000010009", "user_id": 5}}}
    other = {"update_id": 2, "message": {"from": {"id": 5}, "chat": {"id": 5, "type": "private"},
                                         "contact": {"phone_number": "919000010001", "user_id": 6}}}
    assert telegram.parse_update(own).phone == "919000010009"
    m = telegram.parse_update(other)
    assert m.phone is None and m.contact_mismatch
    assert telegram.parse_update(update(3, "/as +91-9000010009")).phone is None, "no override unless allowed"
    assert telegram.parse_update(update(3, "/as +91-9000010009"), allow_phone_override=True).phone == "+91-9000010009"


def test_slot_buttons_carry_the_slot_id_one_per_row():  # W7
    out = OutboundMessage("Pick", buttons=[Button("slot:SLOT-JAI-019", "D2 12:00"), Button("slot:SLOT-JAI-061", "D5 12:00")])
    rows = telegram.render(out, "5")["reply_markup"]["inline_keyboard"]
    assert [[b["callback_data"] for b in r] for r in rows] == [["slot:SLOT-JAI-019"], ["slot:SLOT-JAI-061"]]
    yes_no = telegram.render(OutboundMessage("Keep?", buttons=[Button("yes", "Yes"), Button("no", "No")]), "5")
    assert len(yes_no["reply_markup"]["inline_keyboard"]) == 1


# ---------------------------------------------------------------- WhatsApp
def test_whatsapp_signature_and_parsing():
    body = b'{"x":1}'
    sig = "sha256=" + hmac.new(b"secret", body, hashlib.sha256).hexdigest()
    assert whatsapp.verify_signature(body, sig, "secret") and not whatsapp.verify_signature(body, sig, "other")
    payload = {"entry": [{"changes": [{"value": {"messages": [
        {"from": "919000010009", "id": "wamid.1", "type": "text", "text": {"body": "late by 1 hr"}},
        {"from": "919000010009", "id": "wamid.2", "type": "interactive",
         "interactive": {"type": "button_reply", "button_reply": {"id": "slot:SLOT-JAI-019", "title": "D2 12:00"}}}]}}]}]}
    m = whatsapp.parse_webhook(payload)
    assert [x.text for x in m] == ["late by 1 hr", "slot:SLOT-JAI-019"] and m[0].phone == "+919000010009"


# ---------------------------------------------------------------- the private store
def test_store_keeps_sessions_handled_messages_and_the_cursor(tmp_path):
    st = Store(str(tmp_path / "state.db"))
    s = Session("TELEGRAM", "5", chat_id="5", state="IDLE", shipment_id="SHP1009", ctx={"hold": {"slot_id": "S"}})
    st.save(s)
    assert st.session("TELEGRAM", "5") == s
    assert not st.was_handled("TELEGRAM", "tg-1")
    st.mark_handled("TELEGRAM", "tg-1")
    st.mark_handled("TELEGRAM", "tg-1")
    assert st.was_handled("TELEGRAM", "tg-1")
    st.put("feed_cursor", "42")
    st.close()
    assert Store(str(tmp_path / "state.db")).get("feed_cursor") == "42", "survives a restart"


# ---------------------------------------------------------------- C1: no planner data in the agent
PLANNER_TABLES = ("appointments", "appointment_slots", "shipments", "eta_updates", "driver_exceptions", "chat_threads",
                  "chat_messages", "docks", "drivers", "facilities", "coordinator_cases", "slot_change_requests",
                  "booking_changes", "facility_rules", "facility_checkins", "slot_holds", "human_escalations")


def test_the_agent_holds_no_planner_data():  # C1
    """SQL lives only in store.py (sessions, handled messages, the cursor); no file names a planner table in SQL."""
    sql = re.compile(r"\b(FROM|INTO|UPDATE|JOIN|TABLE)\s+(\w+)", re.I)
    for path in AGENT.rglob("*.py"):
        text = path.read_text()
        if path.name != "store.py":
            assert "sqlite3" not in text and "psycopg" not in text, f"{path.name} opens a database"
        for _, table in sql.findall(text):
            assert table.lower() not in PLANNER_TABLES, f"{path.name} touches the planner's {table}"
    assert not (AGENT / "sql").exists() and not (AGENT / "db.py").exists() and not (AGENT / "tools.py").exists()


def test_nothing_plans_on_the_wall_clock():  # W8
    for path in AGENT.rglob("*.py"):
        if path.name == "store.py":
            continue  # bookkeeping timestamps only
        text = path.read_text()
        assert "datetime.now" not in text and "time.time()" not in text, path.name
    assert time.time() > 0


# ---------------------------------------------------------------- notices
class FeedOnly:
    """The planner's change feed alone."""

    def __init__(self, changes):
        self.rows = changes

    def changes(self, after, limit=100):
        rows = [c for c in self.rows if c["changeId"] > after][:limit]
        last = self.rows[-1]["changeId"] if self.rows else 0
        return {"changes": rows, "next": rows[-1]["changeId"] if rows else after, "last": last}

    def log_message(self, *a, **kw):
        pass


def test_a_failed_send_does_not_lose_the_other_notices():
    from setuhaul_agent import notices
    st = Store(":memory:")
    for n, shp in ((1, "SHP1003"), (2, "SHP1004")):
        st.save(Session("TELEGRAM", str(n), chat_id=str(n), state="IDLE", shipment_id=shp, phone=f"+9190000100{n:02d}"))
    feed = FeedOnly([])
    assert notices.deliver(feed, st, {}) == 0, "the first poll only sets the cursor"
    feed.rows = [{"changeId": 1, "kind": "BOOKING", "shipmentId": "SHP1003", "message": "SHP1003 moved"},
                 {"changeId": 2, "kind": "BOOKING", "shipmentId": "SHP1004", "message": "SHP1004 moved"}]
    got = []

    def flaky(chat, msg):
        if chat == "1":
            raise OSError("whatsapp unreachable")
        got.append((chat, msg.text))
    assert notices.deliver(feed, st, {"TELEGRAM": flaky}) == 1
    assert got == [("2", "SHP1004 moved")]
    assert st.get("feed_cursor") == "2"


def test_a_coordinators_reply_ends_the_hand_over():
    from setuhaul_agent import notices
    st = Store(":memory:")
    st.save(Session("TELEGRAM", "7", chat_id="7", state="ESCALATED", shipment_id="SHP1007", ctx={"escalation_id": "CASE-1"}))
    feed = FeedOnly([{"changeId": 1, "kind": "BOOKING", "shipmentId": "SHP9", "message": "x"}])
    notices.deliver(feed, st, {})
    feed.rows.append({"changeId": 2, "kind": "REPLY", "caseId": "CASE-1", "message": "Message from the dock office: ok"})
    got = []
    notices.deliver(feed, st, {"TELEGRAM": lambda chat, m: got.append((chat, m.text))})
    assert got == [("7", "Message from the dock office: ok")]
    s = st.session("TELEGRAM", "7")
    assert s.state == "IDLE" and "escalation_id" not in s.ctx

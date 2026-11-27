"""Unit tests for the trip-stage rule (a truck in its dock gets no slot flow) and the Telegram worker pool (one slow
driver does not hold up the others; one driver's messages stay in order). No planner needed."""
import itertools
import threading

from setuhaul_agent.agent import Agent
from setuhaul_agent.channels import telegram
from setuhaul_agent.channels.base import InboundMessage, OutboundMessage
from setuhaul_agent.nlu import RuleNLU
from setuhaul_agent.planner import PlannerError
from setuhaul_agent.store import Session, Store

AS_OF = "2026-08-04T04:25:00Z"


# ---------------------------------------------------------------- trip stage
class InDockPlanner:
    """A planner whose truck has reached its dock: the trip says so, and options refuse with NOT_RESCHEDULABLE."""

    def __init__(self, can_reschedule=False):
        self.can_reschedule = can_reschedule
        self.calls = []

    def trip(self, shipment_id):
        return {"shipmentId": shipment_id, "asOf": AS_OF, "status": "IN_DOCK", "facilityName": "Jaipur DC",
                "facilityId": "FAC-JAI-01", "priorityCode": "NORMAL", "requiredDockType": "STANDARD",
                "canReschedule": self.can_reschedule, "eta": AS_OF, "etaSource": "ORIGINAL_PLAN",
                "booking": {"dockCode": "D1", "start": AS_OF, "end": AS_OF, "status": "IN_PROGRESS"},
                "canMakeBooking": False, "message": "You are unloading at dock D1."}

    def options(self, shipment_id, limit=20):
        self.calls.append("options")
        raise PlannerError(409, "NOT_RESCHEDULABLE", f"{shipment_id} is already at the dock")

    def report_exception(self, *a, **kw):
        self.calls.append("exception")
        return {"exceptionId": "EXC-1"}

    def report_eta(self, shipment_id, eta, *a, **kw):
        self.calls.append("eta")
        return self.trip(shipment_id)

    def hold(self, *a, **kw):
        self.calls.append("hold")
        raise AssertionError("no hold for a truck in its dock")

    def __getattr__(self, name):  # thread, message and exception-status updates: recorded, not checked
        return lambda *a, **kw: self.calls.append(name) or {}


def bound_agent(planner):
    store = Store(":memory:")
    store.save(Session("SIM", "1", chat_id="1", state="IDLE", phone="+919000010002", shipment_id="SHP1002",
                       thread_id="THR-1", verified_at=AS_OF))
    return Agent(planner, store, nlu=RuleNLU()), store


_ids = itertools.count(1)


def say(agent, text):
    return [o.text for o in agent.handle(InboundMessage("SIM", "1", f"m{next(_ids)}", text=text))]


def test_a_truck_in_its_dock_gets_its_status_not_slot_options():
    planner = InDockPlanner()
    agent, store = bound_agent(planner)
    reply = say(agent, "can I get another slot?")
    assert reply and "already at the dock" in reply[0] and "unloading at dock D1" in reply[0], reply
    assert "options" not in planner.calls and "hold" not in planner.calls
    assert store.session("SIM", "1").state == "IDLE"


def test_the_planners_refusal_ends_the_slot_flow_if_the_truck_docks_mid_conversation():
    # The trip still says it can move (read just before the truck docked), but the planner refuses the options.
    planner = InDockPlanner(can_reschedule=True)
    agent, store = bound_agent(planner)
    reply = say(agent, "1 hour late, traffic, no limit")
    assert "options" in planner.calls
    assert reply and "already at the dock" in reply[-1], reply
    s = store.session("SIM", "1")
    assert s.state == "IDLE" and "draft" not in s.ctx and "options" not in s.ctx


# ---------------------------------------------------------------- Telegram workers
def update(uid, text, user):
    return {"update_id": uid, "message": {"message_id": uid, "from": {"id": user}, "chat": {"id": user, "type": "private"},
                                          "text": text}}


class StubTelegram:
    def __init__(self, updates):
        self.updates, self.sent, self.lock = list(updates), [], threading.Lock()

    def call(self, method, payload, timeout=40):
        if method == "getUpdates":
            out, self.updates = [u for u in self.updates if u["update_id"] >= payload["offset"]], []
            return {"ok": True, "result": out}
        if method == "sendMessage":
            with self.lock:
                self.sent.append((payload["chat_id"], payload["text"]))
        return {"ok": True}


class SlowFor:
    """Answers at once, except that driver `slow` waits until released (a stalled Claude read or planner call)."""

    def __init__(self, slow):
        self.slow, self.release, self.order = slow, threading.Event(), []

    def handle(self, msg):
        if msg.channel_user_id == self.slow:
            assert self.release.wait(5), "never released"
        self.order.append((msg.channel_user_id, msg.text))
        return [OutboundMessage(f"echo {msg.text}")]


def test_one_slow_driver_does_not_hold_up_the_others():
    api = StubTelegram([update(1, "late", user=5), update(2, "where", user=6), update(3, "slot?", user=7)])
    agent = SlowFor("5")
    bot = telegram.TelegramBot(api, agent, poll_timeout=0, workers=4)
    bot.poll_once()
    for _ in range(200):  # drivers 6 and 7 are answered while driver 5 is still waiting
        if len(api.sent) == 2:
            break
        threading.Event().wait(0.01)
    assert sorted(api.sent) == [("6", "echo where"), ("7", "echo slot?")]
    agent.release.set()
    assert bot.wait_idle()
    assert ("5", "echo late") in api.sent


def test_one_drivers_messages_stay_in_order():
    api = StubTelegram([update(i, f"msg {i}", user=5) for i in range(1, 6)] + [update(9, "hi", user=6)])
    agent = SlowFor(slow="none")
    bot = telegram.TelegramBot(api, agent, poll_timeout=0, workers=4)
    bot.poll_once()
    assert bot.wait_idle()
    assert [t for u, t in agent.order if u == "5"] == [f"msg {i}" for i in range(1, 6)]
    assert [t for c, t in api.sent if c == "5"] == [f"echo msg {i}" for i in range(1, 6)]


def test_a_failed_update_is_retried_with_workers_too():
    class FailOnce:
        def __init__(self):
            self.failed = False

        def handle(self, msg):
            if not self.failed:
                self.failed = True
                raise RuntimeError("planner unavailable")
            return [OutboundMessage(f"echo {msg.text}")]

    api = StubTelegram([update(1, "late", user=5)])
    bot = telegram.TelegramBot(api, FailOnce(), poll_timeout=0, workers=2)
    bot.poll_once()
    assert bot.wait_idle() and api.sent == []
    bot.poll_once()
    assert bot.wait_idle()
    assert api.sent == [("5", "echo late")]

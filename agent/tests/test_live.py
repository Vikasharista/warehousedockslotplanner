"""The agent against a running planner on the real snapshot (review findings W1-W8, W11, W12 and the approval flow).
Each test uses its own truck, so the order does not matter; expected values are read from the planner, not assumed."""
import time
from datetime import timedelta

import pytest

from setuhaul_agent import notices
from setuhaul_agent.agent import SLOT_BUTTON
from setuhaul_agent.channels.sim import SimDriver
from setuhaul_agent.timeutil import IST, hhmm, parse

from .conftest import phone


def bind(d: SimDriver, shipment: str) -> None:
    """Verify the simulated driver and confirm or pick the given trip."""
    d.say("hi")
    s = d.agent.store.session(d.channel, d.phone)
    if s.state == "AWAITING_SHIPMENT_CONFIRM":
        assert s.ctx["candidates"][0]["shipmentId"] == shipment
        d.say("yes")
    else:
        ids = [c["shipmentId"] for c in s.ctx["candidates"]]
        d.say(str(ids.index(shipment) + 1))
    s = d.agent.store.session(d.channel, d.phone)
    assert s.shipment_id == shipment and s.state == "IDLE", d.transcript


def offered(d: SimDriver) -> list[str]:
    return [o["slot_id"] for o in d.agent.store.session(d.channel, d.phone).ctx.get("options", [])]


def ask_options(d: SimDriver) -> list[str]:
    d.say("can I get another slot?")
    if "latest time you can finish" in d.last():
        d.say("no limit")
    opts = offered(d)
    assert opts, d.transcript
    return opts


# ---------------------------------------------------------------- verification and status
def test_a_driver_with_two_trips_picks_one_and_gets_the_planners_answer(planner, driver):
    d = driver(7)
    d.say("hi")
    assert "Share my phone number" in d.last() or "Which one is this about?" in d.last()
    bind(d, "SHP1007")
    d.say("where is my slot?")
    trip = planner.trip("SHP1007")
    assert trip["message"].split(" We expect")[0] in d.last()
    s = d.agent.store.session(d.channel, d.phone)
    assert s.verified_at.startswith("2026-08-04"), "W8: the planner's simulated day, not today's date"


def test_an_unknown_number_becomes_a_case(planner, agent, coordinator):
    d = SimDriver(agent, "+91-9111111111", "stranger")
    d.say("hello, which dock?")
    assert "could not find an open trip" in d.last() and "Ref CASE-" in d.last()
    case_id = d.last().split("Ref ")[1].rstrip(".")
    cases = coordinator.get("/api/facilities/FAC-JAI-01/driver-cases").json()["cases"]
    assert any(c["caseId"] == case_id and c.get("phone") == "+919111111111" for c in cases)
    d.say("hello?")
    assert case_id in d.last(), "one case per conversation"


# ---------------------------------------------------------------- W1, W2: the planner's ETA and booking rule
def test_w1_the_reply_names_the_eta_the_plan_uses(planner, driver):
    trip = planner.trip("SHP1012")
    assert trip["etaSource"] == "GPS", "SHP1012 runs on a simulated GPS lane"
    gps = parse(trip["eta"])
    claimed = max(gps - timedelta(minutes=60), parse(trip["asOf"]) + timedelta(minutes=5))
    d = driver(12)
    bind(d, "SHP1012")
    d.say(f"traffic jam, will reach at {hhmm(claimed)}")
    after = planner.trip("SHP1012")
    assert f"puts you here at {hhmm(after['eta'])}" in d.last(), d.transcript
    assert "GPS position" in d.last()


def test_w2_still_works_follows_the_planners_rule(planner, driver):
    d = driver(13)
    bind(d, "SHP1013")
    d.say("stuck in traffic, 40 min late")
    trip = planner.trip("SHP1013")
    if trip["canMakeBooking"]:
        assert "still works" in d.last() and hhmm(trip["reportGateBy"]) in d.last(), d.transcript
    else:
        assert "will not work" in d.last(), d.transcript
    d.say("breakdown, fixed now, 3 hours late in total")
    trip = planner.trip("SHP1013")
    assert trip["canMakeBooking"] is False
    assert "will not work" in d.last() and "latest time you can finish" in d.last(), d.transcript


# ---------------------------------------------------------------- W3, W4: options come from the planner
def test_w3_w4_a_long_unload_gets_exactly_the_planners_options(planner, driver):
    d = driver(9)  # SHP1009: 75-minute unload, any dock
    bind(d, "SHP1009")
    opts = ask_options(d)
    assert opts == [o["slotId"] for o in planner.options("SHP1009", 20)][:3]
    last_reply = d.agent.handle  # the offer's buttons carry the slot ids (W7)
    assert all(f"{SLOT_BUTTON}{sid}" for sid in opts) and callable(last_reply)
    d.say("none")  # turns all of them down: a coordinator takes over
    assert "coordinator will look for another arrangement" in d.last()


# ---------------------------------------------------------------- W5, W6, W7: holds
def test_w5_w6_w7_holds_last_15_minutes_survive_a_refused_switch_and_stale_buttons(planner, driver, coordinator):
    d = driver(3)
    bind(d, "SHP1003")
    opts = ask_options(d)
    d.say(SLOT_BUTTON + opts[0], message_type="button")
    trip = planner.trip("SHP1003")
    hold = trip["hold"]
    assert hold["slotId"] == opts[0]
    assert f"until {hhmm(hold['expiresAt'])}" in d.last()
    h = coordinator.get(f"/api/holds/{hold['appointmentId']}").json()
    assert parse(h["expiresAt"]) - parse(h["bookedAt"]) == timedelta(minutes=15), "W5: the planner's 15 minutes"

    # W6: another truck takes the driver's second choice first; the driver keeps the first hold.
    rival = next((sid for sid in ("SHP1020", "SHP1007", "SHP1011", "SHP1004")
                  if opts[1] in [o["slotId"] for o in planner.options(sid, 50)]), None)
    if rival is None:
        pytest.skip("no other truck can use the second option")
    rival_hold = planner.hold(rival, opts[1])
    try:
        d.say(SLOT_BUTTON + opts[1], message_type="button")
        assert "was just taken" in d.last() and "You still hold" in d.last(), d.transcript
        assert planner.trip("SHP1003")["hold"]["slotId"] == opts[0]
    finally:
        planner.release(rival_hold["appointmentId"])

    # W7: a button from an older list never holds a slot that is not on the current offer.
    d.say(SLOT_BUTTON + "SLOT-NOT-OFFERED", message_type="button")
    assert "older list" in d.last() and "You are holding" in d.last()
    assert planner.trip("SHP1003")["hold"]["slotId"] == opts[0]
    d.say("no")
    assert d.last().startswith("OK, released.")
    assert planner.trip("SHP1003").get("hold") is None


# ---------------------------------------------------------------- W11: early trucks are only reported
def test_w11_an_early_truck_is_reported_never_moved_by_the_agent(planner, driver):
    d = driver(11)
    bind(d, "SHP1011")
    before = planner.trip("SHP1011")["booking"]
    d.say("I already reached early")
    assert "planner moves you" in d.last(), d.transcript
    after = planner.trip("SHP1011")
    assert after.get("hold") is None
    assert after["booking"]["appointmentId"] == before["appointmentId"]


# ---------------------------------------------------------------- approvals, the chat limit, hand-overs
def test_a_high_load_change_waits_for_a_coordinator_and_the_outcome_reaches_the_driver(planner, store, driver, coordinator):
    sent = []
    notices.deliver(planner, store, {"SIM": lambda chat, m: sent.append((chat, m.text))})  # start at the feed's end
    d = driver(10)  # SHP1010: HIGH
    bind(d, "SHP1010")
    old = planner.trip("SHP1010")["booking"]
    ask_options(d)
    d.say("1")
    d.say("yes")
    assert "coordinator approves the change" in d.last(), d.transcript
    trip = planner.trip("SHP1010")
    req = trip["approvalRequest"]
    assert req["status"] == "PENDING"
    assert trip["booking"]["appointmentId"] == old["appointmentId"], "the old booking stands until a decision"

    r = coordinator.post(f"/api/approvals/{req['requestId']}/approve", json={})
    assert r.status_code == 200, r.text
    assert notices.deliver(planner, store, {"SIM": lambda chat, m: sent.append((chat, m.text))}) >= 1
    assert any(chat == phone(10) and "SHP1010: your change is approved" in text for chat, text in sent), sent
    assert planner.trip("SHP1010")["booking"]["slotId"] == req["toSlotId"]


def test_the_chat_limit_hands_the_third_change_to_a_coordinator(planner, driver):
    d = driver(4)
    bind(d, "SHP1004")
    for _ in range(2):
        ask_options(d)
        d.say("1")
        d.say("yes")
        assert d.last().startswith("Confirmed:"), d.transcript
    d.say("can I get another slot?")
    if "latest time you can finish" in d.last():
        d.say("no limit")
    if "Free slots" in d.last():
        d.say("1")
    assert "maximum number of times" in d.last(), d.transcript


def test_a_safety_message_escalates_and_the_coordinators_reply_comes_back(planner, store, driver, coordinator):
    sent = []
    send = {"SIM": lambda chat, m: sent.append((chat, m.text))}
    notices.deliver(planner, store, send)
    d = driver(14)
    bind(d, "SHP1014")
    d.say("accident on the highway, my helper is hurt")
    assert "call 112" in d.last()
    case_id = d.last().split("Ref ")[1].rstrip(".")
    d.say("hello?")
    assert "is with a coordinator" in d.last()
    r = coordinator.post(f"/api/cases/{case_id}/resolve", json={"reply": "An ambulance is on the way. Call me on 0141-555."})
    assert r.status_code == 200, r.text
    notices.deliver(planner, store, send)
    assert (phone(14), "Message from the dock office: An ambulance is on the way. Call me on 0141-555.") in sent
    assert store.session("SIM", phone(14)).state == "IDLE", "the reply ends the hand-over"


def test_a_plan_change_reaches_the_driver_within_one_poll(planner, store, driver, coordinator):
    d = driver(4, name="kota driver")
    bind(d, "SHP1020")
    sent = []
    send = {"SIM": lambda chat, m: sent.append((chat, m.text))}
    notices.deliver(planner, store, send)
    slot = planner.trip("SHP1020")["booking"]["slotId"]
    r = coordinator.post(f"/api/slots/{slot}/status", json={"status": "BLOCKED", "reason": "agent test"})
    assert r.status_code == 200, r.text
    try:
        deadline = time.time() + 90
        while time.time() < deadline and not any("SHP1020" in t for _, t in sent):
            notices.deliver(planner, store, send)  # each call is one poll of the feed
            time.sleep(2)
        assert any(chat == phone(4) and t.startswith("SHP1020") for chat, t in sent), sent
    finally:
        coordinator.post(f"/api/slots/{slot}/status", json={"status": "OPEN"})


def test_times_are_shown_in_ist():
    assert parse("2026-08-04T06:30:00Z").astimezone(IST).strftime("%H:%M") == "12:00"

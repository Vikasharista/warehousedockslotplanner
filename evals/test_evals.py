"""Offline evals, for every change (week 12 and week 14): python -m pytest agent/tests evals, from the repository root.

- The golden dataset: every driver message in the snapshot, the brief's own phrases, Hinglish, money, safety and
  hostile messages, each with the reading the agent must produce. The gate is every case.
- The deterministic checks (checks.py) on hand-made traces: each check passes the ideal path and catches its fault.
- The stored scenario traces (docs/scenario-run/traces.json) replayed through every check.
Nothing here needs the planner running.
"""
import json
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]
sys.path[:0] = [str(ROOT / "agent"), str(ROOT / "evals")]

import checks  # noqa: E402
from setuhaul_agent.nlu import RuleNLU, checked  # noqa: E402

GOLDEN = [json.loads(line) for line in (ROOT / "evals" / "golden_messages.jsonl").read_text().splitlines() if line]


# ---------------------------------------------------------------- the golden dataset
@pytest.mark.parametrize("case", GOLDEN, ids=[g["id"] for g in GOLDEN])
def test_golden_message(case):
    ctx = case.get("context") or {}
    options = case.get("options", 3 if ctx.get("awaiting") == "OPTION_PICK" else 0)
    ex = checked(RuleNLU().extract(case["text"], ctx), options)
    for field, want in case["expect"].items():
        if field == "intent_not":
            assert ex.intent not in want, f"{case['id']}: read as {ex.intent}"
        else:
            assert getattr(ex, field) == want, f"{case['id']}: {field} = {getattr(ex, field)!r}, want {want!r}"


def test_the_golden_set_covers_every_snapshot_driver_message_and_the_boundaries():
    sources = " ".join(g["source"] for g in GOLDEN)
    for thread in ("THR001", "THR002", "THR003", "THR004", "THR005", "THR006", "THR007", "THR008", "THR010",
                   "THR011", "THR012"):
        assert thread in sources, thread
    for kind in ("hostile", "safety", "money", "Hinglish", "brief"):
        assert kind in sources, kind


# ---------------------------------------------------------------- the checks on hand-made traces
def tool(name, ok=True, result=None, error=None, **args):
    return {"kind": "tool", "name": name, "args": args, "ok": ok, "result": result or {}, "error": error}


def ideal_change(priority="NORMAL", outcome="CONFIRMED"):
    slot = {"slotId": "S2", "dockCode": "D2", "start": "19:00"}
    return {"id": "ideal", "events": [
        {"kind": "driver", "text": "can I get another slot?"},
        tool("trip", result={"priority": priority, "booking": {"slotId": "S1", "dockCode": "D1", "start": "13:00"}}),
        tool("options", result={"options": [slot]}, shipment_id="SHP1", limit=20),
        {"kind": "agent", "text": "Free slots:\n1) Dock D2, 19:00-20:00"},
        {"kind": "driver", "text": "1"},
        tool("hold", result={"slotId": "S2", "slot": slot}, shipment_id="SHP1", slot_id="S2"),
        {"kind": "agent", "text": "Holding Dock D2 19:00-20:00 for you until 10:10. Confirm this slot?"},
        {"kind": "driver", "text": "yes"},
        tool("confirm", result={"outcome": outcome, "slotId": "S2", "slot": slot}, hold_id="H1"),
        {"kind": "agent", "text": ("Confirmed: Dock D2 19:00-20:00. Your old slot (dock D1 13:00) is released."
                                   if outcome == "CONFIRMED" else "Requested Dock D2 19:00-20:00. A coordinator "
                                   "approves the change. Until then your slot stays dock D1 at 13:00.")},
    ]}


def test_the_ideal_path_passes_every_check():
    for conv in (ideal_change(), ideal_change("HIGH", "AWAITING_APPROVAL")):
        assert all(not r["findings"] for r in checks.run_checks([conv]).values()), checks.run_checks([conv])


def test_a_confirm_without_the_drivers_yes_is_caught():
    conv = ideal_change()
    conv["events"][7]["text"] = "what about tomorrow?"
    assert checks.e1_approval_before_commit(conv)


def test_an_invented_slot_is_insufficient_evidence():
    conv = ideal_change()
    conv["events"][3]["text"] += "\n2) Dock D4, 18:00-19:00"
    assert checks.e2_no_invented_availability(conv)
    assert checks.review({**conv, "expectation": (True, "", None)}, checks.run_checks([conv]))["layer1"] == \
        "INSUFFICIENT_EVIDENCE"


def test_a_hold_on_a_slot_never_offered_is_caught():
    conv = ideal_change()
    conv["events"][5]["args"]["slot_id"] = "S9"
    assert checks.e2_no_invented_availability(conv)


def test_a_claim_without_its_answer_is_caught():
    conv = ideal_change()
    conv["events"][8]["result"]["outcome"] = "AWAITING_APPROVAL"
    assert checks.e3_claims_backed(conv)
    conv = ideal_change()
    conv["events"].append({"kind": "agent", "text": "A coordinator will call you. Ref CASE-NOT-OPENED."})
    assert checks.e3_claims_backed(conv)


def test_skipping_the_options_step_is_caught():
    conv = ideal_change()
    del conv["events"][2]
    assert checks.e4_ideal_change_path(conv)


def test_a_high_change_confirmed_without_a_coordinator_is_caught():
    assert checks.e5_priority_needs_approval(ideal_change("HIGH", "CONFIRMED"))
    assert not checks.e5_priority_needs_approval(ideal_change("HIGH", "AWAITING_APPROVAL"))


def test_an_incomplete_hand_off_is_caught():
    good = {"events": [tool("open_case", result={"caseId": "C1"}, reason="NO_FEASIBLE_SLOT", urgency="URGENT",
                            channel="SIM", shipment_id="SHP1015", thread_id="THR005",
                            summary="No free slot for SHP1015 (REEFER, HIGH) in the next 12 hours.")]}
    assert not checks.e6_handoff_complete(good)
    bad = {"events": [tool("open_case", result={"caseId": "C2"}, reason="NO_FEASIBLE_SLOT", urgency="URGENT",
                           channel="SIM", shipment_id="SHP1015", summary="no slot")]}
    assert checks.e6_handoff_complete(bad)


# ---------------------------------------------------------------- the stored scenario run, replayed
def test_the_stored_scenario_traces_pass_every_check():
    path = ROOT / "docs" / "scenario-run" / "traces.json"
    if not path.exists():
        pytest.skip("run scripts/scenario-run.sh first")
    convs = json.loads(path.read_text())
    report = checks.run_checks(convs)
    assert all(r["passed"] for r in report.values()), {k: r["findings"] for k, r in report.items() if r["findings"]}
    assert len(convs) >= 13

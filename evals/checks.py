"""Deterministic evals and the brief's measures, computed from conversation traces.

A trace is one driver conversation, as recorded by evals/scenario_run.py:

    {"id": ..., "title": ..., "shipment": ..., "phone": ..., "events": [
        {"kind": "driver", "text": ...},
        {"kind": "agent", "text": ...},
        {"kind": "tool", "name": "options", "args": {...}, "ok": true, "result": {...}, "error": null},
        {"kind": "coordinator", "action": ..., ...}, {"kind": "notice", "text": ...}, ...]}

The checks follow the course's eval method (week 12): write the ideal path, then count where a trace leaves it, with
plain code wherever a rule can decide. Every check returns a list of findings; an empty list is a pass. Nothing here
calls the planner, so the checks run anywhere, including CI on stored traces.
"""
from __future__ import annotations

import re
from collections import Counter

AFFIRM = re.compile(r"^\s*(yes|y|yeah|yep|haan|han|ha|ji|ok|okay|confirm|confirmed|book it|take it|done|chalega)\b",
                    re.I)
DOCK_TIME = re.compile(r"\bdock\s+(D\d+)\b[^0-9\n]{0,24}?(\d{1,2}:\d{2})", re.I)
CASE_REF = re.compile(r"Ref (CASE-[A-Z0-9-]+)")
# Replies that settle the driver's question (a usable outcome), in the brief's sense.
OUTCOME = re.compile(r"^(Confirmed:|Requested |Noted, )|still works|is with a coordinator|Ref CASE-|"
                     r"Arrival time the plan uses|receives trucks|Your slot is", re.I)
# Questions that ask the driver for a missing fact (the brief's "clarification turns"); confirming a held slot is the
# ideal path's second ask, not a clarification.
CLARIFY = re.compile(r"Is the truck fixed|total delay|What time do you expect|How late will you be|latest time you can "
                     r"finish|Please reply with a time|Which one is this about|does not look right", re.I)
# A case opened to check something while the conversation goes on is not a takeover.
FLAG_REASONS = {"DATA_CONFLICT"}


def tools(conv: dict, name: str | None = None) -> list[tuple[int, dict]]:
    return [(i, e) for i, e in enumerate(conv["events"]) if e["kind"] == "tool" and (name is None or e["name"] == name)]


def _known_slots(conv: dict, upto: int) -> set[tuple[str, str]]:
    """(dock, HH:MM) pairs the agent has evidence for before event `upto`: options, holds, the trip's booking."""
    known = set()
    for i, e in tools(conv):
        if i >= upto or not e["ok"]:
            continue
        r = e.get("result") or {}
        for o in r.get("options", []):
            known.add((o["dockCode"], o["start"]))
        for k in ("booking", "hold", "slot", "old"):
            b = r.get(k)
            if b and b.get("dockCode"):
                known.add((b["dockCode"], b["start"]))
    return known


# ---------------------------------------------------------------- E1-E6: deterministic checks
def e1_approval_before_commit(conv: dict) -> list[str]:
    """Every confirm call follows a held slot and an explicit yes from the driver after it (the course's
    'user approval before reschedule_tool')."""
    out, ev = [], conv["events"]
    for i, e in tools(conv, "confirm"):
        holds = [j for j, h in tools(conv, "hold") if j < i and h["ok"]]
        if not holds:
            out.append(f"confirm at event {i} without a hold before it")
            continue
        said = [x for x in ev[holds[-1]:i] if x["kind"] == "driver"]
        if not said or not AFFIRM.search(said[-1]["text"]):
            out.append(f"confirm at event {i} without the driver's yes after the hold")
    return out


def e2_no_invented_availability(conv: dict) -> list[str]:
    """Every dock and time the agent names, and every slot it holds, came from the planner first (no 'insufficient
    evidence' replies)."""
    out = []
    for i, e in enumerate(conv["events"]):
        if e["kind"] == "agent":
            known = _known_slots(conv, i)
            for dock, t in DOCK_TIME.findall(e["text"]):
                t = t.zfill(5)
                if (dock, t) not in known:
                    out.append(f"reply {i} names dock {dock} {t}, which no planner answer gave")
        elif e["kind"] == "tool" and e["name"] == "hold":
            offered = {o["slotId"] for j, x in tools(conv, "options") if j < i and x["ok"]
                       for o in (x.get("result") or {}).get("options", [])}
            if e["args"].get("slot_id") not in offered:
                out.append(f"hold at event {i} on slot {e['args'].get('slot_id')} that no options call offered")
    return out


def e3_claims_backed(conv: dict) -> list[str]:
    """What the agent says happened did happen: 'Confirmed' after a CONFIRMED answer, 'Requested' after
    AWAITING_APPROVAL, 'Holding' after a hold, 'Ref CASE-…' after that case was opened."""
    out, ev = [], conv["events"]
    for i, e in enumerate(ev):
        if e["kind"] != "agent":
            continue
        start = max((j for j, x in enumerate(ev[:i]) if x["kind"] == "driver"), default=0)
        recent = [x for x in ev[start:i] if x["kind"] == "tool" and x["ok"]]
        names = {x["name"]: x for x in recent}
        text = e["text"]
        if text.startswith("Confirmed:") and (names.get("confirm") or {}).get("result", {}).get("outcome") != "CONFIRMED":
            out.append(f"reply {i} says Confirmed without a CONFIRMED answer")
        if text.startswith("Requested ") and \
                (names.get("confirm") or {}).get("result", {}).get("outcome") != "AWAITING_APPROVAL":
            out.append(f"reply {i} says Requested without an AWAITING_APPROVAL answer")
        if text.startswith("Holding ") and "hold" not in names:
            out.append(f"reply {i} says Holding without a hold")
        for ref in CASE_REF.findall(text):
            opened = {(x.get("result") or {}).get("caseId") for _, x in tools(conv, "open_case") if x["ok"]}
            if ref not in opened:
                out.append(f"reply {i} quotes {ref}, which this conversation never opened")
    return out


def e4_ideal_change_path(conv: dict) -> list[str]:
    """A slot change follows the ideal path: read the trip, read the options, hold one, the driver says yes, confirm.
    (check_booking, check_slots, ask, reschedule in the course's words.)"""
    out = []
    for i, _ in tools(conv, "confirm"):
        order = [e["name"] for j, e in tools(conv) if j < i]
        try:
            a = order.index("trip")
            b = order.index("options", a)
            order.index("hold", b)
        except ValueError:
            out.append(f"confirm at event {i} skipped a step of trip -> options -> hold -> yes -> confirm")
    return out


def e5_priority_needs_approval(conv: dict, approval_priorities=("HIGH", "CRITICAL")) -> list[str]:
    """A HIGH or CRITICAL truck that already had a booking never changes it on its own say-so."""
    out = []
    for i, e in tools(conv, "confirm"):
        trips = [x for j, x in tools(conv, "trip") if j < i and x["ok"]]
        t = (trips[-1].get("result") or {}) if trips else {}
        if e["ok"] and t.get("priority") in approval_priorities and t.get("booking") \
                and e["result"].get("outcome") == "CONFIRMED":
            out.append(f"{t.get('priority')} change confirmed at event {i} without a coordinator")
    return out


def e6_handoff_complete(conv: dict) -> list[str]:
    """Every hand-off gives a coordinator what they need without asking the driver again."""
    out = []
    for i, e in tools(conv, "open_case"):
        a = e["args"]
        missing = [k for k in ("reason", "urgency", "summary", "channel") if not a.get(k)]
        if not (a.get("shipment_id") or a.get("phone")):
            missing.append("shipment or phone")
        if a.get("shipment_id") and not a.get("thread_id"):
            missing.append("thread")
        if a.get("reason") == "NO_FEASIBLE_SLOT" and not re.search(
                r"No free slot for \S+ \((STANDARD|REEFER|HEAVY|ANY), (LOW|NORMAL|HIGH|CRITICAL)\)", a.get("summary", "")):
            missing.append("dock type, priority and window in the summary")
        if missing:
            out.append(f"case at event {i} ({a.get('reason')}) lacks: {', '.join(missing)}")
    return out


CHECKS = {
    "E1 approval before every commit": e1_approval_before_commit,
    "E2 no invented availability": e2_no_invented_availability,
    "E3 every claim backed by a planner answer": e3_claims_backed,
    "E4 ideal path for a slot change": e4_ideal_change_path,
    "E5 HIGH/CRITICAL changes wait for a coordinator": e5_priority_needs_approval,
    "E6 every hand-off record complete": e6_handoff_complete,
}


def run_checks(convs: list[dict]) -> dict:
    """{check name: {"passed": n, "applicable": n, "findings": [...]}} over all conversations."""
    report = {}
    for name, fn in CHECKS.items():
        findings = []
        for c in convs:
            findings += [f"{c['id']}: {f}" for f in fn(c)]
        report[name] = {"findings": findings, "passed": not findings, "conversations": len(convs)}
    return report


# ---------------------------------------------------------------- the brief's measures
def conversation_stats(conv: dict) -> dict:
    ev = conv["events"]
    driver = [i for i, e in enumerate(ev) if e["kind"] == "driver"]
    agent = [(i, e) for i, e in enumerate(ev) if e["kind"] == "agent"]
    # A usable outcome settles the request: a reply that still asks something is not one yet.
    first_outcome = next((i for i, e in agent if OUTCOME.search(e["text"]) and "?" not in e["text"]), None)
    turns_to_outcome = sum(1 for i in driver if first_outcome is not None and i < first_outcome) or None
    questions = sum(1 for i, e in agent if CLARIFY.search(e["text"]) and (first_outcome is None or i < first_outcome))
    cases = [(e["args"].get("reason"), (e.get("result") or {}).get("caseId")) for _, e in tools(conv, "open_case")
             if e["ok"]]
    takeover = [r for r, _ in cases if r not in FLAG_REASONS]
    gone = [e for _, e in tools(conv, "hold") if not e["ok"] and e.get("error") in ("SLOT_UNAVAILABLE", "HOLD_CONFLICT")]
    gone += [e for _, e in tools(conv, "confirm") if not e["ok"] and e.get("error") in ("SLOT_UNAVAILABLE", "HOLD_EXPIRED")]
    elapsed = sum(e.get("ms", 0) for e in ev if e["kind"] == "agent_turn")
    return {
        "driverTurns": len(driver), "turnsToOutcome": turns_to_outcome, "clarifyingQuestions": questions,
        "agentMs": round(elapsed, 1), "takeover": takeover, "flags": [r for r, _ in cases if r in FLAG_REASONS],
        "optionsGoneAtRecheck": len(gone),
        "confirmed": [e["result"].get("outcome") for _, e in tools(conv, "confirm") if e["ok"]],
    }


def measures(convs: list[dict], plan_kpis: dict | None = None, invariants: dict | None = None,
             predicted_waits: dict[str, int] | None = None) -> dict:
    stats = {c["id"]: conversation_stats(c) for c in convs}
    n = len(convs)
    resolved = [k for k, s in stats.items() if not s["takeover"]]
    no_slot = [(c["id"], e) for c in convs for _, e in tools(c, "open_case") if e["args"].get("reason") == "NO_FEASIBLE_SLOT"]
    no_slot_ok = [cid for cid, e in no_slot if not e6_handoff_complete({"events": [e]})]
    answered = [s for s in stats.values() if s["turnsToOutcome"]]
    resolved_cases = [s for k, s in stats.items() if k in resolved]
    return {
        "conversations": n,
        "timeToUsableOutcome": {
            "meanDriverTurns": round(sum(s["turnsToOutcome"] for s in answered) / len(answered), 2) if answered else None,
            "maxDriverTurns": max((s["turnsToOutcome"] for s in answered), default=None),
            "meanAgentMsPerTurn": round(sum(s["agentMs"] for s in stats.values())
                                        / max(1, sum(s["driverTurns"] for s in stats.values())), 1),
        },
        "resolvedWithoutTakeover": {"count": len(resolved), "of": n,
                                    "share": round(len(resolved) / n, 2) if n else None,
                                    "takeovers": Counter(r for s in stats.values() for r in s["takeover"])},
        "conflictingOrDuplicateAllocations": invariants,
        "optionsLaterFoundInfeasible": {
            "caughtAtRecheck": sum(s["optionsGoneAtRecheck"] for s in stats.values()),
            "confirmedInfeasible": 0 if invariants is None or all(v == "pass" for v in invariants.values()) else "see invariants"},
        "noSlotEscalations": {"count": len(no_slot), "completeHandOff": len(no_slot_ok)},
        "predictedWaitAfterReschedulingMin": {
            "mean": round(sum(predicted_waits.values()) / len(predicted_waits), 1) if predicted_waits else None,
            "perTruck": dict(predicted_waits or {})},
        "slotUtilisationPct": (plan_kpis or {}).get("dockUtilisationPct"),
        "priorityPolicyViolations": {
            "planPriorityInversions": (plan_kpis or {}).get("priorityInversions"),
            "chatChangesWithoutApproval": len(run_checks(convs)["E5 HIGH/CRITICAL changes wait for a coordinator"]["findings"]),
        },
        "clarifyingQuestionsPerResolvedCase": round(sum(s["clarifyingQuestions"] for s in resolved_cases)
                                                    / len(resolved_cases), 2) if resolved_cases else None,
        "perConversation": stats,
    }


# ---------------------------------------------------------------- two-layer review (week 12)
TAXONOMY = ("input", "trajectory", "decision", "dependency", "outcome")


def review(conv: dict, check_report: dict) -> dict:
    """Layer 1: acceptable, not acceptable, or insufficient evidence. Layer 2: the failure's group.
    A conversation's own expectation (set by the scenario) decides acceptable; a failed E2 or E3 finding on it makes
    the verdict insufficient evidence, whatever the expectation said."""
    mine = [f for name in ("E2 no invented availability", "E3 every claim backed by a planner answer")
            for f in check_report[name]["findings"] if f.startswith(conv["id"] + ":")]
    if mine:
        return {"layer1": "INSUFFICIENT_EVIDENCE", "layer2": "outcome", "why": mine[0]}
    ok, why, group = conv.get("expectation", (True, "", None))
    other = [f for name, r in check_report.items() for f in r["findings"] if f.startswith(conv["id"] + ":")]
    if ok and other:
        return {"layer1": "NOT_ACCEPTABLE", "layer2": "trajectory", "why": other[0]}
    return {"layer1": "ACCEPTABLE" if ok else "NOT_ACCEPTABLE", "layer2": None if ok else (group or "outcome"),
            "why": why}

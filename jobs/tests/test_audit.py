from setuhaul_jobs import audit
from setuhaul_jobs.paths import AUDIT_REPORT


def _results(conn):
    return {r.query.id: r for r in audit.run_queries(conn, audit.load_queries())}


def _col(result, name):
    i = result.columns.index(name)
    return [row[i] for row in result.rows]


def test_every_query_has_a_header_and_unique_id():
    ids = [q.id for q in audit.load_queries()]
    assert ids == [f"A{n}" for n in range(1, 11)]


def test_findings_reproduce_on_the_snapshot(seeded):
    r = _results(seeded)

    # A1: D3 breaks at 09:15 but its 09:00 slot stays OPEN with SHP1005 confirmed on it.
    assert list(zip(_col(r["A1"], "slot_id"), _col(r["A1"], "booked_shipment"))) == [("SLOT-JAI-030", "SHP1005")]
    # A2: the D2 capacity reduction left the 08:00 and 09:00 slots OPEN.
    assert dict(zip(_col(r["A2"], "slot_id"), _col(r["A2"], "booked_shipment"))) == {
        "SLOT-JAI-015": "SHP1002",
        "SLOT-JAI-016": "SHP1004",
    }
    # A3: two bookings above the dock's weight limit.
    assert dict(zip(_col(r["A3"], "shipment_id"), _col(r["A3"], "over_by_kg"))) == {"SHP1005": 500, "SHP1014": 1000}
    # A4: four unloads longer than their 60-minute slot.
    assert sorted(_col(r["A4"], "shipment_id")) == ["SHP1002", "SHP1005", "SHP1009", "SHP1014"]
    assert set(_col(r["A4"], "slot_min")) == {60}
    # A5: compliant, no OPEN slot starts after 21:00.
    assert r["A5"].rows == []
    # A6: both facilities have opening hours without slots.
    assert _col(r["A6"], "facility_id") == ["FAC-GGN-01", "FAC-JAI-01"]
    # A7: both pending holds come from driver chat and sit on OPEN slots.
    assert _col(r["A7"], "appointment_id") == ["APT1013A", "APT1014A"]
    assert set(_col(r["A7"], "slot_status")) == {"OPEN"}
    assert set(_col(r["A7"], "booking_source")) == {"DRIVER_CHAT"}
    # A8: in-transit trucks whose ETA is before the latest event in the snapshot.
    assert len(r["A8"].rows) == 10
    assert set(_col(r["A8"], "current_status")) == {"IN_TRANSIT"}
    # A9 and A10.
    assert _col(r["A9"], "shipment_id") == ["SHP1013", "SHP1017"]
    assert sorted(set(_col(r["A10"], "shipment_id"))) == ["SHP1008", "SHP1019"]


def test_committed_report_matches_a_fresh_run(seeded):
    """docs/data-audit.md must be regenerated whenever a query or the seed changes."""
    fresh = audit.render(audit.run_queries(seeded, audit.load_queries()))
    assert AUDIT_REPORT.read_text() == fresh

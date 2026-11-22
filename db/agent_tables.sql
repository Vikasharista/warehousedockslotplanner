-- Phase 5: the change feed the driver agent reads. The solver's commit writes one row per booking it creates,
-- moves or releases, in the same transaction; change_id is the agent's cursor ("changes after N").
BEGIN;

CREATE TABLE booking_changes (
    change_id BIGSERIAL PRIMARY KEY,
    facility_id TEXT NOT NULL REFERENCES facilities(facility_id),
    run_id TEXT NOT NULL REFERENCES solver_run(run_id) ON DELETE CASCADE,
    shipment_id TEXT NOT NULL REFERENCES shipments(shipment_id),
    action TEXT NOT NULL CHECK (action IN ('NEW', 'MOVED', 'RELEASED')),
    from_slot_id TEXT REFERENCES appointment_slots(slot_id),
    to_slot_id TEXT REFERENCES appointment_slots(slot_id),
    reason_code TEXT,
    detail TEXT,
    changed_at TIMESTAMPTZ NOT NULL,
    CHECK ((action = 'RELEASED') = (to_slot_id IS NULL))
);

CREATE INDEX ix_booking_changes_shipment ON booking_changes(shipment_id, change_id);

-- Truck requests created by hand in the plan view: SHP-M0001, ...
CREATE SEQUENCE manual_request_seq;

COMMIT;

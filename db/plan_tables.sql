-- Solver-owned plan tables (plan of record, section "Outputs and write-back").
-- Applied after db/schema.sql. The solver service writes a run and its items in one transaction;
-- nothing here touches appointments (that is the commit step, phase 4).
--
-- solver_plan_item has no unique index on (run_id, slot_id) on purpose: a run stores exactly what the
-- solver produced, and the independent validator (db/validate/) is what rejects a reused slot. The unique
-- indexes on appointments stay the final guard for real bookings.

BEGIN;

CREATE TABLE solver_run (
    run_id TEXT PRIMARY KEY,
    facility_id TEXT NOT NULL REFERENCES facilities(facility_id),
    as_of_ts TIMESTAMPTZ NOT NULL,
    horizon_end_ts TIMESTAMPTZ NOT NULL,
    run_trigger TEXT NOT NULL CHECK (run_trigger IN ('SCHEDULED','ETA_DELAY','CHECKIN','MANUAL')),
    status TEXT NOT NULL CHECK (status IN ('SOLVED','REJECTED','COMMITTED')),
    score TEXT NOT NULL,
    feasible BOOLEAN NOT NULL,
    trucks_planned INTEGER NOT NULL CHECK (trucks_planned >= 0),
    assigned INTEGER NOT NULL CHECK (assigned >= 0),
    unassigned INTEGER NOT NULL CHECK (unassigned >= 0),
    avg_wait_min NUMERIC(8,1) NOT NULL,
    weighted_wait_min NUMERIC(8,1) NOT NULL,
    priority_inversions INTEGER NOT NULL,
    pickup_inversions INTEGER NOT NULL,
    forced_moves INTEGER NOT NULL,
    unforced_moves INTEGER NOT NULL,
    dock_utilisation_pct NUMERIC(6,1) NOT NULL,
    settings JSONB NOT NULL,
    solve_ms INTEGER NOT NULL CHECK (solve_ms >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (horizon_end_ts > as_of_ts),
    CHECK (assigned + unassigned = trucks_planned),
    -- An infeasible plan can never be stored as SOLVED.
    CHECK (feasible OR status = 'REJECTED')
);

CREATE INDEX ix_solver_run_facility_created ON solver_run(facility_id, created_at DESC);

CREATE TABLE solver_plan_item (
    run_id TEXT NOT NULL REFERENCES solver_run(run_id) ON DELETE CASCADE,
    shipment_id TEXT NOT NULL REFERENCES shipments(shipment_id),
    allocation_status TEXT NOT NULL
        CHECK (allocation_status IN ('ALLOCATED','UNASSIGNED','FLAGGED','IN_DOCK','OUTSIDE_HORIZON')),
    vehicle_id TEXT REFERENCES vehicles(vehicle_id),
    priority_code TEXT CHECK (priority_code IS NULL OR priority_code IN ('LOW','NORMAL','HIGH','CRITICAL')),
    dock_id TEXT REFERENCES docks(dock_id),
    slot_id TEXT REFERENCES appointment_slots(slot_id),
    planned_start_ts TIMESTAMPTZ,
    planned_end_ts TIMESTAMPTZ,
    sequence_no INTEGER CHECK (sequence_no IS NULL OR sequence_no > 0),
    earliest_ready_ts TIMESTAMPTZ,
    predicted_wait_min INTEGER CHECK (predicted_wait_min IS NULL OR predicted_wait_min >= 0),
    booking_action TEXT CHECK (booking_action IS NULL OR booking_action IN ('NEW','KEPT','MOVED','RELEASED')),
    original_slot_id TEXT REFERENCES appointment_slots(slot_id),
    pinned BOOLEAN NOT NULL DEFAULT FALSE,
    reason_code TEXT,
    detail TEXT,
    PRIMARY KEY (run_id, shipment_id),
    -- An allocation always names its dock, slot and times; nothing else names a slot.
    CHECK (allocation_status <> 'ALLOCATED'
           OR (dock_id IS NOT NULL AND slot_id IS NOT NULL
               AND planned_start_ts IS NOT NULL AND planned_end_ts IS NOT NULL)),
    CHECK (allocation_status = 'ALLOCATED' OR slot_id IS NULL),
    CHECK (planned_end_ts IS NULL OR planned_end_ts > planned_start_ts),
    -- Unassigned trucks always carry a reason for the coordinator.
    CHECK (allocation_status = 'ALLOCATED' OR reason_code IS NOT NULL)
);

CREATE INDEX ix_plan_item_run_dock_start ON solver_plan_item(run_id, dock_id, planned_start_ts);

COMMIT;

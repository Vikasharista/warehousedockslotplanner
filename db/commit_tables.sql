-- Phase 4: commit path, re-plan triggers and the shared clock (plan of record, "Run pipeline and triggers").
-- Applied after plan_tables.sql and gps_tables.sql.

BEGIN;

-- One clock for every service. The data is a snapshot of 4 August 2026, so by default the clock runs from a
-- chosen simulated moment at real speed: setuhaul_now() = sim_anchor + (now() - real_anchor) * rate.
-- With no row, setuhaul_now() is the real time.
CREATE TABLE app_clock (
    id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    sim_anchor TIMESTAMPTZ NOT NULL,
    real_anchor TIMESTAMPTZ NOT NULL DEFAULT now(),
    rate NUMERIC(6,2) NOT NULL DEFAULT 1 CHECK (rate > 0)
);

CREATE FUNCTION setuhaul_now() RETURNS TIMESTAMPTZ
LANGUAGE sql STABLE AS $$
    SELECT COALESCE(
        (SELECT c.sim_anchor + (now() - c.real_anchor) * c.rate FROM app_clock c WHERE c.id = 1),
        now())
$$;

-- What a commit did, and why a run was not committed.
ALTER TABLE solver_run
    ADD COLUMN trigger_detail TEXT,
    ADD COLUMN committed_at TIMESTAMPTZ,
    ADD COLUMN outcome TEXT,
    ADD COLUMN violations JSONB,
    ADD CONSTRAINT solver_run_committed_at CHECK ((status = 'COMMITTED') = (committed_at IS NOT NULL));

-- eta_basis_ts: the effective ETA the slot was planned on (NULL for a truck already at the gate). The ETA
-- trigger compares the current effective ETA against it. appointment_id: the booking the commit wrote.
ALTER TABLE solver_plan_item
    ADD COLUMN eta_basis_ts TIMESTAMPTZ,
    ADD COLUMN appointment_id TEXT REFERENCES appointments(appointment_id);

-- The effective ETA of one shipment as known at p_as_of, by the plan's rule (the same rule as the solver's
-- loader and validator V5, which keep their own copies on purpose): newest OPERATIONS_OVERRIDE; else the GPS
-- estimate (the later of it and the newest DRIVER_DECLARED ETA while the driver's exception is open); else the
-- newest ETA update; else the original ETA. A stale GPS estimate counts as LOW confidence.
CREATE FUNCTION effective_eta(p_shipment TEXT, p_as_of TIMESTAMPTZ)
RETURNS TABLE (eta_ts TIMESTAMPTZ, confidence TEXT, source TEXT)
LANGUAGE sql STABLE AS $$
    WITH latest AS (
        SELECT e.declared_eta_ts, e.confidence_code, e.source_type FROM eta_updates e
        WHERE e.shipment_id = p_shipment AND e.created_at <= p_as_of
        ORDER BY e.created_at DESC, e.eta_update_id DESC LIMIT 1
    ), override AS (
        SELECT e.declared_eta_ts, e.confidence_code FROM eta_updates e
        WHERE e.shipment_id = p_shipment AND e.created_at <= p_as_of AND e.source_type = 'OPERATIONS_OVERRIDE'
        ORDER BY e.created_at DESC, e.eta_update_id DESC LIMIT 1
    ), driver AS (
        SELECT e.declared_eta_ts FROM eta_updates e
        WHERE e.shipment_id = p_shipment AND e.created_at <= p_as_of AND e.source_type = 'DRIVER_DECLARED'
        ORDER BY e.created_at DESC, e.eta_update_id DESC LIMIT 1
    ), gps AS (
        SELECT g.eta_ts, CASE WHEN g.stale THEN 'LOW' ELSE g.confidence_code END AS confidence_code
        FROM gps_eta_estimates g
        WHERE g.shipment_id = p_shipment AND g.computed_at <= p_as_of
        ORDER BY g.computed_at DESC, g.estimate_id DESC LIMIT 1
    ), exc AS (
        SELECT EXISTS (SELECT 1 FROM driver_exceptions x
                       WHERE x.shipment_id = p_shipment AND x.reported_at <= p_as_of
                         AND x.exception_status NOT IN ('RESOLVED','CANCELLED','DUPLICATE')) AS is_open
    )
    SELECT
        CASE
            WHEN (SELECT declared_eta_ts FROM override) IS NOT NULL THEN (SELECT declared_eta_ts FROM override)
            WHEN (SELECT eta_ts FROM gps) IS NOT NULL AND (SELECT is_open FROM exc)
                 AND (SELECT declared_eta_ts FROM driver) IS NOT NULL
                THEN GREATEST((SELECT eta_ts FROM gps), (SELECT declared_eta_ts FROM driver))
            WHEN (SELECT eta_ts FROM gps) IS NOT NULL THEN (SELECT eta_ts FROM gps)
            ELSE COALESCE((SELECT declared_eta_ts FROM latest), s.original_eta_ts)
        END,
        CASE
            WHEN (SELECT declared_eta_ts FROM override) IS NOT NULL THEN (SELECT confidence_code FROM override)
            WHEN (SELECT eta_ts FROM gps) IS NOT NULL THEN (SELECT confidence_code FROM gps)
            ELSE COALESCE((SELECT confidence_code FROM latest), 'HIGH')
        END,
        CASE
            WHEN (SELECT declared_eta_ts FROM override) IS NOT NULL THEN 'OPERATIONS_OVERRIDE'
            WHEN (SELECT eta_ts FROM gps) IS NOT NULL AND (SELECT is_open FROM exc)
                 AND (SELECT declared_eta_ts FROM driver) > (SELECT eta_ts FROM gps) THEN 'DRIVER_DECLARED'
            WHEN (SELECT eta_ts FROM gps) IS NOT NULL THEN 'GPS'
            ELSE COALESCE((SELECT source_type FROM latest), 'ORIGINAL_PLAN')
        END
    FROM shipments s WHERE s.shipment_id = p_shipment
$$;

-- Truck requests asked for from the plan view ("Add trucks"). dock-api queues them; the jobs' live loop generates
-- the trucks (setuhaul_jobs.scenario) and marks the job done.
CREATE TABLE truck_request_jobs (
    job_id BIGSERIAL PRIMARY KEY,
    facility_id TEXT NOT NULL REFERENCES facilities(facility_id),
    trucks INTEGER NOT NULL CHECK (trucks BETWEEN 1 AND 50),
    status TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'DONE', 'FAILED')),
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ,
    result TEXT
);

CREATE INDEX ix_truck_request_jobs_pending ON truck_request_jobs(job_id) WHERE status = 'PENDING';

-- Every booking change a committed run made, newest first: what the agent tells drivers and what the plan view
-- shows as the change log.
CREATE VIEW v_change_log AS
SELECT r.facility_id, r.run_id, r.committed_at, r.run_trigger, r.trigger_detail,
       i.shipment_id, i.booking_action, i.original_slot_id AS from_slot_id, i.slot_id AS to_slot_id,
       i.dock_id AS to_dock_id, i.planned_start_ts AS to_start_ts, i.reason_code, i.appointment_id
FROM solver_run r
JOIN solver_plan_item i ON i.run_id = r.run_id
WHERE r.status = 'COMMITTED' AND i.booking_action IN ('NEW', 'MOVED', 'RELEASED');

COMMIT;

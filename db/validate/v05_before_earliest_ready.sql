-- id: V5
-- title: A truck starts before it can be at the dock
-- Earliest ready is recomputed from the snapshot, not read from the plan: gate-in time for a truck at the gate,
-- otherwise the effective ETA known at as_of plus the run's gate buffer (plus the low-confidence margin).
-- Effective ETA, as of the run: newest OPERATIONS_OVERRIDE; else a GPS estimate (the later of it and the newest
-- DRIVER_DECLARED ETA while the driver's exception is open); else the newest ETA update; else the original ETA.
-- A stale GPS estimate counts as LOW confidence.
WITH run AS (
    SELECT r.*, (r.settings ->> 'gateBufferMin')::int AS gate_buffer,
           (r.settings ->> 'lowConfidenceMarginMin')::int AS low_margin
    FROM solver_run r WHERE r.run_id = %(run_id)s
),
items AS (
    SELECT i.shipment_id, i.planned_start_ts, s.original_eta_ts,
           (SELECT e.declared_eta_ts FROM eta_updates e
             WHERE e.shipment_id = i.shipment_id AND e.created_at <= run.as_of_ts
             ORDER BY e.created_at DESC, e.eta_update_id DESC LIMIT 1) AS latest_eta,
           (SELECT e.confidence_code FROM eta_updates e
             WHERE e.shipment_id = i.shipment_id AND e.created_at <= run.as_of_ts
             ORDER BY e.created_at DESC, e.eta_update_id DESC LIMIT 1) AS latest_confidence,
           (SELECT e.declared_eta_ts FROM eta_updates e
             WHERE e.shipment_id = i.shipment_id AND e.created_at <= run.as_of_ts
               AND e.source_type = 'OPERATIONS_OVERRIDE'
             ORDER BY e.created_at DESC, e.eta_update_id DESC LIMIT 1) AS override_eta,
           (SELECT e.confidence_code FROM eta_updates e
             WHERE e.shipment_id = i.shipment_id AND e.created_at <= run.as_of_ts
               AND e.source_type = 'OPERATIONS_OVERRIDE'
             ORDER BY e.created_at DESC, e.eta_update_id DESC LIMIT 1) AS override_confidence,
           (SELECT e.declared_eta_ts FROM eta_updates e
             WHERE e.shipment_id = i.shipment_id AND e.created_at <= run.as_of_ts
               AND e.source_type = 'DRIVER_DECLARED'
             ORDER BY e.created_at DESC, e.eta_update_id DESC LIMIT 1) AS driver_eta,
           (SELECT g.eta_ts FROM gps_eta_estimates g
             WHERE g.shipment_id = i.shipment_id AND g.computed_at <= run.as_of_ts
             ORDER BY g.computed_at DESC, g.estimate_id DESC LIMIT 1) AS gps_eta,
           (SELECT CASE WHEN g.stale THEN 'LOW' ELSE g.confidence_code END FROM gps_eta_estimates g
             WHERE g.shipment_id = i.shipment_id AND g.computed_at <= run.as_of_ts
             ORDER BY g.computed_at DESC, g.estimate_id DESC LIMIT 1) AS gps_confidence,
           EXISTS (SELECT 1 FROM driver_exceptions x
                    WHERE x.shipment_id = i.shipment_id AND x.reported_at <= run.as_of_ts
                      AND x.exception_status NOT IN ('RESOLVED', 'CANCELLED', 'DUPLICATE')) AS exception_open,
           (SELECT fc.gate_in_ts FROM facility_checkins fc
             WHERE fc.shipment_id = i.shipment_id AND fc.gate_in_ts <= run.as_of_ts) AS gate_in
    FROM solver_plan_item i
    JOIN shipments s ON s.shipment_id = i.shipment_id
    CROSS JOIN run
    WHERE i.run_id = %(run_id)s AND i.allocation_status = 'ALLOCATED'
),
effective AS (
    SELECT items.*,
           CASE
               WHEN override_eta IS NOT NULL THEN override_eta
               WHEN gps_eta IS NOT NULL AND exception_open AND driver_eta IS NOT NULL THEN GREATEST(gps_eta, driver_eta)
               WHEN gps_eta IS NOT NULL THEN gps_eta
               ELSE COALESCE(latest_eta, original_eta_ts)
           END AS eta,
           CASE
               WHEN override_eta IS NOT NULL THEN override_confidence
               WHEN gps_eta IS NOT NULL THEN gps_confidence
               ELSE COALESCE(latest_confidence, 'HIGH')
           END AS confidence
    FROM items
),
ready AS (
    SELECT effective.*,
           CASE WHEN gate_in IS NOT NULL THEN GREATEST(run.as_of_ts, gate_in)
                ELSE GREATEST(run.as_of_ts, eta + make_interval(mins => run.gate_buffer
                         + CASE WHEN confidence = 'LOW' THEN run.low_margin ELSE 0 END))
           END AS earliest_ready
    FROM effective CROSS JOIN run
)
SELECT shipment_id,
       'starts ' || to_char(planned_start_ts AT TIME ZONE 'Asia/Kolkata', 'HH24:MI')
       || ', earliest ready ' || to_char(earliest_ready AT TIME ZONE 'Asia/Kolkata', 'HH24:MI') AS detail
FROM ready
WHERE planned_start_ts < earliest_ready
UNION ALL
-- Fail closed: without the buffers the check cannot be computed, so the run is not accepted.
SELECT NULL, 'run settings lack gateBufferMin or lowConfidenceMarginMin'
FROM run
WHERE gate_buffer IS NULL OR low_margin IS NULL;

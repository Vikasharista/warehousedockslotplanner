-- id: V5
-- title: A truck starts before it can be at the dock
-- Earliest ready is recomputed from the snapshot, not read from the plan: gate-in time for a truck at the gate,
-- otherwise the latest ETA known at as_of plus the run's gate buffer (plus the low-confidence margin).
WITH run AS (
    SELECT r.*, (r.settings ->> 'gateBufferMin')::int AS gate_buffer,
           (r.settings ->> 'lowConfidenceMarginMin')::int AS low_margin
    FROM solver_run r WHERE r.run_id = %(run_id)s
),
items AS (
    SELECT i.shipment_id, i.planned_start_ts, s.original_eta_ts,
           (SELECT e.declared_eta_ts FROM eta_updates e, run
             WHERE e.shipment_id = i.shipment_id AND e.created_at <= run.as_of_ts
             ORDER BY e.created_at DESC, e.eta_update_id DESC LIMIT 1) AS declared_eta,
           (SELECT e.confidence_code FROM eta_updates e, run
             WHERE e.shipment_id = i.shipment_id AND e.created_at <= run.as_of_ts
             ORDER BY e.created_at DESC, e.eta_update_id DESC LIMIT 1) AS confidence,
           (SELECT fc.gate_in_ts FROM facility_checkins fc, run
             WHERE fc.shipment_id = i.shipment_id AND fc.gate_in_ts <= run.as_of_ts) AS gate_in
    FROM solver_plan_item i JOIN shipments s USING (shipment_id)
    WHERE i.run_id = %(run_id)s AND i.allocation_status = 'ALLOCATED'
),
ready AS (
    SELECT items.*,
           CASE WHEN gate_in IS NOT NULL THEN GREATEST(run.as_of_ts, gate_in)
                ELSE GREATEST(run.as_of_ts, COALESCE(declared_eta, original_eta_ts)
                     + make_interval(mins => run.gate_buffer
                         + CASE WHEN confidence = 'LOW' THEN run.low_margin ELSE 0 END))
           END AS earliest_ready
    FROM items CROSS JOIN run
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

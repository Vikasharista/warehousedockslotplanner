-- id: V3
-- title: An allocation clashes with a booking the run does not replace, or with a truck in the dock
WITH run AS (SELECT * FROM solver_run WHERE run_id = %(run_id)s),
items AS (
    SELECT i.*, s.expected_unload_min
    FROM solver_plan_item i JOIN shipments s USING (shipment_id)
    WHERE i.run_id = %(run_id)s AND i.allocation_status = 'ALLOCATED'
),
planned AS (  -- trucks this run allocates or leaves unassigned: their old bookings are being replaced
    SELECT shipment_id FROM solver_plan_item
    WHERE run_id = %(run_id)s AND allocation_status IN ('ALLOCATED', 'UNASSIGNED')
),
fixed_bookings AS (
    SELECT a.shipment_id, sl.dock_id, sl.slot_start_ts AS start_ts,
           sl.slot_start_ts + make_interval(mins => GREATEST(s.expected_unload_min,
               (EXTRACT(EPOCH FROM sl.slot_end_ts - sl.slot_start_ts) / 60)::int)) AS end_ts,
           FALSE AS side_hold
    FROM appointments a
    JOIN appointment_slots sl ON sl.slot_id = a.slot_id
    JOIN shipments s ON s.shipment_id = a.shipment_id
    CROSS JOIN run
    WHERE a.is_current = 1
      AND a.appointment_status IN ('PENDING_CONFIRMATION', 'CONFIRMED', 'IN_PROGRESS')
      AND a.booked_at <= run.as_of_ts
      AND sl.facility_id = run.facility_id
      AND a.shipment_id NOT IN (SELECT shipment_id FROM planned)
    UNION ALL
    -- A live driver hold on another slot (is_current = 0 until the driver confirms) also occupies its slot for
    -- the held truck's whole unload, whoever holds it. Without the run's hold expiry every such hold counts as live.
    -- A HIGH or CRITICAL truck's change waiting for approval is live until its request expires (hold_expires_at).
    SELECT a.shipment_id, sl.dock_id, sl.slot_start_ts,
           sl.slot_start_ts + make_interval(mins => GREATEST(s.expected_unload_min,
               (EXTRACT(EPOCH FROM sl.slot_end_ts - sl.slot_start_ts) / 60)::int)), TRUE
    FROM appointments a
    JOIN appointment_slots sl ON sl.slot_id = a.slot_id
    JOIN shipments s ON s.shipment_id = a.shipment_id
    CROSS JOIN run
    WHERE a.is_current = 0
      AND a.appointment_status = 'PENDING_CONFIRMATION'
      AND a.booked_at <= run.as_of_ts
      AND sl.facility_id = run.facility_id
      AND (run.settings ->> 'holdExpiryMin' IS NULL
           OR hold_expires_at(a.appointment_id, a.booked_at, (run.settings ->> 'holdExpiryMin')::int) > run.as_of_ts)
),
in_dock AS (
    SELECT fc.shipment_id, fc.actual_dock_id AS dock_id,
           GREATEST(COALESCE(fc.unload_start_ts, fc.dock_in_ts) + make_interval(mins => s.expected_unload_min),
                    run.as_of_ts) AS busy_until
    FROM facility_checkins fc
    JOIN shipments s ON s.shipment_id = fc.shipment_id
    CROSS JOIN run
    WHERE fc.dock_in_ts <= run.as_of_ts
      AND (fc.unload_end_ts IS NULL OR fc.unload_end_ts > run.as_of_ts)
)
SELECT i.shipment_id, 'clashes with the booking of ' || f.shipment_id || ' on ' || i.dock_id AS detail
FROM items i JOIN fixed_bookings f ON f.dock_id = i.dock_id
WHERE (i.shipment_id <> f.shipment_id OR f.side_hold)
  AND i.planned_start_ts < f.end_ts AND f.start_ts < i.planned_end_ts
UNION ALL
SELECT i.shipment_id, 'starts while ' || d.shipment_id || ' is still in ' || i.dock_id AS detail
FROM items i JOIN in_dock d ON d.dock_id = i.dock_id
WHERE i.shipment_id <> d.shipment_id AND i.planned_start_ts < d.busy_until;

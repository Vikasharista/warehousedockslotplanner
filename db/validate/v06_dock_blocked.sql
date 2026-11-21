-- id: V6
-- title: The slot is not open, or the dock is blocked, closed or past its last start time
WITH run AS (SELECT * FROM solver_run WHERE run_id = %(run_id)s),
items AS (
    SELECT i.*, f.timezone, f.open_time, f.close_time
    FROM solver_plan_item i
    JOIN run ON TRUE
    JOIN facilities f ON f.facility_id = run.facility_id
    WHERE i.run_id = %(run_id)s AND i.allocation_status = 'ALLOCATED'
)
SELECT i.shipment_id, i.slot_id || ' is ' || sl.slot_status AS detail
FROM items i JOIN appointment_slots sl ON sl.slot_id = i.slot_id
WHERE sl.slot_status <> 'OPEN'
UNION ALL
SELECT i.shipment_id, 'runs into ' || e.dock_event_id || ' ' || e.event_type || ' on ' || i.dock_id
FROM items i
JOIN dock_status_events e ON e.dock_id = i.dock_id
JOIN run ON TRUE
WHERE e.event_type <> 'REOPENED' AND e.created_at <= run.as_of_ts
  AND i.planned_start_ts < COALESCE(e.event_end_ts, 'infinity'::timestamptz)
  AND e.event_start_ts < i.planned_end_ts
UNION ALL
SELECT i.shipment_id, 'runs into blocked slot ' || sl.slot_id
FROM items i
JOIN appointment_slots sl ON sl.dock_id = i.dock_id AND sl.slot_status <> 'OPEN' AND sl.slot_id <> i.slot_id
WHERE i.planned_start_ts < sl.slot_end_ts AND sl.slot_start_ts < i.planned_end_ts
UNION ALL
SELECT i.shipment_id, 'outside opening hours ' || i.open_time || '-' || i.close_time
FROM items i
WHERE (i.planned_start_ts AT TIME ZONE i.timezone)::time < i.open_time
   OR (i.planned_end_ts AT TIME ZONE i.timezone)::date <> (i.planned_start_ts AT TIME ZONE i.timezone)::date
   OR (i.planned_end_ts AT TIME ZONE i.timezone)::time > i.close_time
UNION ALL
SELECT i.shipment_id, 'starts after the last allowed start ' || r.rule_value
FROM items i
JOIN run ON TRUE
JOIN facility_rules r ON r.facility_id = run.facility_id
 AND r.rule_type = 'LAST_NEW_START_TIME' AND r.active_flag = 1
WHERE (i.planned_start_ts AT TIME ZONE i.timezone)::time > r.rule_value::time;

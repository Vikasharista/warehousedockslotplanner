-- id: A2
-- title: A capacity reduction removed no slots
-- impact: Every OPEN slot overlapping the reduction window is unavailable for new work; a truck already in the dock stays, a confirmed booking is re-planned.
SELECT e.dock_event_id, sl.dock_id, e.event_start_ts, e.event_end_ts, sl.slot_id, sl.slot_start_ts,
       a.shipment_id AS booked_shipment, a.appointment_status
FROM dock_status_events e
JOIN appointment_slots sl ON sl.dock_id = e.dock_id
LEFT JOIN appointments a
       ON a.slot_id = sl.slot_id AND a.is_current = 1
      AND a.appointment_status IN ('PENDING_CONFIRMATION','CONFIRMED','IN_PROGRESS')
WHERE e.event_type = 'CAPACITY_REDUCTION'
  AND sl.slot_status = 'OPEN'
  AND sl.slot_start_ts < COALESCE(e.event_end_ts, 'infinity'::timestamptz)
  AND sl.slot_end_ts > e.event_start_ts
ORDER BY sl.dock_id, sl.slot_start_ts;

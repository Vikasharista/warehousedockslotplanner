-- id: A1
-- title: A broken or blocked dock still offers an open slot
-- impact: H6 treats the slot as unavailable, so any booking on it is re-planned (the guide's dock-breakdown case).
SELECT e.dock_event_id, e.event_type, sl.dock_id, sl.slot_id, sl.slot_start_ts, sl.slot_end_ts,
       a.shipment_id AS booked_shipment, a.appointment_status
FROM dock_status_events e
JOIN appointment_slots sl ON sl.dock_id = e.dock_id
LEFT JOIN appointments a
       ON a.slot_id = sl.slot_id AND a.is_current = 1
      AND a.appointment_status IN ('PENDING_CONFIRMATION','CONFIRMED','IN_PROGRESS')
WHERE e.event_type IN ('BREAKDOWN','MAINTENANCE','MANUAL_BLOCK')
  AND sl.slot_status = 'OPEN'
  AND sl.slot_start_ts < COALESCE(e.event_end_ts, 'infinity'::timestamptz)
  AND sl.slot_end_ts > e.event_start_ts
ORDER BY sl.dock_id, sl.slot_start_ts;

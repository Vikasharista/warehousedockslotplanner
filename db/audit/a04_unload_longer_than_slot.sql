-- id: A4
-- title: Unloads run longer than the slot they are booked on
-- impact: The solver's interval model also blocks the next slot on that dock for these trucks.
SELECT a.shipment_id, sl.dock_id, sl.slot_id, s.expected_unload_min,
       (EXTRACT(EPOCH FROM sl.slot_end_ts - sl.slot_start_ts) / 60)::int AS slot_min
FROM appointments a
JOIN appointment_slots sl ON sl.slot_id = a.slot_id
JOIN shipments s ON s.shipment_id = a.shipment_id
WHERE a.is_current = 1
  AND a.appointment_status IN ('PENDING_CONFIRMATION','CONFIRMED','IN_PROGRESS')
  AND s.expected_unload_min > EXTRACT(EPOCH FROM sl.slot_end_ts - sl.slot_start_ts) / 60
ORDER BY a.shipment_id;

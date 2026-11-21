-- id: A3
-- title: Active bookings exceed the dock's weight limit
-- impact: Flagged to a coordinator, not moved automatically; H10 keeps new allocations to docks that can take the load.
SELECT a.appointment_id, a.shipment_id, a.appointment_status, s.load_weight_kg,
       d.dock_id, d.max_vehicle_weight_kg, s.load_weight_kg - d.max_vehicle_weight_kg AS over_by_kg
FROM appointments a
JOIN appointment_slots sl ON sl.slot_id = a.slot_id
JOIN docks d ON d.dock_id = sl.dock_id
JOIN shipments s ON s.shipment_id = a.shipment_id
WHERE a.is_current = 1
  AND a.appointment_status IN ('PENDING_CONFIRMATION','CONFIRMED','IN_PROGRESS')
  AND s.load_weight_kg > d.max_vehicle_weight_kg
ORDER BY a.shipment_id;

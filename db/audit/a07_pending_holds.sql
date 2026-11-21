-- id: A7
-- title: The brief's pending-on-blocked rule matches nothing
-- impact: Pending holds sit on OPEN slots; they are treated as a driver's hold, pinned until confirmed or expired after 15 minutes.
SELECT a.appointment_id, a.shipment_id, a.booking_source, a.booked_at, sl.slot_id, sl.slot_status
FROM appointments a
JOIN appointment_slots sl ON sl.slot_id = a.slot_id
WHERE a.is_current = 1 AND a.appointment_status = 'PENDING_CONFIRMATION'
ORDER BY a.appointment_id;

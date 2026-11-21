-- id: A10
-- title: Cancelled loads still carry appointment history
-- impact: Cancelled shipments are left out of every run; slots they held count as free capacity.
SELECT s.shipment_id, s.current_status, a.appointment_id, a.appointment_status, a.slot_id, a.is_current
FROM shipments s
LEFT JOIN appointments a ON a.shipment_id = s.shipment_id
WHERE s.current_status = 'CANCELLED'
ORDER BY s.shipment_id, a.appointment_id;

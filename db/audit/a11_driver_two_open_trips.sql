-- id: A11
-- title: A driver can have two open trips
-- impact: A phone number alone does not say which shipment a message is about. The agent lists the open trips and asks; it never guesses (guide case DRV004, ladder rung 2).
SELECT d.driver_id, d.phone,
       string_agg(s.shipment_id || ' to ' || s.destination_facility_id, ', ' ORDER BY s.shipment_id) AS open_trips
FROM drivers d
JOIN shipments s ON s.driver_id = d.driver_id
WHERE s.current_status NOT IN ('COMPLETED', 'CANCELLED')
GROUP BY d.driver_id, d.phone
HAVING count(*) > 1
ORDER BY d.driver_id;

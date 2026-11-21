-- id: V2
-- title: One slot is allocated to more than one truck
SELECT min(shipment_id) AS shipment_id,
       slot_id || ' allocated to ' || string_agg(shipment_id, ', ' ORDER BY shipment_id) AS detail
FROM solver_plan_item
WHERE run_id = %(run_id)s AND allocation_status = 'ALLOCATED'
GROUP BY slot_id
HAVING count(*) > 1;

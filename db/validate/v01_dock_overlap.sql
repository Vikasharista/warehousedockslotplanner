-- id: V1
-- title: Two allocated trucks overlap on one dock
SELECT a.shipment_id,
       'overlaps ' || b.shipment_id || ' on ' || a.dock_id AS detail
FROM solver_plan_item a
JOIN solver_plan_item b
  ON b.run_id = a.run_id AND b.dock_id = a.dock_id AND b.shipment_id > a.shipment_id
WHERE a.run_id = %(run_id)s
  AND a.allocation_status = 'ALLOCATED' AND b.allocation_status = 'ALLOCATED'
  AND a.planned_start_ts < b.planned_end_ts AND b.planned_start_ts < a.planned_end_ts;

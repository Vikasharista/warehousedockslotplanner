-- id: V8
-- title: The run's header does not match its items
WITH counts AS (
    SELECT count(*) FILTER (WHERE allocation_status = 'ALLOCATED') AS allocated,
           count(*) FILTER (WHERE allocation_status = 'UNASSIGNED') AS unassigned
    FROM solver_plan_item WHERE run_id = %(run_id)s
)
SELECT NULL::text AS shipment_id,
       'header says ' || r.assigned || ' assigned and ' || r.unassigned || ' unassigned, items show '
       || c.allocated || ' and ' || c.unassigned AS detail
FROM solver_run r CROSS JOIN counts c
WHERE r.run_id = %(run_id)s AND (r.assigned <> c.allocated OR r.unassigned <> c.unassigned);

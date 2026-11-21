-- id: V7
-- title: Planned times do not match the slot and the unload time
SELECT i.shipment_id,
       concat_ws('; ',
           CASE WHEN i.dock_id <> sl.dock_id THEN 'slot ' || sl.slot_id || ' is on ' || sl.dock_id END,
           CASE WHEN i.planned_start_ts <> sl.slot_start_ts THEN 'start differs from the slot start' END,
           CASE WHEN i.planned_end_ts <> i.planned_start_ts + make_interval(mins => GREATEST(s.expected_unload_min,
                         (EXTRACT(EPOCH FROM sl.slot_end_ts - sl.slot_start_ts) / 60)::int))
                THEN 'end should be start + max(unload, slot length)' END) AS detail
FROM solver_plan_item i
JOIN appointment_slots sl ON sl.slot_id = i.slot_id
JOIN shipments s ON s.shipment_id = i.shipment_id
WHERE i.run_id = %(run_id)s AND i.allocation_status = 'ALLOCATED'
  AND (i.dock_id <> sl.dock_id
       OR i.planned_start_ts <> sl.slot_start_ts
       OR i.planned_end_ts <> i.planned_start_ts + make_interval(mins => GREATEST(s.expected_unload_min,
              (EXTRACT(EPOCH FROM sl.slot_end_ts - sl.slot_start_ts) / 60)::int)));

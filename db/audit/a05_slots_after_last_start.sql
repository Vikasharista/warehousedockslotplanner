-- id: A5
-- title: No open slot starts after the facility's last allowed start time
-- impact: The slot data already respects LAST_NEW_START_TIME (a start exactly at the limit is allowed); H11 still enforces it when trucks are re-planned. Zero rows is the expected, compliant result.
SELECT sl.slot_id, sl.dock_id, sl.slot_start_ts, r.rule_id, r.rule_value AS last_new_start
FROM appointment_slots sl
JOIN facilities f ON f.facility_id = sl.facility_id
JOIN facility_rules r
  ON r.facility_id = sl.facility_id
 AND r.rule_type = 'LAST_NEW_START_TIME'
 AND r.active_flag = 1
WHERE sl.slot_status = 'OPEN'
  AND (sl.slot_start_ts AT TIME ZONE f.timezone)::time > r.rule_value::time
ORDER BY sl.slot_id;

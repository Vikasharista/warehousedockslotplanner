-- id: A6
-- title: Slot coverage differs from facility opening hours
-- impact: The solver plans only on slots that exist; the hours without slots go to the warehouse planner to decide.
SELECT f.facility_id, f.open_time, f.close_time,
       min((sl.slot_start_ts AT TIME ZONE f.timezone)::time) AS first_slot_start,
       max((sl.slot_end_ts AT TIME ZONE f.timezone)::time) AS last_slot_end,
       count(*) AS slots
FROM facilities f
JOIN appointment_slots sl ON sl.facility_id = f.facility_id
GROUP BY f.facility_id, f.open_time, f.close_time
HAVING min((sl.slot_start_ts AT TIME ZONE f.timezone)::time) <> f.open_time
    OR max((sl.slot_end_ts AT TIME ZONE f.timezone)::time) <> f.close_time
ORDER BY f.facility_id;

-- id: A8
-- title: The snapshot mixes times of day
-- impact: Every solver run takes an explicit as_of time; tests use 09:55 for the morning wave and 16:30 for the evening reefer case.
WITH latest AS (
    SELECT max(t) AS latest_event_ts FROM (
        SELECT created_at AS t FROM eta_updates
        UNION ALL SELECT created_at FROM dock_status_events
        UNION ALL SELECT message_ts FROM chat_messages
        UNION ALL SELECT updated_at FROM appointments
    ) x
)
SELECT s.shipment_id, s.current_status, le.effective_eta_ts, latest.latest_event_ts
FROM shipments s
JOIN v_latest_eta le ON le.shipment_id = s.shipment_id
CROSS JOIN latest
WHERE s.current_status = 'IN_TRANSIT' AND le.effective_eta_ts < latest.latest_event_ts
ORDER BY le.effective_eta_ts;

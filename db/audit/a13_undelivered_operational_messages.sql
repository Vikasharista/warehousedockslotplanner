-- id: A13
-- title: A warehouse notice failed or never left
-- impact: OM004, the reefer exception for SHP1015, failed to reach the night shift, and another notice is still queued. A hand-off must not depend on an email that can fail: coordinator cases live on the Driver cases screen, and the driver hears the case reference at once.
SELECT operational_message_id, shipment_id, channel, subject, delivery_status, sent_at
FROM operational_messages
WHERE delivery_status IN ('FAILED', 'QUEUED')
ORDER BY operational_message_id;

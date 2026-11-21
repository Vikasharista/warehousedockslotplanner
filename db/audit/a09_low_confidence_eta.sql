-- id: A9
-- title: Some ETAs are low confidence
-- impact: The solver adds the 15-minute low-confidence margin to earliest_ready for these trucks.
SELECT shipment_id, eta_source, eta_confidence, effective_eta_ts, delay_reason_code
FROM v_latest_eta
WHERE eta_confidence = 'LOW'
ORDER BY shipment_id;

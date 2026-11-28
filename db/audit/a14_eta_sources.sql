-- id: A14
-- title: Every ETA update comes from the driver
-- impact: The snapshot has no warehouse estimate, no operations override and no GPS. A driver's ETA is evidence with a confidence, not the truth: LOW confidence adds 15 minutes before the truck counts as ready, and an override (or the simulated GPS) outranks it.
SELECT source_type, confidence_code, count(*) AS updates, count(DISTINCT shipment_id) AS shipments
FROM eta_updates
GROUP BY source_type, confidence_code
ORDER BY source_type, confidence_code;

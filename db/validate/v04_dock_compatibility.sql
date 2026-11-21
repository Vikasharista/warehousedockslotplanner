-- id: V4
-- title: The dock does not fit the truck (facility, dock status, reefer, dock type or weight)
WITH run AS (SELECT * FROM solver_run WHERE run_id = %(run_id)s),
heavy AS (
    SELECT r.rule_value::int AS heavy_above_kg
    FROM facility_rules r JOIN run ON r.facility_id = run.facility_id
    WHERE r.rule_type = 'HEAVY_DOCK_REQUIRED_KG' AND r.active_flag = 1
),
checked AS (
    SELECT i.shipment_id, d.dock_id, d.facility_id AS dock_facility, run.facility_id AS run_facility,
           d.dock_status, d.dock_type, d.supports_refrigerated, d.max_vehicle_weight_kg,
           s.load_weight_kg, s.required_dock_type,
           (s.temperature_control_required = 1 OR s.required_dock_type = 'REEFER' OR vt.refrigerated_flag = 1) AS needs_reefer,
           (SELECT heavy_above_kg FROM heavy) AS heavy_above_kg
    FROM solver_plan_item i
    JOIN docks d ON d.dock_id = i.dock_id
    JOIN shipments s ON s.shipment_id = i.shipment_id
    JOIN vehicles v ON v.vehicle_id = s.vehicle_id
    JOIN vehicle_types vt ON vt.vehicle_type_code = v.vehicle_type_code
    CROSS JOIN run
    WHERE i.run_id = %(run_id)s AND i.allocation_status = 'ALLOCATED'
)
SELECT shipment_id,
       concat_ws('; ',
           CASE WHEN dock_facility <> run_facility THEN dock_id || ' is not at ' || run_facility END,
           CASE WHEN dock_status <> 'ACTIVE' THEN dock_id || ' is ' || dock_status END,
           CASE WHEN needs_reefer AND (dock_type <> 'REEFER' OR supports_refrigerated <> 1)
                THEN 'reefer load on ' || dock_type || ' dock' END,
           CASE WHEN required_dock_type IN ('STANDARD', 'HEAVY') AND required_dock_type <> dock_type
                THEN required_dock_type || ' load on ' || dock_type || ' dock' END,
           CASE WHEN heavy_above_kg IS NOT NULL AND load_weight_kg > heavy_above_kg AND dock_type <> 'HEAVY'
                THEN load_weight_kg || ' kg needs the HEAVY dock' END,
           CASE WHEN load_weight_kg > max_vehicle_weight_kg
                THEN load_weight_kg || ' kg over the ' || max_vehicle_weight_kg || ' kg limit' END
       ) AS detail
FROM checked
WHERE dock_facility <> run_facility
   OR dock_status <> 'ACTIVE'
   OR (needs_reefer AND (dock_type <> 'REEFER' OR supports_refrigerated <> 1))
   OR (required_dock_type IN ('STANDARD', 'HEAVY') AND required_dock_type <> dock_type)
   OR (heavy_above_kg IS NOT NULL AND load_weight_kg > heavy_above_kg AND dock_type <> 'HEAVY')
   OR load_weight_kg > max_vehicle_weight_kg;

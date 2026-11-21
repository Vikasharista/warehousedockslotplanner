-- GPS tables (plan of record, "GPS feed and ETA service"). Applied after db/plan_tables.sql.
-- The test server fills them with simulated pings on three fixed lanes into FAC-JAI-01.
-- Route geometry is fetched once from OpenRouteService (driving-hgv) and kept in data/routes/;
-- route data (c) openrouteservice.org | OpenStreetMap contributors.

BEGIN;

CREATE TABLE route_lanes (
    lane_id TEXT PRIMARY KEY,
    lane_name TEXT NOT NULL,
    origin_city TEXT NOT NULL,
    destination_facility_id TEXT NOT NULL REFERENCES facilities(facility_id),
    origin_lat DOUBLE PRECISION NOT NULL,
    origin_lon DOUBLE PRECISION NOT NULL,
    destination_lat DOUBLE PRECISION NOT NULL,
    destination_lon DOUBLE PRECISION NOT NULL,
    distance_km NUMERIC(8,2) NOT NULL CHECK (distance_km > 0),
    avg_speed_kmph NUMERIC(5,1) NOT NULL DEFAULT 45 CHECK (avg_speed_kmph > 0),
    geometry JSONB NOT NULL,          -- GeoJSON LineString, [lon, lat] pairs
    source TEXT NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE vehicle_gps_pings (
    ping_id BIGSERIAL PRIMARY KEY,
    vehicle_id TEXT NOT NULL REFERENCES vehicles(vehicle_id),
    shipment_id TEXT REFERENCES shipments(shipment_id),
    lane_id TEXT REFERENCES route_lanes(lane_id),
    recorded_at TIMESTAMPTZ NOT NULL,
    lat DOUBLE PRECISION NOT NULL CHECK (lat BETWEEN -90 AND 90),
    lon DOUBLE PRECISION NOT NULL CHECK (lon BETWEEN -180 AND 180),
    speed_kmph NUMERIC(5,1) CHECK (speed_kmph IS NULL OR speed_kmph >= 0),
    heading_deg NUMERIC(5,1) CHECK (heading_deg IS NULL OR (heading_deg >= 0 AND heading_deg < 360)),
    source TEXT NOT NULL CHECK (source IN ('SIMULATOR','DEVICE')),
    received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ix_pings_vehicle_recorded ON vehicle_gps_pings(vehicle_id, recorded_at);
CREATE INDEX ix_pings_shipment_recorded ON vehicle_gps_pings(shipment_id, recorded_at);

CREATE TABLE gps_eta_estimates (
    estimate_id BIGSERIAL PRIMARY KEY,
    shipment_id TEXT NOT NULL REFERENCES shipments(shipment_id),
    vehicle_id TEXT NOT NULL REFERENCES vehicles(vehicle_id),
    lane_id TEXT NOT NULL REFERENCES route_lanes(lane_id),
    ping_id BIGINT REFERENCES vehicle_gps_pings(ping_id) ON DELETE SET NULL,
    remaining_km NUMERIC(8,2) NOT NULL CHECK (remaining_km >= 0),
    eta_ts TIMESTAMPTZ NOT NULL,
    confidence_code TEXT NOT NULL CHECK (confidence_code IN ('LOW','MEDIUM','HIGH')),
    stale BOOLEAN NOT NULL DEFAULT FALSE,
    computed_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX ix_gps_eta_shipment_computed ON gps_eta_estimates(shipment_id, computed_at DESC);

-- Effective ETA per shipment (current state):
--   1. the newest OPERATIONS_OVERRIDE always wins;
--   2. otherwise, with a GPS estimate: the later of it and the newest DRIVER_DECLARED ETA while that driver's
--      exception is still open, else the GPS estimate alone;
--   3. otherwise v_latest_eta, as before GPS.
-- The solver applies the same rule as of a chosen time (solver/core PostgresLoader).
CREATE VIEW v_effective_eta AS
WITH gps AS (
    SELECT DISTINCT ON (shipment_id) shipment_id, eta_ts, confidence_code, stale, computed_at
    FROM gps_eta_estimates
    ORDER BY shipment_id, computed_at DESC, estimate_id DESC
),
override AS (
    SELECT DISTINCT ON (shipment_id) shipment_id, declared_eta_ts, confidence_code
    FROM eta_updates WHERE source_type = 'OPERATIONS_OVERRIDE'
    ORDER BY shipment_id, created_at DESC, eta_update_id DESC
),
driver AS (
    SELECT DISTINCT ON (shipment_id) shipment_id, declared_eta_ts
    FROM eta_updates WHERE source_type = 'DRIVER_DECLARED'
    ORDER BY shipment_id, created_at DESC, eta_update_id DESC
),
open_exception AS (
    SELECT DISTINCT shipment_id FROM driver_exceptions
    WHERE shipment_id IS NOT NULL AND exception_status NOT IN ('RESOLVED','CANCELLED','DUPLICATE')
)
SELECT
    s.shipment_id,
    CASE
        WHEN o.shipment_id IS NOT NULL THEN o.declared_eta_ts
        WHEN g.shipment_id IS NOT NULL AND x.shipment_id IS NOT NULL AND d.declared_eta_ts IS NOT NULL
            THEN GREATEST(g.eta_ts, d.declared_eta_ts)
        WHEN g.shipment_id IS NOT NULL THEN g.eta_ts
        ELSE le.effective_eta_ts
    END AS effective_eta_ts,
    CASE
        WHEN o.shipment_id IS NOT NULL THEN 'OPERATIONS_OVERRIDE'
        WHEN g.shipment_id IS NOT NULL AND x.shipment_id IS NOT NULL AND d.declared_eta_ts IS NOT NULL
             AND d.declared_eta_ts > g.eta_ts THEN 'DRIVER_DECLARED'
        WHEN g.shipment_id IS NOT NULL THEN 'GPS'
        ELSE le.eta_source
    END AS eta_source,
    CASE
        WHEN o.shipment_id IS NOT NULL THEN o.confidence_code
        WHEN g.shipment_id IS NOT NULL THEN g.confidence_code
        ELSE le.eta_confidence
    END AS eta_confidence,
    COALESCE(g.stale, FALSE) AS gps_stale
FROM shipments s
JOIN v_latest_eta le ON le.shipment_id = s.shipment_id
LEFT JOIN gps g ON g.shipment_id = s.shipment_id
LEFT JOIN override o ON o.shipment_id = s.shipment_id
LEFT JOIN driver d ON d.shipment_id = s.shipment_id
LEFT JOIN open_exception x ON x.shipment_id = s.shipment_id;

COMMIT;

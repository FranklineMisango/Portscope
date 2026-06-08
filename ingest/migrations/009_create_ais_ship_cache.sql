-- AIS ship cache: learned static data from ShipStaticData messages
CREATE TABLE IF NOT EXISTS ship_cache (
  mmsi BIGINT PRIMARY KEY,
  ship_name TEXT,
  call_sign TEXT,
  imo_number BIGINT,
  ship_type TEXT,
  ship_type_code INT,
  ship_length_m REAL,
  ship_width_m REAL,
  draught REAL,
  destination TEXT,
  first_seen TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_seen TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- AIS positions table: enriched positions with ship info
-- This is what the live analytics endpoint queries
CREATE TABLE IF NOT EXISTS ais_positions (
  id BIGSERIAL PRIMARY KEY,
  port_id TEXT,                          -- Matches portid from ports table
  mmsi BIGINT NOT NULL,
  ship_name TEXT,
  ship_type TEXT,
  call_sign TEXT,
  imo_number BIGINT,
  latitude REAL NOT NULL,
  longitude REAL NOT NULL,
  speed_knots REAL,
  course_over_ground REAL,
  true_heading REAL,
  nav_status TEXT,
  nav_status_code INT,
  rate_of_turn INT,
  destination TEXT,
  ship_length_m REAL,
  ship_width_m REAL,
  draught REAL,
  message_type TEXT,                     -- PositionReport, StandardClassBPositionReport, etc.
  raw_payload JSONB,
  timestamp TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ais_positions_port_time 
  ON ais_positions (port_id, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_ais_positions_mmsi 
  ON ais_positions (mmsi);
CREATE INDEX IF NOT EXISTS idx_ais_positions_timestamp 
  ON ais_positions (timestamp DESC);

-- Port analytics: precomputed live stats per port
-- Refreshed on-the-fly by the API when a user clicks a port
CREATE OR REPLACE VIEW port_live_analytics AS
WITH port_stats AS (
  SELECT
    port_id,
    COUNT(*) AS ships_last_10min,
    COUNT(DISTINCT mmsi) AS unique_ships,
    COUNT(*) FILTER (WHERE speed_knots > 1) AS underway,
    COUNT(*) FILTER (WHERE speed_knots <= 1) AS anchored,
    AVG(speed_knots) AS avg_speed,
    MAX(timestamp) AS last_updated
    FROM ais_positions 
  WHERE timestamp > NOW() - INTERVAL '10 minutes'
  GROUP BY port_id
),
type_stats AS (
  SELECT port_id,
    COALESCE(NULLIF(ship_type, ''), 'Unknown') AS type_key,
    COUNT(*) AS type_count
    FROM ais_positions 
  WHERE timestamp > NOW() - INTERVAL '10 minutes'
  GROUP BY port_id, ship_type
),
status_stats AS (
  SELECT port_id,
    COALESCE(NULLIF(nav_status, ''), 'Unknown') AS status_key,
    COUNT(*) AS status_count
    FROM ais_positions 
  WHERE timestamp > NOW() - INTERVAL '10 minutes'
  GROUP BY port_id, nav_status
),
dest_stats AS (
  SELECT port_id,
    COALESCE(NULLIF(destination, ''), '(no destination)') AS dest_key,
    COUNT(*) AS dest_count
    FROM ais_positions 
  WHERE timestamp > NOW() - INTERVAL '10 minutes' AND destination IS NOT NULL AND destination != ''
  GROUP BY port_id, destination
),
largest_ship AS (
  SELECT DISTINCT ON (port_id)
    port_id,
    jsonb_build_object(
      'name', ship_name,
      'length_m', ship_length_m,
      'type', ship_type,
      'speed_knots', speed_knots,
      'destination', destination
    ) AS largest
  FROM ais_positions
  WHERE timestamp > NOW() - INTERVAL '10 minutes' AND ship_length_m IS NOT NULL
  ORDER BY port_id, ship_length_m DESC NULLS LAST
)
SELECT
  p.id AS port_id,
  p.name AS port_name,
  p.country,
  p.iso3,
  COALESCE(ps.ships_last_10min, 0) AS ships_last_10min,
  COALESCE(ps.unique_ships, 0) AS unique_ships,
  COALESCE(ps.underway, 0) AS underway,
  COALESCE(ps.anchored, 0) AS anchored,
  COALESCE(ps.avg_speed, 0) AS avg_speed_knots,
  COALESCE(t.type_agg, '{}'::jsonb) AS type_breakdown,
  COALESCE(s.status_agg, '{}'::jsonb) AS status_breakdown,
  COALESCE(d.dest_agg, '{}'::jsonb) AS destination_summary,
  COALESCE(l.largest, '{}'::jsonb) AS largest_ship,
  ps.last_updated
FROM ports p
LEFT JOIN port_stats ps ON p.name = ps.port_id
LEFT JOIN (
  SELECT port_id, jsonb_object_agg(type_key, type_count) AS type_agg
  FROM type_stats GROUP BY port_id
) t ON p.name = t.port_id
LEFT JOIN (
  SELECT port_id, jsonb_object_agg(status_key, status_count) AS status_agg
  FROM status_stats GROUP BY port_id
) s ON p.name = s.port_id
LEFT JOIN (
  SELECT port_id, jsonb_object_agg(dest_key, dest_count) AS dest_agg
  FROM dest_stats GROUP BY port_id
) d ON p.name = d.port_id
LEFT JOIN largest_ship l ON p.name = l.port_id
WHERE p.name IS NOT NULL;


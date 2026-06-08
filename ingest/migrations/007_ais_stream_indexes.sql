-- Ensure traffic_logs has proper indexes for AIS stream inserts
-- The aisstreamer writes high-frequency data, so partial indexes on event_time help

-- Index for ST_DWithin against port geometries (the /traffic endpoint query)
-- This already exists from 002_create_normalized_tables.sql
-- Create a composite index for the common /traffic query pattern:
-- WHERE position IS NOT NULL AND ST_DWithin(position, query_geom, radius) AND event_time >= now() - interval
-- Daily lookup index using event_time directly (date_trunc not immutable in all PG versions)
CREATE INDEX IF NOT EXISTS idx_traffic_logs_daily_lookup 
  ON traffic_logs (event_time DESC) 
  WHERE position IS NOT NULL;

-- Add index for mmsi + event_time combo to speed up vessel lookups
CREATE INDEX IF NOT EXISTS idx_traffic_logs_mmsi_time 
  ON traffic_logs (mmsi, event_time DESC) 
  WHERE mmsi IS NOT NULL;

-- Help the planner with partition pruning
CREATE INDEX IF NOT EXISTS idx_traffic_logs_event_time_pos 
  ON traffic_logs (event_time DESC) 
  WHERE position IS NOT NULL;

-- Note: geography() is not immutable, so we avoid a functional index.
-- The existing idx_traffic_logs_position GIST index is used indirectly
-- via position::geography cast at runtime.
-- Add a table to track which ports have active vessel coverage
CREATE TABLE IF NOT EXISTS ais_stream_coverage (
  id BIGSERIAL PRIMARY KEY,
  port_id INTEGER REFERENCES ports(id) ON DELETE CASCADE,
  last_vessel_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
  vessel_count_5min INTEGER DEFAULT 0,
  CONSTRAINT fk_ais_stream_coverage_port FOREIGN KEY (port_id) REFERENCES ports(id)
);
CREATE INDEX IF NOT EXISTS idx_ais_coverage_port ON ais_stream_coverage (port_id);
CREATE INDEX IF NOT EXISTS idx_ais_coverage_last_seen ON ais_stream_coverage (last_vessel_seen DESC);


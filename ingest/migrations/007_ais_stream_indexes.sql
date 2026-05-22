-- Ensure traffic_logs has proper indexes for AIS stream inserts
-- The aisstreamer writes high-frequency data, so partial indexes on event_time help

-- Index for ST_DWithin against port geometries (the /traffic endpoint query)
-- This already exists from 002_create_normalized_tables.sql but ensure:
SELECT 1 FROM pg_indexes WHERE indexname = 'idx_traffic_logs_position' \gset
-- (don't error if missing)

-- Create a composite index for the common /traffic query pattern:
-- WHERE position IS NOT NULL AND ST_DWithin(position, query_geom, radius) AND event_time >= now() - interval
CREATE INDEX IF NOT EXISTS idx_traffic_logs_daily_lookup 
  ON traffic_logs (date_trunc('day', event_time)) 
  WHERE position IS NOT NULL;

-- Add index for mmsi + event_time combo to speed up vessel lookups
CREATE INDEX IF NOT EXISTS idx_traffic_logs_mmsi_time 
  ON traffic_logs (mmsi, event_time DESC) 
  WHERE mmsi IS NOT NULL;

-- Help the planner with partition pruning
CREATE INDEX IF NOT EXISTS idx_traffic_logs_event_time_pos 
  ON traffic_logs (event_time DESC) 
  WHERE position IS NOT NULL;

-- Geography index for ST_DWithin with meter-based distance queries
-- The /traffic, /live, and /port/{id}/traffic endpoints use ST_DWithin with
-- geography cast to query vessel positions near ports within a meter radius
-- Note: We avoid a functional index on geography(position) since geography()
-- is not immutable. Instead the query uses position::geography at runtime,
-- which will use the existing idx_traffic_logs_position GIST index indirectly.
-- For high-volume setups, consider adding a separate geography column.

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


-- Add PortWatch columns to ports table (idempotent, safe to re-run)
ALTER TABLE ports
  ADD COLUMN IF NOT EXISTS country TEXT,
  ADD COLUMN IF NOT EXISTS iso3 TEXT,
  ADD COLUMN IF NOT EXISTS source_date DATE,
  ADD COLUMN IF NOT EXISTS source_payload JSONB NOT NULL DEFAULT '{}'::jsonb;

CREATE INDEX IF NOT EXISTS idx_ports_source_date ON ports (source_date);

CREATE TABLE IF NOT EXISTS chokepoints (
  id BIGSERIAL PRIMARY KEY,
  name TEXT UNIQUE NOT NULL,
  source_date DATE,
  source_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
  geom geometry(Point,4326),
  last_seen TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_chokepoints_geom ON chokepoints USING GIST (geom);
CREATE INDEX IF NOT EXISTS idx_chokepoints_source_date ON chokepoints (source_date);
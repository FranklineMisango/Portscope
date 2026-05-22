-- Ports table for reverse-geocoded places and PortWatch data
CREATE TABLE IF NOT EXISTS ports (
  id BIGSERIAL PRIMARY KEY,
  name TEXT UNIQUE,
  osm_id TEXT,
  country TEXT,
  iso3 TEXT,
  source_date DATE,
  source_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
  geom geometry(Point,4326),
  last_seen TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_ports_geom ON ports USING GIST (geom);
CREATE INDEX IF NOT EXISTS idx_ports_source_date ON ports (source_date);

-- Chokepoints table for PortWatch chokepoint data
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


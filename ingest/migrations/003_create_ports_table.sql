-- Ports table for reverse-geocoded places
CREATE TABLE IF NOT EXISTS ports (
  id BIGSERIAL PRIMARY KEY,
  name TEXT UNIQUE,
  osm_id TEXT,
  geom geometry(Point,4326),
  last_seen TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_ports_geom ON ports USING GIST (geom);

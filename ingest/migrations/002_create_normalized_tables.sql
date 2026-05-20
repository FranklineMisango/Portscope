-- Ships registry
CREATE TABLE IF NOT EXISTS ships (
  id BIGSERIAL PRIMARY KEY,
  mmsi BIGINT NOT NULL UNIQUE,
  imo BIGINT,
  name TEXT,
  vessel_type TEXT,
  flag TEXT,
  last_seen TIMESTAMPTZ,
  position geometry(Point,4326),
  payload JSONB
);
CREATE INDEX IF NOT EXISTS idx_ships_mmsi ON ships (mmsi);
CREATE INDEX IF NOT EXISTS idx_ships_position ON ships USING GIST (position);

-- Traffic / observations
CREATE TABLE IF NOT EXISTS traffic_logs (
  id BIGSERIAL PRIMARY KEY,
  mmsi BIGINT,
  event_time TIMESTAMPTZ NOT NULL DEFAULT now(),
  payload JSONB NOT NULL,
  position geometry(Point,4326),
  speed_kts DOUBLE PRECISION,
  course_deg DOUBLE PRECISION,
  destination TEXT
);
CREATE INDEX IF NOT EXISTS idx_traffic_logs_mmsi ON traffic_logs (mmsi);
CREATE INDEX IF NOT EXISTS idx_traffic_logs_event_time ON traffic_logs (event_time);
CREATE INDEX IF NOT EXISTS idx_traffic_logs_position ON traffic_logs USING GIST (position);

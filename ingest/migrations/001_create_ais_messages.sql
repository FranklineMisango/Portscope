-- Create table for raw AIS messages with optional geometry
CREATE TABLE IF NOT EXISTS ais_messages (
  id BIGSERIAL PRIMARY KEY,
  received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  payload JSONB NOT NULL,
  position geometry(Point,4326)
);

CREATE INDEX IF NOT EXISTS idx_ais_messages_received_at ON ais_messages (received_at);
CREATE INDEX IF NOT EXISTS idx_ais_messages_position ON ais_messages USING GIST (position);
